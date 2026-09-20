package app

import (
	"testing"

	"github.com/yvesas/wagering-core/internal/domain"
)

func TestEventsAreWrittenInTheSameCommitAsTheFact(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	if _, err := f.submit.Execute(callerContext(), f.command("BET", "25.00", "tx-1")); err != nil {
		t.Fatal(err)
	}

	// The opening produced two and the bet produced two. All four are in the
	// store because their transactions committed; nothing was published yet,
	// because publishing is a separate step by design.
	if len(f.store.outbox) != 4 {
		t.Fatalf("got %d events, want 4", len(f.store.outbox))
	}
	for _, record := range f.store.outbox {
		if record.Published() {
			t.Errorf("event %s was published before anyone tried", record.EventID)
		}
	}
}

func TestARejectedOperationDoesNotAnnounceABalanceChange(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	before := len(f.store.outbox)

	if _, err := f.submit.Execute(callerContext(), f.command("BET", "500.00", "tx-1")); err != nil {
		t.Fatal(err)
	}

	types := eventTypesSince(f, before)
	if len(types) != 1 || types[0] != domain.EventWagerTransactionRejected {
		t.Fatalf("events = %v, want one rejection", types)
	}
}

func TestALossAnnouncesCompletionButNoBalanceChange(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	before := len(f.store.outbox)

	if _, err := f.submit.Execute(callerContext(), f.command("LOSS", "0.00", "tx-1")); err != nil {
		t.Fatal(err)
	}

	// This is the case that justifies two events instead of one: the operation
	// finished and the balance did not move. Folding them together would erase
	// the distinction.
	types := eventTypesSince(f, before)
	if len(types) != 1 || types[0] != domain.EventWagerTransactionProcessed {
		t.Fatalf("events = %v, want one processed and no balance change", types)
	}
}

func TestAWaitingReversalAnnouncesItself(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	before := len(f.store.outbox)

	cmd := f.command("REFUND", "25.00", "refund-1")
	cmd.ReferenceExternalID = "a-bet-that-has-not-arrived"
	if _, err := f.submit.Execute(callerContext(), cmd); err != nil {
		t.Fatal(err)
	}

	types := eventTypesSince(f, before)
	if len(types) != 1 || types[0] != domain.EventWagerTransactionPendingReference {
		t.Fatalf("events = %v, want one pending-reference", types)
	}
}

func TestEventsCarryTheCorrelationOfWhatCausedThem(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	ctx := WithCorrelationID(callerContext(), "request-42")
	if _, err := f.submit.Execute(ctx, f.command("BET", "25.00", "tx-1")); err != nil {
		t.Fatal(err)
	}

	// The opening had no correlation id; the bet's events carry the request's.
	found := 0
	for _, record := range f.store.outbox {
		if record.CorrelationID == "request-42" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("%d events carry the correlation id, want the bet's two", found)
	}
}

func eventTypesSince(f submitFixture, from int) []domain.EventType {
	var types []domain.EventType
	for _, record := range f.store.outbox[from:] {
		types = append(types, record.Event.Type)
	}
	return types
}
