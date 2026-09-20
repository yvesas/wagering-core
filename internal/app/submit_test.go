package app

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// testReferencePolicy waits briefly, so a test that exercises the deadline does
// not spend two minutes doing it.
var testReferencePolicy = ReferencePolicy{
	MaxAttempts: 3,
	BaseBackoff: time.Millisecond,
	MaxBackoff:  2 * time.Millisecond,
	TTL:         50 * time.Millisecond,
	BatchSize:   10,
	Interval:    time.Millisecond,
}

type submitFixture struct {
	submit *SubmitTransaction
	store  *memoryStore
	wallet domain.Wallet
	clock  *movableClock

	// metrics is what the fixture recorded, so a test can assert on the numbers
	// an operator would see.
	metrics *recordingMetrics

	// ids is the fixture's own generator. A second one would start over and
	// mint identifiers this store already holds.
	ids *fakeIDs
}

// newSubmitFixture opens a wallet through the real use case, so what the submit
// tests run against is a wallet the system itself produced.
func newSubmitFixture(t *testing.T, balance string) submitFixture {
	t.Helper()
	return newSubmitFixtureWithLogger(t, balance, discardLogger())
}

// newSubmitFixtureWithLogger is newSubmitFixture with somewhere to read the log
// lines back from, for the tests that are about what gets written.
func newSubmitFixtureWithLogger(t *testing.T, balance string, logger *slog.Logger) submitFixture {
	t.Helper()
	store := &memoryStore{}
	ids := &fakeIDs{}
	clock := newMovableClock(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))
	uow := &memoryUnitOfWork{store: store}
	queries := &memoryQueries{store: store}

	recorder := newRecordingMetrics()

	wallet, err := NewOpenWallet(uow, ids, clock).Execute(callerContext(), OpenWalletCommand{
		PlayerID: "player-1",
		Amount:   balance,
		Currency: "BRL",
	})
	if err != nil {
		t.Fatalf("opening the wallet: %v", err)
	}

	return submitFixture{
		submit:  NewSubmitTransaction(uow, queries, ids, clock, testReferencePolicy, recorder, logger),
		store:   store,
		wallet:  wallet,
		clock:   clock,
		ids:     ids,
		metrics: recorder,
	}
}

func (f submitFixture) command(kind, amount, external string) SubmitCommand {
	return SubmitCommand{
		IdempotencyKey: "provider-a:" + external,
		ProviderID:     "provider-a",
		ExternalID:     external,
		PlayerID:       f.wallet.PlayerID().String(),
		WalletID:       f.wallet.ID().String(),
		RoundID:        "round-987",
		GameID:         "fortune-chimp",
		Kind:           kind,
		Amount:         amount,
		Currency:       "BRL",
	}
}

func (f submitFixture) storedWallet(t *testing.T) domain.Wallet {
	t.Helper()
	w, err := (&memoryQueries{store: f.store}).Wallets().FindByID(callerContext(), f.wallet.ID())
	if err != nil {
		t.Fatalf("reading the wallet back: %v", err)
	}
	return w
}

func TestSubmitBetDebits(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	result, err := f.submit.Execute(callerContext(), f.command("BET", "25.00", "tx-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Transaction.Status() != domain.StatusProcessed {
		t.Errorf("status = %q", result.Transaction.Status())
	}
	if result.Balance.String() != "75.00" {
		t.Errorf("balance = %s, want 75.00", result.Balance)
	}
	if result.Replay {
		t.Error("a first submission reported itself as a replay")
	}
	if got := f.storedWallet(t); got.Balance().String() != "75.00" || got.Version() != 2 {
		t.Errorf("stored wallet is %s at version %d", got.Balance(), got.Version())
	}
	// The opening credit plus this debit.
	if len(f.store.entries) != 2 {
		t.Errorf("got %d ledger entries, want 2", len(f.store.entries))
	}
}

func TestSubmitWinCredits(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	result, err := f.submit.Execute(callerContext(), f.command("WIN", "50.00", "tx-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Balance.String() != "150.00" {
		t.Errorf("balance = %s, want 150.00", result.Balance)
	}
}

func TestSubmitLossMovesNothing(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	before := f.storedWallet(t)

	result, err := f.submit.Execute(callerContext(), f.command("LOSS", "0.00", "tx-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Transaction.Status() != domain.StatusProcessed {
		t.Errorf("status = %q", result.Transaction.Status())
	}
	after := f.storedWallet(t)
	// No entry, no version bump, and the balance reported is the one it had.
	if after.Version() != before.Version() {
		t.Errorf("version moved from %d to %d", before.Version(), after.Version())
	}
	if len(f.store.entries) != 1 {
		t.Errorf("a LOSS produced a ledger entry: %d entries", len(f.store.entries))
	}
	if result.Balance.String() != "100.00" {
		t.Errorf("balance = %s", result.Balance)
	}
}

func TestSubmitRejectsNonZeroLoss(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	_, err := f.submit.Execute(callerContext(), f.command("LOSS", "25.00", "tx-1"))
	if !errors.Is(err, domain.ErrInvalidAmountForKind) {
		t.Fatalf("error = %v, want ErrInvalidAmountForKind", err)
	}
}

func TestInsufficientFundsIsARecordedRejection(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	result, err := f.submit.Execute(callerContext(), f.command("BET", "100.01", "tx-1"))
	// Not an error: the refusal is the result, and it is committed so a resend
	// reads it instead of trying again.
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Transaction.Status() != domain.StatusRejected {
		t.Fatalf("status = %q, want REJECTED", result.Transaction.Status())
	}
	if result.Transaction.FailureCode() != domain.CodeInsufficientFunds {
		t.Errorf("failure code = %q", result.Transaction.FailureCode())
	}
	if f.storedWallet(t).Balance().String() != "100.00" {
		t.Error("a rejected bet moved the balance")
	}
	if len(f.store.entries) != 1 {
		t.Error("a rejected bet produced a ledger entry")
	}
}

func TestReplayReturnsTheStoredResult(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	cmd := f.command("BET", "25.00", "tx-1")

	first, err := f.submit.Execute(callerContext(), cmd)
	if err != nil {
		t.Fatal(err)
	}

	second, err := f.submit.Execute(callerContext(), cmd)
	if err != nil {
		t.Fatalf("the resend failed: %v", err)
	}
	if !second.Replay {
		t.Error("the resend was not reported as a replay")
	}
	if second.Transaction.ID() != first.Transaction.ID() {
		t.Error("the resend produced a second transaction")
	}
	if f.storedWallet(t).Balance().String() != "75.00" {
		t.Error("the resend moved the balance a second time")
	}
	if len(f.store.entries) != 2 {
		t.Errorf("the resend produced another ledger entry: %d entries", len(f.store.entries))
	}
}

func TestReplayReturnsTheOriginalBalanceNotTheCurrentOne(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	bet := f.command("BET", "25.00", "tx-1")

	if _, err := f.submit.Execute(callerContext(), bet); err != nil {
		t.Fatal(err)
	}
	// Something else moves the wallet in between.
	if _, err := f.submit.Execute(callerContext(), f.command("WIN", "500.00", "tx-2")); err != nil {
		t.Fatal(err)
	}
	if got := f.storedWallet(t).Balance().String(); got != "575.00" {
		t.Fatalf("the wallet is at %s, the test needs it to have moved", got)
	}

	replay, err := f.submit.Execute(callerContext(), bet)
	if err != nil {
		t.Fatal(err)
	}
	// The easy implementation returns the wallet's balance now and passes every
	// happy-path test. This is where it is wrong.
	if replay.Balance.String() != "75.00" {
		t.Fatalf("replay returned %s, want the 75.00 observed at the time", replay.Balance)
	}
}

func TestReplayOfARejectionIsStillARejection(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	cmd := f.command("BET", "500.00", "tx-1")

	if _, err := f.submit.Execute(callerContext(), cmd); err != nil {
		t.Fatal(err)
	}

	replay, err := f.submit.Execute(callerContext(), cmd)
	if err != nil {
		t.Fatalf("the resend failed: %v", err)
	}
	// A resend that suddenly succeeded would tell a provider its bet went
	// through. The stored outcome is the answer, forever.
	if replay.Transaction.Status() != domain.StatusRejected {
		t.Fatalf("status = %q, want REJECTED", replay.Transaction.Status())
	}
	if !replay.Replay {
		t.Error("not reported as a replay")
	}
}

func TestSameKeyWithDifferentContentConflicts(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "1000.00")

	first := f.command("BET", "25.00", "tx-1")
	if _, err := f.submit.Execute(callerContext(), first); err != nil {
		t.Fatal(err)
	}

	// Same key, different operation entirely.
	second := f.command("BET", "999.00", "tx-2")
	second.IdempotencyKey = first.IdempotencyKey

	_, err := f.submit.Execute(callerContext(), second)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if f.storedWallet(t).Balance().String() != "975.00" {
		t.Error("the conflicting submission moved the balance")
	}
}

func TestSameOperationUnderAnotherKeyConflicts(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "1000.00")

	first := f.command("BET", "25.00", "tx-1")
	if _, err := f.submit.Execute(callerContext(), first); err != nil {
		t.Fatal(err)
	}

	// The pair (provider, externalId) identifies the operation, so resubmitting
	// it with a fresh key is not a new operation -- it is the same money.
	again := f.command("BET", "25.00", "tx-1")
	again.IdempotencyKey = "provider-a:a-brand-new-key"

	_, err := f.submit.Execute(callerContext(), again)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if f.storedWallet(t).Balance().String() != "975.00" {
		t.Error("the second key applied the operation again")
	}
}

func TestSameExternalIDWithDifferentContentConflicts(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "1000.00")

	if _, err := f.submit.Execute(callerContext(), f.command("BET", "25.00", "tx-1")); err != nil {
		t.Fatal(err)
	}

	// Same key, same external id, different amount: the content changed under
	// an identity that is supposed to be fixed.
	changed := f.command("BET", "26.00", "tx-1")

	_, err := f.submit.Execute(callerContext(), changed)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

func TestIdempotencyDoesNotDependOnProcessMemory(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	cmd := f.command("BET", "25.00", "tx-1")

	if _, err := f.submit.Execute(callerContext(), cmd); err != nil {
		t.Fatal(err)
	}

	// A brand new use case over the same store: nothing carried over in RAM,
	// which is what a restart looks like from the code's point of view. The
	// integration test does the real thing with a real database.
	restarted := NewSubmitTransaction(
		&memoryUnitOfWork{store: f.store},
		&memoryQueries{store: f.store},
		&fakeIDs{},
		f.clock,
		testReferencePolicy, newRecordingMetrics(), discardLogger(),
	)

	replay, err := restarted.Execute(callerContext(), cmd)
	if err != nil {
		t.Fatalf("after a restart: %v", err)
	}
	if !replay.Replay {
		t.Error("the restarted process did not recognise the operation")
	}
	if f.storedWallet(t).Balance().String() != "75.00" {
		t.Error("the restarted process applied the operation again")
	}
}

func TestSubmitRejectsAWalletThatBelongsToSomeoneElse(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	cmd := f.command("BET", "25.00", "tx-1")
	cmd.PlayerID = "a-different-player"

	_, err := f.submit.Execute(callerContext(), cmd)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	if f.storedWallet(t).Balance().String() != "100.00" {
		t.Error("the balance moved")
	}
}

func TestSubmitRejectsAnUnknownWallet(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	cmd := f.command("BET", "25.00", "tx-1")
	cmd.WalletID = "no-such-wallet"

	if _, err := f.submit.Execute(callerContext(), cmd); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestSubmitRejectsBadInput(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*SubmitCommand){
		"no idempotency key": func(c *SubmitCommand) { c.IdempotencyKey = "" },
		"no provider":        func(c *SubmitCommand) { c.ProviderID = "" },
		"no external id":     func(c *SubmitCommand) { c.ExternalID = "" },
		"no round":           func(c *SubmitCommand) { c.RoundID = "" },
		"no game":            func(c *SubmitCommand) { c.GameID = "" },
		"unknown kind":       func(c *SubmitCommand) { c.Kind = "TRANSFER" },
		"excess scale":       func(c *SubmitCommand) { c.Amount = "25.000" },
		"negative amount":    func(c *SubmitCommand) { c.Amount = "-25.00" },
		"lowercase currency": func(c *SubmitCommand) { c.Currency = "brl" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSubmitFixture(t, "100.00")
			cmd := f.command("BET", "25.00", "tx-1")
			mutate(&cmd)

			if _, err := f.submit.Execute(callerContext(), cmd); err == nil {
				t.Fatal("want a rejection")
			}
			if len(f.store.transactions) != 1 {
				t.Errorf("a malformed command was recorded: %d transactions", len(f.store.transactions))
			}
		})
	}
}

func TestConcurrentDuplicatesProduceOneMovement(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	cmd := f.command("BET", "25.00", "tx-1")

	// The lookup before the insert is an optimisation; the constraint is the
	// guarantee. Forcing the lookup to miss simulates the window where two
	// copies both find nothing -- the insert has to be what refuses the second.
	f.store.transactions = nil

	first, err := f.submit.Execute(callerContext(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replay {
		t.Error("the first submission reported a replay")
	}

	second, err := f.submit.Execute(callerContext(), cmd)
	if err != nil {
		t.Fatalf("the racing duplicate failed instead of replaying: %v", err)
	}
	if !second.Replay {
		t.Error("the racing duplicate was not resolved into a replay")
	}
	if f.storedWallet(t).Balance().String() != "75.00" {
		t.Errorf("balance = %s: the duplicate moved money", f.storedWallet(t).Balance())
	}
}
