package app

import (
	"context"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// InboxMessage is a message this consumer has seen.
type InboxMessage struct {
	ConsumerName string
	MessageID    string
	PayloadHash  domain.PayloadHash
	ReceivedAt   time.Time
	CompletedAt  time.Time
}

// Handled reports whether the handling finished durably.
func (m InboxMessage) Handled() bool { return !m.CompletedAt.IsZero() }

// InboxRepository records what has been handled.
//
// It is only on the write side: a row here is written in the same transaction
// as the domain changes it causes, and reading it outside one would answer a
// question that is about to change.
type InboxRepository interface {
	// Claim records a message as received. It returns ErrConflict when this
	// consumer has seen the id before, which is the deduplication: the caller
	// then reads the existing row rather than handling the message again.
	Claim(ctx context.Context, message InboxMessage) error

	// Complete marks the handling as durably finished. It runs in the same
	// transaction as everything the handling did, so the record and the effect
	// cannot disagree.
	Complete(ctx context.Context, consumerName, messageID string, at time.Time) error

	// Find returns what is known about a message, or ErrNotFound.
	Find(ctx context.Context, consumerName, messageID string) (InboxMessage, error)
}
