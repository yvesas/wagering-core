//go:build integration

package test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// These run against the SQS emulator and the three-instance cluster, so six
// consumer loops in three processes share one queue. A bug that deduplicated in
// process memory would pass a single-process test and fail here.

// sendMessage puts an envelope on the queue using the AWS CLI, which keeps the
// test honest: it exercises the wire format rather than the producer we would
// otherwise have to write and then trust.
func sendMessage(t *testing.T, queueName, groupID, dedupID, body string) {
	t.Helper()
	endpoint := envOr("TEST_QUEUE_ENDPOINT", "http://localhost:4567")

	url := queueURL(t, queueName)
	cmd := exec.Command("aws", "--endpoint-url="+endpoint, "sqs", "send-message",
		"--queue-url", url,
		"--message-body", body,
		"--message-group-id", groupID,
		"--message-deduplication-id", dedupID,
		"--region", "us-east-1", "--output", "text")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sending a message: %v\n%s", err, out)
	}
}

func queueURL(t *testing.T, queueName string) string {
	t.Helper()
	endpoint := envOr("TEST_QUEUE_ENDPOINT", "http://localhost:4567")
	cmd := exec.Command("aws", "--endpoint-url="+endpoint, "sqs", "get-queue-url",
		"--queue-name", queueName, "--region", "us-east-1", "--output", "text")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resolving the queue url: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func requireAWSCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("aws"); err != nil {
		t.Skip("the aws cli is not installed; these tests drive the queue through it")
	}
}

// envelopeJSON builds the message the producer would send.
func envelopeJSON(messageID, providerID, externalID, playerID, walletID, kind, amount string) string {
	payload := map[string]any{
		"messageId":  messageID,
		"type":       "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339),
		"data": map[string]any{
			"providerId":            providerID,
			"externalTransactionId": externalID,
			"idempotencyKey":        providerID + ":" + externalID,
			"playerId":              playerID,
			"walletId":              walletID,
			"roundId":               "round-1",
			"gameId":                "fortune-chimp",
			"kind":                  kind,
			"money":                 map[string]string{"amount": amount, "currency": "BRL"},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(body)
}

// waitForBalance polls until the wallet reaches the amount, or gives up.
// The consumer works on its own schedule, so the test asks rather than assumes.
func waitForBalance(t *testing.T, base, walletID, want string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = readWallet(t, base, walletID).Balance.Amount
		if last == want {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("balance is %s after 45s, want %s", last, want)
}

// queueCluster starts the cluster on a queue of its own, so one test cannot
// consume another's messages.
func queueCluster(t *testing.T) (cluster, string) {
	t.Helper()
	requireAWSCLI(t)

	suffix := strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	queueName := "wager-" + suffix + ".fifo"

	c := startClusterWith(t, map[string]string{
		"QUEUE_NAME":     queueName,
		"QUEUE_DLQ_NAME": "wager-dlq-" + suffix + ".fifo",
	})
	return c, queueName
}

func TestAnOperationArrivesThroughTheQueue(t *testing.T) {
	c, queueName := queueCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-queue-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")
	externalID := "tx-" + suffix

	sendMessage(t, queueName, walletID, "dedup-"+suffix,
		envelopeJSON("msg-"+suffix, "provider-a", externalID, player, walletID, "BET", "25.00"))

	waitForBalance(t, c.next(1), walletID, "75.00")

	// The same use case as the HTTP path, so the record looks the same.
	stored := readTransaction(t, c.next(2), externalID)
	if stored.Status != "PROCESSED" {
		t.Fatalf("status = %q", stored.Status)
	}
	assertLedgerMatchesBalance(t, c.next(0), walletID)
}

func TestRepeatedDeliveriesMoveMoneyOnce(t *testing.T) {
	c, queueName := queueCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-dupes-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")
	externalID := "tx-" + suffix
	body := envelopeJSON("msg-"+suffix, "provider-a", externalID, player, walletID, "BET", "25.00")

	// Ten copies of the same message. Each gets its own deduplication id, so
	// the broker's own five-minute window does not filter them -- what absorbs
	// them is the inbox, which is the point.
	for i := 0; i < 10; i++ {
		sendMessage(t, queueName, walletID, fmt.Sprintf("dedup-%s-%d", suffix, i), body)
	}

	waitForBalance(t, c.next(1), walletID, "75.00")

	// Give the remaining copies time to be absorbed, then check nothing moved.
	time.Sleep(3 * time.Second)
	if got := readWallet(t, c.next(2), walletID).Balance.Amount; got != "75.00" {
		t.Fatalf("balance = %s: a duplicate was applied", got)
	}
	ledger := readLedger(t, c.next(0), walletID)
	if len(ledger.Entries) != 2 {
		t.Fatalf("got %d ledger entries, want 2", len(ledger.Entries))
	}
	assertLedgerMatchesBalance(t, c.next(1), walletID)
}

func TestTheSameOperationOverHTTPAndTheQueueMovesMoneyOnce(t *testing.T) {
	c, queueName := queueCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-cross-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")
	externalID := "tx-" + suffix

	// HTTP first.
	if body, status := submit(t, c.next(0), "provider-a:"+externalID,
		bet(player, walletID, externalID, "30.00")); status != 200 {
		t.Fatalf("the http submission answered %d: %s", status, body)
	}

	// Then the same operation over the queue, under a message id the inbox has
	// never seen. The inbox lets it through and idempotency stops it: two
	// layers doing two different jobs.
	sendMessage(t, queueName, walletID, "dedup-"+suffix,
		envelopeJSON("msg-"+suffix, "provider-a", externalID, player, walletID, "BET", "30.00"))

	time.Sleep(4 * time.Second)
	if got := readWallet(t, c.next(1), walletID).Balance.Amount; got != "70.00" {
		t.Fatalf("balance = %s: the operation was applied twice", got)
	}
	ledger := readLedger(t, c.next(2), walletID)
	if len(ledger.Entries) != 2 {
		t.Fatalf("got %d ledger entries, want 2", len(ledger.Entries))
	}
}

func TestManyOperationsThroughTheQueueAcrossProcesses(t *testing.T) {
	c, queueName := queueCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-many-" + suffix

	walletID := openWallet(t, c.next(0), player, "1000.00")

	// Twenty different bets on one wallet, sent at once. Six consumer loops in
	// three processes compete for them, and the wallet lock is what makes the
	// result deterministic.
	const bets = 20
	var wg sync.WaitGroup
	for i := 0; i < bets; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			external := fmt.Sprintf("tx-%s-%d", suffix, i)
			sendMessage(t, queueName, walletID, fmt.Sprintf("dedup-%s-%d", suffix, i),
				envelopeJSON("msg-"+external, "provider-a", external, player, walletID, "BET", "1.00"))
		}(i)
	}
	wg.Wait()

	waitForBalance(t, c.next(0), walletID, "980.00")

	wallet := readWallet(t, c.next(1), walletID)
	// Twenty debits plus the opening credit, and no lost update anywhere.
	if wallet.Version != bets+1 {
		t.Fatalf("version = %d, want %d", wallet.Version, bets+1)
	}
	assertLedgerMatchesBalance(t, c.next(2), walletID)
}

func TestAMalformedEnvelopeDoesNotStopTheConsumer(t *testing.T) {
	c, queueName := queueCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-garbage-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")

	// Rubbish first. A consumer that retried it would block everything behind
	// it on a FIFO queue -- which is exactly why a broken payload is dropped
	// rather than redelivered.
	sendMessage(t, queueName, walletID, "dedup-garbage-"+suffix, `{"this":"is not an envelope"}`)

	// Then real work, in the same message group, so it queues behind the
	// rubbish.
	externalID := "tx-" + suffix
	sendMessage(t, queueName, walletID, "dedup-good-"+suffix,
		envelopeJSON("msg-"+suffix, "provider-a", externalID, player, walletID, "BET", "25.00"))

	waitForBalance(t, c.next(1), walletID, "75.00")
}

func TestABusinessRejectionArrivesThroughTheQueueAndIsRecorded(t *testing.T) {
	c, queueName := queueCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-rejected-" + suffix

	walletID := openWallet(t, c.next(0), player, "10.00")
	externalID := "tx-" + suffix

	sendMessage(t, queueName, walletID, "dedup-"+suffix,
		envelopeJSON("msg-"+suffix, "provider-a", externalID, player, walletID, "BET", "500.00"))

	// A rejection is a recorded result, so it is observable and the message is
	// gone: insufficient funds does not improve on the fifth delivery.
	rejected := waitForStatus(t, c.next(1), externalID, "REJECTED")
	if rejected.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("failure code = %q", rejected.FailureCode)
	}
	if got := readWallet(t, c.next(2), walletID).Balance.Amount; got != "10.00" {
		t.Fatalf("a rejected bet moved the balance to %s", got)
	}
}

func TestShutdownDoesNotLoseAMessage(t *testing.T) {
	requireAWSCLI(t)
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	queueName := "wager-shutdown-" + suffix + ".fifo"

	// One instance, so the shutdown is unambiguous.
	first := startClusterWith(t, map[string]string{
		"QUEUE_NAME":     queueName,
		"QUEUE_DLQ_NAME": "wager-shutdown-dlq-" + suffix + ".fifo",
	})
	player := "player-shutdown-" + suffix
	walletID := openWallet(t, first.next(0), player, "100.00")
	externalID := "tx-" + suffix

	// Stop everything, then post the work. Nobody is listening.
	stopCluster(t, first)

	sendMessage(t, queueName, walletID, "dedup-"+suffix,
		envelopeJSON("msg-"+suffix, "provider-a", externalID, player, walletID, "BET", "25.00"))

	// A new cluster picks it up. The message survived the process that was
	// never there to take it, which is the queue doing its job -- and the point
	// is that nothing in our code had to remember it.
	second := startClusterWith(t, map[string]string{
		"QUEUE_NAME":     queueName,
		"QUEUE_DLQ_NAME": "wager-shutdown-dlq-" + suffix + ".fifo",
	})
	waitForBalance(t, second.next(0), walletID, "75.00")
	assertLedgerMatchesBalance(t, second.next(1), walletID)
}

var _ = context.Background
var _ = os.Getenv
