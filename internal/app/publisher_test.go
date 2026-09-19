package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakePublisher records what went out and can be told to fail.
type fakePublisher struct {
	mu        sync.Mutex
	sent      []OutboxRecord
	failUntil int
	calls     int
}

func (p *fakePublisher) Publish(_ context.Context, record OutboxRecord) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls <= p.failUntil {
		return errors.New("the broker is unreachable")
	}
	p.sent = append(p.sent, record)
	return nil
}

func (p *fakePublisher) sentIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.sent))
	for _, record := range p.sent {
		ids = append(ids, record.EventID)
	}
	return ids
}

var testPublisherPolicy = PublisherPolicy{
	BatchSize:      10,
	Interval:       time.Millisecond,
	BaseBackoff:    time.Millisecond,
	MaxBackoff:     2 * time.Millisecond,
	PublishTimeout: time.Second,
}

func (f submitFixture) publisher(broker EventPublisher) *Publisher {
	return NewPublisher(&memoryUnitOfWork{store: f.store}, broker, f.clock,
		testPublisherPolicy, discardLogger())
}

func TestThePublisherSendsAndMarks(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	broker := &fakePublisher{}

	sent, err := f.publisher(broker).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if sent != 2 {
		t.Fatalf("published %d, want the opening's two events", sent)
	}

	for _, record := range f.store.outbox {
		if !record.Published() {
			t.Errorf("event %s was sent but not marked", record.EventID)
		}
	}

	// A second pass has nothing to do: the marks are what stop it republishing.
	again, err := f.publisher(broker).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("the second pass published %d events again", again)
	}
}

func TestAFailedPublishIsRescheduledAndRetried(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	broker := &fakePublisher{failUntil: 2} // both of the opening's events fail once

	publisher := f.publisher(broker)
	if _, err := publisher.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Nothing went out, nothing is marked, and the attempt was counted.
	if len(broker.sentIDs()) != 0 {
		t.Fatalf("something was published: %v", broker.sentIDs())
	}
	for _, record := range f.store.outbox {
		if record.Published() {
			t.Error("a failed publish was marked as published")
		}
		if record.Attempts != 1 {
			t.Errorf("attempts = %d, want 1", record.Attempts)
		}
		if record.LastError == "" {
			t.Error("the failure was not recorded")
		}
	}

	// Past the backoff, the broker is healthy and both go out.
	f.clock.advance(time.Second)
	if _, err := publisher.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(broker.sentIDs()); got != 2 {
		t.Fatalf("published %d after recovery, want 2", got)
	}
}

func TestOneBadEventDoesNotHoldUpTheBatch(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	// The first publish fails, the rest succeed. The batch must not stop at the
	// failure: one event the broker dislikes would otherwise block everything
	// written after it.
	broker := &fakePublisher{failUntil: 1}

	if _, err := f.publisher(broker).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := len(broker.sentIDs()); got != 1 {
		t.Fatalf("published %d, want the one that did not fail", got)
	}

	published, pending := 0, 0
	for _, record := range f.store.outbox {
		if record.Published() {
			published++
		} else {
			pending++
		}
	}
	if published != 1 || pending != 1 {
		t.Fatalf("%d published and %d pending, want one of each", published, pending)
	}
}

func TestRepublishingKeepsTheEventID(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// A publisher that died between sending and marking: the event went out,
	// and the mark did not land.
	broker := &fakePublisher{}
	if err := broker.Publish(context.Background(), f.store.outbox[0]); err != nil {
		t.Fatal(err)
	}
	firstID := broker.sentIDs()[0]

	// The next pass finds it still unpublished and sends it again.
	if _, err := f.publisher(broker).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	ids := broker.sentIDs()
	if len(ids) < 2 {
		t.Fatalf("the event was not republished: %v", ids)
	}
	// The same identity, which is the entire reason a consumer can deduplicate
	// what at-least-once delivery hands it.
	if ids[0] != firstID || ids[1] != firstID {
		t.Fatalf("republished under a different id: %v", ids)
	}
}

func TestThePublisherPublishesInWrittenOrder(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "1000.00")

	for i, amount := range []string{"10.00", "20.00", "30.00"} {
		if _, err := f.submit.Execute(context.Background(),
			f.command("BET", amount, "tx-"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}

	broker := &fakePublisher{}
	if _, err := f.publisher(broker).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Events of one aggregate go out in the order the facts happened, because
	// that is the order they were written.
	sent := broker.sentIDs()
	want := make([]string, 0, len(f.store.outbox))
	for _, record := range f.store.outbox {
		want = append(want, record.EventID)
	}
	if len(sent) != len(want) {
		t.Fatalf("published %d of %d events", len(sent), len(want))
	}
	for i := range sent {
		if sent[i] != want[i] {
			t.Fatalf("event %d is %s, want %s", i, sent[i], want[i])
		}
	}
}
