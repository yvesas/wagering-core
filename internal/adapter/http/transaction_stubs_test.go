package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

// TestMain silences the structured logger. These tests exercise the middleware
// on purpose, and its output would otherwise bury the one line that matters
// when something fails.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type stubSubmitter struct {
	result app.SubmitResult
	err    error
	got    app.SubmitCommand
}

func (s *stubSubmitter) Execute(_ context.Context, cmd app.SubmitCommand) (app.SubmitResult, error) {
	s.got = cmd
	return s.result, s.err
}

type stubTxReader struct {
	transaction domain.WagerTransaction
	err         error

	gotProvider string
	gotExternal string
}

func (s *stubTxReader) Get(context.Context, string) (domain.WagerTransaction, error) {
	return s.transaction, s.err
}

func (s *stubTxReader) GetByBusinessID(_ context.Context, provider, external string) (domain.WagerTransaction, error) {
	s.gotProvider, s.gotExternal = provider, external
	return s.transaction, s.err
}

// testToken is what the stub verifier accepts. It is not a JWT and does not
// need to be: what a token has to survive is tested against a real signature in
// the oidc package, and repeating that here would test the same thing twice
// while making every handler test depend on a key pair.
const testToken = "test-token"

type stubVerifier struct {
	identity app.Identity
	err      error
}

func (s *stubVerifier) Verify(_ context.Context, token string) (app.Identity, error) {
	if s.err != nil {
		return app.Identity{}, s.err
	}
	if token != testToken {
		return app.Identity{}, app.ErrUnauthenticated
	}
	return s.identity, nil
}

// testIdentity is a provider credential carrying every scope this service
// defines, so a handler test fails for the reason it is about rather than for a
// scope nobody meant to exercise.
func testIdentity(t *testing.T) app.Identity {
	t.Helper()
	identity, err := app.NewIdentity(app.IdentityParams{
		Subject:    "service-account-provider-a",
		ProviderID: "provider-a",
		Scopes: []string{
			string(app.ScopeSubmit), string(app.ScopeRead), string(app.ScopeWallets),
		},
	})
	if err != nil {
		t.Fatalf("building the test identity: %v", err)
	}
	return identity
}

func testAuth(t *testing.T) *Authenticator {
	t.Helper()
	return NewAuthenticator(&stubVerifier{identity: testIdentity(t)})
}

// authenticated adds the credential the stub verifier accepts.
func authenticated(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer "+testToken)
	return req
}
