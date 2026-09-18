package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

type inboxRepository struct {
	q querier
}

// Claim inserts the record, and a uniqueness violation is the deduplication.
//
// It is an insert rather than a check-then-insert for the same reason
// idempotency is (ADR 0006): between a lookup and an insert, another delivery
// of the same message fits, and both would find nothing.
func (r *inboxRepository) Claim(ctx context.Context, message app.InboxMessage) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at)
		VALUES ($1, $2, $3, $4)`,
		message.ConsumerName,
		message.MessageID,
		message.PayloadHash.String(),
		message.ReceivedAt,
	)
	return translate(err)
}

func (r *inboxRepository) Complete(ctx context.Context, consumerName, messageID string, at time.Time) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE inbox_messages
		   SET completed_at = $1
		 WHERE consumer_name = $2 AND message_id = $3`,
		at, consumerName, messageID)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrNotFound
	}
	return nil
}

func (r *inboxRepository) Find(ctx context.Context, consumerName, messageID string) (app.InboxMessage, error) {
	var (
		rawHash     string
		receivedAt  time.Time
		completedAt *time.Time
	)
	err := r.q.QueryRow(ctx, `
		SELECT payload_hash, received_at, completed_at
		  FROM inbox_messages
		 WHERE consumer_name = $1 AND message_id = $2`,
		consumerName, messageID).Scan(&rawHash, &receivedAt, &completedAt)
	if err != nil {
		return app.InboxMessage{}, translate(err)
	}

	hash, err := domain.ParsePayloadHash(rawHash)
	if err != nil {
		return app.InboxMessage{}, fmt.Errorf("stored payload hash: %w", err)
	}

	return app.InboxMessage{
		ConsumerName: consumerName,
		MessageID:    messageID,
		PayloadHash:  hash,
		ReceivedAt:   receivedAt,
		CompletedAt:  derefTime(completedAt),
	}, nil
}
