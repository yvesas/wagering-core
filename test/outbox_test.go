//go:build integration

package test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// eventEnvelope is what a consumer of our events reads off the queue.
type eventEnvelope struct {
	EventID       string          `json:"eventId"`
	EventType     string          `json:"eventType"`
	Version       int             `json:"version"`
	AggregateID   string          `json:"aggregateId"`
	CorrelationID string          `json:"correlationId"`
	OccurredAt    string          `json:"occurredAt"`
	Data          json.RawMessage `json:"data"`
}

// drainEvents reads everything currently on the events queue.
//
// It keeps asking until two empty receives in a row: SQS returns a subset of
// what is available on any one call, so a single receive proves nothing about
// what is there.
func drainEvents(t *testing.T, queueName string, until time.Duration) []eventEnvelope {
	t.Helper()
	endpoint := envOr("TEST_QUEUE_ENDPOINT", "http://localhost:4567")
	url := queueURL(t, queueName)

	var events []eventEnvelope
	deadline := time.Now().Add(until)
	empty := 0

	for time.Now().Before(deadline) && empty < 2 {
		cmd := exec.Command("aws", "--endpoint-url="+endpoint, "sqs", "receive-message",
			"--queue-url", url, "--max-number-of-messages", "10",
			"--wait-time-seconds", "1", "--region", "us-east-1")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("receiving events: %v", err)
		}

		var batch struct {
			Messages []struct {
				Body          string `json:"Body"`
				ReceiptHandle string `json:"ReceiptHandle"`
			} `json:"Messages"`
		}
		if len(strings.TrimSpace(string(out))) == 0 {
			empty++
			continue
		}
		if err := json.Unmarshal(out, &batch); err != nil {
			t.Fatalf("decoding a receive: %v", err)
		}
		if len(batch.Messages) == 0 {
			empty++
			continue
		}
		empty = 0

		for _, message := range batch.Messages {
			var envelope eventEnvelope
			if err := json.Unmarshal([]byte(message.Body), &envelope); err != nil {
				t.Fatalf("decoding an event: %v", err)
			}
			events = append(events, envelope)

			// Delete, so a later drain in the same test does not see it again.
			del := exec.Command("aws", "--endpoint-url="+endpoint, "sqs", "delete-message",
				"--queue-url", url, "--receipt-handle", message.ReceiptHandle,
				"--region", "us-east-1")
			if out, err := del.CombinedOutput(); err != nil {
				t.Fatalf("deleting an event: %v\n%s", err, out)
			}
		}
	}
	return events
}

// eventCluster starts the cluster with queues of its own.
func eventCluster(t *testing.T) (cluster, string) {
	t.Helper()
	requireAWSCLI(t)

	suffix := strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	eventsQueue := "events-" + suffix + ".fifo"

	c := startClusterWith(t, map[string]string{
		"QUEUE_NAME":        "in-" + suffix + ".fifo",
		"QUEUE_DLQ_NAME":    "in-dlq-" + suffix + ".fifo",
		"QUEUE_EVENTS_NAME": eventsQueue,
	})
	return c, eventsQueue
}

func typesOf(events []eventEnvelope) map[string]int {
	counts := map[string]int{}
	for _, event := range events {
		counts[event.EventType]++
	}
	return counts
}

func TestOperationsPublishTheirEvents(t *testing.T) {
	c, eventsQueue := eventCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-events-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")

	// A bet moves money; a loss does not. Both finish.
	betID := "bet-" + suffix
	if _, status := submit(t, c.next(0), "provider-a:"+betID,
		bet(player, walletID, betID, "25.00")); status != 200 {
		t.Fatalf("the bet answered %d", status)
	}

	lossID := "loss-" + suffix
	loss := strings.ReplaceAll(bet(player, walletID, lossID, "0.00"), `"BET"`, `"LOSS"`)
	if body, status := submit(t, c.next(1), "provider-a:"+lossID, loss); status != 200 {
		t.Fatalf("the loss answered %d: %s", status, body)
	}

	events := drainEvents(t, eventsQueue, 30*time.Second)
	counts := typesOf(events)

	// Opening, bet and loss all completed: three.
	if counts["WagerTransactionProcessed"] != 3 {
		t.Errorf("got %d processed events, want 3: %v", counts["WagerTransactionProcessed"], counts)
	}
	// Only the opening and the bet moved money. The loss is the case that makes
	// these two separate events worth having.
	if counts["WalletBalanceChanged"] != 2 {
		t.Errorf("got %d balance-changed events, want 2: %v", counts["WalletBalanceChanged"], counts)
	}

	for _, event := range events {
		if event.EventID == "" || event.Version == 0 || event.AggregateID == "" || event.OccurredAt == "" {
			t.Errorf("an event is missing envelope fields: %+v", event)
		}
	}
}

func TestARejectionPublishesARejectedEvent(t *testing.T) {
	c, eventsQueue := eventCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-reject-" + suffix

	walletID := openWallet(t, c.next(0), player, "10.00")
	drainEvents(t, eventsQueue, 15*time.Second) // clear the opening's events

	externalID := "bet-" + suffix
	if _, status := submit(t, c.next(0), "provider-a:"+externalID,
		bet(player, walletID, externalID, "500.00")); status != 422 {
		t.Fatalf("the bet answered %d, want 422", status)
	}

	events := drainEvents(t, eventsQueue, 30*time.Second)
	counts := typesOf(events)
	if counts["WagerTransactionRejected"] != 1 {
		t.Fatalf("got %v, want one rejection", counts)
	}
	// A refusal moved nothing, so it must not announce a balance change.
	if counts["WalletBalanceChanged"] != 0 {
		t.Fatalf("a rejection announced a balance change: %v", counts)
	}

	var payload struct {
		FailureCode string `json:"failureCode"`
	}
	if err := json.Unmarshal(events[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Errorf("failure code = %q", payload.FailureCode)
	}
}

func TestEveryEventIsPublishedExactlyOnceWithThreePublishers(t *testing.T) {
	c, eventsQueue := eventCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-publishers-" + suffix

	walletID := openWallet(t, c.next(0), player, "1000.00")

	// Thirty bets, spread across the instances. Three publishers are running,
	// one per process, competing for the same outbox.
	const bets = 30
	for i := 0; i < bets; i++ {
		external := fmt.Sprintf("tx-%s-%d", suffix, i)
		if _, status := submit(t, c.next(i), "provider-a:"+external,
			bet(player, walletID, external, "1.00")); status != 200 {
			t.Fatalf("bet %d answered %d", i, status)
		}
	}

	events := drainEvents(t, eventsQueue, 60*time.Second)

	// The opening's two, plus two for each bet.
	want := 2 + bets*2
	if len(events) != want {
		t.Fatalf("got %d events, want %d: %v", len(events), want, typesOf(events))
	}

	// And none of them twice. Three publishers claiming from one outbox with
	// SKIP LOCKED is what makes that true -- a row another publisher holds is
	// passed over, not waited on and not taken.
	seen := map[string]int{}
	for _, event := range events {
		seen[event.EventID]++
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("event %s was published %d times", id, count)
		}
	}
	if len(seen) != want {
		t.Fatalf("%d distinct event ids for %d events", len(seen), want)
	}
}

func TestEventsSurviveABrokerThatWasNotThereYet(t *testing.T) {
	requireAWSCLI(t)
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	eventsQueue := "events-late-" + suffix + ".fifo"
	overrides := map[string]string{
		"QUEUE_NAME":        "in-late-" + suffix + ".fifo",
		"QUEUE_DLQ_NAME":    "in-late-dlq-" + suffix + ".fifo",
		"QUEUE_EVENTS_NAME": eventsQueue,
		// Far enough out that nothing publishes during the first cluster's
		// life, which is how the outbox holding the events is made observable.
		"PUBLISHER_INTERVAL": "3600s",
	}

	first := startClusterWith(t, overrides)
	player := "player-late-" + suffix
	walletID := openWallet(t, first.next(0), player, "100.00")

	externalID := "bet-" + suffix
	if _, status := submit(t, first.next(0), "provider-a:"+externalID,
		bet(player, walletID, externalID, "25.00")); status != 200 {
		t.Fatalf("the bet answered %d", status)
	}

	// The money moved even though nothing was published: no use case calls the
	// broker, so a broker that is down does not stop an operation.
	if got := readWallet(t, first.next(1), walletID).Balance.Amount; got != "75.00" {
		t.Fatalf("balance = %s", got)
	}
	if events := drainEvents(t, eventsQueue, 5*time.Second); len(events) != 0 {
		t.Fatalf("%d events went out while publishing was parked", len(events))
	}

	stopCluster(t, first)

	// A new cluster with a normal interval finds them waiting and sends them.
	overrides["PUBLISHER_INTERVAL"] = "200ms"
	second := startClusterWith(t, overrides)
	_ = second

	events := drainEvents(t, eventsQueue, 45*time.Second)
	counts := typesOf(events)
	if counts["WagerTransactionProcessed"] != 2 || counts["WalletBalanceChanged"] != 2 {
		t.Fatalf("got %v, want the opening's and the bet's events", counts)
	}
}
