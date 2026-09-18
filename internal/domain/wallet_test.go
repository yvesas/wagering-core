package domain

import (
	"errors"
	"testing"
	"time"
)

// openWith opens a wallet at the given balance, failing the test if it cannot.
func openWith(t *testing.T, balance string) Wallet {
	t.Helper()
	o, err := OpenWallet(OpenWalletParams{
		ID:                   walletID(t),
		PlayerID:             playerID(t),
		InitialBalance:       mustParse(t, balance, brl(t)),
		OpeningTransactionID: txID(t),
		OpeningEntryID:       entryID(t),
		CreatedAt:            at(t),
	})
	if err != nil {
		t.Fatalf("OpenWallet(%s): %v", balance, err)
	}
	return o.Wallet
}

func TestOpenWalletWithBalance(t *testing.T) {
	t.Parallel()
	o, err := OpenWallet(OpenWalletParams{
		ID:                   walletID(t),
		PlayerID:             playerID(t),
		InitialBalance:       mustParse(t, "1000.00", brl(t)),
		OpeningTransactionID: txID(t),
		OpeningEntryID:       entryID(t),
		CreatedAt:            at(t),
	})
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}

	if got := o.Wallet.Balance().String(); got != "1000.00" {
		t.Errorf("balance = %s, want 1000.00", got)
	}
	// The opening credit is part of creating the wallet, not a movement applied
	// to an existing one, so there is no earlier version to advance from.
	if o.Wallet.Version() != 1 {
		t.Errorf("version = %d, want 1", o.Wallet.Version())
	}
	if len(o.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(o.Entries))
	}

	e := o.Entries[0]
	if e.Direction() != Credit {
		t.Errorf("direction = %q, want CREDIT", e.Direction())
	}
	if e.BalanceBefore().String() != "0.00" || e.BalanceAfter().String() != "1000.00" {
		t.Errorf("entry balances = %s -> %s", e.BalanceBefore(), e.BalanceAfter())
	}
}

func TestOpenWalletAtZeroCreatesNoEntry(t *testing.T) {
	t.Parallel()
	o, err := OpenWallet(OpenWalletParams{
		ID:                   walletID(t),
		PlayerID:             playerID(t),
		InitialBalance:       mustParse(t, "0.00", brl(t)),
		OpeningTransactionID: txID(t),
		OpeningEntryID:       entryID(t),
		CreatedAt:            at(t),
	})
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	if len(o.Entries) != 0 {
		t.Fatalf("got %d entries, want none: an opening of nothing moves nothing", len(o.Entries))
	}
	if !o.Wallet.Balance().IsZero() {
		t.Errorf("balance = %s, want zero", o.Wallet.Balance())
	}
	if o.Wallet.Version() != 1 {
		t.Errorf("version = %d, want 1", o.Wallet.Version())
	}
}

func TestOpenWalletRejects(t *testing.T) {
	t.Parallel()
	c := brl(t)
	valid := OpenWalletParams{
		ID:                   walletID(t),
		PlayerID:             playerID(t),
		InitialBalance:       mustParse(t, "100.00", c),
		OpeningTransactionID: txID(t),
		OpeningEntryID:       entryID(t),
		CreatedAt:            at(t),
	}
	tests := []struct {
		name     string
		mutate   func(*OpenWalletParams)
		wantCode Code
	}{
		{"no wallet id", func(p *OpenWalletParams) { p.ID = WalletID{} }, CodeInvalidIdentifier},
		{"no player id", func(p *OpenWalletParams) { p.PlayerID = PlayerID{} }, CodeInvalidIdentifier},
		{"no timestamp", func(p *OpenWalletParams) { p.CreatedAt = time.Time{} }, CodeInvalidTimestamp},
		{"uninitialised balance", func(p *OpenWalletParams) { p.InitialBalance = Money{} }, CodeInvalidCurrency},
		{"negative balance", func(p *OpenWalletParams) {
			p.InitialBalance = mustParse(t, "-1.00", c)
		}, CodeNegativeAmount},
		{"missing opening entry id", func(p *OpenWalletParams) {
			p.OpeningEntryID = LedgerEntryID{}
		}, CodeInvalidIdentifier},
		{"missing opening transaction id", func(p *OpenWalletParams) {
			p.OpeningTransactionID = TransactionID{}
		}, CodeInvalidIdentifier},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := valid
			tc.mutate(&p)
			got, err := OpenWallet(p)
			if err == nil {
				t.Fatalf("OpenWallet = %v, want rejection", got)
			}
			var de *Error
			if !errors.As(err, &de) {
				t.Fatalf("error %v is not a *domain.Error", err)
			}
			if de.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q (%v)", de.Code, tc.wantCode, err)
			}
		})
	}
}

func TestOpenWalletAtZeroDoesNotNeedOpeningIDs(t *testing.T) {
	t.Parallel()
	// No entry is produced, so the ids for it are not consumed. Demanding them
	// would make the caller mint identities that nothing ever uses.
	if _, err := OpenWallet(OpenWalletParams{
		ID:             walletID(t),
		PlayerID:       playerID(t),
		InitialBalance: mustParse(t, "0.00", brl(t)),
		CreatedAt:      at(t),
	}); err != nil {
		t.Fatalf("OpenWallet at zero without opening ids: %v", err)
	}
}

func TestDebit(t *testing.T) {
	t.Parallel()
	c := brl(t)
	w := openWith(t, "100.00")

	m, err := w.Debit(entryID(t), txID(t), mustParse(t, "25.00", c), at(t))
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if got := m.Wallet.Balance().String(); got != "75.00" {
		t.Errorf("balance = %s, want 75.00", got)
	}
	if m.Wallet.Version() != 2 {
		t.Errorf("version = %d, want 2", m.Wallet.Version())
	}
	if m.Entry.Direction() != Debit {
		t.Errorf("entry direction = %q", m.Entry.Direction())
	}
	if m.Entry.BalanceAfter() != m.Wallet.Balance() {
		t.Error("entry and wallet disagree about the resulting balance")
	}
}

func TestCredit(t *testing.T) {
	t.Parallel()
	c := brl(t)
	w := openWith(t, "100.00")

	m, err := w.Credit(entryID(t), txID(t), mustParse(t, "25.00", c), at(t))
	if err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if got := m.Wallet.Balance().String(); got != "125.00" {
		t.Errorf("balance = %s, want 125.00", got)
	}
	if m.Wallet.Version() != 2 {
		t.Errorf("version = %d, want 2", m.Wallet.Version())
	}
}

func TestDebitToExactlyZeroIsAllowed(t *testing.T) {
	t.Parallel()
	c := brl(t)
	w := openWith(t, "100.00")

	m, err := w.Debit(entryID(t), txID(t), mustParse(t, "100.00", c), at(t))
	if err != nil {
		t.Fatalf("Debit to zero: %v", err)
	}
	if !m.Wallet.Balance().IsZero() {
		t.Errorf("balance = %s, want zero", m.Wallet.Balance())
	}
}

func TestDebitBeyondBalanceIsRejected(t *testing.T) {
	t.Parallel()
	c := brl(t)
	w := openWith(t, "100.00")

	got, err := w.Debit(entryID(t), txID(t), mustParse(t, "100.01", c), at(t))
	if err == nil {
		t.Fatalf("Debit = %v, want rejection", got)
	}
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("error = %v, want ErrInsufficientFunds", err)
	}
}

func TestRejectedDebitLeavesTheWalletUntouched(t *testing.T) {
	t.Parallel()
	c := brl(t)
	w := openWith(t, "100.00")
	before := w

	if _, err := w.Debit(entryID(t), txID(t), mustParse(t, "500.00", c), at(t)); err == nil {
		t.Fatal("expected the debit to be rejected")
	}

	// The value receiver is what makes this hold without the method having to
	// remember to roll anything back.
	if w != before {
		t.Fatal("the wallet changed despite the rejection")
	}
	if w.Balance().String() != "100.00" || w.Version() != 1 {
		t.Fatalf("wallet is now %s at version %d", w.Balance(), w.Version())
	}
}

func TestMovementRejects(t *testing.T) {
	t.Parallel()
	c := brl(t)
	w := openWith(t, "100.00")

	tests := []struct {
		name     string
		run      func() (Movement, error)
		wantCode Code
	}{
		{"zero amount", func() (Movement, error) {
			return w.Debit(entryID(t), txID(t), mustParse(t, "0.00", c), at(t))
		}, CodeNonPositiveAmount},
		{"negative amount", func() (Movement, error) {
			return w.Credit(entryID(t), txID(t), mustParse(t, "-5.00", c), at(t))
		}, CodeNonPositiveAmount},
		{"uninitialised amount", func() (Movement, error) {
			return w.Credit(entryID(t), txID(t), Money{}, at(t))
		}, CodeNonPositiveAmount},
		{"foreign currency", func() (Movement, error) {
			return w.Credit(entryID(t), txID(t), mustParse(t, "5.00", usd(t)), at(t))
		}, CodeCurrencyMismatch},
		{"no timestamp", func() (Movement, error) {
			return w.Credit(entryID(t), txID(t), mustParse(t, "5.00", c), time.Time{})
		}, CodeInvalidTimestamp},
		{"no entry id", func() (Movement, error) {
			return w.Credit(LedgerEntryID{}, txID(t), mustParse(t, "5.00", c), at(t))
		}, CodeInvalidIdentifier},
		{"uninitialised wallet", func() (Movement, error) {
			var none Wallet
			return none.Credit(entryID(t), txID(t), mustParse(t, "5.00", c), at(t))
		}, CodeInvalidIdentifier},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.run()
			if err == nil {
				t.Fatalf("movement = %v, want rejection", got)
			}
			var de *Error
			if !errors.As(err, &de) {
				t.Fatalf("error %v is not a *domain.Error", err)
			}
			if de.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q (%v)", de.Code, tc.wantCode, err)
			}
		})
	}
}

func TestVersionAdvancesOnlyWithTheBalance(t *testing.T) {
	t.Parallel()
	c := brl(t)
	w := openWith(t, "100.00")
	if w.Version() != 1 {
		t.Fatalf("opened at version %d", w.Version())
	}

	for i, amount := range []string{"10.00", "5.00", "1.00"} {
		m, err := w.Debit(entryID(t), txID(t), mustParse(t, amount, c), at(t))
		if err != nil {
			t.Fatalf("debit %s: %v", amount, err)
		}
		if want := int64(i + 2); m.Wallet.Version() != want {
			t.Fatalf("version = %d, want %d", m.Wallet.Version(), want)
		}
		w = m.Wallet
	}

	// A rejected movement is not a change, so it must not consume a version.
	atVersion := w.Version()
	if _, err := w.Debit(entryID(t), txID(t), mustParse(t, "9999.00", c), at(t)); err == nil {
		t.Fatal("expected rejection")
	}
	if w.Version() != atVersion {
		t.Fatalf("version moved to %d on a rejected debit", w.Version())
	}
}

func TestBalanceAlwaysMatchesTheLedger(t *testing.T) {
	t.Parallel()
	c := brl(t)
	// The property the whole system rests on: the stored balance equals the sum
	// of credits minus debits. Here it is checked over a sequence of movements.
	o, err := OpenWallet(OpenWalletParams{
		ID:                   walletID(t),
		PlayerID:             playerID(t),
		InitialBalance:       mustParse(t, "100.00", c),
		OpeningTransactionID: txID(t),
		OpeningEntryID:       entryID(t),
		CreatedAt:            at(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	w := o.Wallet
	entries := o.Entries

	steps := []struct {
		direction Direction
		amount    string
	}{
		{Debit, "25.00"},
		{Credit, "50.00"},
		{Debit, "0.01"},
		{Credit, "0.99"},
		{Debit, "125.98"},
	}
	for _, s := range steps {
		var m Movement
		var err error
		if s.direction == Debit {
			m, err = w.Debit(entryID(t), txID(t), mustParse(t, s.amount, c), at(t))
		} else {
			m, err = w.Credit(entryID(t), txID(t), mustParse(t, s.amount, c), at(t))
		}
		if err != nil {
			t.Fatalf("%s %s: %v", s.direction, s.amount, err)
		}
		w = m.Wallet
		entries = append(entries, m.Entry)
	}

	total, err := ZeroMoney(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		signed, err := e.SignedAmount()
		if err != nil {
			t.Fatal(err)
		}
		total, err = total.Add(signed)
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != w.Balance() {
		t.Fatalf("ledger sums to %s but the balance is %s", total, w.Balance())
	}
	if w.Balance().String() != "0.00" {
		t.Fatalf("balance = %s, want 0.00", w.Balance())
	}
}

func TestRehydrateWallet(t *testing.T) {
	t.Parallel()
	c := brl(t)
	w, err := RehydrateWallet(RehydrateWalletParams{
		ID:        walletID(t),
		PlayerID:  playerID(t),
		Balance:   mustParse(t, "975.00", c),
		Version:   7,
		CreatedAt: at(t),
		UpdatedAt: at(t),
	})
	if err != nil {
		t.Fatalf("RehydrateWallet: %v", err)
	}

	// Rehydration replays nothing: the version and balance come back exactly as
	// they were stored, with no movement re-applied on the way in.
	if w.Version() != 7 {
		t.Errorf("version = %d, want 7", w.Version())
	}
	if w.Balance().String() != "975.00" {
		t.Errorf("balance = %s, want 975.00", w.Balance())
	}
	if w.Currency() != c {
		t.Error("currency was not taken from the stored balance")
	}
}

func TestRehydrateWalletRejectsCorruptRows(t *testing.T) {
	t.Parallel()
	c := brl(t)
	valid := RehydrateWalletParams{
		ID:        walletID(t),
		PlayerID:  playerID(t),
		Balance:   mustParse(t, "10.00", c),
		Version:   3,
		CreatedAt: at(t),
		UpdatedAt: at(t),
	}
	tests := []struct {
		name     string
		mutate   func(*RehydrateWalletParams)
		wantCode Code
	}{
		{"no wallet id", func(p *RehydrateWalletParams) { p.ID = WalletID{} }, CodeInvalidIdentifier},
		{"no player id", func(p *RehydrateWalletParams) { p.PlayerID = PlayerID{} }, CodeInvalidIdentifier},
		{"uninitialised balance", func(p *RehydrateWalletParams) { p.Balance = Money{} }, CodeInvalidCurrency},
		// A negative stored balance is corruption, not a balance. It stops here
		// rather than being read back as fact.
		{"negative stored balance", func(p *RehydrateWalletParams) {
			p.Balance = mustParse(t, "-0.01", c)
		}, CodeInconsistentEntry},
		{"version zero", func(p *RehydrateWalletParams) { p.Version = 0 }, CodeInvalidVersion},
		{"negative version", func(p *RehydrateWalletParams) { p.Version = -1 }, CodeInvalidVersion},
		{"no created at", func(p *RehydrateWalletParams) { p.CreatedAt = time.Time{} }, CodeInvalidTimestamp},
		{"no updated at", func(p *RehydrateWalletParams) { p.UpdatedAt = time.Time{} }, CodeInvalidTimestamp},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := valid
			tc.mutate(&p)
			got, err := RehydrateWallet(p)
			if err == nil {
				t.Fatalf("RehydrateWallet = %v, want rejection", got)
			}
			var de *Error
			if !errors.As(err, &de) {
				t.Fatalf("error %v is not a *domain.Error", err)
			}
			if de.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q (%v)", de.Code, tc.wantCode, err)
			}
		})
	}
}

func TestOpenAndRehydrateAreDifferentPaths(t *testing.T) {
	t.Parallel()
	c := brl(t)
	// Opening at 1000.00 produces a ledger entry. Rehydrating at 1000.00 must
	// not: reading a wallet back is not re-crediting it.
	opened, err := OpenWallet(OpenWalletParams{
		ID:                   walletID(t),
		PlayerID:             playerID(t),
		InitialBalance:       mustParse(t, "1000.00", c),
		OpeningTransactionID: txID(t),
		OpeningEntryID:       entryID(t),
		CreatedAt:            at(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Entries) != 1 {
		t.Fatalf("opening produced %d entries, want 1", len(opened.Entries))
	}

	rehydrated, err := RehydrateWallet(RehydrateWalletParams{
		ID:        walletID(t),
		PlayerID:  playerID(t),
		Balance:   mustParse(t, "1000.00", c),
		Version:   1,
		CreatedAt: at(t),
		UpdatedAt: at(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rehydrated != opened.Wallet {
		t.Fatal("the two paths should agree on the wallet they describe")
	}
}
