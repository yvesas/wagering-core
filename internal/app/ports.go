package app

import (
	"context"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// The ports are declared here, by the code that consumes them, and implemented
// in internal/adapter. That direction is the whole point: a use case names what
// it needs, and the driver is chosen somewhere it cannot influence.
//
// Reading and writing are split into separate interfaces. A read outside a
// transaction is legitimate -- a balance lookup, a paginated statement -- and
// the split means the compiler, rather than a convention, is what stops a write
// from happening there. See docs/adr/0003-transactional-boundary.md.

// WalletReader looks wallets up.
type WalletReader interface {
	FindByID(ctx context.Context, id domain.WalletID) (domain.Wallet, error)
	FindByPlayerAndCurrency(ctx context.Context, player domain.PlayerID, currency domain.Currency) (domain.Wallet, error)
}

// WalletRepository reads and writes wallets.
type WalletRepository interface {
	WalletReader

	// FindByIDForUpdate reads the wallet and holds its row until the
	// transaction ends, so concurrent writers to the same wallet queue instead
	// of racing. It is on the write port and not on WalletReader because
	// locking a row outside a transaction means nothing -- the compiler is what
	// keeps that from being attempted. See docs/adr/0007-per-wallet-concurrency.md.
	FindByIDForUpdate(ctx context.Context, id domain.WalletID) (domain.Wallet, error)

	Insert(ctx context.Context, wallet domain.Wallet) error

	// UpdateBalance writes the new balance only while the stored version is
	// still expectedVersion, and reports ErrVersionMismatch when no row
	// matched. That is the whole optimistic control: the check and the write
	// are one statement, so nothing can slip between them.
	UpdateBalance(ctx context.Context, wallet domain.Wallet, expectedVersion int64) error
}

// LedgerCursor points at a position in a wallet's ledger. It is opaque on
// purpose: the caller passes back what it was given and nothing else.
type LedgerCursor struct {
	// after is the sequence number the previous page ended on. Zero starts from
	// the beginning.
	after int64
}

// NewLedgerCursor builds a cursor from a stored position.
func NewLedgerCursor(after int64) LedgerCursor { return LedgerCursor{after: after} }

// After exposes the position so an adapter can encode it for the wire.
func (c LedgerCursor) After() int64 { return c.after }

// LedgerPage is one page of entries plus where to continue.
type LedgerPage struct {
	Entries []domain.LedgerEntry

	// Next is the cursor for the following page. HasMore says whether there is
	// one, because a page that happens to end exactly on the last entry is
	// indistinguishable from one that does not.
	Next    LedgerCursor
	HasMore bool
}

// LedgerReader reads the ledger.
type LedgerReader interface {
	// ListByWallet returns entries in a stable total order.
	ListByWallet(ctx context.Context, wallet domain.WalletID, cursor LedgerCursor, limit int) (LedgerPage, error)

	// SumByWallet rebuilds the balance from the entries: credits minus debits.
	// It returns the count as well, because reconciliation has to report how
	// many entries it looked at.
	SumByWallet(ctx context.Context, wallet domain.WalletID, currency domain.Currency) (domain.Money, int, error)
}

// LedgerRepository appends to the ledger. There is no update and no delete,
// and there never will be: a financial correction is a new entry.
type LedgerRepository interface {
	LedgerReader

	Append(ctx context.Context, entry domain.LedgerEntry) error
}

// TransactionReader looks operations up.
type TransactionReader interface {
	FindByID(ctx context.Context, id domain.TransactionID) (domain.WagerTransaction, error)

	// FindByBusinessID resolves the pair that identifies an operation to a
	// provider. It is what a reversal uses to find what it undoes, and what a
	// replay uses to find its stored result.
	FindByBusinessID(ctx context.Context, provider domain.ProviderID, external domain.ExternalTransactionID) (domain.WagerTransaction, error)

	// FindProcessedReversalOf returns the successful reversal of an operation,
	// or ErrNotFound. At most one can exist -- the schema enforces it -- so the
	// answer is singular by construction.
	FindProcessedReversalOf(ctx context.Context, reference domain.TransactionID) (domain.WagerTransaction, error)
}

// TransactionRepository reads and writes operations.
type TransactionRepository interface {
	TransactionReader

	Insert(ctx context.Context, transaction domain.WagerTransaction) error

	// Update writes a transition. It refuses to move a row that is already
	// terminal, so a late worker cannot overwrite a stored result.
	Update(ctx context.Context, transaction domain.WagerTransaction) error

	// ClaimDueReferences reserves pending reversals whose next attempt has come
	// round, holding them for the rest of the transaction. Rows another worker
	// already holds are skipped rather than waited on, which is what lets
	// several workers share the queue.
	ClaimDueReferences(ctx context.Context, now time.Time, limit int) ([]domain.WagerTransaction, error)
}

// Repositories is the bundle bound to one transaction. It is handed to the
// callback of [UnitOfWork.Do] and has no meaning outside it.
type Repositories interface {
	Wallets() WalletRepository
	Ledger() LedgerRepository
	Transactions() TransactionRepository
	Inbox() InboxRepository
	Outbox() OutboxRepository
}

// Queries is read-only access outside any transaction, for the lookups that do
// not need atomicity.
//
// The day one of them has to see what another just wrote, it moves inside a
// Do -- which is a visible change to the call site rather than a side effect.
type Queries interface {
	Wallets() WalletReader
	Ledger() LedgerReader
	Transactions() TransactionReader
}

// Snapshot runs fn against one unchanging view of the database.
//
// It is not [UnitOfWork] with the writes taken away, and the difference is the
// point. A plain transaction at the default isolation level gives each
// *statement* its own snapshot, so two reads inside one transaction can see two
// different states -- which is exactly the hole reconciliation would fall into:
// read the balance, have a bet commit, read the ledger, and report a difference
// that never existed.
//
// The adapter opens this read-only and at an isolation level where the whole
// transaction sees one snapshot. Read-only because reconciliation must not
// change anything, and saying so to the database is stronger than saying so in
// a comment.
//
// It is deliberately separate from UnitOfWork rather than an option on it: the
// two have different guarantees, and a boolean argument would let a caller ask
// for the wrong one without noticing.
type Snapshot interface {
	Do(ctx context.Context, fn func(context.Context, Queries) error) error
}

// UnitOfWork runs fn inside a single database transaction.
//
// A non-nil error from fn rolls back; nil commits; a panic rolls back and is
// repropagated. Nothing in fn calls Commit or Rollback, which is why neither
// can be forgotten.
//
// fn must be safe to run more than once: the unit of work is where a retry on
// a serialization failure will live, and the callback is the only thing that
// sees the whole transaction.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(context.Context, Repositories) error) error
}
