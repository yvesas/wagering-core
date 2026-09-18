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
func (s *WalletQueries) Get(ctx context.Context, walletID string) (domain.Wallet, error) {
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
func (s *TransactionQueries) Get(ctx context.Context, transactionID string) (domain.WagerTransaction, error) {
	id, err := domain.ParseTransactionID(transactionID)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	return s.queries.Transactions().FindByID(ctx, id)
}

// GetByBusinessID returns an operation by the pair that identifies it to its
// provider. This is the lookup a provider uses to follow up on something it
// submitted, using only identifiers it already has.
func (s *TransactionQueries) GetByBusinessID(ctx context.Context, providerID, externalID string) (domain.WagerTransaction, error) {
	provider, err := domain.ParseProviderID(providerID)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	external, err := domain.ParseExternalTransactionID(externalID)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	return s.queries.Transactions().FindByBusinessID(ctx, provider, external)
}
