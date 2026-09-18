// Package system implements the ports that reach outside the process for
// something other than storage: the clock and identity generation.
package system

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

// Clock reads the wall clock.
type Clock struct{}

func NewClock() app.Clock { return Clock{} }

// Now is always UTC. Returning local time would put the timezone of whichever
// machine happened to run the process into stored timestamps and emitted
// events, and the difference only shows up when two machines disagree.
func (Clock) Now() time.Time { return time.Now().UTC() }

// IDGenerator mints UUIDv7 identifiers.
//
// Version 7 rather than 4 because it is ordered by creation time. That matters
// twice here: a time-ordered primary key keeps B-tree inserts at the right edge
// of the index instead of scattering them, and an identifier that sorts by age
// makes a ledger or a transaction list readable without a second column.
//
// Written against google/uuid rather than by hand. UUIDv7 is only a timestamp
// and some randomness, which is exactly why writing it looks like a harmless
// thirty lines -- and why getting the monotonic counter subtly wrong inside
// the same millisecond would be found late, by duplicate keys under load.
type IDGenerator struct{}

func NewIDGenerator() app.IDGenerator { return IDGenerator{} }

func (g IDGenerator) NewWalletID(ctx context.Context) (domain.WalletID, error) {
	s, err := g.next(ctx)
	if err != nil {
		return domain.WalletID{}, err
	}
	return domain.ParseWalletID(s)
}

func (g IDGenerator) NewTransactionID(ctx context.Context) (domain.TransactionID, error) {
	s, err := g.next(ctx)
	if err != nil {
		return domain.TransactionID{}, err
	}
	return domain.ParseTransactionID(s)
}

func (g IDGenerator) NewLedgerEntryID(ctx context.Context) (domain.LedgerEntryID, error) {
	s, err := g.next(ctx)
	if err != nil {
		return domain.LedgerEntryID{}, err
	}
	return domain.ParseLedgerEntryID(s)
}

func (IDGenerator) next(ctx context.Context) (string, error) {
	// Generation reads the entropy pool, which can fail. A caller that is
	// already being cancelled should not wait on it.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generating a uuid: %w", err)
	}
	return id.String(), nil
}
