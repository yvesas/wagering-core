package app

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"
)

// Publisher sends what the outbox is holding.
type Publisher struct {
	uow       UnitOfWork
	publisher EventPublisher
	clock     Clock
	policy    PublisherPolicy
	logger    *slog.Logger
}

func NewPublisher(uow UnitOfWork, publisher EventPublisher, clock Clock, policy PublisherPolicy, logger *slog.Logger) *Publisher {
	return &Publisher{
		uow:       uow,
		publisher: publisher,
		clock:     clock,
		policy:    policy.normalised(),
		logger:    logger,
	}
}

// Run publishes until the context is done.
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.policy.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := p.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.Error("publishing events", slog.String("error", err.Error()))
			}
		}
	}
}

// RunOnce publishes one batch and reports how many went out.
//
// Claiming, publishing and marking happen in one transaction. That holds a row
// lock across a network call, which is normally a smell -- and here it is the
// point: the lock *is* the lease. A publisher that hangs or dies releases its
// rows the moment PostgreSQL notices the connection is gone, with no expiry
// field to tune and no clock to trust between machines. See
// docs/adr/0010-outbox.md.
//
// The publish timeout is what keeps that bounded.
func (p *Publisher) RunOnce(ctx context.Context) (int, error) {
	published := 0

	err := p.uow.Do(ctx, func(ctx context.Context, repos Repositories) error {
		now := p.clock.Now()

		due, err := repos.Outbox().ClaimDue(ctx, now, p.policy.BatchSize)
		if err != nil {
			return err
		}

		for _, record := range due {
			if err := p.publishOne(ctx, repos, record, now); err != nil {
				return err
			}
			published++
		}
		return nil
	})
	if err != nil {
		return published, err
	}
	return published, nil
}

func (p *Publisher) publishOne(ctx context.Context, repos Repositories, record OutboxRecord, now time.Time) error {
	publishCtx, cancel := context.WithTimeout(ctx, p.policy.PublishTimeout)
	defer cancel()

	if err := p.publisher.Publish(publishCtx, record); err != nil {
		// The broker refused or was unreachable. Reschedule and keep the
		// transaction going: one bad event must not hold up the batch behind
		// it, and the row stays unpublished so it comes round again.
		p.logger.Warn("an event could not be published",
			slog.String("eventId", record.EventID),
			slog.String("eventType", string(record.Event.Type)),
			slog.Int("attempts", record.Attempts+1),
			slog.String("error", err.Error()))

		return repos.Outbox().Reschedule(ctx, record.EventID,
			p.nextAttemptAt(now, record.Attempts+1), err.Error())
	}

	// Published. A process that dies between here and the commit publishes the
	// event again on the next pass -- with the same event id, which is what
	// lets the consumer absorb it.
	if err := repos.Outbox().MarkPublished(ctx, record.EventID, now); err != nil {
		return err
	}
	return nil
}

// nextAttemptAt doubles per attempt and jitters, for the reason every backoff
// here does: events that failed together would otherwise retry together, in
// step, against a broker that is already struggling.
func (p *Publisher) nextAttemptAt(now time.Time, attempt int) time.Time {
	window := p.policy.BaseBackoff << min(attempt-1, 16)
	if window > p.policy.MaxBackoff || window <= 0 {
		window = p.policy.MaxBackoff
	}
	wait := window/2 + time.Duration(rand.Int64N(int64(window/2)+1))
	return now.Add(wait)
}
