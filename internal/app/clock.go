package app

import (
	"context"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// Clock reads the time.
//
// It is a port because a use case that calls time.Now() directly can only be
// tested by sleeping or by accepting whatever the machine says. Both make a
// test slow, flaky, or quietly dependent on the timezone of whoever runs it.
type Clock interface {
	// Now returns the current instant in UTC.
	Now() time.Time
}

// IDGenerator mints the identities the domain refuses to invent.
//
// The domain models what an identifier means and validates it, but deciding
// that it is a UUIDv7 rather than a sequence is a storage and ordering concern.
// Keeping it out here is what lets the domain stay free of a UUID library.
//
// Generation can fail -- an entropy source can be exhausted -- and pretending
// otherwise would push a panic into a request path.
type IDGenerator interface {
	NewWalletID(ctx context.Context) (domain.WalletID, error)
	NewTransactionID(ctx context.Context) (domain.TransactionID, error)
	NewLedgerEntryID(ctx context.Context) (domain.LedgerEntryID, error)

	// NewEventID is the identity an event keeps forever, including across a
	// republish.
	NewEventID(ctx context.Context) (string, error)
}
