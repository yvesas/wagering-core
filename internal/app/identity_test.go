package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yvesas/wagering-core/internal/domain"
)

// testCaller is who the rest of this package's tests run as: provider-a, with
// every scope this service defines.
//
// A single permissive credential on purpose. These tests are about money, and
// giving each of them its own identity would make every one of them also a test
// of authorisation -- which is what the cases in this file are for.
var testCaller = mustIdentity(IdentityParams{
	Subject:    "test-suite",
	ProviderID: "provider-a",
	Scopes:     []string{string(ScopeSubmit), string(ScopeRead), string(ScopeWallets)},
})

func mustIdentity(p IdentityParams) Identity {
	identity, err := NewIdentity(p)
	if err != nil {
		panic("building a test identity: " + err.Error())
	}
	return identity
}

// callerContext is the context a use case is called with in these tests.
func callerContext() context.Context {
	return WithIdentity(context.Background(), testCaller)
}

func TestNewIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		params IdentityParams
		want   error
	}{
		{
			name:   "a provider credential",
			params: IdentityParams{Subject: "sa-acme", ProviderID: "acme", Scopes: []string{"wagering:submit"}},
		},
		{
			name:   "an internal credential has no provider",
			params: IdentityParams{Subject: "sa-platform", Scopes: []string{"wallets:manage"}},
		},
		{
			name:   "a credential with no subject cannot be named in an audit",
			params: IdentityParams{ProviderID: "acme", Scopes: []string{"wagering:submit"}},
			want:   ErrUnauthenticated,
		},
		{
			name: "a provider id the domain refuses",
			params: IdentityParams{
				Subject:    "sa-acme",
				ProviderID: string(make([]byte, 200)),
			},
			want: ErrUnauthenticated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewIdentity(tc.params)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUnknownScopesAreDroppedRatherThanRefused(t *testing.T) {
	t.Parallel()
	// An identity provider serves more than this service. A token carrying
	// "profile" is not malformed; it carries a scope we have no use for.
	identity := mustIdentity(IdentityParams{
		Subject: "sa-acme",
		Scopes:  []string{"openid", "profile", "wallets:manage", "wallets:manage"},
	})

	if got := identity.Scopes(); len(got) != 1 || got[0] != ScopeWallets {
		t.Fatalf("scopes = %v, want only %s", got, ScopeWallets)
	}
}

func TestTheZeroIdentityCanDoNothing(t *testing.T) {
	t.Parallel()
	// The zero value is reachable -- a struct field, a failed constructor whose
	// error someone ignored -- so it has to be the powerless one.
	var nobody Identity

	if err := nobody.authorise(ScopeSubmit); !errors.Is(err, ErrForbidden) {
		t.Errorf("authorise = %v, want ErrForbidden", err)
	}
	if _, err := nobody.provider(); !errors.Is(err, ErrForbidden) {
		t.Errorf("provider = %v, want ErrForbidden", err)
	}
	if nobody.owns(domain.ProviderID{}) {
		t.Error("the zero identity owns an operation with no provider")
	}
}

func TestCallerRefusesAContextWithNoIdentity(t *testing.T) {
	t.Parallel()
	// This is the fail-closed path: a port that forgets to authenticate does
	// not get an anonymous caller, it gets an error.
	if _, err := caller(context.Background(), ScopeSubmit); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("caller = %v, want ErrUnauthenticated", err)
	}
}

func TestCallerRefusesAMissingScope(t *testing.T) {
	t.Parallel()
	reader := mustIdentity(IdentityParams{
		Subject:    "sa-acme",
		ProviderID: "acme",
		Scopes:     []string{string(ScopeRead)},
	})

	// A credential that may read is not a credential that may move money.
	ctx := WithIdentity(context.Background(), reader)
	if _, err := caller(ctx, ScopeSubmit); !errors.Is(err, ErrForbidden) {
		t.Fatalf("caller = %v, want ErrForbidden", err)
	}
	if _, err := caller(ctx, ScopeRead); err != nil {
		t.Fatalf("caller with the granted scope: %v", err)
	}
}

func TestMayActAs(t *testing.T) {
	t.Parallel()

	acme := mustIdentity(IdentityParams{Subject: "sa-acme", ProviderID: "acme"})
	platform := mustIdentity(IdentityParams{Subject: "sa-platform", Scopes: []string{string(ScopeWallets)}})

	cases := []struct {
		name       string
		identity   Identity
		providerID string
		want       error
	}{
		{name: "its own provider", identity: acme, providerID: "acme"},
		{name: "somebody else's provider", identity: acme, providerID: "rival", want: ErrForbidden},
		{
			// The platform credential opens wallets; it does not place bets on
			// a provider's behalf, and the schema refuses that row too.
			name:       "an internal credential acts for no provider",
			identity:   platform,
			providerID: "acme",
			want:       ErrForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			providerID, err := domain.ParseProviderID(tc.providerID)
			if err != nil {
				t.Fatalf("parsing the provider: %v", err)
			}
			if err := tc.identity.mayActAs(providerID); !errors.Is(err, tc.want) {
				t.Fatalf("mayActAs = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestARefusalNamesTheCallerAndNotTheTarget(t *testing.T) {
	t.Parallel()
	acme := mustIdentity(IdentityParams{Subject: "sa-acme", ProviderID: "acme"})

	rival, err := domain.ParseProviderID("rival-with-a-distinctive-name")
	if err != nil {
		t.Fatalf("parsing the provider: %v", err)
	}

	// Echoing the rejected provider back would answer "does acme exist?" for
	// the price of one wrong request.
	message := acme.mayActAs(rival).Error()
	if strings.Contains(message, "rival-with-a-distinctive-name") {
		t.Fatalf("the refusal echoes the provider that was asked for: %q", message)
	}
	if !strings.Contains(message, "acme") {
		t.Fatalf("the refusal does not say which provider the credential acts for: %q", message)
	}
}

func TestOwns(t *testing.T) {
	t.Parallel()

	acme := mustIdentity(IdentityParams{Subject: "sa-acme", ProviderID: "acme", Scopes: []string{string(ScopeRead)}})
	platform := mustIdentity(IdentityParams{Subject: "sa-platform", Scopes: []string{string(ScopeWallets)}})
	stranger := mustIdentity(IdentityParams{Subject: "sa-nobody"})

	cases := []struct {
		name       string
		identity   Identity
		providerID string
		want       bool
	}{
		{name: "a provider owns its own", identity: acme, providerID: "acme", want: true},
		{name: "a provider does not own another's", identity: acme, providerID: "rival"},
		{
			// An OPENING has no provider. It belongs to the platform, and a
			// provider reading one would be reading how a wallet was funded.
			name:     "a provider does not own an internal operation",
			identity: acme,
		},
		{name: "the platform sees everything", identity: platform, providerID: "acme", want: true},
		{name: "the platform sees its own operations", identity: platform, want: true},
		{
			// No provider and no wallets scope: a token that proves who you
			// are and authorises nothing.
			name:       "a credential with neither owns nothing",
			identity:   stranger,
			providerID: "acme",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var providerID domain.ProviderID
			if tc.providerID != "" {
				parsed, err := domain.ParseProviderID(tc.providerID)
				if err != nil {
					t.Fatalf("parsing the provider: %v", err)
				}
				providerID = parsed
			}
			if got := tc.identity.owns(providerID); got != tc.want {
				t.Fatalf("owns(%q) = %v, want %v", tc.providerID, got, tc.want)
			}
		})
	}
}
