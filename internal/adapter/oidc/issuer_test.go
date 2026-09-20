package oidc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testIssuer is an identity provider in this process.
//
// The tests sign with a real key and this serves a real JWKS, because the thing
// under test *is* the signature check: a fake verifier that returns whatever it
// is told would pass every case in this file while the real one accepted
// forgeries. Keycloak is what runs in docker-compose.yml, and what it adds --
// an admin interface, a realm, a login page -- is not what these prove.
type testIssuer struct {
	server *httptest.Server

	mu   sync.Mutex
	keys map[string]*rsa.PrivateKey

	// signing is the key new tokens are minted with, so a test can rotate by
	// pointing it somewhere else.
	signing string

	jwksRequests atomic.Int64
}

const testAudience = "wagering-core"

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()

	issuer := &testIssuer{keys: map[string]*rsa.PrivateKey{}}
	issuer.addKey(t, "key-1")
	issuer.signing = "key-1"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{
			"issuer":   issuer.url(),
			"jwks_uri": issuer.url() + "/jwks",
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		issuer.jwksRequests.Add(1)
		writeJSON(w, map[string]any{"keys": issuer.jwks()})
	})

	issuer.server = httptest.NewServer(mux)
	t.Cleanup(issuer.server.Close)
	return issuer
}

func (i *testIssuer) url() string { return i.server.URL }

func (i *testIssuer) addKey(t *testing.T, kid string) {
	t.Helper()
	// 2048 bits: the smallest size still worth calling RSA, and generating it
	// is the slowest thing these tests do.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating a signing key: %v", err)
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.keys[kid] = key
}

// rotate replaces the key set with a new key, the way an issuer does when it
// rolls one. The old key stops being published *and* stops signing.
func (i *testIssuer) rotate(t *testing.T, kid string) {
	t.Helper()
	i.addKey(t, kid)

	i.mu.Lock()
	defer i.mu.Unlock()
	for existing := range i.keys {
		if existing != kid {
			delete(i.keys, existing)
		}
	}
	i.signing = kid
}

func (i *testIssuer) jwks() []map[string]string {
	i.mu.Lock()
	defer i.mu.Unlock()

	keys := make([]map[string]string, 0, len(i.keys)+1)
	for kid, key := range i.keys {
		keys = append(keys, map[string]string{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": kid,
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		})
	}

	// An encryption key sits next to the signing key in a real key set --
	// Keycloak publishes one. It has to be skipped rather than fail the set.
	keys = append(keys, map[string]string{
		"kty": "RSA", "use": "enc", "alg": "RSA-OAEP", "kid": "encryption-key",
		"n": "not-a-modulus", "e": "AQAB",
	})
	return keys
}

// claims is the token body, with everything a valid one needs already filled.
func (i *testIssuer) claims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":         i.url(),
		"aud":         testAudience,
		"sub":         "service-account-acme",
		"exp":         time.Now().Add(time.Hour).Unix(),
		"iat":         time.Now().Unix(),
		"scope":       "wagering:submit wagering:read",
		"provider_id": "acme",
	}
}

// token mints a signed token from the claims, with the current signing key.
func (i *testIssuer) token(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()

	i.mu.Lock()
	kid := i.signing
	key := i.keys[kid]
	i.mu.Unlock()

	return signWith(t, jwt.SigningMethodRS256, key, kid, claims)
}

func signWith(t *testing.T, method jwt.SigningMethod, key any, kid string, claims jwt.MapClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(method, claims)
	if kid != "" {
		token.Header["kid"] = kid
	}
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("signing the token: %v", err)
	}
	return signed
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
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
