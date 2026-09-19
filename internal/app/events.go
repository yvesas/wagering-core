package app

import (
	"context"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// eventRecorder appends events to the outbox inside the transaction that caused
// them.
//
// It exists so every call site does the same three things -- mint an id, set
// the first attempt, carry the correlation id -- and does them the same way.
// Spread across the use cases, the correlation id is the field that quietly
// stops being set.
type eventRecorder struct {
	ids   IDGenerator
	clock Clock
}

func newEventRecorder(ids IDGenerator, clock Clock) eventRecorder {
	return eventRecorder{ids: ids, clock: clock}
}

// record appends the events. It is called inside the caller's transaction, so
// there is no moment at which the change exists and its events do not.
func (r eventRecorder) record(ctx context.Context, repos Repositories, events ...domain.Event) error {
	if len(events) == 0 {
		return nil
	}

	correlationID := CorrelationIDFrom(ctx)
	now := r.clock.Now()

	for _, event := range events {
		eventID, err := r.ids.NewEventID(ctx)
		if err != nil {
			return err
		}
		if err := repos.Outbox().Append(ctx, OutboxRecord{
			EventID:       eventID,
			Event:         event,
			CorrelationID: correlationID,
			// Due immediately: the publisher is expected to pick it up on its
			// next pass, and the schedule only matters after a failure.
			NextAttemptAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// correlationIDKey carries the id of the request or message that caused this
// work, so an event can be traced back to it.
//
// This is the one thing the app layer reads out of a context, and it is not
// state the code depends on: a missing correlation id costs traceability, never
// correctness. That is the difference from carrying a transaction in a context,
// which ADR 0003 refused.
type correlationIDKey struct{}

// WithCorrelationID attaches the id of whatever caused this work.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationIDKey{}, id)
}

// CorrelationIDFrom returns the id, or "" when there is none.
func CorrelationIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey{}).(string)
	return id
}

// eventsForOutcome builds the events a finished operation produces.
//
// The pairing is the part worth being careful about: a processed operation
// always reports that it finished, and reports a balance change only when the
// balance actually changed. LOSS is the case that separates them -- it
// completes without moving anything -- and folding the two into one event would
// erase the distinction.
func eventsForOutcome(transaction domain.WagerTransaction, wallet domain.Wallet, entry domain.LedgerEntry, moved bool) ([]domain.Event, error) {
	var events []domain.Event

	switch transaction.Status() {
	case domain.StatusProcessed:
		processed, err := domain.NewWagerTransactionProcessed(transaction)
		if err != nil {
			return nil, err
		}
		events = append(events, processed)

		if moved {
			changed, err := domain.NewWalletBalanceChanged(wallet, entry)
			if err != nil {
				return nil, err
			}
			events = append(events, changed)
		}

	case domain.StatusRejected:
		rejected, err := domain.NewWagerTransactionRejected(transaction)
		if err != nil {
			return nil, err
		}
		events = append(events, rejected)

	case domain.StatusPendingReference:
		waiting, err := domain.NewWagerTransactionPendingReference(transaction)
		if err != nil {
			return nil, err
		}
		events = append(events, waiting)
	}

	return events, nil
}

// PublisherPolicy bounds how hard a failing publish is retried.
type PublisherPolicy struct {
	BatchSize   int
	Interval    time.Duration
	BaseBackoff time.Duration
	MaxBackoff  time.Duration

	// PublishTimeout bounds one publish call. Without it the row lock that acts
	// as the lease would last as long as the broker felt like taking.
	PublishTimeout time.Duration
}

// DefaultPublisherPolicy publishes promptly and backs off quickly.
var DefaultPublisherPolicy = PublisherPolicy{
	BatchSize:      50,
	Interval:       500 * time.Millisecond,
	BaseBackoff:    time.Second,
	MaxBackoff:     time.Minute,
	PublishTimeout: 10 * time.Second,
}

func (p PublisherPolicy) normalised() PublisherPolicy {
	d := DefaultPublisherPolicy
	if p.BatchSize < 1 {
		p.BatchSize = d.BatchSize
	}
	if p.Interval <= 0 {
		p.Interval = d.Interval
	}
	if p.BaseBackoff <= 0 {
		p.BaseBackoff = d.BaseBackoff
	}
	if p.MaxBackoff < p.BaseBackoff {
		p.MaxBackoff = max(d.MaxBackoff, p.BaseBackoff)
	}
	if p.PublishTimeout <= 0 {
		p.PublishTimeout = d.PublishTimeout
	}
	return p
}
