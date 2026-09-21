//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

// The inbox against a real database.
//
// The in-memory fake cannot stand in for this one. A duplicate Claim is not
// just "an error the caller handles" -- in PostgreSQL an error inside a
// transaction *aborts the transaction*, and every statement after it fails
// until the transaction ends. The caller's next move on a duplicate is to read
// the stored record, so the test that matters is the whole sequence, in one
// transaction, exactly as the consumer runs it.
//
//	make up-test && make test-integration

func inboxMessage(t *testing.T, id, payload string) app.InboxMessage {
	t.Helper()
	hash, err := domain.ParsePayloadHash(payload)
	if err != nil {
		t.Fatalf("parsing the payload hash: %v", err)
	}
	return app.InboxMessage{
		ConsumerName: "test-consumer",
		MessageID:    id,
		PayloadHash:  hash,
		ReceivedAt:   time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestADuplicateClaimLeavesTheTransactionUsable(t *testing.T) {
	ctx := context.Background()
	uow := NewUnitOfWork(testPool, nil)
	message := inboxMessage(t, "msg-"+uniqueSuffix(), "hash-one")

	// The first delivery: claim it and complete it, the way the consumer does.
	err := uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
		if err := repos.Inbox().Claim(ctx, message); err != nil {
			return err
		}
		return repos.Inbox().Complete(ctx, message.ConsumerName, message.MessageID, message.ReceivedAt)
	})
	if err != nil {
		t.Fatalf("the first delivery: %v", err)
	}

	// The redelivery. This is the sequence that was broken: the claim is
	// refused, and then the consumer reads the stored record to decide what the
	// repeat means. A claim that raised a database error would have aborted the
	// transaction, and this read would fail with 25P02 instead of answering.
	var seen app.InboxMessage
	err = uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
		claimErr := repos.Inbox().Claim(ctx, message)
		if !errors.Is(claimErr, app.ErrConflict) {
			t.Fatalf("the second claim = %v, want ErrConflict", claimErr)
		}

		var findErr error
		seen, findErr = repos.Inbox().Find(ctx, message.ConsumerName, message.MessageID)
		return findErr
	})
	if err != nil {
		t.Fatalf("reading the record back after a refused claim: %v", err)
	}

	if !seen.Handled() {
		t.Error("the stored record does not report itself as handled")
	}
	if seen.PayloadHash != message.PayloadHash {
		t.Errorf("payload hash = %q, want %q", seen.PayloadHash, message.PayloadHash)
	}
}

func TestADuplicateClaimDoesNotOverwriteWhatWasStored(t *testing.T) {
	ctx := context.Background()
	uow := NewUnitOfWork(testPool, nil)

	first := inboxMessage(t, "msg-"+uniqueSuffix(), "hash-original")
	if err := uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
		return repos.Inbox().Claim(ctx, first)
	}); err != nil {
		t.Fatalf("the first claim: %v", err)
	}

	// The same message id carrying different content. ON CONFLICT DO NOTHING
	// has to mean *nothing*: an upsert here would quietly rewrite the record a
	// producer's mistake is meant to be caught by.
	second := first
	second.PayloadHash = inboxMessage(t, second.MessageID, "hash-different").PayloadHash

	var seen app.InboxMessage
	err := uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
		if claimErr := repos.Inbox().Claim(ctx, second); !errors.Is(claimErr, app.ErrConflict) {
			t.Fatalf("the second claim = %v, want ErrConflict", claimErr)
		}
		var findErr error
		seen, findErr = repos.Inbox().Find(ctx, second.ConsumerName, second.MessageID)
		return findErr
	})
	if err != nil {
		t.Fatalf("reading the record back: %v", err)
	}

	if seen.PayloadHash != first.PayloadHash {
		t.Errorf("payload hash = %q, want the original %q", seen.PayloadHash, first.PayloadHash)
	}
	if seen.Handled() {
		t.Error("a record that was never completed reports itself as handled")
	}
}
