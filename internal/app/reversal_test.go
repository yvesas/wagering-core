package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// reversalOf builds a reversal pointing at an operation's external id.
func (f submitFixture) reversalOf(kind, amount, external, reference string) SubmitCommand {
	cmd := f.command(kind, amount, external)
	cmd.ReferenceExternalID = reference
	return cmd
}

func (f submitFixture) worker() *ReferenceWorker {
	return NewReferenceWorker(
		&memoryUnitOfWork{store: f.store},
		&fakeIDs{},
		f.clock,
		testReferencePolicy,
		discardLogger(),
	)
}

func TestRefundReturnsABet(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	if _, err := f.submit.Execute(context.Background(), f.command("BET", "25.00", "bet-1")); err != nil {
		t.Fatal(err)
	}
	if got := f.storedWallet(t).Balance().String(); got != "75.00" {
		t.Fatalf("after the bet the balance is %s", got)
	}

	result, err := f.submit.Execute(context.Background(),
		f.reversalOf("REFUND", "25.00", "refund-1", "bet-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Transaction.Status() != domain.StatusProcessed {
		t.Fatalf("status = %q", result.Transaction.Status())
	}
	// A bet debits, so undoing it credits. The direction is derived from the
	// reference, never configured.
	if result.Balance.String() != "100.00" {
		t.Fatalf("balance = %s, want 100.00", result.Balance)
	}
	if result.Transaction.ResolvedReferenceID().IsZero() {
		t.Error("the reversal did not record what it reversed")
	}
}

func TestRollbackAppliesTheOppositeMovement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		originKind  string
		originStake string
		wantAfter   string
	}{
		// A bet debited, so rolling it back credits.
		{"of a bet", "BET", "25.00", "100.00"},
		// A win credited, so rolling it back debits.
		{"of a win", "WIN", "25.00", "100.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSubmitFixture(t, "100.00")

			if _, err := f.submit.Execute(context.Background(),
				f.command(tc.originKind, tc.originStake, "origin-1")); err != nil {
				t.Fatal(err)
			}

			result, err := f.submit.Execute(context.Background(),
				f.reversalOf("ROLLBACK", tc.originStake, "rollback-1", "origin-1"))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if result.Transaction.Status() != domain.StatusProcessed {
				t.Fatalf("status = %q, failure %q",
					result.Transaction.Status(), result.Transaction.FailureCode())
			}
			if result.Balance.String() != tc.wantAfter {
				t.Fatalf("balance = %s, want %s", result.Balance, tc.wantAfter)
			}
		})
	}
}

func TestTheSameDebitIsNotReturnedTwice(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	if _, err := f.submit.Execute(context.Background(), f.command("BET", "25.00", "bet-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.submit.Execute(context.Background(),
		f.reversalOf("REFUND", "25.00", "refund-1", "bet-1")); err != nil {
		t.Fatal(err)
	}
	if got := f.storedWallet(t).Balance().String(); got != "100.00" {
		t.Fatalf("after the refund the balance is %s", got)
	}

	// A rollback of the same bet is a different *type* of reversal, which is
	// exactly why "no two of the same type" is not enough: this would hand back
	// the same 25.00 a second time.
	result, err := f.submit.Execute(context.Background(),
		f.reversalOf("ROLLBACK", "25.00", "rollback-1", "bet-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Transaction.Status() != domain.StatusRejected {
		t.Fatalf("status = %q, want REJECTED", result.Transaction.Status())
	}
	if result.Transaction.FailureCode() != domain.CodeAlreadyReversed {
		t.Errorf("failure code = %q", result.Transaction.FailureCode())
	}
	if got := f.storedWallet(t).Balance().String(); got != "100.00" {
		t.Fatalf("the second reversal moved money: balance is %s", got)
	}
}

func TestReversingARefundIsNotReversingItsBetAgain(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// BET -> REFUND -> ROLLBACK(of the refund). Each operation is reversed
	// once, and the balance ends where a bet that was never refunded would
	// leave it. This is the chain the rule has to keep allowing.
	if _, err := f.submit.Execute(context.Background(), f.command("BET", "25.00", "bet-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.submit.Execute(context.Background(),
		f.reversalOf("REFUND", "25.00", "refund-1", "bet-1")); err != nil {
		t.Fatal(err)
	}

	result, err := f.submit.Execute(context.Background(),
		f.reversalOf("ROLLBACK", "25.00", "rollback-1", "refund-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Transaction.Status() != domain.StatusProcessed {
		t.Fatalf("status = %q, failure %q",
			result.Transaction.Status(), result.Transaction.FailureCode())
	}
	// The refund credited 25.00, so rolling it back debits them again.
	if result.Balance.String() != "75.00" {
		t.Fatalf("balance = %s, want 75.00", result.Balance)
	}
}

func TestReversalRejections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		setUp    func(*testing.T, submitFixture)
		reversal func(submitFixture) SubmitCommand
		wantCode domain.Code
	}{
		{
			name: "a refund cannot return a win",
			setUp: func(t *testing.T, f submitFixture) {
				mustSubmit(t, f, f.command("WIN", "25.00", "origin-1"))
			},
			reversal: func(f submitFixture) SubmitCommand {
				return f.reversalOf("REFUND", "25.00", "refund-1", "origin-1")
			},
			wantCode: domain.CodeReferenceNotReversible,
		},
		{
			name: "a loss moved nothing to undo",
			setUp: func(t *testing.T, f submitFixture) {
				mustSubmit(t, f, f.command("LOSS", "0.00", "origin-1"))
			},
			reversal: func(f submitFixture) SubmitCommand {
				// A rollback always carries a positive amount, so this cannot
				// be built to "match" a loss. The kind check fires first, which
				// is the right answer: a loss moved nothing to undo.
				return f.reversalOf("ROLLBACK", "25.00", "rollback-1", "origin-1")
			},
			wantCode: domain.CodeReferenceNotReversible,
		},
		{
			name: "a rejected operation never moved money",
			setUp: func(t *testing.T, f submitFixture) {
				// 500.00 against 100.00: recorded as REJECTED.
				mustSubmit(t, f, f.command("BET", "500.00", "origin-1"))
			},
			reversal: func(f submitFixture) SubmitCommand {
				return f.reversalOf("REFUND", "500.00", "refund-1", "origin-1")
			},
			wantCode: domain.CodeReferenceNotReversible,
		},
		{
			name: "partial reversals are out of scope",
			setUp: func(t *testing.T, f submitFixture) {
				mustSubmit(t, f, f.command("BET", "25.00", "origin-1"))
			},
			reversal: func(f submitFixture) SubmitCommand {
				return f.reversalOf("REFUND", "10.00", "refund-1", "origin-1")
			},
			wantCode: domain.CodeReferenceAmountMismatch,
		},
		{
			name: "the reference belongs to another round",
			setUp: func(t *testing.T, f submitFixture) {
				mustSubmit(t, f, f.command("BET", "25.00", "origin-1"))
			},
			reversal: func(f submitFixture) SubmitCommand {
				cmd := f.reversalOf("REFUND", "25.00", "refund-1", "origin-1")
				cmd.RoundID = "a-different-round"
				return cmd
			},
			wantCode: domain.CodeReferenceMismatch,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSubmitFixture(t, "100.00")
			tc.setUp(t, f)
			before := f.storedWallet(t).Balance().String()

			result, err := f.submit.Execute(context.Background(), tc.reversal(f))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if result.Transaction.Status() != domain.StatusRejected {
				t.Fatalf("status = %q, want REJECTED", result.Transaction.Status())
			}
			if result.Transaction.FailureCode() != tc.wantCode {
				t.Fatalf("failure code = %q, want %q",
					result.Transaction.FailureCode(), tc.wantCode)
			}
			if after := f.storedWallet(t).Balance().String(); after != before {
				t.Errorf("a rejected reversal moved the balance from %s to %s", before, after)
			}
		})
	}
}

func TestAReversalThatDoesNotFitHasItsOwnCode(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "0.00")

	// The player wins, then spends it. Rolling back the win now needs a debit
	// the wallet cannot cover.
	mustSubmit(t, f, f.command("WIN", "50.00", "win-1"))
	mustSubmit(t, f, f.command("BET", "50.00", "bet-1"))
	if got := f.storedWallet(t).Balance().String(); got != "0.00" {
		t.Fatalf("balance is %s", got)
	}

	result, err := f.submit.Execute(context.Background(),
		f.reversalOf("ROLLBACK", "50.00", "rollback-1", "win-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Transaction.Status() != domain.StatusRejected {
		t.Fatalf("status = %q", result.Transaction.Status())
	}
	// Deliberately not INSUFFICIENT_FUNDS. A bet without balance is a player
	// hitting their limit and is routine; this is money already handed over
	// that cannot be taken back, and it needs a person to look.
	if result.Transaction.FailureCode() != domain.CodeReversalExceedsBalance {
		t.Fatalf("failure code = %q, want %q",
			result.Transaction.FailureCode(), domain.CodeReversalExceedsBalance)
	}
	if result.Transaction.FailureCode() == domain.CodeInsufficientFunds {
		t.Fatal("the reversal shares a code with an ordinary rejected bet")
	}
}

func TestAReversalThatArrivesFirstWaits(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// The refund overtakes the bet it undoes. At-least-once delivery has no
	// order, so this is expected rather than exceptional.
	result, err := f.submit.Execute(context.Background(),
		f.reversalOf("REFUND", "25.00", "refund-1", "bet-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Transaction.Status() != domain.StatusPendingReference {
		t.Fatalf("status = %q, want PENDING_REFERENCE", result.Transaction.Status())
	}
	if result.Transaction.ReferenceAttempts() != 1 {
		t.Errorf("attempts = %d, want 1", result.Transaction.ReferenceAttempts())
	}
	if result.Transaction.ReferenceDeadlineAt().IsZero() {
		t.Error("the wait has no deadline")
	}
	if f.storedWallet(t).Balance().String() != "100.00" {
		t.Error("a waiting reversal moved money")
	}
}

func TestTheWorkerResolvesAWaitingReversal(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// It waits...
	waiting, err := f.submit.Execute(context.Background(),
		f.reversalOf("REFUND", "25.00", "refund-1", "bet-1"))
	if err != nil {
		t.Fatal(err)
	}

	// ...the worker finds nothing to do while the bet is still missing...
	worker := f.worker()
	// Far enough for the first attempt to come due, well short of the TTL.
	f.clock.advance(5 * time.Millisecond)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if statusOf(t, f, waiting.Transaction.ID()) != domain.StatusPendingReference {
		t.Fatal("the worker resolved a reversal whose reference had not arrived")
	}

	// ...then the bet lands...
	mustSubmit(t, f, f.command("BET", "25.00", "bet-1"))

	// ...and the next pass applies it.
	f.clock.advance(5 * time.Millisecond)
	handled, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if handled == 0 {
		t.Fatal("the worker did no work")
	}

	if got := statusOf(t, f, waiting.Transaction.ID()); got != domain.StatusProcessed {
		t.Fatalf("status = %q, want PROCESSED", got)
	}
	// The bet debited 25.00 and the refund put it back.
	if got := f.storedWallet(t).Balance().String(); got != "100.00" {
		t.Fatalf("balance = %s, want 100.00", got)
	}
}

func TestTheWaitEndsWhenTheDeadlinePasses(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	waiting, err := f.submit.Execute(context.Background(),
		f.reversalOf("REFUND", "25.00", "refund-1", "never-arrives"))
	if err != nil {
		t.Fatal(err)
	}

	// The reference never shows up. The worker keeps looking until the deadline
	// or the attempt budget runs out, whichever comes first.
	// Time is pushed forward rather than slept through: the wait is bounded by
	// a deadline and an attempt budget, and a test that waited for real would
	// be both slow and timing-dependent.
	worker := f.worker()
	for i := 0; i < 20; i++ {
		f.clock.advance(10 * time.Millisecond)
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if statusOf(t, f, waiting.Transaction.ID()) == domain.StatusRejected {
			break
		}
	}

	stored := transactionOf(t, f, waiting.Transaction.ID())
	if stored.Status() != domain.StatusRejected {
		t.Fatalf("status = %q, want REJECTED after the wait expired", stored.Status())
	}
	if stored.FailureCode() != domain.CodeReferenceNotFound {
		t.Errorf("failure code = %q", stored.FailureCode())
	}
	if f.storedWallet(t).Balance().String() != "100.00" {
		t.Error("an expired reversal moved money")
	}
}

func TestAWaitingReversalRejectsWhenItsReferenceEndsBadly(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// The rollback arrives first...
	waiting, err := f.submit.Execute(context.Background(),
		f.reversalOf("ROLLBACK", "500.00", "rollback-1", "bet-1"))
	if err != nil {
		t.Fatal(err)
	}

	// ...and the bet it undoes turns out to have been rejected. Waiting longer
	// would be waiting forever: it never moved money.
	mustSubmit(t, f, f.command("BET", "500.00", "bet-1"))

	f.clock.advance(5 * time.Millisecond)
	if _, err := f.worker().RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored := transactionOf(t, f, waiting.Transaction.ID())
	if stored.Status() != domain.StatusRejected {
		t.Fatalf("status = %q, want REJECTED", stored.Status())
	}
	if stored.FailureCode() != domain.CodeReferenceNotReversible {
		t.Errorf("failure code = %q", stored.FailureCode())
	}
}

func TestTheWorkerDoesNothingWhenNothingIsDue(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	handled, err := f.worker().RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if handled != 0 {
		t.Fatalf("handled %d items with an empty queue", handled)
	}
}

func TestReferencePolicyBackoffGrowsAndIsJittered(t *testing.T) {
	t.Parallel()
	policy := ReferencePolicy{
		BaseBackoff: 100 * time.Millisecond,
		MaxBackoff:  10 * time.Second,
	}.normalised()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	// Pendings created together would otherwise wake together, look together
	// and fail together, in step, for as long as they live.
	distinct := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		distinct[policy.nextAttemptAt(now, 1).Sub(now)] = true
	}
	if len(distinct) < 5 {
		t.Fatalf("50 schedules produced %d distinct delays; the backoff is not jittered", len(distinct))
	}

	// And it is bounded: a large attempt number must not overflow into a
	// deadline in the past or a wait measured in years.
	for _, attempt := range []int{1, 5, 20, 64} {
		wait := policy.nextAttemptAt(now, attempt).Sub(now)
		if wait <= 0 || wait > policy.MaxBackoff {
			t.Errorf("attempt %d scheduled %v away, outside (0, %v]", attempt, wait, policy.MaxBackoff)
		}
	}
}

// --- helpers ---------------------------------------------------------------

func mustSubmit(t *testing.T, f submitFixture, cmd SubmitCommand) SubmitResult {
	t.Helper()
	result, err := f.submit.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("submitting %s: %v", cmd.Kind, err)
	}
	return result
}

func transactionOf(t *testing.T, f submitFixture, id domain.TransactionID) domain.WagerTransaction {
	t.Helper()
	stored, err := (&memoryQueries{store: f.store}).Transactions().FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("reading transaction %s: %v", id, err)
	}
	return stored
}

func statusOf(t *testing.T, f submitFixture, id domain.TransactionID) domain.Status {
	t.Helper()
	return transactionOf(t, f, id).Status()
}

var _ = errors.Is
