//go:build integration

package test

import (
	"fmt"
	"os"
	"testing"

	"github.com/yvesas/wagering-core/internal/adapter/oidc/oidctest"
)

// The issuer these scenarios authenticate against.
//
// It runs in the test process and the instances reach it over the loopback,
// exactly as they would reach Keycloak. Keycloak itself stays out of the
// automated suite: it would add a container, a minute of start-up and an admin
// interface to prove something a key pair already proves.
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

// token mints a credential for a provider with the scopes given.
func token(t *testing.T, providerID string, scopes ...string) string {
	t.Helper()
	minted, err := issuer.Token(issuer.Claims(providerID, scopes...))
	if err != nil {
		t.Fatalf("minting a token: %v", err)
	}
	return minted
}

// platformToken is the internal service: it opens wallets and reads them, and
// it acts for no provider.
func platformToken(t *testing.T) string {
	t.Helper()
	return token(t, "", "wallets:manage", "wagering:read")
}

// providerToken is a provider's credential, which may submit and read back what
// it submitted -- and nothing else.
func providerToken(t *testing.T, providerID string) string {
	t.Helper()
	return token(t, providerID, "wagering:submit", "wagering:read")
}
