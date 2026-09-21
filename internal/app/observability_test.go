package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAnOperationIsCountedByItsOutcome(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	if _, err := f.submit.Execute(callerContext(), f.command("BET", "25.00", "tx-1")); err != nil {
		t.Fatalf("the bet: %v", err)
	}
	// A refusal is an outcome, not an error, and it is counted like one. A
	// counter that only moved on success would make a wallet refusing every
	// bet look like a quiet afternoon.
	if _, err := f.submit.Execute(callerContext(), f.command("BET", "500.00", "tx-2")); err != nil {
		t.Fatalf("the refused bet: %v", err)
	}

	if got := f.metrics.count(SourceHTTP, "BET", "PROCESSED"); got != 1 {
		t.Errorf("processed = %d, want 1", got)
	}
	if got := f.metrics.count(SourceHTTP, "BET", "REJECTED"); got != 1 {
		t.Errorf("rejected = %d, want 1", got)
	}
	if got := f.metrics.observations(); got != 2 {
		t.Errorf("latency observations = %d, want 2", got)
	}
}

func TestTheSameOperationIsCountedByTheSourceItArrivedOn(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")

	f.queue.push("receipt-1", f.envelopeFor("msg-1", "BET", "25.00", "tx-1"))
	if _, err := f.consumer.RunOnce(callerContext()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// The two ports share the use case and are told apart by one label. Without
	// it, "the queue is slow" and "HTTP is slow" would be the same number.
	if got := f.metrics.count(SourceQueue, "BET", "PROCESSED"); got != 1 {
		t.Errorf("queue-side processed = %d, want 1", got)
	}
	if got := f.metrics.count(SourceHTTP, "BET", "PROCESSED"); got != 0 {
		t.Errorf("http-side processed = %d, want 0", got)
	}
}

func TestADuplicateDeliveryIsCountedAndNotTimed(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")

	envelope := f.envelopeFor("msg-1", "BET", "25.00", "tx-1")
	f.queue.push("receipt-1", envelope)
	f.queue.push("receipt-2", envelope)

	for range 2 {
		if _, err := f.consumer.RunOnce(callerContext()); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
	}

	if got := f.metrics.duplicates(); got != 1 {
		t.Errorf("duplicates = %d, want 1", got)
	}
	// One delivery did the work and one found it done. Timing both would make
	// a redelivery storm -- where the second is nearly free -- look like the
	// service got faster.
	if got := f.metrics.observations(); got != 1 {
		t.Errorf("latency observations = %d, want 1", got)
	}
	if got := f.metrics.count(SourceQueue, "BET", "PROCESSED"); got != 1 {
		t.Errorf("processed = %d, want 1 -- the duplicate was counted as work", got)
	}
}

func TestADiscardedMessageSaysWhy(t *testing.T) {
	t.Parallel()
	f := newConsumerFixture(t, "100.00")

	f.queue.push("receipt-1", "{this is not json")
	f.queue.push("receipt-2", f.envelopeFor("msg-2", "BET", "25.00.00", "tx-2"))

	if _, err := f.consumer.RunOnce(callerContext()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Two drops for two different reasons, and the reason is what a person
	// would have to fix. One counter for "discarded" would say something is
	// wrong and nothing about where to look.
	if got := f.metrics.discardedFor(DiscardMalformed); got != 1 {
		t.Errorf("malformed = %d, want 1", got)
	}
	if got := f.metrics.discardedFor(DiscardUnusable); got != 1 {
		t.Errorf("unusable = %d, want 1", got)
	}
}

func TestPublishLagIsMeasuredFromWhenTheFactHappened(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	if _, err := f.submit.Execute(callerContext(), f.command("BET", "25.00", "tx-1")); err != nil {
		t.Fatalf("the bet: %v", err)
	}

	// The broker was away for a minute. The lag has to reflect that, because
	// the number exists to answer "is the outbox keeping up" -- and measuring
	// from when this pass picked the row up would answer "how fast do we
	// publish what we chose to publish", which nobody is asking.
	f.clock.advance(time.Minute)

	broker := &fakePublisher{}
	publisher := NewPublisher(&memoryUnitOfWork{store: f.store}, broker, f.clock,
		testPublisherPolicy, f.metrics, discardLogger())

	if _, err := publisher.RunOnce(callerContext()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	f.metrics.mu.Lock()
	lags := append([]time.Duration(nil), f.metrics.publishLag...)
	f.metrics.mu.Unlock()

	if len(lags) == 0 {
		t.Fatal("nothing was published")
	}
	for _, lag := range lags {
		if lag < time.Minute {
			t.Errorf("lag = %s, want at least a minute", lag)
		}
	}
}

// TestTheSettledLogCarriesIdentifiersAndNoMoney is REQ-OBS-001 and REQ-OBS-002
// in one place, because they pull in opposite directions: the line has to carry
// enough to trace an operation and none of what makes a log a data leak.
func TestTheSettledLogCarriesIdentifiersAndNoMoney(t *testing.T) {
	t.Parallel()

	captured := &capturingHandler{}
	f := newSubmitFixtureWithLogger(t, "100.00", captured.logger())

	if _, err := f.submit.Execute(callerContext(), f.command("BET", "25.00", "tx-1")); err != nil {
		t.Fatalf("the bet: %v", err)
	}

	line, ok := captured.find("operation settled")
	if !ok {
		t.Fatal("no line was written for a settled operation")
	}

	for _, key := range []string{
		"transactionId", "walletId", "playerId", "providerId",
		"externalTransactionId", "kind", "status", "correlationId",
	} {
		if !strings.Contains(line, key) {
			t.Errorf("the line does not carry %s: %s", key, line)
		}
	}

	// The amount and the balance stay out. A log aggregator is not where a
	// financial payload belongs, and the transaction id is enough to find both
	// in the database for whoever is entitled to see them.
	for _, leak := range []string{"25.00", "100.00", "75.00", "amount", "balance"} {
		if strings.Contains(line, leak) {
			t.Errorf("the line carries %q: %s", leak, line)
		}
	}
}

// capturingHandler keeps every line a test logged, so an assertion can be made
// about what reaches an aggregator rather than about what the code intended.
type capturingHandler struct {
	mu    sync.Mutex
	lines []string
}

func (h *capturingHandler) logger() *slog.Logger { return slog.New(h) }

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString(record.Message)
	record.Attrs(func(attr slog.Attr) bool {
		line.WriteString(" " + attr.Key + "=" + attr.Value.String())
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, line.String())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

// find returns the first line whose message matches.
func (h *capturingHandler) find(message string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, line := range h.lines {
		if strings.HasPrefix(line, message) {
			return line, true
		}
	}
	return "", false
}
