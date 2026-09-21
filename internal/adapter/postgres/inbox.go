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

// Claim records that this consumer has taken the message, and reports
// ErrConflict when it had already taken it before.
//
// It is an insert rather than a check-then-insert for the same reason
// idempotency is (ADR 0006): between a lookup and an insert, another delivery
// of the same message fits, and both would find nothing.
//
// ON CONFLICT DO NOTHING, and the reason is not style. A plain INSERT that
// violates the primary key raises a database error, and in PostgreSQL an error
// **aborts the whole transaction**: every statement after it fails with 25P02
// until the transaction ends. The caller's next move on a duplicate is to read
// the stored record and decide what the repeat means -- and that read would be
// the first statement to hit the aborted transaction.
//
// So the duplicate has to be detected without raising. No row inserted means
// the row was already there, which is the same answer without the wreckage.
// The alternative -- a savepoint around the insert -- works too and costs a
// round trip plus a rollback path to get wrong.
func (r *inboxRepository) Claim(ctx context.Context, message app.InboxMessage) error {
	tag, err := r.q.Exec(ctx, `
		INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (consumer_name, message_id) DO NOTHING`,
		message.ConsumerName,
		message.MessageID,
		message.PayloadHash.String(),
		message.ReceivedAt,
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return app.NewConflict(constraintInboxIdentity)
	}
	return nil
}

// constraintInboxIdentity is the primary key this insert would have violated.
// It is named so the conflict reads the same as one the database raised, which
// is what lets the caller branch on it without knowing which of the two
// happened.
const constraintInboxIdentity = "inbox_messages_identity"

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
