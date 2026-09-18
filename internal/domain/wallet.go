package domain

import "time"

// Wallet is the root of the financial aggregate: a player's balance in one
// currency, plus the version that makes a lost update detectable.
//
// Every method that changes the balance takes a value receiver and returns a
// new Wallet. That is deliberate. A rejected debit must leave the caller's
// wallet untouched, and with a pointer receiver "untouched" would be a promise
// the type keeps only as long as every early return remembers to. Here the
// compiler keeps it: a failed call returns the zero Movement and the original
// value is still the original value.
type Wallet struct {
	id        WalletID
	playerID  PlayerID
	currency  Currency
	balance   Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// Movement is a balance change: the wallet as it now is, and the entry that
// records how it got there. They come back together because they are committed
// together — a balance that moved without its ledger entry is exactly the
// corruption the ledger exists to rule out.
type Movement struct {
	Wallet Wallet
	Entry  LedgerEntry
}

// Opening is the result of opening a wallet. Entries holds the initial credit,
// and is empty when the wallet opened at zero: an opening of nothing creates no
// transaction and no ledger entry.
type Opening struct {
	Wallet  Wallet
	Entries []LedgerEntry
}

// OpenWalletParams carries what opening a wallet needs. OpeningEntryID is only
// consumed when InitialBalance is positive.
type OpenWalletParams struct {
	ID             WalletID
	PlayerID       PlayerID
	InitialBalance Money

	// OpeningTransactionID and OpeningEntryID identify the internal OPENING
	// movement. The caller mints them, because the domain does not generate
	// identities.
	OpeningTransactionID TransactionID
	OpeningEntryID       LedgerEntryID

	CreatedAt time.Time
}

// OpenWallet creates a wallet and, when it opens with money, the ledger entry
// that records the initial credit.
//
// The version is 1 in both cases. That looks like a special case and is not:
// the opening credit is part of creating the wallet, not a movement applied to
// an existing one, so there is no earlier version for it to advance from.
// Versions start counting from what the wallet was born as.
func OpenWallet(p OpenWalletParams) (Opening, error) {
	switch {
	case p.ID.IsZero():
		return Opening{}, fail(CodeInvalidIdentifier, "wallet id is empty")
	case p.PlayerID.IsZero():
		return Opening{}, fail(CodeInvalidIdentifier, "player id is empty")
	case p.CreatedAt.IsZero():
		return Opening{}, fail(CodeInvalidTimestamp, "created at is not set")
	case !p.InitialBalance.IsInitialised():
		return Opening{}, fail(CodeInvalidCurrency, "initial balance is not initialised")
	case p.InitialBalance.IsNegative():
		return Opening{}, fail(CodeNegativeAmount,
			"wallet cannot open at %s", p.InitialBalance)
	}

	at := p.CreatedAt.UTC()
	currency := p.InitialBalance.Currency()
	zero, err := ZeroMoney(currency)
	if err != nil {
		return Opening{}, err
	}

	wallet := Wallet{
		id:        p.ID,
		playerID:  p.PlayerID,
		currency:  currency,
		balance:   p.InitialBalance,
		version:   1,
		createdAt: at,
		updatedAt: at,
	}

	if p.InitialBalance.IsZero() {
		return Opening{Wallet: wallet}, nil
	}

	entry, err := NewLedgerEntry(LedgerEntryParams{
		ID:            p.OpeningEntryID,
		WalletID:      p.ID,
		TransactionID: p.OpeningTransactionID,
		Direction:     Credit,
		Amount:        p.InitialBalance,
		BalanceBefore: zero,
		BalanceAfter:  p.InitialBalance,
		CreatedAt:     at,
	})
	if err != nil {
		return Opening{}, err
	}

	return Opening{Wallet: wallet, Entries: []LedgerEntry{entry}}, nil
}

// RehydrateWalletParams carries a wallet as it was stored.
type RehydrateWalletParams struct {
	ID        WalletID
	PlayerID  PlayerID
	Balance   Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RehydrateWallet rebuilds a wallet from storage.
//
// It is a separate path from [OpenWallet] and it applies no movement, raises no
// transition and emits no event: reading a wallet back must not re-run the
// history that produced it. It still validates, because a row that violates an
// invariant is corruption, and corruption should stop here rather than be
// treated as a balance.
func RehydrateWallet(p RehydrateWalletParams) (Wallet, error) {
	switch {
	case p.ID.IsZero():
		return Wallet{}, fail(CodeInvalidIdentifier, "wallet id is empty")
	case p.PlayerID.IsZero():
		return Wallet{}, fail(CodeInvalidIdentifier, "player id is empty")
	case !p.Balance.IsInitialised():
		return Wallet{}, fail(CodeInvalidCurrency, "balance is not initialised")
	case p.Balance.IsNegative():
		return Wallet{}, fail(CodeInconsistentEntry,
			"stored balance %s is negative", p.Balance)
	case p.Version < 1:
		return Wallet{}, fail(CodeInvalidVersion, "version %d is below 1", p.Version)
	case p.CreatedAt.IsZero():
		return Wallet{}, fail(CodeInvalidTimestamp, "created at is not set")
	case p.UpdatedAt.IsZero():
		return Wallet{}, fail(CodeInvalidTimestamp, "updated at is not set")
	}

	return Wallet{
		id:        p.ID,
		playerID:  p.PlayerID,
		currency:  p.Balance.Currency(),
		balance:   p.Balance,
		version:   p.Version,
		createdAt: p.CreatedAt.UTC(),
		updatedAt: p.UpdatedAt.UTC(),
	}, nil
}

func (w Wallet) ID() WalletID         { return w.id }
func (w Wallet) PlayerID() PlayerID   { return w.playerID }
func (w Wallet) Currency() Currency   { return w.currency }
func (w Wallet) Balance() Money       { return w.balance }
func (w Wallet) Version() int64       { return w.version }
func (w Wallet) CreatedAt() time.Time { return w.createdAt }
func (w Wallet) UpdatedAt() time.Time { return w.updatedAt }

// IsInitialised reports whether the wallet came from a constructor.
func (w Wallet) IsInitialised() bool { return !w.id.IsZero() }

// Debit takes money out of the wallet.
//
// It fails when the balance would go below zero, and the caller's wallet is
// unchanged when it does. This is the rejection a bet gets for insufficient
// funds, and it carries [CodeInsufficientFunds].
func (w Wallet) Debit(entryID LedgerEntryID, txID TransactionID, amount Money, at time.Time) (Movement, error) {
	return w.move(Debit, entryID, txID, amount, at)
}

// Credit puts money into the wallet.
func (w Wallet) Credit(entryID LedgerEntryID, txID TransactionID, amount Money, at time.Time) (Movement, error) {
	return w.move(Credit, entryID, txID, amount, at)
}

func (w Wallet) move(d Direction, entryID LedgerEntryID, txID TransactionID, amount Money, at time.Time) (Movement, error) {
	if !w.IsInitialised() {
		return Movement{}, fail(CodeInvalidIdentifier, "wallet is not initialised")
	}
	if at.IsZero() {
		return Movement{}, fail(CodeInvalidTimestamp, "movement time is not set")
	}
	if !amount.IsInitialised() || !amount.IsPositive() {
		return Movement{}, fail(CodeNonPositiveAmount,
			"movement amount must be positive, got %v", amount)
	}
	if amount.Currency() != w.currency {
		return Movement{}, fail(CodeCurrencyMismatch,
			"cannot move %s on a %s wallet", amount.Currency(), w.currency)
	}

	after, err := applyDirection(d, w.balance, amount)
	if err != nil {
		return Movement{}, err
	}
	if after.IsNegative() {
		return Movement{}, fail(CodeInsufficientFunds,
			"balance %s cannot absorb a debit of %s", w.balance, amount)
	}

	entry, err := NewLedgerEntry(LedgerEntryParams{
		ID:            entryID,
		WalletID:      w.id,
		TransactionID: txID,
		Direction:     d,
		Amount:        amount,
		BalanceBefore: w.balance,
		BalanceAfter:  after,
		CreatedAt:     at,
	})
	if err != nil {
		return Movement{}, err
	}

	moved := w
	moved.balance = after
	moved.version = w.version + 1
	moved.updatedAt = at.UTC()

	return Movement{Wallet: moved, Entry: entry}, nil
}
