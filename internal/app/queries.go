package app

import (
	"context"
	"fmt"

	"github.com/yvesas/wagering-core/internal/domain"
)

// WalletQueries answers the reads that do not need a transaction.
//
// It exists so a handler stays transport-only: parsing an identifier and
// fetching by it is application work, and doing it in the HTTP layer would put
// the same three lines in every entry point that ever reads a wallet.
type WalletQueries struct {
	queries Queries
}

func NewWalletQueries(queries Queries) *WalletQueries {
	return &WalletQueries{queries: queries}
}

// Get returns a wallet, or ErrNotFound.
//
// A wallet is the platform's, not a provider's: it belongs to a player and
// there is no column tying it to whoever sends operations against it. So the
// read is restricted to the internal credential rather than filtered by
// provider -- REQ-SEC-004. Filtering by something the schema does not record
// would be authorisation theatre.
func (s *WalletQueries) Get(ctx context.Context, walletID string) (domain.Wallet, error) {
	if _, err := caller(ctx, ScopeWallets); err != nil {
		return domain.Wallet{}, err
	}
	id, err := domain.ParseWalletID(walletID)
	if err != nil {
		return domain.Wallet{}, err
	}
	return s.queries.Wallets().FindByID(ctx, id)
}

// DefaultLedgerPageSize is used when the caller asks for nothing.
const DefaultLedgerPageSize = 50

// MaxLedgerPageSize caps what a caller can ask for. Without a ceiling, one
// request can pull an entire ledger into memory -- the client's, ours, and the
// database's at the same time.
const MaxLedgerPageSize = 200

// Ledger returns a page of a wallet's entries.
//
// It checks the wallet exists first. Paging a wallet that is not there would
// otherwise return an empty page, which reads as "no movements yet" and is a
// different answer from "no such wallet".
func (s *WalletQueries) Ledger(ctx context.Context, walletID string, cursor LedgerCursor, limit int) (LedgerPage, error) {
	if _, err := caller(ctx, ScopeWallets); err != nil {
		return LedgerPage{}, err
	}
	id, err := domain.ParseWalletID(walletID)
	if err != nil {
		return LedgerPage{}, err
	}

	switch {
	case limit <= 0:
		limit = DefaultLedgerPageSize
	case limit > MaxLedgerPageSize:
		return LedgerPage{}, fmt.Errorf("%w: limit %d is above the maximum of %d",
			ErrInvalidInput, limit, MaxLedgerPageSize)
	}

	if _, err := s.queries.Wallets().FindByID(ctx, id); err != nil {
		return LedgerPage{}, err
	}
	return s.queries.Ledger().ListByWallet(ctx, id, cursor, limit)
}

// TransactionQueries answers the reads for operations.
type TransactionQueries struct {
	queries Queries
}

func NewTransactionQueries(queries Queries) *TransactionQueries {
	return &TransactionQueries{queries: queries}
}

// Get returns an operation by our own identifier.
//
// An operation that belongs to another provider answers ErrNotFound, and the
// error is the bare sentinel the repository itself returns -- not a wrapped
// variant -- so the two answers are the same bytes on the wire. A 403 here
// would be a lookup service for other people's transaction ids: refusing is
// itself a confirmation that there is something to refuse.
func (s *TransactionQueries) Get(ctx context.Context, transactionID string) (domain.WagerTransaction, error) {
	identity, err := caller(ctx, ScopeRead)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	id, err := domain.ParseTransactionID(transactionID)
	if err != nil {
		return domain.WagerTransaction{}, err
	}

	transaction, err := s.queries.Transactions().FindByID(ctx, id)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	if !identity.owns(transaction.ProviderID()) {
		return domain.WagerTransaction{}, ErrNotFound
	}
	return transaction, nil
}

// GetByBusinessID returns an operation by the pair that identifies it to its
// provider. This is the lookup a provider uses to follow up on something it
// submitted, using only identifiers it already has.
func (s *TransactionQueries) GetByBusinessID(ctx context.Context, providerID, externalID string) (domain.WagerTransaction, error) {
	identity, err := caller(ctx, ScopeRead)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	provider, err := domain.ParseProviderID(providerID)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	if !identity.owns(provider) {
		// Refused before the query rather than after it. There is nothing to
		// learn from the database here, and not asking is one fewer way for the
		// answer to differ -- in wording or in timing -- from a genuine miss.
		return domain.WagerTransaction{}, ErrNotFound
	}
	external, err := domain.ParseExternalTransactionID(externalID)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	return s.queries.Transactions().FindByBusinessID(ctx, provider, external)
}
