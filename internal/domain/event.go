package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType names a fact that happened.
type EventType string

const (
	EventWagerTransactionProcessed        EventType = "WagerTransactionProcessed"
	EventWagerTransactionRejected         EventType = "WagerTransactionRejected"
	EventWalletBalanceChanged             EventType = "WalletBalanceChanged"
	EventWagerTransactionPendingReference EventType = "WagerTransactionPendingReference"
)

// Event is something that happened, ready to be recorded and later published.
//
// The type and the version are set by the constructors below and cannot be
// passed in. A caller that could choose them could publish a v1 payload labelled
// v2, and the consumer would parse it wrong in a way nothing here would catch.
type Event struct {
	Type        EventType
	Version     int
	AggregateID string
	OccurredAt  time.Time

	// Payload is the values at the instant the fact happened, already
	// serialised. Not a reference to be resolved later: a payload rendered at
	// publish time would report the state of now, and the same event would say
	// different things depending on when the publisher woke up.
	Payload json.RawMessage
}

// currentEventVersion is the schema version of every payload below.
//
// It is a single constant while all four move together. The moment one of them
// needs to change shape on its own, it gets its own constant -- and the
// constructor is the only place that would have to change.
const currentEventVersion = 1

// moneyPayload is how money appears in an event: a decimal string, for the same
// reason it is one on the wire. A JSON number invites the consumer to parse it
// as a float.
type moneyPayload struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func toMoneyPayload(m Money) moneyPayload {
	return moneyPayload{Amount: m.String(), Currency: m.Currency().String()}
}

type transactionPayload struct {
	TransactionID string        `json:"transactionId"`
	Kind          string        `json:"kind"`
	Status        string        `json:"status"`
	WalletID      string        `json:"walletId"`
	PlayerID      string        `json:"playerId"`
	Money         moneyPayload  `json:"money"`
	ProviderID    string        `json:"providerId,omitempty"`
	ExternalID    string        `json:"externalTransactionId,omitempty"`
	RoundID       string        `json:"roundId,omitempty"`
	GameID        string        `json:"gameId,omitempty"`
	FailureCode   string        `json:"failureCode,omitempty"`
	BalanceAfter  *moneyPayload `json:"balanceAfter,omitempty"`
}

func toTransactionPayload(t WagerTransaction) transactionPayload {
	payload := transactionPayload{
		TransactionID: t.ID().String(),
		Kind:          string(t.Kind()),
		Status:        string(t.Status()),
		WalletID:      t.WalletID().String(),
		PlayerID:      t.PlayerID().String(),
		Money:         toMoneyPayload(t.Money()),
		ProviderID:    t.ProviderID().String(),
		ExternalID:    t.ExternalID().String(),
		RoundID:       t.RoundID().String(),
		GameID:        t.GameID().String(),
		FailureCode:   string(t.FailureCode()),
	}
	// An operation that was never processed has no observed balance, and
	// rendering zero would be a claim about what the wallet held.
	if t.BalanceAfter().IsInitialised() {
		balance := toMoneyPayload(t.BalanceAfter())
		payload.BalanceAfter = &balance
	}
	return payload
}

// NewWagerTransactionProcessed reports a completed operation.
//
// It fires for LOSS too, which is exactly why it is a separate event from
// WalletBalanceChanged: a loss finished without moving anything, and folding
// the two into one would erase that distinction.
func NewWagerTransactionProcessed(t WagerTransaction) (Event, error) {
	if t.Status() != StatusProcessed {
		return Event{}, fail(CodeInvalidStatus,
			"a processed event needs a processed transaction, got %s", t.Status())
	}
	return marshalEvent(EventWagerTransactionProcessed, t.ID().String(), t.UpdatedAt(),
		toTransactionPayload(t))
}

// NewWagerTransactionRejected reports a definitive business refusal.
func NewWagerTransactionRejected(t WagerTransaction) (Event, error) {
	if t.Status() != StatusRejected {
		return Event{}, fail(CodeInvalidStatus,
			"a rejected event needs a rejected transaction, got %s", t.Status())
	}
	if t.FailureCode() == "" {
		// The code is what the consumer branches on. An event without one says
		// "it was refused" and nothing a listener can act upon.
		return Event{}, fail(CodeInvalidStatus, "a rejected event needs a failure code")
	}
	return marshalEvent(EventWagerTransactionRejected, t.ID().String(), t.UpdatedAt(),
		toTransactionPayload(t))
}

// NewWagerTransactionPendingReference reports a reversal parked waiting for
// what it undoes.
func NewWagerTransactionPendingReference(t WagerTransaction) (Event, error) {
	if t.Status() != StatusPendingReference {
		return Event{}, fail(CodeInvalidStatus,
			"a pending-reference event needs a waiting transaction, got %s", t.Status())
	}
	type pendingPayload struct {
		transactionPayload
		ReferenceExternalID string `json:"referenceExternalTransactionId"`
		Attempts            int    `json:"attempts"`
		DeadlineAt          string `json:"deadlineAt"`
	}
	return marshalEvent(EventWagerTransactionPendingReference, t.ID().String(), t.UpdatedAt(),
		pendingPayload{
			transactionPayload:  toTransactionPayload(t),
			ReferenceExternalID: t.ReferenceExternalID().String(),
			Attempts:            t.ReferenceAttempts(),
			DeadlineAt:          t.ReferenceDeadlineAt().Format(time.RFC3339Nano),
		})
}

// NewWalletBalanceChanged reports money actually moving.
//
// The aggregate is the wallet, not the transaction: a listener following a
// balance wants every change to one wallet in order, and ordering by the
// transaction id would scatter them.
func NewWalletBalanceChanged(wallet Wallet, entry LedgerEntry) (Event, error) {
	if !wallet.IsInitialised() {
		return Event{}, fail(CodeInvalidIdentifier, "wallet is not initialised")
	}
	if entry.WalletID() != wallet.ID() {
		return Event{}, fail(CodeInvalidIdentifier,
			"the entry belongs to wallet %s and the event to %s", entry.WalletID(), wallet.ID())
	}
	if entry.BalanceAfter() != wallet.Balance() {
		// The two disagree about what the balance became, which means one of
		// them is stale. Publishing either would be publishing a number nobody
		// can reproduce.
		return Event{}, fail(CodeInconsistentEntry,
			"the entry ends at %s and the wallet holds %s", entry.BalanceAfter(), wallet.Balance())
	}

	type balancePayload struct {
		WalletID      string       `json:"walletId"`
		TransactionID string       `json:"transactionId"`
		Direction     string       `json:"direction"`
		Money         moneyPayload `json:"money"`
		BalanceBefore moneyPayload `json:"balanceBefore"`
		BalanceAfter  moneyPayload `json:"balanceAfter"`
		WalletVersion int64        `json:"walletVersion"`
	}
	return marshalEvent(EventWalletBalanceChanged, wallet.ID().String(), entry.CreatedAt(),
		balancePayload{
			WalletID:      wallet.ID().String(),
			TransactionID: entry.TransactionID().String(),
			Direction:     string(entry.Direction()),
			Money:         toMoneyPayload(entry.Amount()),
			BalanceBefore: toMoneyPayload(entry.BalanceBefore()),
			BalanceAfter:  toMoneyPayload(entry.BalanceAfter()),
			WalletVersion: wallet.Version(),
		})
}

func marshalEvent(eventType EventType, aggregateID string, occurredAt time.Time, payload any) (Event, error) {
	if aggregateID == "" {
		return Event{}, fail(CodeInvalidIdentifier, "the event has no aggregate")
	}
	if occurredAt.IsZero() {
		return Event{}, fail(CodeInvalidTimestamp, "the event has no instant")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("serialising a %s payload: %w", eventType, err)
	}

	return Event{
		Type:        eventType,
		Version:     currentEventVersion,
		AggregateID: aggregateID,
		OccurredAt:  occurredAt.UTC(),
		Payload:     body,
	}, nil
}
