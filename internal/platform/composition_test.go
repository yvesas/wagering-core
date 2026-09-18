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
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

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
	} {
		t.Setenv(key, value)
	}
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

func get(t *testing.T, url string) (string, int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

func post(t *testing.T, url, payload string) (string, int) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}
