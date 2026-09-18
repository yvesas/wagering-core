package app

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// ReferencePolicy bounds how long a reversal waits for what it undoes.
//
// Attempts and a deadline together, because either alone is weak. With
// exponential backoff, five attempts might be thirty seconds or thirty minutes
// depending on the base; and a long window with a short backoff is thousands of
// useless lookups. Together they bound both the wait and its cost.
type ReferencePolicy struct {
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	TTL         time.Duration

	// BatchSize is how many pending items one tick will work through, one
	// transaction each.
	BatchSize int

	// Interval is how often the worker wakes up when there is nothing due.
	Interval time.Duration
}

// DefaultReferencePolicy waits a couple of minutes in total. An out-of-order
// delivery arrives in seconds or does not arrive at all.
var DefaultReferencePolicy = ReferencePolicy{
	MaxAttempts: 8,
	BaseBackoff: time.Second,
	MaxBackoff:  30 * time.Second,
	TTL:         2 * time.Minute,
	BatchSize:   20,
	Interval:    time.Second,
}

func (p ReferencePolicy) normalised() ReferencePolicy {
	d := DefaultReferencePolicy
	if p.MaxAttempts < 1 {
		p.MaxAttempts = d.MaxAttempts
	}
	if p.BaseBackoff <= 0 {
		p.BaseBackoff = d.BaseBackoff
	}
	if p.MaxBackoff < p.BaseBackoff {
		p.MaxBackoff = max(d.MaxBackoff, p.BaseBackoff)
	}
	if p.TTL <= 0 {
		p.TTL = d.TTL
	}
	if p.BatchSize < 1 {
		p.BatchSize = d.BatchSize
	}
	if p.Interval <= 0 {
		p.Interval = d.Interval
	}
	return p
}

// nextAttemptAt is when to look again, doubling per attempt and jittered.
//
// The jitter matters for the same reason it does in the unit of work: pendings
// created together would otherwise wake together, look together, and fail
// together, in step, for as long as they live.
func (p ReferencePolicy) nextAttemptAt(now time.Time, attempt int) time.Time {
	window := p.BaseBackoff << min(attempt-1, 16)
	if window > p.MaxBackoff || window <= 0 {
		window = p.MaxBackoff
	}
	wait := window/2 + time.Duration(rand.Int64N(int64(window/2)+1))
	return now.Add(wait)
}

// ReferenceWorker resolves reversals that are waiting for what they undo.
type ReferenceWorker struct {
	uow    UnitOfWork
	ids    IDGenerator
	clock  Clock
	policy ReferencePolicy
	logger *slog.Logger
}

func NewReferenceWorker(uow UnitOfWork, ids IDGenerator, clock Clock, policy ReferencePolicy, logger *slog.Logger) *ReferenceWorker {
	return &ReferenceWorker{
		uow:    uow,
		ids:    ids,
		clock:  clock,
		policy: policy.normalised(),
		logger: logger,
	}
}

// Run works until the context is done.
func (w *ReferenceWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.policy.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.Error("resolving pending references",
					slog.String("error", err.Error()))
			}
		}
	}
}

// RunOnce works through one batch and reports how many it touched.
//
// Each pending item gets its own transaction. That is not an optimisation, it
// is the deadlock rule from ADR 0007: a transaction locks exactly one wallet
// row. Claiming twenty pendings for twenty different wallets in one transaction
// would take twenty wallet locks in whatever order the scan returned them, and
// two workers scanning at once would eventually take two of them in opposite
// orders.
func (w *ReferenceWorker) RunOnce(ctx context.Context) (int, error) {
	handled := 0
	for i := 0; i < w.policy.BatchSize; i++ {
		if err := ctx.Err(); err != nil {
			return handled, err
		}

		worked, err := w.handleOne(ctx)
		if err != nil {
			return handled, err
		}
		if !worked {
			// Nothing else is due.
			return handled, nil
		}
		handled++
	}
	return handled, nil
}

func (w *ReferenceWorker) handleOne(ctx context.Context) (bool, error) {
	worked := false

	err := w.uow.Do(ctx, func(ctx context.Context, repos Repositories) error {
		now := w.clock.Now()

		// One at a time: the claim holds the row for the rest of this
		// transaction, and other workers skip it rather than queue behind it.
		due, err := repos.Transactions().ClaimDueReferences(ctx, now, 1)
		if err != nil {
			return err
		}
		if len(due) == 0 {
			return nil
		}
		worked = true
		pending := due[0]

		// The wallet is locked before anything is decided, so a reversal
		// resolving here and a bet arriving over HTTP cannot both read the same
		// balance.
		wallet, err := repos.Wallets().FindByIDForUpdate(ctx, pending.WalletID())
		if err != nil {
			return err
		}

		decision, err := resolveReference(ctx, repos, pending)
		if err != nil {
			return err
		}

		switch decision.outcome {
		case resolveApply:
			applied, _, err := applyReversal(ctx, repos, w.ids, pending, decision, wallet, now)
			if err != nil {
				var de *domain.Error
				if !errors.As(err, &de) {
					return err
				}
				return w.reject(ctx, repos, pending, de.Code, now)
			}
			w.logger.Info("pending reference resolved",
				slog.String("transactionId", applied.ID().String()),
				slog.Int("attempts", applied.ReferenceAttempts()))
			return nil

		case resolveReject:
			return w.reject(ctx, repos, pending, decision.code, now)
		}

		// Still not usable. Give up if the wait is over, otherwise come back.
		if pending.ReferenceDeadlinePassed(now) || pending.ReferenceAttempts() >= w.policy.MaxAttempts {
			return w.reject(ctx, repos, pending, domain.CodeReferenceNotFound, now)
		}

		rescheduled, err := pending.RescheduleReference(now,
			w.policy.nextAttemptAt(now, pending.ReferenceAttempts()+1))
		if err != nil {
			return err
		}
		return repos.Transactions().Update(ctx, rescheduled)
	})

	return worked, err
}

func (w *ReferenceWorker) reject(ctx context.Context, repos Repositories, pending domain.WagerTransaction, code domain.Code, now time.Time) error {
	rejected, err := pending.Reject(code, now)
	if err != nil {
		return err
	}
	w.logger.Info("pending reference rejected",
		slog.String("transactionId", rejected.ID().String()),
		slog.String("failureCode", string(code)),
		slog.Int("attempts", rejected.ReferenceAttempts()))
	return repos.Transactions().Update(ctx, rejected)
}
