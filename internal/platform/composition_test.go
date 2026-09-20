//go:build integration

package platform_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/yvesas/wagering-core/internal/adapter/oidc/oidctest"
	"github.com/yvesas/wagering-core/internal/platform"
)

// These tests build the real graph and start it.
//
// fx resolves dependencies at run time, so a missing or ambiguous provider is
// not a compile error -- it is a process that fails to boot. A graph only ever
// exercised in production is a graph that breaks in production, and validating
// it without starting would miss exactly the part that matters here: the order
// the lifecycle hooks run in.
//
//	make up-test && make test-integration

func freePort(t *testing.T) string {
	t.Helper()
	// Asking the kernel for a free port and handing it back is a small race,
	// and the alternative -- a hardcoded port -- fails whenever two runs
	// overlap, which is worse and harder to read.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func setTestEnv(t *testing.T, addr string) {
	t.Helper()

	for key, value := range map[string]string{
		"OIDC_ISSUER_URL": issuer.URL(),
		"OIDC_AUDIENCE":   oidctest.Audience,

		"APP_ENV":              "test",
		"APP_HTTP_ADDR":        addr,
		"APP_LOG_LEVEL":        "error",
		"APP_SHUTDOWN_TIMEOUT": "5s",
		"DB_HOST":              envOr("TEST_DB_HOST", "localhost"),
		"DB_PORT":              envOr("TEST_DB_PORT", "5433"),
		"DB_NAME":              envOr("TEST_DB_NAME", "wagering_test"),
		"DB_USER":              envOr("TEST_DB_USER", "wagering"),
		"DB_PASSWORD":          envOr("TEST_DB_PASSWORD", "local-dev-only"),
		"DB_SSLMODE":           "disable",

		// Without these the SDK looks for real AWS, and the graph fails to
		// build for a reason that has nothing to do with what is under test.
		"QUEUE_ENDPOINT":        envOr("TEST_QUEUE_ENDPOINT", "http://localhost:4567"),
		"QUEUE_REGION":          "us-east-1",
		"QUEUE_WAIT_TIME":       "1s",
		"AWS_ACCESS_KEY_ID":     "test",
		"AWS_SECRET_ACCESS_KEY": "test",
	} {
		t.Setenv(key, value)
	}

	// A queue per test, so one run cannot consume another's messages.
	unique := strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	t.Setenv("QUEUE_NAME", "composition-"+unique+".fifo")
	t.Setenv("QUEUE_DLQ_NAME", "composition-dlq-"+unique+".fifo")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestTheGraphStartsServesAndStops(t *testing.T) {
	addr := freePort(t)
	setTestEnv(t, addr)

	var pool *pgxpool.Pool
	application := fx.New(
		platform.Module,
		fx.Populate(&pool),
		fx.NopLogger,
	)

	startCtx, cancelStart := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStart()
	if err := application.Start(startCtx); err != nil {
		t.Fatalf("the graph did not start: %v", err)
	}

	base := "http://" + addr

	t.Run("readiness reports the database", func(t *testing.T) {
		body, status := get(t, base+"/health/ready")
		if status != http.StatusOK {
			t.Fatalf("status = %d: %s", status, body)
		}
		if !strings.Contains(body, `"postgres":"ok"`) {
			t.Errorf("readiness did not report postgres: %s", body)
		}
	})

	t.Run("a wallet can be opened end to end", func(t *testing.T) {
		// One request through every layer that exists: handler, use case,
		// domain, unit of work, repositories, database.
		payload := fmt.Sprintf(
			`{"playerId":%q,"initialBalance":{"amount":"250.00","currency":"BRL"}}`,
			"player-composition-"+time.Now().Format("150405.000000000"))

		body, status := post(t, base+"/wallets", payload)
		if status != http.StatusCreated {
			t.Fatalf("status = %d: %s", status, body)
		}

		var created struct {
			ID      string `json:"id"`
			Balance struct {
				Amount string `json:"amount"`
			} `json:"balance"`
			Version int64 `json:"version"`
		}
		if err := json.Unmarshal([]byte(body), &created); err != nil {
			t.Fatalf("decoding %s: %v", body, err)
		}
		if created.Balance.Amount != "250.00" || created.Version != 1 {
			t.Fatalf("created %+v", created)
		}

		ledger, status := get(t, base+"/wallets/"+created.ID+"/ledger")
		if status != http.StatusOK {
			t.Fatalf("ledger status = %d: %s", status, ledger)
		}
		if !strings.Contains(ledger, `"direction":"CREDIT"`) {
			t.Errorf("the opening credit is missing: %s", ledger)
		}
	})

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStop()
	if err := application.Stop(stopCtx); err != nil {
		t.Fatalf("the graph did not stop cleanly: %v", err)
	}

	t.Run("the listener is released", func(t *testing.T) {
		// Binding the same address again is the honest check: if the server had
		// leaked its listener, this is where it shows.
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("the address is still held after stop: %v", err)
		}
		_ = listener.Close()
	})

	t.Run("the pool is closed", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err == nil {
			t.Fatal("the pool still answers after stop")
		}
	})
}

func TestStartFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	setTestEnv(t, freePort(t))
	t.Setenv("DB_PORT", "1")

	application := fx.New(platform.Module, fx.NopLogger)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Starting "successfully" and failing on the first request would move a
	// configuration mistake from where it is obvious into somewhere it looks
	// like a bug in the request.
	if err := application.Start(ctx); err == nil {
		_ = application.Stop(ctx)
		t.Fatal("the graph started without a database")
	}
}

func TestStartFailsOnMalformedConfiguration(t *testing.T) {
	setTestEnv(t, freePort(t))
	// No unit. Falling back to the default here would mean a deploy that looks
	// configured and drains for the default anyway.
	t.Setenv("APP_SHUTDOWN_TIMEOUT", "30")

	application := fx.New(platform.Module, fx.NopLogger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := application.Start(ctx); err == nil {
		_ = application.Stop(ctx)
		t.Fatal("the graph started with an unparseable timeout")
	}
}

func TestPortAlreadyInUseFailsTheStart(t *testing.T) {
	addr := freePort(t)
	setTestEnv(t, addr)

	// Hold the port so the server cannot have it.
	blocker, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("holding the port: %v", err)
	}
	defer blocker.Close()

	application := fx.New(platform.Module, fx.NopLogger)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// The listener is opened in the start hook rather than inside a goroutine
	// precisely so this fails the start. Left to Serve, the process would
	// report "started" and serve nothing.
	if err := application.Start(ctx); err == nil {
		_ = application.Stop(ctx)
		t.Fatal("the graph started on a port it could not bind")
	}
}

// The identity provider these tests authenticate against.
//
// The graph reaches it while it is being built, so it has to outlive any one
// test. What is under test here is composition -- that the whole thing stands
// up, serves and shuts down -- so every request carries one credential with
// every scope. Who may do what is proved in internal/app, against the use cases
// that decide it.
var issuer *oidctest.Issuer

func TestMain(m *testing.M) {
	started, err := oidctest.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting the test issuer: %v\n", err)
		os.Exit(1)
	}
	issuer = started

	code := m.Run()
	issuer.Close()
	os.Exit(code)
}

// credentialFor mints a token that acts for a provider and carries every scope.
//
// Every scope on purpose: these tests are about composition -- that the whole
// thing stands up, serves and shuts down -- so a missing scope would fail them
// for a reason that has nothing to do with what they assert. Who may do what is
// proved in internal/app, against the use cases that decide it.
func credentialFor(t *testing.T, providerID string) string {
	t.Helper()
	token, err := issuer.Token(issuer.Claims(providerID,
		"wagering:submit", "wagering:read", "wallets:manage"))
	if err != nil {
		t.Fatalf("minting a token: %v", err)
	}
	return token
}

func credential(t *testing.T) string {
	t.Helper()
	return credentialFor(t, "provider-a")
}

func do(t *testing.T, method, url, token, payload string, headers map[string]string) (string, int) {
	t.Helper()

	var body io.Reader
	if payload != "" {
		body = strings.NewReader(payload)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
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

func get(t *testing.T, url string) (string, int) {
	t.Helper()
	return do(t, http.MethodGet, url, credential(t), "", nil)
}

func post(t *testing.T, url, payload string) (string, int) {
	t.Helper()
	return do(t, http.MethodPost, url, credential(t), payload, nil)
}

// TestIdempotencySurvivesARestart is the test the whole phase exists for.
//
// It sends an operation, tears the entire process graph down -- every pool,
// every in-memory anything -- builds a brand new one against the same database,
// and sends the identical request again. Nothing carried over in RAM, which is
// the only honest way to show that idempotency lives in the database.
//
// It also checks the part that is easy to get wrong and passes every happy-path
// test: the replay returns the balance observed at the time, not the balance
// the wallet has now.
func TestIdempotencySurvivesARestart(t *testing.T) {
	suffix := time.Now().Format("150405.000000000")

	// --- first process --------------------------------------------------
	addr := freePort(t)
	setTestEnv(t, addr)
	first := fx.New(platform.Module, fx.NopLogger)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := first.Start(ctx); err != nil {
		t.Fatalf("starting: %v", err)
	}
	base := "http://" + addr

	walletID := openWallet(t, base, "player-restart-"+suffix, "100.00")

	bet := fmt.Sprintf(`{
	  "providerId":"provider-restart-%[1]s",
	  "externalTransactionId":"tx-%[1]s",
	  "playerId":"player-restart-%[1]s",
	  "walletId":%[2]q,
	  "roundId":"round-1",
	  "gameId":"fortune-chimp",
	  "kind":"BET",
	  "money":{"amount":"25.00","currency":"BRL"}
	}`, suffix, walletID)
	key := "provider-restart-" + suffix + ":tx-" + suffix

	body, status := submit(t, base, key, bet)
	if status != http.StatusOK {
		t.Fatalf("first submission: %d %s", status, body)
	}
	firstResult := decodeSubmit(t, body)
	if firstResult.Balance.Amount != "75.00" || firstResult.Replay {
		t.Fatalf("first submission: %+v", firstResult)
	}

	// Move the wallet, so "the balance now" and "the balance then" differ.
	win := strings.ReplaceAll(bet, `"tx-`+suffix+`"`, `"tx-win-`+suffix+`"`)
	win = strings.ReplaceAll(win, `"BET"`, `"WIN"`)
	win = strings.ReplaceAll(win, `"25.00"`, `"500.00"`)
	if body, status := submit(t, base, key+":win", win); status != http.StatusOK {
		t.Fatalf("the intervening win: %d %s", status, body)
	}

	// --- tear the process down ------------------------------------------
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStop()
	if err := first.Stop(stopCtx); err != nil {
		t.Fatalf("stopping: %v", err)
	}

	// --- a completely new process ---------------------------------------
	secondAddr := freePort(t)
	setTestEnv(t, secondAddr)
	second := fx.New(platform.Module, fx.NopLogger)

	if err := second.Start(ctx); err != nil {
		t.Fatalf("restarting: %v", err)
	}
	defer func() {
		if err := second.Stop(stopCtx); err != nil {
			t.Errorf("stopping the second process: %v", err)
		}
	}()
	restarted := "http://" + secondAddr

	body, status = submit(t, restarted, key, bet)
	if status != http.StatusOK {
		t.Fatalf("the resend after the restart: %d %s", status, body)
	}
	replayed := decodeSubmit(t, body)

	if !replayed.Replay {
		t.Error("the restarted process did not recognise the operation")
	}
	if replayed.TransactionID != firstResult.TransactionID {
		t.Error("the resend created a second transaction")
	}
	// The easy implementation returns the wallet's balance now -- 575.00 -- and
	// passes every happy-path test. This is the assertion that catches it.
	if replayed.Balance.Amount != "75.00" {
		t.Fatalf("the replay returned %s, want the 75.00 observed at the time",
			replayed.Balance.Amount)
	}

	// And the money moved exactly once.
	wallet, status := get(t, restarted+"/wallets/"+walletID)
	if status != http.StatusOK {
		t.Fatalf("reading the wallet: %d %s", status, wallet)
	}
	if !strings.Contains(wallet, `"amount":"575.00"`) {
		t.Fatalf("balance after the resend: %s", wallet)
	}

	ledger, _ := get(t, restarted+"/wallets/"+walletID+"/ledger")
	// The opening credit, the bet and the win. A fourth would mean the resend
	// moved money.
	if got := strings.Count(ledger, `"direction"`); got != 3 {
		t.Fatalf("got %d ledger entries, want 3: %s", got, ledger)
	}
}

func TestKeyReusedWithDifferentContentIsRefused(t *testing.T) {
	suffix := time.Now().Format("150405.000000000")
	addr := freePort(t)
	setTestEnv(t, addr)

	application := fx.New(platform.Module, fx.NopLogger)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatalf("starting: %v", err)
	}
	defer application.Stop(ctx)

	base := "http://" + addr
	walletID := openWallet(t, base, "player-conflict-"+suffix, "1000.00")
	key := "provider-conflict-" + suffix + ":key"

	operation := func(external, amount string) string {
		return fmt.Sprintf(`{
		  "providerId":"provider-conflict-%[1]s",
		  "externalTransactionId":%[2]q,
		  "playerId":"player-conflict-%[1]s",
		  "walletId":%[3]q,
		  "roundId":"round-1",
		  "gameId":"fortune-chimp",
		  "kind":"BET",
		  "money":{"amount":%[4]q,"currency":"BRL"}
		}`, suffix, external, walletID, amount)
	}

	if body, status := submit(t, base, key, operation("tx-a-"+suffix, "25.00")); status != http.StatusOK {
		t.Fatalf("first: %d %s", status, body)
	}

	// Same key, different operation.
	body, status := submit(t, base, key, operation("tx-b-"+suffix, "99.00"))
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", status, body)
	}

	// Same operation, a fresh key: still refused, because the pair
	// (provider, externalId) is what identifies it.
	body, status = submit(t, base, key+"-different", operation("tx-a-"+suffix, "25.00"))
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", status, body)
	}

	wallet, _ := get(t, base+"/wallets/"+walletID)
	if !strings.Contains(wallet, `"amount":"975.00"`) {
		t.Fatalf("a refused submission moved money: %s", wallet)
	}
}

func TestConcurrentDuplicatesMoveMoneyOnce(t *testing.T) {
	suffix := time.Now().Format("150405.000000000")
	addr := freePort(t)
	setTestEnv(t, addr)

	application := fx.New(platform.Module, fx.NopLogger)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatalf("starting: %v", err)
	}
	defer application.Stop(ctx)

	base := "http://" + addr
	walletID := openWallet(t, base, "player-race-"+suffix, "1000.00")

	payload := fmt.Sprintf(`{
	  "providerId":"provider-race-%[1]s",
	  "externalTransactionId":"tx-%[1]s",
	  "playerId":"player-race-%[1]s",
	  "walletId":%[2]q,
	  "roundId":"round-1",
	  "gameId":"fortune-chimp",
	  "kind":"BET",
	  "money":{"amount":"25.00","currency":"BRL"}
	}`, suffix, walletID)
	key := "provider-race-" + suffix + ":tx-" + suffix

	// Twenty copies of the same request, released together. The lookup before
	// the insert cannot save this -- all twenty find nothing -- so what refuses
	// nineteen of them is the uniqueness constraint.
	const copies = 20
	start := make(chan struct{})
	statuses := make([]int, copies)
	var wg sync.WaitGroup

	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, status := submit(t, base, key, payload)
			statuses[i] = status
		}(i)
	}
	close(start)
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Errorf("copy %d answered %d; every copy should get the same result", i, status)
		}
	}

	wallet, _ := get(t, base+"/wallets/"+walletID)
	if !strings.Contains(wallet, `"amount":"975.00"`) {
		t.Fatalf("twenty duplicates moved money more than once: %s", wallet)
	}

	ledger, _ := get(t, base+"/wallets/"+walletID+"/ledger")
	if got := strings.Count(ledger, `"direction"`); got != 2 {
		t.Fatalf("got %d ledger entries, want 2 (the opening and one debit)", got)
	}
}

type submitBody struct {
	TransactionID string `json:"transactionId"`
	Status        string `json:"status"`
	Balance       struct {
		Amount string `json:"amount"`
	} `json:"balance"`
	Replay      bool   `json:"idempotentReplay"`
	FailureCode string `json:"failureCode"`
}

func decodeSubmit(t *testing.T, body string) submitBody {
	t.Helper()
	var out submitBody
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return out
}

func openWallet(t *testing.T, base, playerID, amount string) string {
	t.Helper()
	body, status := post(t, base+"/wallets", fmt.Sprintf(
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

// submit sends an operation under the credential of the provider the operation
// itself names.
//
// The provider is read out of the payload rather than passed alongside it. The
// two have to agree -- the token is the authority over providerId and a
// disagreement is a 403 -- and a second copy of the string in every call is a
// second copy to get out of step. These tests give each run its own provider so
// their rows cannot collide with an earlier one's.
func submit(t *testing.T, base, key, payload string) (string, int) {
	t.Helper()

	var operation struct {
		ProviderID string `json:"providerId"`
	}
	if err := json.Unmarshal([]byte(payload), &operation); err != nil {
		t.Fatalf("reading the provider out of %s: %v", payload, err)
	}

	return do(t, http.MethodPost, base+"/wagering/transactions",
		credentialFor(t, operation.ProviderID), payload,
		map[string]string{"Idempotency-Key": key})
}
