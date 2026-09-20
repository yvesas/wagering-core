package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// fakeQueue is an in-memory broker that records what the consumer did with each
// message. Delete and Release are the whole point of these tests: getting them
// the wrong way round loses money or replays it forever, and neither shows up
// in a balance assertion.
type fakeQueue struct {
	mu        sync.Mutex
	pending   []QueueMessage
	deleted   []string
	released  []string
	failNext  error
	receiveNo int
}

func (q *fakeQueue) push(receipt, body string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, QueueMessage{ReceiptHandle: receipt, Body: []byte(body)})
}

func (q *fakeQueue) Receive(context.Context, int) ([]QueueMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.receiveNo++
	if q.failNext != nil {
		err := q.failNext
		q.failNext = nil
		return nil, err
	}
	batch := q.pending
	q.pending = nil
	return batch, nil
}

func (q *fakeQueue) Delete(_ context.Context, receipt string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.deleted = append(q.deleted, receipt)
	return nil
}

func (q *fakeQueue) Release(_ context.Context, receipt string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.released = append(q.released, receipt)
	return nil
}

func (q *fakeQueue) counts() (deleted, released int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.deleted), len(q.released)
}

type consumerFixture struct {
	submitFixture
	queue    *fakeQueue
	consumer *Consumer
}

func newConsumerFixture(t *testing.T, balance string) consumerFixture {
	t.Helper()
	base := newSubmitFixture(t, balance)
	queue := &fakeQueue{}
	consumer := NewConsumer(queue, &memoryUnitOfWork{store: base.store}, base.submit,
		base.clock, ConsumerConfig{Name: "test-consumer", BatchSize: 10}, discardLogger())
	return consumerFixture{submitFixture: base, queue: queue, consumer: consumer}
}

// envelopeFor builds a well-formed message for an operation.
func (f consumerFixture) envelopeFor(messageID, kind, amount, external string) string {
	envelope := Envelope{
		MessageID:  messageID,
		Type:       TypeWagerTransactionRequested,
		OccurredAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		Data: EnvelopeData{
			ProviderID:     "provider-a",
			ExternalID:     external,
			IdempotencyKey: "provider-a:" + external,
			PlayerID:       f.wallet.PlayerID().String(),
			WalletID:       f.wallet.ID().String(),
			RoundID:        "round-987",
			GameID:         "fortune-chimp",
			Kind:           kind,
			Money:          MoneyData{Amount: amount, Currency: "BRL"},
		},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return string(body)
}

func TestConsumerAppliesAnOperation(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")
	f.queue.push("receipt-1", f.envelopeFor("msg-1", "BET", "25.00", "tx-1"))

	if _, err := f.consumer.RunOnce(callerContext()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := f.storedWallet(t).Balance().String(); got != "75.00" {
		t.Fatalf("balance = %s, want 75.00", got)
	}
	deleted, released := f.queue.counts()
	if deleted != 1 || released != 0 {
		t.Fatalf("deleted %d, released %d; an applied message is deleted", deleted, released)
	}
}

func TestTheInboxAbsorbsARepeatedDelivery(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")
	body := f.envelopeFor("msg-1", "BET", "25.00", "tx-1")

	// The same message, three times. At-least-once delivery makes this ordinary.
	f.queue.push("receipt-1", body)
	f.queue.push("receipt-2", body)
	f.queue.push("receipt-3", body)

	if _, err := f.consumer.RunOnce(callerContext()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := f.storedWallet(t).Balance().String(); got != "75.00" {
		t.Fatalf("balance = %s: the operation was applied more than once", got)
	}
	if len(f.store.entries) != 2 {
		t.Fatalf("got %d ledger entries, want 2", len(f.store.entries))
	}
	// Every copy is deleted: a duplicate is handled, not failed.
	if deleted, released := f.queue.counts(); deleted != 3 || released != 0 {
		t.Fatalf("deleted %d, released %d, want 3 and 0", deleted, released)
	}
	if len(f.store.inbox) != 1 {
		t.Fatalf("got %d inbox rows, want 1", len(f.store.inbox))
	}
}

func TestTheSameOperationFromBothPathsMovesMoneyOnce(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")

	// HTTP first.
	if _, err := f.submit.Execute(callerContext(), f.command("BET", "25.00", "tx-1")); err != nil {
		t.Fatal(err)
	}

	// Then the same operation over the queue, under a message id the inbox has
	// never seen. The inbox lets it through -- it is a new message -- and
	// idempotency stops it, because it is not a new operation. Two layers, two
	// different jobs.
	f.queue.push("receipt-1", f.envelopeFor("msg-brand-new", "BET", "25.00", "tx-1"))

	if _, err := f.consumer.RunOnce(callerContext()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := f.storedWallet(t).Balance().String(); got != "75.00" {
		t.Fatalf("balance = %s: the operation was applied twice", got)
	}
	if deleted, _ := f.queue.counts(); deleted != 1 {
		t.Fatalf("deleted %d, want 1", deleted)
	}
}

func TestABusinessRejectionDeletesTheMessage(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")
	f.queue.push("receipt-1", f.envelopeFor("msg-1", "BET", "500.00", "tx-1"))

	if _, err := f.consumer.RunOnce(callerContext()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Insufficient funds does not improve on the fifth attempt. Keeping the
	// message would turn a refusal into something that only leaves the queue
	// via the dead-letter queue, much later, looking like infrastructure.
	if deleted, released := f.queue.counts(); deleted != 1 || released != 0 {
		t.Fatalf("deleted %d, released %d; a rejection is terminal", deleted, released)
	}

	stored := transactionOf(t, f.submitFixture, lastTransaction(t, f.submitFixture).ID())
	if stored.Status() != domain.StatusRejected {
		t.Fatalf("status = %q, want REJECTED", stored.Status())
	}
	if got := f.storedWallet(t).Balance().String(); got != "100.00" {
		t.Errorf("a rejected bet moved the balance to %s", got)
	}
}

func TestAMalformedEnvelopeIsDiscarded(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"not json":           `{`,
		"no message id":      `{"type":"WagerTransactionRequested","data":{}}`,
		"unknown type":       `{"messageId":"m","type":"SomethingElse","data":{}}`,
		"unknown field":      `{"messageId":"m","type":"WagerTransactionRequested","nonsense":1,"data":{}}`,
		"no idempotency key": `{"messageId":"m","type":"WagerTransactionRequested","data":{"providerId":"p"}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newConsumerFixture(t, "100.00")
			f.queue.push("receipt-1", body)

			if _, err := f.consumer.RunOnce(callerContext()); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}

			// Redelivering a broken payload repeats the same failure until the
			// dead-letter queue takes it. Dropping it with a loud log is the
			// honest choice.
			if deleted, released := f.queue.counts(); deleted != 1 || released != 0 {
				t.Fatalf("deleted %d, released %d", deleted, released)
			}
			if len(f.store.inbox) != 0 {
				t.Errorf("a message that could not be read was recorded")
			}
		})
	}
}

func TestAnUnusableCommandIsRecordedRatherThanRetriedForever(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")

	// A well-formed envelope carrying a currency that does not exist. The
	// domain refuses it every time, so redelivering is a loop.
	body := f.envelopeFor("msg-1", "BET", "25.00", "tx-1")
	body = replaceOnce(body, `"currency":"BRL"`, `"currency":"brl"`)
	f.queue.push("receipt-1", body)

	if _, err := f.consumer.RunOnce(callerContext()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if deleted, released := f.queue.counts(); deleted != 1 || released != 0 {
		t.Fatalf("deleted %d, released %d", deleted, released)
	}
	// Recorded as handled, so a redelivery is absorbed rather than reprocessed.
	if len(f.store.inbox) != 1 || !f.store.inbox[0].Handled() {
		t.Fatalf("inbox = %+v", f.store.inbox)
	}
	if got := f.storedWallet(t).Balance().String(); got != "100.00" {
		t.Errorf("an unusable command moved the balance to %s", got)
	}
}

func TestATransientFailureReleasesTheMessage(t *testing.T) {
	t.Parallel()
	base := newSubmitFixture(t, "100.00")
	queue := &fakeQueue{}

	// The database refuses the write. Nothing about the message is wrong, so it
	// has to come back.
	base.store.failOnInsertTx = errors.New("the database went away")

	consumer := NewConsumer(queue, &memoryUnitOfWork{store: base.store}, base.submit,
		base.clock, ConsumerConfig{Name: "test-consumer"}, discardLogger())

	f := consumerFixture{submitFixture: base, queue: queue, consumer: consumer}
	queue.push("receipt-1", f.envelopeFor("msg-1", "BET", "25.00", "tx-1"))

	if _, err := consumer.RunOnce(callerContext()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Released, not deleted: deleting would lose the operation, and waiting out
	// the visibility timeout would delay it for no reason.
	if deleted, released := queue.counts(); deleted != 0 || released != 1 {
		t.Fatalf("deleted %d, released %d; a transient failure releases", deleted, released)
	}
	if len(base.store.inbox) != 0 {
		t.Errorf("the inbox row survived a rolled-back handling: %+v", base.store.inbox)
	}
}

func TestARepeatedMessageIdWithDifferentContentIsRefused(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")

	f.queue.push("receipt-1", f.envelopeFor("msg-1", "BET", "25.00", "tx-1"))
	if _, err := f.consumer.RunOnce(callerContext()); err != nil {
		t.Fatal(err)
	}

	// The same message id, a different operation. A producer reusing an
	// identifier; applying it would apply an operation under someone else's
	// identity.
	f.queue.push("receipt-2", f.envelopeFor("msg-1", "BET", "40.00", "tx-2"))
	if _, err := f.consumer.RunOnce(callerContext()); err != nil {
		t.Fatal(err)
	}

	if got := f.storedWallet(t).Balance().String(); got != "75.00" {
		t.Fatalf("balance = %s: the reused id was applied", got)
	}
	if deleted, _ := f.queue.counts(); deleted != 2 {
		t.Fatalf("deleted %d, want both messages gone", deleted)
	}
}

func TestAClaimedButUncompletedMessageIsRetried(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")

	// A process that died between claiming and committing leaves nothing
	// behind -- the claim rolls back with everything else. This is the shape
	// where a row exists but was never completed, which would mean the commit
	// half-happened; the consumer refuses to call it done.
	f.store.inbox = append(f.store.inbox, InboxMessage{
		ConsumerName: "test-consumer",
		MessageID:    "msg-1",
		PayloadHash:  hashOfCommand(t, f, "BET", "25.00", "tx-1"),
		ReceivedAt:   f.clock.Now(),
	})

	f.queue.push("receipt-1", f.envelopeFor("msg-1", "BET", "25.00", "tx-1"))
	if _, err := f.consumer.RunOnce(callerContext()); err != nil {
		t.Fatal(err)
	}

	if deleted, released := f.queue.counts(); deleted != 0 || released != 1 {
		t.Fatalf("deleted %d, released %d; an incomplete handling is retried", deleted, released)
	}
}

func TestRunStopsWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")

	ctx, cancel := context.WithCancel(callerContext())
	done := make(chan struct{})
	go func() { defer close(done); f.consumer.Run(ctx) }()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

func TestAMessageInHandIsFinishedEvenWhileShuttingDown(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")
	f.queue.push("receipt-1", f.envelopeFor("msg-1", "BET", "25.00", "tx-1"))

	// Already cancelled: this is the shutdown case. What the consumer is
	// holding still has to be handled and committed, or the message comes back
	// for no reason and the work is done twice.
	ctx, cancel := context.WithCancel(callerContext())
	cancel()

	if _, err := f.consumer.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := f.storedWallet(t).Balance().String(); got != "75.00" {
		t.Fatalf("balance = %s: the in-flight message was dropped", got)
	}
	if deleted, _ := f.queue.counts(); deleted != 1 {
		t.Fatalf("deleted %d: the message was not finished", deleted)
	}
}

// --- helpers ---------------------------------------------------------------

func lastTransaction(t *testing.T, f submitFixture) domain.WagerTransaction {
	t.Helper()
	if len(f.store.transactions) == 0 {
		t.Fatal("no transactions were stored")
	}
	return f.store.transactions[len(f.store.transactions)-1]
}

func hashOfCommand(t *testing.T, f consumerFixture, kind, amount, external string) domain.PayloadHash {
	t.Helper()
	var envelope Envelope
	if err := json.Unmarshal([]byte(f.envelopeFor("ignored", kind, amount, external)), &envelope); err != nil {
		t.Fatal(err)
	}
	hash, err := envelope.Command().PayloadHash()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func replaceOnce(s, old, new string) string {
	i := indexOf(s, old)
	if i < 0 {
		panic(fmt.Sprintf("%q not found", old))
	}
	return s[:i] + new + s[i+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
