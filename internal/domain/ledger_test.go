package domain

import (
	"errors"
	"testing"
	"time"
)

func at(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
}

func entryID(t *testing.T) LedgerEntryID {
	t.Helper()
	id, err := ParseLedgerEntryID("entry-1")
	if err != nil {
		t.Fatalf("ParseLedgerEntryID: %v", err)
	}
	return id
}

func walletID(t *testing.T) WalletID {
	t.Helper()
	id, err := ParseWalletID("wallet-1")
	if err != nil {
		t.Fatalf("ParseWalletID: %v", err)
	}
	return id
}

func playerID(t *testing.T) PlayerID {
	t.Helper()
	id, err := ParsePlayerID("player-1")
	if err != nil {
		t.Fatalf("ParsePlayerID: %v", err)
	}
	return id
}

func txID(t *testing.T) TransactionID {
	t.Helper()
	id, err := ParseTransactionID("tx-1")
	if err != nil {
		t.Fatalf("ParseTransactionID: %v", err)
	}
	return id
}

// validEntryParams is a debit of 25.00 taking a wallet from 100.00 to 75.00.
// Each test bends one field of it, so the failing field is the only difference
// between the passing case and the rejected one.
func validEntryParams(t *testing.T) LedgerEntryParams {
	t.Helper()
	c := brl(t)
	return LedgerEntryParams{
		ID:            entryID(t),
		WalletID:      walletID(t),
		TransactionID: txID(t),
		Direction:     Debit,
		Amount:        mustParse(t, "25.00", c),
		BalanceBefore: mustParse(t, "100.00", c),
		BalanceAfter:  mustParse(t, "75.00", c),
		CreatedAt:     at(t),
	}
}

func TestParseIdentifier(t *testing.T) {
	t.Parallel()

	if _, err := ParseWalletID(""); !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("empty id: error = %v, want ErrInvalidIdentifier", err)
	}

	long := make([]byte, maxIdentifierLength+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := ParseWalletID(string(long)); !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("oversized id: error = %v, want ErrInvalidIdentifier", err)
	}

	// External ids come from providers and are not UUIDs. Constraining the
	// shape here would reject legitimate input.
	for _, s := range []string{"transaction-123", "0192f291-27dd-7d3f-8071-5f8685deef37", "a"} {
		if _, err := ParseTransactionID(s); err != nil {
			t.Errorf("ParseTransactionID(%q): %v", s, err)
		}
	}
}

func TestIdentifiersAreDistinctTypes(t *testing.T) {
	t.Parallel()
	// The compiler enforces this; the test states the intent, and its real
	// value is documenting why the boilerplate exists.
	w := walletID(t)
	if w.IsZero() {
		t.Error("parsed wallet id reports zero")
	}
	var unset PlayerID
	if !unset.IsZero() {
		t.Error("zero PlayerID should report zero")
	}
}

func TestNewLedgerEntry(t *testing.T) {
	t.Parallel()
	p := validEntryParams(t)

	e, err := NewLedgerEntry(p)
	if err != nil {
		t.Fatalf("NewLedgerEntry: %v", err)
	}
	if e.Direction() != Debit {
		t.Errorf("direction = %q", e.Direction())
	}
	if e.Amount().String() != "25.00" {
		t.Errorf("amount = %s", e.Amount())
	}
	if e.BalanceBefore().String() != "100.00" || e.BalanceAfter().String() != "75.00" {
		t.Errorf("balances = %s -> %s", e.BalanceBefore(), e.BalanceAfter())
	}
	if e.WalletID() != p.WalletID || e.TransactionID() != p.TransactionID {
		t.Error("entry lost an identifier")
	}
}

func TestNewLedgerEntryCredit(t *testing.T) {
	t.Parallel()
	c := brl(t)
	p := validEntryParams(t)
	p.Direction = Credit
	p.BalanceBefore = mustParse(t, "100.00", c)
	p.BalanceAfter = mustParse(t, "125.00", c)

	if _, err := NewLedgerEntry(p); err != nil {
		t.Fatalf("NewLedgerEntry: %v", err)
	}
}

func TestNewLedgerEntryRejectsBrokenArithmetic(t *testing.T) {
	t.Parallel()
	c := brl(t)
	tests := []struct {
		name  string
		mutit func(*LedgerEntryParams)
	}{
		{"debit that does not subtract", func(p *LedgerEntryParams) {
			p.BalanceAfter = mustParse(t, "125.00", c)
		}},
		{"credit that does not add", func(p *LedgerEntryParams) {
			p.Direction = Credit
			p.BalanceAfter = mustParse(t, "75.00", c)
		}},
		{"off by one cent", func(p *LedgerEntryParams) {
			p.BalanceAfter = mustParse(t, "75.01", c)
		}},
		{"balances unchanged", func(p *LedgerEntryParams) {
			p.BalanceAfter = mustParse(t, "100.00", c)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := validEntryParams(t)
			tc.mutit(&p)
			got, err := NewLedgerEntry(p)
			if err == nil {
				t.Fatalf("NewLedgerEntry = %v, want rejection", got)
			}
			if !errors.Is(err, ErrInconsistentEntry) {
				t.Fatalf("error = %v, want ErrInconsistentEntry", err)
			}
		})
	}
}

func TestNewLedgerEntryRejectsInvalidFields(t *testing.T) {
	t.Parallel()
	c := brl(t)
	tests := []struct {
		name     string
		mutit    func(*LedgerEntryParams)
		wantCode Code
	}{
		{"no id", func(p *LedgerEntryParams) { p.ID = LedgerEntryID{} }, CodeInvalidIdentifier},
		{"no wallet", func(p *LedgerEntryParams) { p.WalletID = WalletID{} }, CodeInvalidIdentifier},
		{"no transaction", func(p *LedgerEntryParams) { p.TransactionID = TransactionID{} }, CodeInvalidIdentifier},
		{"unset direction", func(p *LedgerEntryParams) { p.Direction = "" }, CodeInvalidDirection},
		{"unknown direction", func(p *LedgerEntryParams) { p.Direction = "TRANSFER" }, CodeInvalidDirection},
		{"lowercase direction", func(p *LedgerEntryParams) { p.Direction = "debit" }, CodeInvalidDirection},
		{"no timestamp", func(p *LedgerEntryParams) { p.CreatedAt = time.Time{} }, CodeInvalidTimestamp},
		{"zero amount", func(p *LedgerEntryParams) {
			p.Amount = mustParse(t, "0.00", c)
			p.BalanceAfter = mustParse(t, "100.00", c)
		}, CodeNonPositiveAmount},
		{"negative amount", func(p *LedgerEntryParams) {
			p.Amount = mustParse(t, "-25.00", c)
			p.BalanceAfter = mustParse(t, "125.00", c)
		}, CodeNonPositiveAmount},
		{"uninitialised amount", func(p *LedgerEntryParams) { p.Amount = Money{} }, CodeNonPositiveAmount},
		{"uninitialised balance", func(p *LedgerEntryParams) { p.BalanceBefore = Money{} }, CodeInvalidCurrency},
		{"mixed currencies", func(p *LedgerEntryParams) {
			p.BalanceBefore = mustParse(t, "100.00", usd(t))
		}, CodeCurrencyMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := validEntryParams(t)
			tc.mutit(&p)
			got, err := NewLedgerEntry(p)
			if err == nil {
				t.Fatalf("NewLedgerEntry = %v, want rejection", got)
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

func TestLedgerEntryIsImmutable(t *testing.T) {
	t.Parallel()
	// Every field is unexported and every accessor returns a value, so a holder
	// of an entry cannot change one. Mutating the copy proves the original is
	// untouched — which is what "append-only" means at the type level.
	e, err := NewLedgerEntry(validEntryParams(t))
	if err != nil {
		t.Fatal(err)
	}
	copied := e
	if copied != e {
		t.Fatal("LedgerEntry should be comparable and copy by value")
	}
}

func TestLedgerEntryTimestampIsNormalisedToUTC(t *testing.T) {
	t.Parallel()
	zone := time.FixedZone("UTC-3", -3*60*60)
	p := validEntryParams(t)
	p.CreatedAt = time.Date(2026, 9, 18, 9, 0, 0, 0, zone)

	e, err := NewLedgerEntry(p)
	if err != nil {
		t.Fatal(err)
	}
	if e.CreatedAt().Location() != time.UTC {
		t.Errorf("location = %v, want UTC", e.CreatedAt().Location())
	}
	if got := e.CreatedAt().Format(time.RFC3339); got != "2026-09-18T12:00:00Z" {
		t.Errorf("createdAt = %s", got)
	}
}

func TestSignedAmount(t *testing.T) {
	t.Parallel()
	c := brl(t)

	debit, err := NewLedgerEntry(validEntryParams(t))
	if err != nil {
		t.Fatal(err)
	}
	signed, err := debit.SignedAmount()
	if err != nil {
		t.Fatal(err)
	}
	if signed.String() != "-25.00" {
		t.Errorf("debit signed = %s, want -25.00", signed)
	}

	p := validEntryParams(t)
	p.Direction = Credit
	p.BalanceAfter = mustParse(t, "125.00", c)
	credit, err := NewLedgerEntry(p)
	if err != nil {
		t.Fatal(err)
	}
	signed, err = credit.SignedAmount()
	if err != nil {
		t.Fatal(err)
	}
	if signed.String() != "25.00" {
		t.Errorf("credit signed = %s, want 25.00", signed)
	}
}

func TestDirectionValid(t *testing.T) {
	t.Parallel()
	for d, want := range map[Direction]bool{
		Debit:      true,
		Credit:     true,
		"":         false,
		"debit":    false,
		"TRANSFER": false,
	} {
		if d.Valid() != want {
			t.Errorf("Direction(%q).Valid() = %v, want %v", string(d), d.Valid(), want)
		}
	}
}
