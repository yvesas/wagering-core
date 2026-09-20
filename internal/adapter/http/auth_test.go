package http

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yvesas/wagering-core/internal/app"
)

func testMux(t *testing.T, verifier TokenVerifier) http.Handler {
	t.Helper()
	return Handler(Routes(
		NewAuthenticator(verifier),
		NewWalletHandler(&stubOpener{}, &stubReader{}),
		NewTransactionHandler(&stubSubmitter{}, &stubTxReader{}),
		NewHealthHandler(),
	))
}

// TestEveryBusinessRouteRequiresACredential walks the route table itself, so a
// route added tomorrow is covered by this test the moment it is registered.
//
// That is the whole reason the table exists as data. A list of paths written
// out here would prove that the paths someone remembered to list are protected,
// which is not the same claim at all.
func TestEveryBusinessRouteRequiresACredential(t *testing.T) {
	t.Parallel()
	mux := testMux(t, &stubVerifier{identity: testIdentity(t)})

	for _, rt := range routeTable(
		NewWalletHandler(&stubOpener{}, &stubReader{}),
		NewTransactionHandler(&stubSubmitter{}, &stubTxReader{}),
		NewHealthHandler(),
	) {
		t.Run(rt.pattern, func(t *testing.T) {
			t.Parallel()
			method, pattern, _ := strings.Cut(rt.pattern, " ")

			// A wildcard is a path segment to the router; any value reaches the
			// same handler, and the stubs answer whatever arrives.
			target := strings.NewReplacer(
				"{walletId}", "wallet-1",
				"{transactionId}", "tx-1",
				"{providerId}", "provider-a",
				"{externalTransactionId}", "external-1",
			).Replace(pattern)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader("{}")))

			if rt.public {
				if rec.Code == http.StatusUnauthorized {
					t.Fatalf("%s is public and answered 401", rt.pattern)
				}
				return
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s answered %d without a credential, want 401", rt.pattern, rec.Code)
			}
		})
	}
}

func TestAnUnauthenticatedAnswerCarriesTheChallengeAndNoDetail(t *testing.T) {
	t.Parallel()
	mux := testMux(t, &stubVerifier{
		err: errors.New("token is expired by 4h12m, signed by key-7 for audience billing"),
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authenticated(httptest.NewRequest(http.MethodGet, "/wallets/wallet-1", nil)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// RFC 6750: without the challenge a well-behaved client has to guess how to
	// authenticate.
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer ") {
		t.Errorf("WWW-Authenticate = %q", got)
	}

	// "Expired at 10:04" and "signed by an unknown key" are the same answer out
	// here. Splitting them apart tells whoever is probing which part of the
	// forgery to fix.
	body := rec.Body.String()
	for _, leak := range []string{"expired", "key-7", "billing"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("the refusal reason reached the client (%q): %s", leak, body)
		}
	}
	if decodeBody[ErrorBody](t, rec).CorrelationID == "" {
		t.Error("no correlation id to match the refusal against the log")
	}
}

func TestBearerHeaderParsing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{name: "a bearer token", header: "Bearer " + testToken, want: http.StatusOK},
		{
			// RFC 7235 says the scheme is case-insensitive, and clients do send
			// this. Refusing it would be refusing a correct client.
			name: "a lowercase scheme", header: "bearer " + testToken, want: http.StatusOK,
		},
		{name: "no header at all", header: "", want: http.StatusUnauthorized},
		{name: "the token with no scheme", header: testToken, want: http.StatusUnauthorized},
		{name: "another scheme", header: "Basic " + testToken, want: http.StatusUnauthorized},
		{name: "an empty credential", header: "Bearer ", want: http.StatusUnauthorized},
		{name: "a token the issuer never signed", header: "Bearer forged", want: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mux := testMux(t, &stubVerifier{identity: testIdentity(t)})

			req := httptest.NewRequest(http.MethodGet, "/wallets/wallet-1", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestTheIdentityReachesTheUseCase(t *testing.T) {
	t.Parallel()

	// The handler is transport; what it has to do is put the caller where the
	// use case looks for it. Everything after that is the use case's business.
	var seen app.Identity
	opener := &stubOpener{wallet: sampleWallet(t), inspect: func(identity app.Identity) {
		seen = identity
	}}

	mux := Handler(Routes(
		testAuth(t),
		NewWalletHandler(opener, &stubReader{}),
		NewTransactionHandler(&stubSubmitter{}, &stubTxReader{}),
		NewHealthHandler(),
	))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authenticated(httptest.NewRequest(http.MethodPost, "/wallets",
		strings.NewReader(`{"playerId":"player-1","initialBalance":{"amount":"0.00","currency":"BRL"}}`))))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if seen.Subject() != "service-account-provider-a" {
		t.Fatalf("the use case saw subject %q", seen.Subject())
	}
}

func TestAForbiddenUseCaseAnswers403(t *testing.T) {
	t.Parallel()
	// The use case, not the edge, is what refuses this: the credential is valid
	// and does not carry the scope. The edge only has to render it.
	handler := NewWalletHandler(&stubOpener{err: app.ErrForbidden}, &stubReader{})

	rec := serve(t, handler, http.MethodPost, "/wallets",
		`{"playerId":"player-1","initialBalance":{"amount":"0.00","currency":"BRL"}}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if got := decodeBody[ErrorBody](t, rec).Code; got != "FORBIDDEN" {
		t.Errorf("code = %q", got)
	}
}
