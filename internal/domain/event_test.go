package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func processedBet(t *testing.T) WagerTransaction {
	t.Helper()
	tx, err := NewExternalTransaction(externalParams(t))
	if err != nil {
		t.Fatal(err)
	}
	processed, err := tx.MarkProcessed(mustParse(t, "975.00", brl(t)), at(t))
	if err != nil {
		t.Fatal(err)
	}
	return processed
}

func decodePayload(t *testing.T, event Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decoding %s: %v", event.Payload, err)
	}
	return payload
}

func TestProcessedEvent(t *testing.T) {
	t.Parallel()
	event, err := NewWagerTransactionProcessed(processedBet(t))
	if err != nil {
		t.Fatalf("NewWagerTransactionProcessed: %v", err)
	}

	if event.Type != EventWagerTransactionProcessed {
		t.Errorf("type = %q", event.Type)
	}
	// The version is set by the constructor and cannot be passed in: a caller
	// that could choose it could publish a v1 payload labelled v2, and the
	// consumer would parse it wrong in a way nothing here would catch.
	if event.Version != currentEventVersion {
		t.Errorf("version = %d", event.Version)
	}

	payload := decodePayload(t, event)
	if payload["status"] != "PROCESSED" {
		t.Errorf("status = %v", payload["status"])
	}
	// Money is a decimal string in the payload too. A JSON number would invite
	// the consumer to parse it as a float.
	money, ok := payload["money"].(map[string]any)
	if !ok {
		t.Fatalf("money is %T", payload["money"])
	}
	if _, ok := money["amount"].(string); !ok {
		t.Fatalf("amount is %T, want a string", money["amount"])
	}
}

func TestProcessedEventRefusesAnUnprocessedTransaction(t *testing.T) {
	t.Parallel()
	pending, err := NewExternalTransaction(externalParams(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWagerTransactionProcessed(pending); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("error = %v, want ErrInvalidStatus", err)
	}
}

func TestRejectedEventNeedsAFailureCode(t *testing.T) {
	t.Parallel()
	tx, err := NewExternalTransaction(externalParams(t))
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := tx.Reject(CodeInsufficientFunds, at(t))
	if err != nil {
		t.Fatal(err)
	}

	event, err := NewWagerTransactionRejected(rejected)
	if err != nil {
		t.Fatalf("NewWagerTransactionRejected: %v", err)
	}
	// The code is what a consumer branches on. Without it the event says "it
	// was refused" and nothing a listener can act upon.
	if decodePayload(t, event)["failureCode"] != string(CodeInsufficientFunds) {
		t.Errorf("payload = %s", event.Payload)
	}

	if _, err := NewWagerTransactionRejected(processedBet(t)); !errors.Is(err, ErrInvalidStatus) {
		t.Errorf("a processed transaction produced a rejected event: %v", err)
	}
}

func TestBalanceChangedEvent(t *testing.T) {
	t.Parallel()
	c := brl(t)
	opening, err := OpenWallet(OpenWalletParams{
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

	event, err := NewWalletBalanceChanged(opening.Wallet, opening.Entries[0])
	if err != nil {
		t.Fatalf("NewWalletBalanceChanged: %v", err)
	}

	// The aggregate is the wallet, not the transaction: a listener following a
	// balance wants every change to one wallet in order, and keying on the
	// transaction would scatter them.
	if event.AggregateID != opening.Wallet.ID().String() {
		t.Errorf("aggregate = %q", event.AggregateID)
	}

	// Decoded into a typed struct rather than a map. A map would decode
	// walletVersion into a float64, and the package's own float scan refuses
	// that -- correctly: a version read through a float is a version that can
	// round.
	var payload struct {
		Direction     string          `json:"direction"`
		WalletVersion int64           `json:"walletVersion"`
		Money         json.RawMessage `json:"money"`
		BalanceBefore json.RawMessage `json:"balanceBefore"`
		BalanceAfter  json.RawMessage `json:"balanceAfter"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decoding %s: %v", event.Payload, err)
	}
	if payload.Direction != "CREDIT" {
		t.Errorf("direction = %q", payload.Direction)
	}
	if payload.WalletVersion != 1 {
		t.Errorf("wallet version = %d", payload.WalletVersion)
	}
	for name, field := range map[string]json.RawMessage{
		"money": payload.Money, "balanceBefore": payload.BalanceBefore, "balanceAfter": payload.BalanceAfter,
	} {
		if len(field) == 0 {
			t.Errorf("the payload has no %s", name)
		}
	}
}

func TestBalanceChangedEventRefusesDisagreement(t *testing.T) {
	t.Parallel()
	c := brl(t)
	opening, err := OpenWallet(OpenWalletParams{
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

	// A wallet that has moved on since the entry was made. One of the two is
	// stale, and publishing either would publish a number nobody can reproduce.
	moved, err := opening.Wallet.Debit(entryID(t), txID(t), mustParse(t, "10.00", c), at(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWalletBalanceChanged(moved.Wallet, opening.Entries[0]); !errors.Is(err, ErrInconsistentEntry) {
		t.Fatalf("error = %v, want ErrInconsistentEntry", err)
	}
}

func TestPendingReferenceEvent(t *testing.T) {
	t.Parallel()
	p := externalParams(t)
	p.Kind = KindRefund
	p.ReferenceExternalID = externalID(t, "transaction-1")
	tx, err := NewExternalTransaction(p)
	if err != nil {
		t.Fatal(err)
	}
	parked, err := tx.MarkPendingReference(at(t), at(t).Add(time.Second), at(t).Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	event, err := NewWagerTransactionPendingReference(parked)
	if err != nil {
		t.Fatalf("NewWagerTransactionPendingReference: %v", err)
	}
	payload := decodePayload(t, event)
	if payload["referenceExternalTransactionId"] != "transaction-1" {
		t.Errorf("reference = %v", payload["referenceExternalTransactionId"])
	}
	// A listener that wants to warn about a stuck reversal needs to know how
	// long it has left.
	if payload["deadlineAt"] == nil {
		t.Error("the payload does not say when the wait ends")
	}
}

func TestAnUnprocessedOperationReportsNoBalance(t *testing.T) {
	t.Parallel()
	tx, err := NewExternalTransaction(externalParams(t))
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := tx.Reject(CodeInsufficientFunds, at(t))
	if err != nil {
		t.Fatal(err)
	}

	event, err := NewWagerTransactionRejected(rejected)
	if err != nil {
		t.Fatal(err)
	}
	// Rendering zero would be a claim about what the wallet held.
	if _, present := decodePayload(t, event)["balanceAfter"]; present {
		t.Errorf("a rejected operation reported a balance: %s", event.Payload)
	}
}

func TestEventPayloadIsASnapshot(t *testing.T) {
	t.Parallel()
	processed := processedBet(t)

	event, err := NewWagerTransactionProcessed(processed)
	if err != nil {
		t.Fatal(err)
	}
	before := string(event.Payload)

	// Whatever happens to the transaction afterwards, the event already holds
	// the values as they were. A payload rendered at publish time would report
	// the state of now, and the same event would say different things depending
	// on when the publisher woke up.
	_, _ = processed.Reject(CodeInsufficientFunds, at(t))

	if string(event.Payload) != before {
		t.Fatal("the payload changed after the event was built")
	}
}
