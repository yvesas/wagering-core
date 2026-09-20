// Package oidctest serves an OpenID Connect issuer in the process under test.
//
// It exists because three suites need one -- the verifier's own tests, the
// composition test and the integration scenarios -- and because a fake verifier
// would not do: the thing worth proving is that a real signature is checked,
// and a stub that returns whatever it is told passes every case while the real
// code accepts forgeries.
//
// It is support code for tests. Keycloak is what runs in docker-compose.yml,
// and what it adds -- a realm, an admin interface, a login page -- is not what
// these prove.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Audience is the audience the issuer mints tokens for.
const Audience = "wagering-core"

// Issuer is an identity provider listening on a local port.
type Issuer struct {
	server *httptest.Server

	mu      sync.Mutex
	keys    map[string]*rsa.PrivateKey
	signing string

	jwksRequests atomic.Int64
}

// New starts an issuer with one signing key. Close it when the test ends.
func New() (*Issuer, error) {
	issuer := &Issuer{keys: map[string]*rsa.PrivateKey{}}
	if err := issuer.addKey("key-1"); err != nil {
		return nil, err
	}
	issuer.signing = "key-1"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{
			"issuer":   issuer.URL(),
			"jwks_uri": issuer.URL() + "/jwks",
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		issuer.jwksRequests.Add(1)
		writeJSON(w, map[string]any{"keys": issuer.jwks()})
	})

	issuer.server = httptest.NewServer(mux)
	return issuer, nil
}

// Close stops serving.
func (i *Issuer) Close() { i.server.Close() }

// URL is the issuer, as it appears in the iss claim.
func (i *Issuer) URL() string { return i.server.URL }

// JWKSRequests counts how often the key set has been fetched, which is how a
// test proves the cache is doing something.
func (i *Issuer) JWKSRequests() int64 { return i.jwksRequests.Load() }

func (i *Issuer) addKey(kid string) error {
	// 2048 bits: the smallest size still worth calling RSA, and generating it
	// is the slowest thing a test using this does.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generating a signing key: %w", err)
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.keys[kid] = key
	return nil
}

// Rotate replaces the key set with a new key, the way an issuer does when it
// rolls one: the old key stops being published *and* stops signing.
func (i *Issuer) Rotate(kid string) error {
	if err := i.addKey(kid); err != nil {
		return err
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	for existing := range i.keys {
		if existing != kid {
			delete(i.keys, existing)
		}
	}
	i.signing = kid
	return nil
}

// PublicModulus is the current signing key's modulus, for the test that signs
// an HMAC with it.
func (i *Issuer) PublicModulus() []byte {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.keys[i.signing].PublicKey.N.Bytes()
}

func (i *Issuer) jwks() []map[string]string {
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
	// Keycloak publishes one -- and has to be skipped rather than fail the set.
	keys = append(keys, map[string]string{
		"kty": "RSA", "use": "enc", "alg": "RSA-OAEP", "kid": "encryption-key",
		"n": "not-a-modulus", "e": "AQAB",
	})
	return keys
}

// Claims is a valid token body for a provider: everything filled, nothing to
// object to. A test changes the one field it is about.
func (i *Issuer) Claims(providerID string, scopes ...string) jwt.MapClaims {
	claims := jwt.MapClaims{
		"iss": i.URL(),
		"aud": Audience,
		"sub": "service-account-" + providerID,
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	if providerID != "" {
		claims["provider_id"] = providerID
	}
	if len(scopes) > 0 {
		claims["scope"] = strings.Join(scopes, " ")
	}
	return claims
}

// Token mints a token signed with the current key.
func (i *Issuer) Token(claims jwt.MapClaims) (string, error) {
	i.mu.Lock()
	kid := i.signing
	key := i.keys[kid]
	i.mu.Unlock()

	return SignWith(jwt.SigningMethodRS256, key, kid, claims)
}

// SignWith mints a token with a method and key of the caller's choosing, which
// is how a test produces something that must be refused.
func SignWith(method jwt.SigningMethod, key any, kid string, claims jwt.MapClaims) (string, error) {
	token := jwt.NewWithClaims(method, claims)
	if kid != "" {
		token.Header["kid"] = kid
	}
	signed, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("signing the token: %w", err)
	}
	return signed, nil
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
