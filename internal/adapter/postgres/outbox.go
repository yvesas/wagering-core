package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

type outboxRepository struct {
	q querier
}

const outboxColumns = `event_id, event_type, event_version, aggregate_id, payload, ` +
	`correlation_id, causation_id, occurred_at, attempts, next_attempt_at, published_at, last_error`

func (r *outboxRepository) Append(ctx context.Context, record app.OutboxRecord) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO outbox_events (`+outboxColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		record.EventID,
		string(record.Event.Type),
		record.Event.Version,
		record.Event.AggregateID,
		[]byte(record.Event.Payload),
		nullable(record.CorrelationID),
		nullable(record.CausationID),
		record.Event.OccurredAt,
		record.Attempts,
		record.NextAttemptAt,
		nullableTime(record.PublishedAt),
		nullable(record.LastError),
	)
	return translate(err)
}

// ClaimDue reserves pending events and holds them for the rest of the
// transaction.
//
// SKIP LOCKED is what lets several publishers share the queue, and the row lock
// is the lease: a publisher that dies releases its rows when the connection
// goes, with no expiry field to tune and no clock to trust between machines.
func (r *outboxRepository) ClaimDue(ctx context.Context, now time.Time, limit int) ([]app.OutboxRecord, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive, got %d", limit)
	}

	rows, err := r.q.Query(ctx, `
		SELECT `+outboxColumns+`
		  FROM outbox_events
		 WHERE published_at IS NULL AND next_attempt_at <= $1
		 ORDER BY seq
		 LIMIT $2
		 FOR UPDATE SKIP LOCKED`,
		now, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var claimed []app.OutboxRecord
	for rows.Next() {
		record, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, record)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return claimed, nil
}

func (r *outboxRepository) MarkPublished(ctx context.Context, eventID string, at time.Time) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE outbox_events
		   SET published_at = $1, last_error = NULL
		 WHERE event_id = $2 AND published_at IS NULL`,
		at, eventID)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		// Already marked, which is harmless: it means a publish happened twice
		// and the consumer deduplicates on the event id.
		return app.ErrNotFound
	}
	return nil
}

func (r *outboxRepository) Reschedule(ctx context.Context, eventID string, nextAttemptAt time.Time, cause string) error {
	_, err := r.q.Exec(ctx, `
		UPDATE outbox_events
		   SET attempts = attempts + 1, next_attempt_at = $1, last_error = $2
		 WHERE event_id = $3 AND published_at IS NULL`,
		nextAttemptAt, nullable(cause), eventID)
	return translate(err)
}

func scanOutbox(row scanner) (app.OutboxRecord, error) {
	var (
		eventID, eventType, aggregateID string
		version, attempts               int
		payload                         []byte
		correlationID, causationID      *string
		lastError                       *string
		occurredAt, nextAttemptAt       time.Time
		publishedAt                     *time.Time
	)

	if err := row.Scan(&eventID, &eventType, &version, &aggregateID, &payload,
		&correlationID, &causationID, &occurredAt, &attempts,
		&nextAttemptAt, &publishedAt, &lastError); err != nil {
		return app.OutboxRecord{}, translate(err)
	}

	return app.OutboxRecord{
		EventID: eventID,
		Event: domain.Event{
			Type:        domain.EventType(eventType),
			Version:     version,
			AggregateID: aggregateID,
			OccurredAt:  occurredAt,
			Payload:     json.RawMessage(payload),
		},
		CorrelationID: deref(correlationID),
		CausationID:   deref(causationID),
		Attempts:      attempts,
		NextAttemptAt: nextAttemptAt,
		PublishedAt:   derefTime(publishedAt),
		LastError:     deref(lastError),
	}, nil
}
