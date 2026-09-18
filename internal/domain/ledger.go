package domain

import "time"

// Direction is which way money moved.
type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

// Valid reports whether the direction is one of the two known values. The zero
// value is not, which is what keeps an unset field from passing as a debit.
func (d Direction) Valid() bool { return d == Debit || d == Credit }

// Opposite is which way undoing this movement goes.
func (d Direction) Opposite() Direction {
	if d == Debit {
		return Credit
	}
	return Debit
}

// LedgerEntryParams carries what a ledger entry needs. It is a struct rather
// than eight positional arguments because two of them are Money and three are
// identifiers: at that point the compiler stops catching a swapped pair.
type LedgerEntryParams struct {
	ID            LedgerEntryID
	WalletID      WalletID
	TransactionID TransactionID
	Direction     Direction
	Amount        Money
	BalanceBefore Money
	BalanceAfter  Money
	CreatedAt     time.Time
}

// LedgerEntry is one immutable movement on a wallet: the amount, the direction,
// and the balance on both sides of it.
//
// It is the auditable proof that the balance got where it got. The ledger is
// append-only — a financial correction is a new entry, never an edit of an old
// one — so this type exposes readers and no mutators at all.
type LedgerEntry struct {
	id            LedgerEntryID
	walletID      WalletID
	transactionID TransactionID
	direction     Direction
	amount        Money
	balanceBefore Money
	balanceAfter  Money
	createdAt     time.Time
}

// NewLedgerEntry validates and builds an entry.
//
// It is also the rehydration path, on purpose: re-checking the arithmetic when
// a row comes back from the database is how a corrupted ledger announces itself
// instead of being read as fact.
func NewLedgerEntry(p LedgerEntryParams) (LedgerEntry, error) {
	switch {
	case p.ID.IsZero():
		return LedgerEntry{}, fail(CodeInvalidIdentifier, "ledger entry id is empty")
	case p.WalletID.IsZero():
		return LedgerEntry{}, fail(CodeInvalidIdentifier, "wallet id is empty")
	case p.TransactionID.IsZero():
		return LedgerEntry{}, fail(CodeInvalidIdentifier, "transaction id is empty")
	case !p.Direction.Valid():
		return LedgerEntry{}, fail(CodeInvalidDirection, "unknown direction %q", string(p.Direction))
	case p.CreatedAt.IsZero():
		return LedgerEntry{}, fail(CodeInvalidTimestamp, "created at is not set")
	}

	// A movement of nothing is not a movement. Operations that do not move
	// money produce no entry at all rather than an entry of zero.
	if !p.Amount.IsInitialised() || !p.Amount.IsPositive() {
		return LedgerEntry{}, fail(CodeNonPositiveAmount,
			"ledger entry amount must be positive, got %v", p.Amount)
	}
	if !p.BalanceBefore.IsInitialised() || !p.BalanceAfter.IsInitialised() {
		return LedgerEntry{}, fail(CodeInvalidCurrency, "balance is not initialised")
	}

	currency := p.Amount.Currency()
	if p.BalanceBefore.Currency() != currency || p.BalanceAfter.Currency() != currency {
		return LedgerEntry{}, fail(CodeCurrencyMismatch,
			"entry mixes %s, %s and %s",
			currency, p.BalanceBefore.Currency(), p.BalanceAfter.Currency())
	}

	expected, err := applyDirection(p.Direction, p.BalanceBefore, p.Amount)
	if err != nil {
		return LedgerEntry{}, err
	}
	if expected != p.BalanceAfter {
		return LedgerEntry{}, fail(CodeInconsistentEntry,
			"%s of %s on %s gives %s, not %s",
			p.Direction, p.Amount, p.BalanceBefore, expected, p.BalanceAfter)
	}

	return LedgerEntry{
		id:            p.ID,
		walletID:      p.WalletID,
		transactionID: p.TransactionID,
		direction:     p.Direction,
		amount:        p.Amount,
		balanceBefore: p.BalanceBefore,
		balanceAfter:  p.BalanceAfter,
		createdAt:     p.CreatedAt.UTC(),
	}, nil
}

// applyDirection is the single definition of what a direction does to a
// balance. Both the entry's own check and the wallet's movement go through it,
// so the two can never disagree about what a debit means.
func applyDirection(d Direction, balance, amount Money) (Money, error) {
	switch d {
	case Debit:
		return balance.Sub(amount)
	case Credit:
		return balance.Add(amount)
	default:
		return Money{}, fail(CodeInvalidDirection, "unknown direction %q", string(d))
	}
}

func (e LedgerEntry) ID() LedgerEntryID            { return e.id }
func (e LedgerEntry) WalletID() WalletID           { return e.walletID }
func (e LedgerEntry) TransactionID() TransactionID { return e.transactionID }
func (e LedgerEntry) Direction() Direction         { return e.direction }
func (e LedgerEntry) Amount() Money                { return e.amount }
func (e LedgerEntry) BalanceBefore() Money         { return e.balanceBefore }
func (e LedgerEntry) BalanceAfter() Money          { return e.balanceAfter }
func (e LedgerEntry) CreatedAt() time.Time         { return e.createdAt }

// SignedAmount is the movement as a signed value: negative for a debit.
// Reconciliation sums these and compares the total against the stored balance.
func (e LedgerEntry) SignedAmount() (Money, error) {
	if e.direction == Debit {
		return e.amount.Neg()
	}
	return e.amount, nil
}
