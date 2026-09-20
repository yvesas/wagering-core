//go:build integration

// Package test holds the scenarios that need more than one process.
//
// Goroutines inside one binary share a pool, a mutex table and an address
// space. They prove the code is safe for threads; they cannot prove the
// guarantee lives in the database, because a bug that relied on a local lock
// would pass every one of them. These tests build the server, run three
// independent instances against the same database, and fire requests at all
// three.
//
//	make up-test && make test-integration
package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/adapter/oidc/oidctest"
)

// instances is the number of independent processes. Three, because two can
// still be explained by a coincidence of ordering and three cannot.
const instances = 3

type cluster struct {
	bases []string

	// stop shuts each instance down. Calling it twice is safe, so a test can
	// stop the cluster explicitly and the cleanup can still run.
	stop []func()
}

// stopper sends an interrupt and waits, falling back to a kill.
func stopper(cmd *exec.Cmd) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan struct{})
			go func() { _, _ = cmd.Process.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				_ = cmd.Process.Kill()
			}
		})
	}
}

// stopCluster shuts every instance down and waits for them.
func stopCluster(t *testing.T, c cluster) {
	t.Helper()
	for _, stop := range c.stop {
		stop()
	}
}

// next spreads requests across the instances, round robin by index, so a
// scenario exercises all of them rather than hammering one.
func (c cluster) next(i int) string { return c.bases[i%len(c.bases)] }

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

// startCluster builds the server once and runs it three times.
func startCluster(t *testing.T) cluster { return startClusterWith(t, nil) }

// startClusterWith is startCluster with settings overridden, so a test can make
// a timeout observable instead of waiting out the production one.
func startClusterWith(t *testing.T, overrides map[string]string) cluster {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "api")
	build := exec.Command("go", "build", "-o", binary, "./cmd/api")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the server: %v\n%s", err, out)
	}

	env := append(os.Environ(),
		"APP_ENV=test",
		"APP_LOG_LEVEL=error",
		"APP_SHUTDOWN_TIMEOUT=5s",
		"DB_HOST="+envOr("TEST_DB_HOST", "localhost"),
		"DB_PORT="+envOr("TEST_DB_PORT", "5433"),
		"DB_NAME="+envOr("TEST_DB_NAME", "wagering_test"),
		"DB_USER="+envOr("TEST_DB_USER", "wagering"),
		"DB_PASSWORD="+envOr("TEST_DB_PASSWORD", "local-dev-only"),
		"DB_SSLMODE=disable",
		"QUEUE_ENDPOINT="+envOr("TEST_QUEUE_ENDPOINT", "http://localhost:4567"),
		"QUEUE_REGION=us-east-1",
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		// Short polling in tests: the production ten seconds would make every
		// shutdown assertion wait for it.
		"QUEUE_WAIT_TIME=1s",
		"PUBLISHER_INTERVAL=200ms",
		// The instances authenticate against the issuer running in this test
		// process. There is no setting that switches this off, so an instance
		// that could not reach it would not start at all.
		"OIDC_ISSUER_URL="+issuer.URL(),
		"OIDC_AUDIENCE="+oidctest.Audience,
	)

	for key, value := range overrides {
		env = append(env, key+"="+value)
	}

	var c cluster
	for i := 0; i < instances; i++ {
		addr := freePort(t)
		cmd := exec.Command(binary)
		cmd.Env = append(env, "APP_HTTP_ADDR="+addr)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr

		if err := cmd.Start(); err != nil {
			t.Fatalf("starting instance %d: %v", i, err)
		}
		stop := stopper(cmd)
		c.stop = append(c.stop, stop)
		t.Cleanup(stop)

		base := "http://" + addr
		waitReady(t, base)
		c.bases = append(c.bases, base)
	}
	return c
}

func waitReady(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never became ready", base)
}

// --- http helpers ----------------------------------------------------------

// do issues a request with a credential, which every business endpoint now
// requires. The token is a parameter rather than a default because these
// scenarios have two callers -- the platform and a provider -- and which one is
// asking is part of what they assert.
func do(t *testing.T, method, url, token, payload string, headers map[string]string) (string, int) {
	t.Helper()

	var body io.Reader
	if payload != "" {
		body = strings.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	return string(answer), resp.StatusCode
}

func post(t *testing.T, url, token, payload string) (string, int) {
	t.Helper()
	return do(t, http.MethodPost, url, token, payload, nil)
}

func get(t *testing.T, url, token string) (string, int) {
	t.Helper()
	return do(t, http.MethodGet, url, token, "", nil)
}

func submit(t *testing.T, base, key, payload string) (string, int) {
	t.Helper()
	return submitAs(t, base, providerToken(t, "provider-a"), key, payload)
}

// submitAs is submit with the credential chosen by the caller, for the
// scenarios that are about who is asking.
func submitAs(t *testing.T, base, token, key, payload string) (string, int) {
	t.Helper()
	return do(t, http.MethodPost, base+"/wagering/transactions", token, payload,
		map[string]string{"Idempotency-Key": key})
}

func openWallet(t *testing.T, base, playerID, amount string) string {
	t.Helper()
	body, status := post(t, base+"/wallets", platformToken(t), fmt.Sprintf(
		`{"playerId":%q,"initialBalance":{"amount":%q,"currency":"BRL"}}`, playerID, amount))
	if status != http.StatusCreated {
		t.Fatalf("opening a wallet: %d %s", status, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return created.ID
}

func bet(playerID, walletID, externalID, amount string) string {
	return fmt.Sprintf(`{
	  "providerId":"provider-a",
	  "externalTransactionId":%q,
	  "playerId":%q,
	  "walletId":%q,
	  "roundId":"round-1",
	  "gameId":"fortune-chimp",
	  "kind":"BET",
	  "money":{"amount":%q,"currency":"BRL"}
	}`, externalID, playerID, walletID, amount)
}

type walletState struct {
	Balance struct {
		Amount string `json:"amount"`
	} `json:"balance"`
	Version int64 `json:"version"`
}

func readWallet(t *testing.T, base, walletID string) walletState {
	t.Helper()
	body, status := get(t, base+"/wallets/"+walletID, platformToken(t))
	if status != http.StatusOK {
		t.Fatalf("reading the wallet: %d %s", status, body)
	}
	var w walletState
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return w
}

type ledgerPage struct {
	Entries []struct {
		ID           string                  `json:"id"`
		Direction    string                  `json:"direction"`
		Money        struct{ Amount string } `json:"money"`
		BalanceAfter struct{ Amount string } `json:"balanceAfter"`
	} `json:"entries"`
	NextCursor string `json:"nextCursor"`
	HasMore    bool   `json:"hasMore"`
}

// readLedger pages through the whole statement, because the checks below have
// to see every entry and a single page could hide one.
func readLedger(t *testing.T, base, walletID string) ledgerPage {
	t.Helper()
	var all ledgerPage
	cursor := ""
	for {
		url := base + "/wallets/" + walletID + "/ledger?limit=100"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		body, status := get(t, url, platformToken(t))
		if status != http.StatusOK {
			t.Fatalf("reading the ledger: %d %s", status, body)
		}
		var page ledgerPage
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatalf("decoding %s: %v", body, err)
		}
		all.Entries = append(all.Entries, page.Entries...)
		if !page.HasMore {
			return all
		}
		cursor = page.NextCursor
	}
}

// assertLedgerMatchesBalance is the check every scenario ends with: the stored
// balance equals credits minus debits. It works in minor units so no float ever
// touches the arithmetic that is verifying that no float touched the system.
func assertLedgerMatchesBalance(t *testing.T, base, walletID string) {
	t.Helper()
	wallet := readWallet(t, base, walletID)
	ledger := readLedger(t, base, walletID)

	var total int64
	for _, entry := range ledger.Entries {
		amount := minorUnits(t, entry.Money.Amount)
		if entry.Direction == "DEBIT" {
			total -= amount
		} else {
			total += amount
		}
	}
	if got := minorUnits(t, wallet.Balance.Amount); got != total {
		t.Fatalf("the ledger sums to %d minor units and the balance is %d", total, got)
	}
}

// minorUnits parses "25.00" into 2500 without a float, the same way the domain
// does. Using strconv.ParseFloat here would make the verification depend on the
// thing it is verifying.
func minorUnits(t *testing.T, amount string) int64 {
	t.Helper()
	negative := strings.HasPrefix(amount, "-")
	amount = strings.TrimPrefix(amount, "-")

	whole, fraction, found := strings.Cut(amount, ".")
	if !found || len(fraction) != 2 {
		t.Fatalf("malformed amount %q", amount)
	}
	var value int64
	for _, digit := range whole + fraction {
		if digit < '0' || digit > '9' {
			t.Fatalf("malformed amount %q", amount)
		}
		value = value*10 + int64(digit-'0')
	}
	if negative {
		return -value
	}
	return value
}

// --- the scenarios ---------------------------------------------------------

// TestTwoBetsRaceForOneBalance is the scenario the requirements name outright.
//
// A wallet holds 100.00 and receives two different 80.00 bets at the same
// instant, from different processes. Exactly one is processed, the other is
// rejected for insufficient funds, the balance ends at 20.00, and the ledger
// holds exactly one debit.
func TestTwoBetsRaceForOneBalance(t *testing.T) {
	c := startCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-race-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")

	type outcome struct {
		status int
		body   string
	}
	outcomes := make([]outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			external := fmt.Sprintf("tx-%s-%d", suffix, i)
			<-start
			body, status := submit(t, c.next(i), "provider-a:"+external,
				bet(player, walletID, external, "80.00"))
			outcomes[i] = outcome{status: status, body: body}
		}(i)
	}
	close(start)
	wg.Wait()

	processed, rejected := 0, 0
	for i, o := range outcomes {
		switch o.status {
		case http.StatusOK:
			processed++
		case http.StatusUnprocessableEntity:
			rejected++
			if !strings.Contains(o.body, "INSUFFICIENT_FUNDS") {
				t.Errorf("bet %d was rejected for the wrong reason: %s", i, o.body)
			}
		default:
			t.Errorf("bet %d answered %d: %s", i, o.status, o.body)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("got %d processed and %d rejected, want exactly one of each", processed, rejected)
	}

	wallet := readWallet(t, c.next(2), walletID)
	if wallet.Balance.Amount != "20.00" {
		t.Fatalf("balance = %s, want 20.00", wallet.Balance.Amount)
	}

	ledger := readLedger(t, c.next(0), walletID)
	debits := 0
	for _, entry := range ledger.Entries {
		if entry.Direction == "DEBIT" {
			debits++
		}
	}
	if debits != 1 {
		t.Fatalf("got %d debits, want exactly one", debits)
	}
	assertLedgerMatchesBalance(t, c.next(1), walletID)
}

// TestFiftyIdenticalBetsProduceOneDebit sends the same operation fifty times,
// across three processes, released together.
//
// The idempotency constraint is what refuses forty-nine of them, and it is in
// the database -- which is why the copies are spread over processes that share
// nothing but that database.
func TestFiftyIdenticalBetsProduceOneDebit(t *testing.T) {
	c := startCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-fifty-" + suffix

	walletID := openWallet(t, c.next(0), player, "1000.00")

	const copies = 50
	external := "tx-" + suffix
	payload := bet(player, walletID, external, "25.00")
	key := "provider-a:" + external

	statuses := make([]int, copies)
	bodies := make([]string, copies)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			bodies[i], statuses[i] = submit(t, c.next(i), key, payload)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Errorf("copy %d answered %d: %s", i, status, bodies[i])
		}
	}

	wallet := readWallet(t, c.next(1), walletID)
	if wallet.Balance.Amount != "975.00" {
		t.Fatalf("balance = %s, want 975.00: the operation was applied more than once",
			wallet.Balance.Amount)
	}
	// The version moved exactly once, from the opening.
	if wallet.Version != 2 {
		t.Fatalf("version = %d, want 2", wallet.Version)
	}

	ledger := readLedger(t, c.next(2), walletID)
	if len(ledger.Entries) != 2 {
		t.Fatalf("got %d ledger entries, want 2 (the opening and one debit)", len(ledger.Entries))
	}
	assertLedgerMatchesBalance(t, c.next(0), walletID)
}

// TestDistinctWalletsAdvanceInParallel checks the other half of the
// requirement: coordination is per wallet, so wallets that share nothing must
// not queue behind each other.
func TestDistinctWalletsAdvanceInParallel(t *testing.T) {
	c := startCluster(t)
	suffix := time.Now().Format("150405.000000000")

	const wallets = 12
	const betsEach = 4

	players := make([]string, wallets)
	ids := make([]string, wallets)
	for i := range players {
		players[i] = fmt.Sprintf("player-parallel-%s-%d", suffix, i)
		ids[i] = openWallet(t, c.next(i), players[i], "1000.00")
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	failures := make(chan string, wallets*betsEach)

	for w := 0; w < wallets; w++ {
		for b := 0; b < betsEach; b++ {
			wg.Add(1)
			go func(w, b int) {
				defer wg.Done()
				external := fmt.Sprintf("tx-%s-%d-%d", suffix, w, b)
				<-start
				body, status := submit(t, c.next(w+b), "provider-a:"+external,
					bet(players[w], ids[w], external, "10.00"))
				if status != http.StatusOK {
					failures <- fmt.Sprintf("wallet %d bet %d: %d %s", w, b, status, body)
				}
			}(w, b)
		}
	}
	close(start)
	wg.Wait()
	close(failures)

	for failure := range failures {
		t.Error(failure)
	}

	for i, id := range ids {
		wallet := readWallet(t, c.next(i), id)
		if wallet.Balance.Amount != "960.00" {
			t.Errorf("wallet %d ended at %s, want 960.00", i, wallet.Balance.Amount)
		}
		assertLedgerMatchesBalance(t, c.next(i), id)
	}
}

// TestManyDifferentBetsOnOneWalletAllLand is the contention case the lock
// exists for: forty different operations, one wallet, three processes.
//
// Every one of them must land. With optimistic control alone this is where the
// retry budget starts deciding who gets to bet, and the failure would look like
// the system refusing work while it is merely busy.
func TestManyDifferentBetsOnOneWalletAllLand(t *testing.T) {
	c := startCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-contended-" + suffix

	walletID := openWallet(t, c.next(0), player, "1000.00")

	const bets = 40
	statuses := make([]int, bets)
	bodies := make([]string, bets)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < bets; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			external := fmt.Sprintf("tx-%s-%d", suffix, i)
			<-start
			bodies[i], statuses[i] = submit(t, c.next(i), "provider-a:"+external,
				bet(player, walletID, external, "1.00"))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Errorf("bet %d answered %d: %s", i, status, bodies[i])
		}
	}

	wallet := readWallet(t, c.next(1), walletID)
	if wallet.Balance.Amount != "960.00" {
		t.Fatalf("balance = %s, want 960.00", wallet.Balance.Amount)
	}
	// Forty debits plus the opening credit, and no lost update anywhere.
	if wallet.Version != bets+1 {
		t.Fatalf("version = %d, want %d", wallet.Version, bets+1)
	}
	assertLedgerMatchesBalance(t, c.next(2), walletID)
}

// TestBalanceNeverGoesNegativeUnderContention throws more bets at a wallet than
// it can pay for, from three processes at once.
//
// The split between accepted and refused is not fixed -- that depends on
// ordering -- but the arithmetic is: accepted times the stake equals what left
// the wallet, the balance never goes below zero, and the ledger agrees.
func TestBalanceNeverGoesNegativeUnderContention(t *testing.T) {
	c := startCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-oversubscribed-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")

	const bets = 30 // 30 x 10.00 against 100.00: at most ten can succeed
	statuses := make([]int, bets)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < bets; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			external := fmt.Sprintf("tx-%s-%d", suffix, i)
			<-start
			_, statuses[i] = submit(t, c.next(i), "provider-a:"+external,
				bet(player, walletID, external, "10.00"))
		}(i)
	}
	close(start)
	wg.Wait()

	accepted := 0
	for i, status := range statuses {
		switch status {
		case http.StatusOK:
			accepted++
		case http.StatusUnprocessableEntity:
		default:
			t.Errorf("bet %d answered %d", i, status)
		}
	}
	if accepted != 10 {
		t.Fatalf("%d bets were accepted; 100.00 pays for exactly ten of 10.00", accepted)
	}

	wallet := readWallet(t, c.next(1), walletID)
	if wallet.Balance.Amount != "0.00" {
		t.Fatalf("balance = %s, want 0.00", wallet.Balance.Amount)
	}
	if strings.HasPrefix(wallet.Balance.Amount, "-") {
		t.Fatalf("the balance went negative: %s", wallet.Balance.Amount)
	}
	assertLedgerMatchesBalance(t, c.next(2), walletID)
}

// reversal builds a REFUND or ROLLBACK naming what it undoes.
func reversal(kind, playerID, walletID, externalID, amount, reference string) string {
	return fmt.Sprintf(`{
	  "providerId":"provider-a",
	  "externalTransactionId":%q,
	  "playerId":%q,
	  "walletId":%q,
	  "roundId":"round-1",
	  "gameId":"fortune-chimp",
	  "kind":%q,
	  "money":{"amount":%q,"currency":"BRL"},
	  "referenceExternalTransactionId":%q
	}`, externalID, playerID, walletID, kind, amount, reference)
}

type transactionState struct {
	TransactionID string `json:"transactionId"`
	Status        string `json:"status"`
	FailureCode   string `json:"failureCode"`
}

func readTransaction(t *testing.T, base, externalID string) transactionState {
	t.Helper()
	body, status := get(t, base+"/providers/provider-a/wagering/transactions/"+externalID,
		providerToken(t, "provider-a"))
	if status != http.StatusOK {
		t.Fatalf("reading %s: %d %s", externalID, status, body)
	}
	var out transactionState
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return out
}

// waitForStatus polls until the transaction reaches one of the wanted states.
// The worker runs on its own schedule, so the test asks rather than assumes.
func waitForStatus(t *testing.T, base, externalID string, wanted ...string) transactionState {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last transactionState
	for time.Now().Before(deadline) {
		last = readTransaction(t, base, externalID)
		for _, want := range wanted {
			if last.Status == want {
				return last
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s is %q after 30s, wanted one of %v", externalID, last.Status, wanted)
	return last
}

// TestAReversalThatArrivesBeforeItsReferenceIsResolvedLater is the out-of-order
// case, end to end.
//
// The refund overtakes the bet it undoes, which at-least-once delivery makes
// ordinary rather than exceptional. It is recorded as waiting, the bet lands,
// and the worker -- possibly in a different process from the one that took the
// request -- finishes it.
func TestAReversalThatArrivesBeforeItsReferenceIsResolvedLater(t *testing.T) {
	c := startCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-outoforder-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")
	betID := "bet-" + suffix
	refundID := "refund-" + suffix

	// The refund arrives first.
	body, status := submit(t, c.next(0), "provider-a:"+refundID,
		reversal("REFUND", player, walletID, refundID, "25.00", betID))
	if status != http.StatusAccepted {
		t.Fatalf("the early refund answered %d, want 202: %s", status, body)
	}
	if !strings.Contains(body, "PENDING_REFERENCE") {
		t.Fatalf("the early refund was not parked: %s", body)
	}
	if got := readWallet(t, c.next(1), walletID).Balance.Amount; got != "100.00" {
		t.Fatalf("a waiting reversal moved money: balance is %s", got)
	}

	// Then the bet it undoes, through a different instance.
	if body, status := submit(t, c.next(1), "provider-a:"+betID,
		bet(player, walletID, betID, "25.00")); status != http.StatusOK {
		t.Fatalf("the bet answered %d: %s", status, body)
	}

	// The worker takes it from here, on its own schedule.
	resolved := waitForStatus(t, c.next(2), refundID, "PROCESSED")
	if resolved.FailureCode != "" {
		t.Errorf("a processed refund carries a failure code: %q", resolved.FailureCode)
	}

	// Debited and put back.
	if got := readWallet(t, c.next(0), walletID).Balance.Amount; got != "100.00" {
		t.Fatalf("balance = %s, want 100.00", got)
	}
	assertLedgerMatchesBalance(t, c.next(1), walletID)
}

// TestAReversalWhoseReferenceNeverArrivesExpires covers the other end of the
// wait: the bet never lands, and the refund cannot wait forever.
func TestAReversalWhoseReferenceNeverArrivesExpires(t *testing.T) {
	c := startClusterWith(t, map[string]string{
		// A short TTL, so the test exercises the deadline instead of waiting
		// out the production one.
		"REFERENCE_TTL":          "2s",
		"REFERENCE_BASE_BACKOFF": "100ms",
		"REFERENCE_MAX_ATTEMPTS": "20",
	})
	suffix := time.Now().Format("150405.000000000")
	player := "player-expired-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")
	refundID := "refund-" + suffix

	if body, status := submit(t, c.next(0), "provider-a:"+refundID,
		reversal("REFUND", player, walletID, refundID, "25.00", "a-bet-that-never-arrives")); status != http.StatusAccepted {
		t.Fatalf("status = %d: %s", status, body)
	}

	expired := waitForStatus(t, c.next(1), refundID, "REJECTED")
	if expired.FailureCode != "REFERENCE_NOT_FOUND" {
		t.Fatalf("failure code = %q, want REFERENCE_NOT_FOUND", expired.FailureCode)
	}
	if got := readWallet(t, c.next(2), walletID).Balance.Amount; got != "100.00" {
		t.Fatalf("an expired reversal moved money: balance is %s", got)
	}
}

// TestTheSameDebitIsNotReturnedTwiceAcrossProcesses is the rule that matters
// most, proven where it has to hold: in the database, with the two reversals
// arriving at different instances at the same instant.
func TestTheSameDebitIsNotReturnedTwiceAcrossProcesses(t *testing.T) {
	c := startCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-doublereturn-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")
	betID := "bet-" + suffix

	if body, status := submit(t, c.next(0), "provider-a:"+betID,
		bet(player, walletID, betID, "25.00")); status != http.StatusOK {
		t.Fatalf("the bet answered %d: %s", status, body)
	}

	// A refund and a rollback of the same bet, released together at different
	// instances. They are different types, which is exactly why "no two of the
	// same type" would not be enough: both would hand back the same 25.00.
	type outcome struct {
		status int
		body   string
	}
	outcomes := make([]outcome, 2)
	kinds := []string{"REFUND", "ROLLBACK"}
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i, kind := range kinds {
		wg.Add(1)
		go func(i int, kind string) {
			defer wg.Done()
			external := fmt.Sprintf("%s-%s", strings.ToLower(kind), suffix)
			<-start
			body, status := submit(t, c.next(i), "provider-a:"+external,
				reversal(kind, player, walletID, external, "25.00", betID))
			outcomes[i] = outcome{status: status, body: body}
		}(i, kind)
	}
	close(start)
	wg.Wait()

	applied, refused := 0, 0
	for i, o := range outcomes {
		switch {
		case o.status == http.StatusOK && strings.Contains(o.body, "PROCESSED"):
			applied++
		case o.status == http.StatusUnprocessableEntity && strings.Contains(o.body, "ALREADY_REVERSED"):
			refused++
		default:
			t.Errorf("%s answered %d: %s", kinds[i], o.status, o.body)
		}
	}
	if applied != 1 || refused != 1 {
		t.Fatalf("%d applied and %d refused, want one of each", applied, refused)
	}

	// The 25.00 came back once.
	if got := readWallet(t, c.next(2), walletID).Balance.Amount; got != "100.00" {
		t.Fatalf("balance = %s, want 100.00", got)
	}
	assertLedgerMatchesBalance(t, c.next(0), walletID)
}

// TestReversingARefundIsAllowed guards the other side of the rule: the chain
// BET -> REFUND -> ROLLBACK(of the refund) is legitimate, and a rule that
// blocked it would be too blunt.
func TestReversingARefundIsAllowed(t *testing.T) {
	c := startCluster(t)
	suffix := time.Now().Format("150405.000000000")
	player := "player-chain-" + suffix

	walletID := openWallet(t, c.next(0), player, "100.00")
	betID, refundID, rollbackID := "bet-"+suffix, "refund-"+suffix, "rollback-"+suffix

	if _, status := submit(t, c.next(0), "provider-a:"+betID,
		bet(player, walletID, betID, "25.00")); status != http.StatusOK {
		t.Fatalf("the bet answered %d", status)
	}
	if _, status := submit(t, c.next(1), "provider-a:"+refundID,
		reversal("REFUND", player, walletID, refundID, "25.00", betID)); status != http.StatusOK {
		t.Fatalf("the refund answered %d", status)
	}

	// The rollback names the refund, not the bet: it undoes the return, not the
	// stake, and each operation has still been reversed exactly once.
	body, status := submit(t, c.next(2), "provider-a:"+rollbackID,
		reversal("ROLLBACK", player, walletID, rollbackID, "25.00", refundID))
	if status != http.StatusOK {
		t.Fatalf("the rollback answered %d: %s", status, body)
	}

	// Back where a bet that was never refunded would have left it.
	if got := readWallet(t, c.next(0), walletID).Balance.Amount; got != "75.00" {
		t.Fatalf("balance = %s, want 75.00", got)
	}
	assertLedgerMatchesBalance(t, c.next(1), walletID)
}
