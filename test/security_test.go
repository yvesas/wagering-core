//go:build integration

package test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/yvesas/wagering-core/internal/adapter/oidc/oidctest"
)

// These are the isolation scenarios, run against three independent processes
// and a real database.
//
// The rules themselves are proved in internal/app, against the use cases that
// decide them. What only this file can show is that they survive the whole
// stack -- a real token, a real signature check, three instances that share
// nothing but PostgreSQL -- and that the answer is the same whichever instance
// is asked.
//
//	make up-test && make test-scenarios

func TestNoCredentialReachesNoBusinessEndpoint(t *testing.T) {
	c := startCluster(t)
	base := c.next(0)

	// The load balancer has no token, and health is the one thing it asks for.
	if _, status := get(t, base+"/health/ready", ""); status != http.StatusOK {
		t.Errorf("readiness needed a credential: %d", status)
	}

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{name: "opening a wallet", method: http.MethodPost, path: "/wallets"},
		{name: "reading a wallet", method: http.MethodGet, path: "/wallets/any-wallet"},
		{name: "reading a ledger", method: http.MethodGet, path: "/wallets/any-wallet/ledger"},
		{name: "submitting an operation", method: http.MethodPost, path: "/wagering/transactions"},
		{name: "reading an operation", method: http.MethodGet, path: "/wagering/transactions/any-id"},
		{
			name:   "reading an operation by its business id",
			method: http.MethodGet,
			path:   "/providers/provider-a/wagering/transactions/any-id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, status := do(t, tc.method, base+tc.path, "", "{}", nil)
			if status != http.StatusUnauthorized {
				t.Fatalf("status = %d without a credential, want 401: %s", status, body)
			}
			if !strings.Contains(body, "UNAUTHENTICATED") {
				t.Errorf("the refusal does not say why: %s", body)
			}
		})
	}
}

func TestAForgedTokenIsRefusedByTheRunningService(t *testing.T) {
	c := startCluster(t)

	rogue, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the rogue key: %v", err)
	}
	// Every claim is right. Only the signature is somebody else's, and that is
	// the whole of what a bearer token is.
	forged, err := oidctest.SignWith(jwt.SigningMethodRS256, rogue, "key-1",
		issuer.Claims("provider-a", "wagering:submit"))
	if err != nil {
		t.Fatalf("signing the forgery: %v", err)
	}

	body, status := get(t, c.next(0)+"/wallets/any-wallet", forged)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d for a forged token, want 401: %s", status, body)
	}
}

func TestAProviderCannotOpenAWallet(t *testing.T) {
	c := startCluster(t)

	// Opening a wallet mints the initial balance. A provider that could do it
	// could credit itself.
	body, status := post(t, c.next(0)+"/wallets", providerToken(t, "provider-a"),
		`{"playerId":"player-sec-1","initialBalance":{"amount":"100.00","currency":"BRL"}}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", status, body)
	}
	if !strings.Contains(body, "FORBIDDEN") {
		t.Errorf("the refusal does not say why: %s", body)
	}
}

func TestTwoProvidersCannotSeeEachOther(t *testing.T) {
	c := startCluster(t)

	suffix := time.Now().Format("150405.000000000")
	player := "player-sec-" + suffix
	walletID := openWallet(t, c.next(0), player, "100.00")

	acme := providerToken(t, "provider-a")
	rival := providerToken(t, "provider-b")

	// provider-a places a bet, on one instance.
	external := "sec-bet-" + suffix
	body, status := submitAs(t, c.next(0), acme, "provider-a:"+external,
		bet(player, walletID, external, "25.00"))
	if status != http.StatusOK {
		t.Fatalf("the bet was not applied: %d %s", status, body)
	}
	transactionID := decodeSubmitted(t, body).TransactionID

	t.Run("it cannot be submitted again by someone else", func(t *testing.T) {
		// Byte for byte what provider-a sent, from provider-b's credential.
		// REQ-SEC-003: a replay must not reach another provider's operation.
		body, status := submitAs(t, c.next(1), rival, "provider-a:"+external,
			bet(player, walletID, external, "25.00"))
		if status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", status, body)
		}
	})

	t.Run("it cannot be read by its id", func(t *testing.T) {
		// A different instance, so the answer cannot come from anything one
		// process happens to remember.
		hidden, status := get(t, c.next(1)+"/wagering/transactions/"+transactionID, rival)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", status, hidden)
		}

		// 403 would confirm the id exists. The answer has to be the one a
		// transaction that never existed gets, not merely a similar one.
		missing, _ := get(t, c.next(1)+"/wagering/transactions/no-such-transaction", rival)
		if errorCode(t, hidden) != errorCode(t, missing) {
			t.Fatalf("the two answers differ: %s and %s", hidden, missing)
		}
	})

	t.Run("it cannot be read by its business id", func(t *testing.T) {
		body, status := get(t,
			c.next(2)+"/providers/provider-a/wagering/transactions/"+external, rival)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", status, body)
		}
	})

	t.Run("its owner still reads it from every instance", func(t *testing.T) {
		for i := range instances {
			body, status := get(t, c.next(i)+"/wagering/transactions/"+transactionID, acme)
			if status != http.StatusOK {
				t.Fatalf("instance %d answered %d to the owner: %s", i, status, body)
			}
		}
	})

	t.Run("the money moved exactly once", func(t *testing.T) {
		if got := readWallet(t, c.next(0), walletID).Balance.Amount; got != "75.00" {
			t.Fatalf("balance = %s, want 75.00", got)
		}
	})
}

func TestTheSameExternalIDUnderTwoProvidersIsTwoOperations(t *testing.T) {
	c := startCluster(t)

	suffix := time.Now().Format("150405.000000000")
	player := "player-shared-" + suffix
	walletID := openWallet(t, c.next(0), player, "100.00")
	external := "sec-shared-" + suffix

	// An operation is identified by (provider, externalId), and the idempotency
	// key is scoped the same way. Two providers numbering their operations the
	// same is not a collision -- and if it were, one provider's numbering would
	// be visible to the other.
	first, status := submitAs(t, c.next(0), providerToken(t, "provider-a"),
		"provider-a:"+external, bet(player, walletID, external, "25.00"))
	if status != http.StatusOK {
		t.Fatalf("provider-a: %d %s", status, first)
	}

	payload := betFor("provider-b", player, walletID, external, "25.00")
	second, status := submitAs(t, c.next(1), providerToken(t, "provider-b"),
		"provider-a:"+external, payload)
	if status != http.StatusOK {
		t.Fatalf("provider-b: %d %s", status, second)
	}

	if decodeSubmitted(t, second).Replay {
		t.Error("provider-b's operation was answered as provider-a's replay")
	}
	if decodeSubmitted(t, first).TransactionID == decodeSubmitted(t, second).TransactionID {
		t.Error("the two operations share a transaction id")
	}
	if got := readWallet(t, c.next(2), walletID).Balance.Amount; got != "50.00" {
		t.Fatalf("balance = %s, want 50.00 -- both bets applied", got)
	}
}

// --- helpers ---------------------------------------------------------------

type submittedBody struct {
	TransactionID string `json:"transactionId"`
	Status        string `json:"status"`
	Replay        bool   `json:"idempotentReplay"`
}

func decodeSubmitted(t *testing.T, body string) submittedBody {
	t.Helper()
	var out submittedBody
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return out
}

func errorCode(t *testing.T, body string) string {
	t.Helper()
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return out.Code
}

// betFor is bet with the provider chosen by the caller.
func betFor(providerID, playerID, walletID, externalID, amount string) string {
	return fmt.Sprintf(`{
	  "providerId":%q,
	  "externalTransactionId":%q,
	  "playerId":%q,
	  "walletId":%q,
	  "roundId":"round-1",
	  "gameId":"fortune-chimp",
	  "kind":"BET",
	  "money":{"amount":%q,"currency":"BRL"}
	}`, providerID, externalID, playerID, walletID, amount)
}
