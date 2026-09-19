package app

import (
	"context"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// OutboxRecord is an event as it sits waiting to be published.
type OutboxRecord struct {
	EventID string
	Event   domain.Event

	CorrelationID string
	CausationID   string

	Attempts      int
	NextAttemptAt time.Time
	PublishedAt   time.Time
	LastError     string
}

// Published reports whether the event has gone out.
func (r OutboxRecord) Published() bool { return !r.PublishedAt.IsZero() }

// OutboxRepository stores events beside the facts they describe.
//
// It is on the write side only. An event is appended in the same transaction as
// the change that caused it, which is the whole mechanism: there is no moment at
// which the change exists and its event does not.
type OutboxRepository interface {
	// Append records an event. The caller supplies the id, minted once and
	// never again -- republishing keeps it, which is what lets a consumer
	// deduplicate.
	Append(ctx context.Context, record OutboxRecord) error

	// ClaimDue reserves unpublished events whose next attempt has come round,
	// holding them for the rest of the transaction. Rows another publisher
	// holds are skipped rather than waited on.
	//
	// The row lock is the lease. A publisher that hangs, is killed, or loses
	// the network releases its rows the moment PostgreSQL notices -- no expiry
	// field to tune and no clock to trust across machines. See
	// docs/adr/0010-outbox.md.
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]OutboxRecord, error)

	// MarkPublished records that the event went out.
	MarkPublished(ctx context.Context, eventID string, at time.Time) error

	// Reschedule records a failed attempt and when to try again.
	Reschedule(ctx context.Context, eventID string, nextAttemptAt time.Time, cause string) error
}

// EventPublisher sends an event to its destination.
type EventPublisher interface {
	// Publish delivers the record. It may be called more than once for the same
	// event -- a process that dies between publishing and marking will publish
	// again -- so the event id is stable and the consumer deduplicates.
	Publish(ctx context.Context, record OutboxRecord) error
}
