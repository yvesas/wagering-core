package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/yvesas/wagering-core/internal/app"
)

// TokenVerifier turns a bearer token into the caller it proves.
//
// Declared here, by the code that consumes it, rather than next to the OIDC
// adapter that satisfies it: the edge needs "a token in, an identity out" and
// nothing else, and a test drives it with eight lines instead of an issuer.
type TokenVerifier interface {
	Verify(ctx context.Context, token string) (app.Identity, error)
}

// realm is what a 401 tells the client to authenticate against. It is the
// service, not the issuer: the issuer's address is discovery, not a hint we owe
// an unauthenticated caller.
const realm = "wagering-core"

// Authenticator turns the Authorization header into an identity in the context.
//
// It authenticates and stops there. What the caller may *do* is decided by the
// use case, because both entry ports have to agree on it and only one of them
// has headers. See docs/adr/0011-authentication-and-isolation.md.
type Authenticator struct {
	verifier TokenVerifier
}

func NewAuthenticator(verifier TokenVerifier) *Authenticator {
	return &Authenticator{verifier: verifier}
}

// require wraps a handler so it only ever runs for a verified caller.
//
// Wrapping happens per route, from the table in Routes, and the table's zero
// value is "protected". A route added without a thought about authentication is
// therefore authenticated -- which is the safe direction for a mistake to fall,
// and the opposite of what a public-paths allowlist would give.
func (a *Authenticator) require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := bearerToken(r)
		if err != nil {
			a.refuse(w, r, err)
			return
		}

		identity, err := a.verifier.Verify(r.Context(), token)
		if err != nil {
			a.refuse(w, r, err)
			return
		}

		slog.DebugContext(r.Context(), "authenticated",
			slog.Any("identity", identity),
			slog.String("correlationId", CorrelationIDFrom(r.Context())))

		next(w, r.WithContext(app.WithIdentity(r.Context(), identity)))
	}
}

// refuse answers a caller we could not authenticate.
//
// The reason goes to the log and never to the client. "Expired at 10:04",
// "signed by an unknown key" and "audience is someone else's" are all the same
// answer from out here: present a credential this service accepts. Splitting
// them apart tells whoever is probing which part of the forgery to fix.
func (a *Authenticator) refuse(w http.ResponseWriter, r *http.Request, err error) {
	slog.WarnContext(r.Context(), "refusing an unauthenticated request",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()),
		slog.String("correlationId", CorrelationIDFrom(r.Context())))

	// RFC 6750: the challenge is what tells a client *how* to authenticate, and
	// a 401 without it leaves a well-behaved client guessing.
	w.Header().Set("WWW-Authenticate",
		`Bearer realm="`+realm+`", error="invalid_token"`)

	writeJSON(w, r, http.StatusUnauthorized, ErrorBody{
		Code:          "UNAUTHENTICATED",
		Message:       "a valid bearer token is required",
		CorrelationID: CorrelationIDFrom(r.Context()),
	})
}

// bearerToken reads the credential out of the request.
func bearerToken(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", errors.New("the Authorization header is missing")
	}

	scheme, token, found := strings.Cut(header, " ")
	// The scheme is case-insensitive per RFC 7235, and clients do send
	// "bearer". Refusing that would be refusing a correct client.
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", errors.New("the Authorization header is not a bearer credential")
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("the bearer credential is empty")
	}
	return token, nil
}
