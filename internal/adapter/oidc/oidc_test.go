package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/yvesas/wagering-core/internal/app"
)

func newVerifier(t *testing.T, issuer *testIssuer) *Verifier {
	t.Helper()
	verifier, err := New(context.Background(), Config{
		IssuerURL: issuer.url(),
		Audience:  testAudience,
	})
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	return verifier
}

func TestVerifyAcceptsATokenFromTheIssuer(t *testing.T) {
	t.Parallel()
	issuer := newTestIssuer(t)
	verifier := newVerifier(t, issuer)

	identity, err := verifier.Verify(context.Background(), issuer.token(t, issuer.claims()))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if identity.Subject() != "service-account-acme" {
		t.Errorf("subject = %q", identity.Subject())
	}
	if identity.ProviderID().String() != "acme" {
		t.Errorf("provider = %q", identity.ProviderID())
	}
	if !identity.Has(app.ScopeSubmit) || !identity.Has(app.ScopeRead) {
		t.Errorf("scopes = %v", identity.Scopes())
	}
	if identity.Has(app.ScopeWallets) {
		t.Error("a provider credential was given the internal scope")
	}
}

// TestVerifyRefusesAForgery is the table this package exists for. Every case is
// a token that must not be accepted, and each one is a different way of being
// wrong -- so a change that broke only one of them cannot hide behind the rest.
func TestVerifyRefusesAForgery(t *testing.T) {
	t.Parallel()
	issuer := newTestIssuer(t)
	verifier := newVerifier(t, issuer)

	// An attacker's key. Nothing at the issuer has ever heard of it.
	rogue, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the rogue key: %v", err)
	}

	cases := []struct {
		name  string
		token func(t *testing.T) string
	}{
		{
			name: "expired",
			token: func(t *testing.T) string {
				claims := issuer.claims()
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
				return issuer.token(t, claims)
			},
		},
		{
			name: "not valid yet",
			token: func(t *testing.T) string {
				claims := issuer.claims()
				claims["nbf"] = time.Now().Add(time.Hour).Unix()
				return issuer.token(t, claims)
			},
		},
		{
			// A token with no expiry never stops working, so a leaked one is a
			// permanent credential.
			name: "no expiry at all",
			token: func(t *testing.T) string {
				claims := issuer.claims()
				delete(claims, "exp")
				return issuer.token(t, claims)
			},
		},
		{
			// Signed by the same issuer, with the same key, for a different
			// service. Accepting it would make every system in the realm a way
			// in here.
			name: "minted for another audience",
			token: func(t *testing.T) string {
				claims := issuer.claims()
				claims["aud"] = "some-other-service"
				return issuer.token(t, claims)
			},
		},
		{
			name: "issued by someone else",
			token: func(t *testing.T) string {
				claims := issuer.claims()
				claims["iss"] = "https://issuer.example.invalid"
				return issuer.token(t, claims)
			},
		},
		{
			name: "signed by a key the issuer never published",
			token: func(t *testing.T) string {
				return signWith(t, jwt.SigningMethodRS256, rogue, "key-1", issuer.claims())
			},
		},
		{
			// The forgery that costs nothing: edit the header to say the token
			// is unsigned and hope the library obliges.
			name: "alg none",
			token: func(t *testing.T) string {
				return signWith(t, jwt.SigningMethodNone,
					jwt.UnsafeAllowNoneSignatureType, "key-1", issuer.claims())
			},
		},
		{
			// Algorithm confusion: the RSA *public* key is public, so a
			// verifier that honours "HS256" would be checking an HMAC against a
			// secret the attacker also has.
			name: "an hmac signed with the public key",
			token: func(t *testing.T) string {
				issuer.mu.Lock()
				public := issuer.keys[issuer.signing].PublicKey.N.Bytes()
				issuer.mu.Unlock()
				return signWith(t, jwt.SigningMethodHS256, public, "key-1", issuer.claims())
			},
		},
		{
			name: "a key id that is not in the set",
			token: func(t *testing.T) string {
				return signWith(t, jwt.SigningMethodRS256, rogue, "key-nobody-has", issuer.claims())
			},
		},
		{
			name: "no key id at all",
			token: func(t *testing.T) string {
				return signWith(t, jwt.SigningMethodRS256, rogue, "", issuer.claims())
			},
		},
		{
			name:  "not a token",
			token: func(*testing.T) string { return "this is not a jwt" },
		},
		{
			// A mapper that emits the wrong type. Coercing it would turn a
			// provider identifier into whatever %v prints.
			name: "a provider claim that is not a string",
			token: func(t *testing.T) string {
				claims := issuer.claims()
				claims["provider_id"] = 42
				return issuer.token(t, claims)
			},
		},
		{
			name: "no subject",
			token: func(t *testing.T) string {
				claims := issuer.claims()
				delete(claims, "sub")
				return issuer.token(t, claims)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := verifier.Verify(context.Background(), tc.token(t))
			if !errors.Is(err, app.ErrUnauthenticated) {
				t.Fatalf("Verify = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestACredentialWithNoProviderIsTheInternalService(t *testing.T) {
	t.Parallel()
	issuer := newTestIssuer(t)
	verifier := newVerifier(t, issuer)

	claims := issuer.claims()
	delete(claims, "provider_id")
	claims["scope"] = "wallets:manage"

	identity, err := verifier.Verify(context.Background(), issuer.token(t, claims))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if identity.IsProvider() {
		t.Error("a token with no provider claim produced a provider identity")
	}
	if !identity.Has(app.ScopeWallets) {
		t.Errorf("scopes = %v", identity.Scopes())
	}
}

func TestScopesArriveInEitherShape(t *testing.T) {
	t.Parallel()
	issuer := newTestIssuer(t)
	verifier := newVerifier(t, issuer)

	claims := issuer.claims()
	delete(claims, "scope")
	// Some issuers send an array in "scp" instead of a space-delimited string.
	claims["scp"] = []any{"wagering:submit", 7}

	identity, err := verifier.Verify(context.Background(), issuer.token(t, claims))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !identity.Has(app.ScopeSubmit) {
		t.Errorf("scopes = %v", identity.Scopes())
	}
}

func TestTheKeySetIsCached(t *testing.T) {
	t.Parallel()
	issuer := newTestIssuer(t)
	verifier := newVerifier(t, issuer)

	for range 5 {
		if _, err := verifier.Verify(context.Background(), issuer.token(t, issuer.claims())); err != nil {
			t.Fatalf("Verify: %v", err)
		}
	}

	// One fetch for five tokens. Without the cache this endpoint would be
	// called once per request, and the issuer would become a dependency of
	// every single call rather than of the first one.
	if got := issuer.jwksRequests.Load(); got != 1 {
		t.Fatalf("the jwks was fetched %d times, want 1", got)
	}
}

func TestARotatedKeyIsPickedUpWithoutARestart(t *testing.T) {
	t.Parallel()
	issuer := newTestIssuer(t)
	clock := newMovableClock()

	verifier, err := New(context.Background(), Config{
		IssuerURL: issuer.url(),
		Audience:  testAudience,
		Clock:     clock,
	})
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}

	if _, err := verifier.Verify(context.Background(), issuer.token(t, issuer.claims())); err != nil {
		t.Fatalf("Verify before the rotation: %v", err)
	}

	issuer.rotate(t, "key-2")

	// Straight after a fetch, an unknown key id does not send us back to the
	// issuer: that is the floor doing its job, and it is what a rotation costs.
	if _, err := verifier.Verify(context.Background(), issuer.token(t, issuer.claims())); err == nil {
		t.Fatal("an unknown key id refetched inside the refresh floor")
	}

	clock.advance(minRefreshInterval + time.Second)

	// Past the floor and still well inside the cache TTL. Refusing here would
	// mean every rotation takes the service down until someone restarts it.
	identity, err := verifier.Verify(context.Background(), issuer.token(t, issuer.claims()))
	if err != nil {
		t.Fatalf("Verify after the rotation: %v", err)
	}
	if identity.ProviderID().String() != "acme" {
		t.Errorf("provider = %q", identity.ProviderID())
	}
}

func TestAnUnknownKeyIdDoesNotRefetchWithoutLimit(t *testing.T) {
	t.Parallel()
	issuer := newTestIssuer(t)
	verifier := newVerifier(t, issuer)

	rogue, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the rogue key: %v", err)
	}

	// Ten tokens, ten key ids nobody has ever published. Without the floor on
	// refreshes, each one would send us to the issuer -- a free way to make
	// this service hammer its own identity provider.
	for range 10 {
		token := signWith(t, jwt.SigningMethodRS256, rogue, "made-up", issuer.claims())
		if _, err := verifier.Verify(context.Background(), token); err == nil {
			t.Fatal("a token signed by an unpublished key was accepted")
		}
	}

	if got := issuer.jwksRequests.Load(); got > 2 {
		t.Fatalf("the jwks was fetched %d times for ten unknown key ids", got)
	}
}

func TestAnIssuerThatIsBrieflyDownDoesNotInvalidateEveryToken(t *testing.T) {
	t.Parallel()
	issuer := newTestIssuer(t)

	verifier, err := New(context.Background(), Config{
		IssuerURL: issuer.url(),
		Audience:  testAudience,
		// Expire the cache immediately, so the next verification has to refetch
		// and will find the issuer unreachable.
		CacheTTL: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}

	token := issuer.token(t, issuer.claims())
	if _, err := verifier.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify while the issuer is up: %v", err)
	}

	issuer.server.Close()

	// The keys we already hold verify this signature perfectly well. Dropping
	// them because the issuer is unreachable would turn its outage into ours.
	if _, err := verifier.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify while the issuer is down: %v", err)
	}
}

func TestNewRefusesAMisconfiguredIssuer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		config func(t *testing.T) Config
	}{
		{
			name: "no issuer url",
			config: func(*testing.T) Config {
				return Config{Audience: testAudience}
			},
		},
		{
			// Without an audience every token this issuer ever minted, for any
			// service in the realm, would be accepted here.
			name: "no audience",
			config: func(t *testing.T) Config {
				return Config{IssuerURL: newTestIssuer(t).url()}
			},
		},
		{
			name: "an issuer that is not there",
			config: func(*testing.T) Config {
				return Config{IssuerURL: "http://127.0.0.1:1", Audience: testAudience}
			},
		},
		{
			// A wrong URL, or a redirect, handing us somebody else's key set.
			// Every token that key signed would then verify.
			name: "a discovery document naming another issuer",
			config: func(t *testing.T) Config {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(w, map[string]string{
						"issuer":   "https://somebody.else.invalid",
						"jwks_uri": "https://somebody.else.invalid/jwks",
					})
				}))
				t.Cleanup(server.Close)
				return Config{IssuerURL: server.URL, Audience: testAudience}
			},
		},
		{
			name: "a discovery document with no jwks_uri",
			config: func(t *testing.T) Config {
				var server *httptest.Server
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(w, map[string]string{"issuer": server.URL})
				}))
				t.Cleanup(server.Close)
				return Config{IssuerURL: server.URL, Audience: testAudience}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(context.Background(), tc.config(t)); err == nil {
				t.Fatal("New accepted a misconfigured issuer")
			}
		})
	}
}

func TestClockSkewIsAllowedButNotUnbounded(t *testing.T) {
	t.Parallel()
	issuer := newTestIssuer(t)

	verifier, err := New(context.Background(), Config{
		IssuerURL: issuer.url(),
		Audience:  testAudience,
		Leeway:    time.Minute,
	})
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}

	justExpired := issuer.claims()
	justExpired["exp"] = time.Now().Add(-30 * time.Second).Unix()
	if _, err := verifier.Verify(context.Background(), issuer.token(t, justExpired)); err != nil {
		t.Errorf("a token inside the skew allowance was refused: %v", err)
	}

	longExpired := issuer.claims()
	longExpired["exp"] = time.Now().Add(-10 * time.Minute).Unix()
	// The allowance is for two clocks disagreeing, not for keeping dead tokens
	// alive. A generous one is an expiry that does not expire.
	if _, err := verifier.Verify(context.Background(), issuer.token(t, longExpired)); err == nil {
		t.Error("a token ten minutes past its expiry was accepted")
	}
}
