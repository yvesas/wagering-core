package oidc

import (
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/yvesas/wagering-core/internal/adapter/oidc/oidctest"
)

// The issuer itself lives in oidctest, because the composition test and the
// integration scenarios need the same one. What is left here is the wrapping
// that turns its errors into t.Fatalf.

const testAudience = oidctest.Audience

func newTestIssuer(t *testing.T) *oidctest.Issuer {
	t.Helper()
	issuer, err := oidctest.New()
	if err != nil {
		t.Fatalf("starting the test issuer: %v", err)
	}
	t.Cleanup(issuer.Close)
	return issuer
}

// claimsFor is a valid provider token body, as acme with both provider scopes.
func claimsFor(issuer *oidctest.Issuer) jwt.MapClaims {
	return issuer.Claims("acme", "wagering:submit", "wagering:read")
}

func mint(t *testing.T, issuer *oidctest.Issuer, claims jwt.MapClaims) string {
	t.Helper()
	token, err := issuer.Token(claims)
	if err != nil {
		t.Fatalf("minting a token: %v", err)
	}
	return token
}

func signWith(t *testing.T, method jwt.SigningMethod, key any, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token, err := oidctest.SignWith(method, key, kid, claims)
	if err != nil {
		t.Fatalf("signing a token: %v", err)
	}
	return token
}

func rotate(t *testing.T, issuer *oidctest.Issuer, kid string) {
	t.Helper()
	if err := issuer.Rotate(kid); err != nil {
		t.Fatalf("rotating the signing key: %v", err)
	}
}

// movableClock is a clock a test pushes forward, so a cache TTL and a refresh
// floor can be crossed without waiting for them.
type movableClock struct {
	mu sync.Mutex
	at time.Time
}

func newMovableClock() *movableClock {
	return &movableClock{at: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
}

func (c *movableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *movableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}
