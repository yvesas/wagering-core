package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/yvesas/wagering-core/internal/app"
)

// Config is what it takes to trust an issuer.
type Config struct {
	// IssuerURL is the issuer as it appears in the iss claim. Discovery hangs
	// off it, and the document it returns has to name it back -- see New.
	IssuerURL string

	// Audience is this service's identifier at the issuer. A token minted for
	// another service is signed by the same key and is not ours to accept.
	Audience string

	// ProviderClaim carries the provider a token acts for. Empty means
	// DefaultProviderClaim.
	ProviderClaim string

	// CacheTTL is how long the signing keys are kept before refetching.
	CacheTTL time.Duration

	// Leeway absorbs clock skew between this process and the issuer. Zero means
	// DefaultLeeway; it is never unbounded, because a large allowance is an
	// expired token that still works.
	Leeway time.Duration

	// HTTPClient reaches the issuer. Nil means a client with a timeout, which
	// http.DefaultClient is not.
	HTTPClient *http.Client

	// Clock is what the key cache ages against. Nil means the real one.
	//
	// It exists because the alternative for testing a cache and a refresh floor
	// is sleeping through them, and a suite that sleeps is a suite people stop
	// running. It does not affect token validity: exp and nbf are checked by
	// the parser against the real clock, so a movable one here cannot revive an
	// expired token.
	Clock app.Clock
}

// DefaultProviderClaim is the claim the provider is read from.
//
// A claim of our own rather than azp or client_id: those tie a provider's
// identifier to the name of a client in the issuer's realm, and renaming a
// client would become a data migration here. See
// docs/adr/0011-authentication-and-isolation.md.
const DefaultProviderClaim = "provider_id"

// DefaultLeeway is the accepted clock skew, both directions.
const DefaultLeeway = 30 * time.Second

// DefaultCacheTTL is how long the key set is trusted without asking again.
const DefaultCacheTTL = 5 * time.Minute

// signingMethods is the allowlist, and it is the single most important line in
// this package.
//
// Without it, a token is verified with whatever algorithm the token itself
// names -- so "alg": "none" verifies with no key at all, and "alg": "HS256"
// verifies an attacker's HMAC using the RSA *public* key as the shared secret.
// Both are forgery by header edit. The allowlist is what makes the header a
// declaration rather than an instruction.
var signingMethods = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512"}

// Verifier checks bearer tokens against one issuer.
type Verifier struct {
	issuer        string
	providerClaim string
	keys          *keySet
	parser        *jwt.Parser
}

// New reads the issuer's discovery document and builds the verifier.
//
// It reaches the issuer at start-up, which means an identity provider that is
// unreachable fails the boot. That is deliberate: a process that starts anyway
// would answer 401 to every caller while looking healthy, and "everything is
// rejected" is much harder to read in an incident than "this did not start".
func New(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.IssuerURL == "" {
		return nil, fmt.Errorf("the oidc issuer url is required")
	}
	if cfg.Audience == "" {
		// Without an audience, any token this issuer minted for any service
		// would be accepted here -- including one a provider obtained for a
		// system that is not this one.
		return nil, fmt.Errorf("the oidc audience is required")
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	issuer := strings.TrimSuffix(cfg.IssuerURL, "/")

	jwksURI, err := discover(ctx, client, issuer)
	if err != nil {
		return nil, err
	}

	providerClaim := cfg.ProviderClaim
	if providerClaim == "" {
		providerClaim = DefaultProviderClaim
	}
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	leeway := cfg.Leeway
	if leeway <= 0 {
		leeway = DefaultLeeway
	}

	now := time.Now
	if cfg.Clock != nil {
		now = cfg.Clock.Now
	}

	return &Verifier{
		issuer:        issuer,
		providerClaim: providerClaim,
		keys:          newKeySet(jwksURI, client, ttl, now),
		parser: jwt.NewParser(
			jwt.WithValidMethods(signingMethods),
			jwt.WithIssuer(issuer),
			jwt.WithAudience(cfg.Audience),
			// A token without an expiry never stops being valid, and a leaked
			// one would be a permanent credential.
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(leeway),
		),
	}, nil
}

// discovery is the part of the OIDC discovery document this service uses.
type discovery struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// maxDiscoveryBytes bounds the discovery document, for the same reason the key
// set is bounded.
const maxDiscoveryBytes = 1 << 20

func discover(ctx context.Context, client *http.Client, issuer string) (string, error) {
	url := issuer + "/.well-known/openid-configuration"

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building the discovery request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("reaching the identity provider at %s: %w", url, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the identity provider answered %s for %s", response.Status, url)
	}

	var document discovery
	if err := json.NewDecoder(io.LimitReader(response.Body, maxDiscoveryBytes)).Decode(&document); err != nil {
		return "", fmt.Errorf("decoding the discovery document: %w", err)
	}

	// The document has to name the issuer we asked about. Skipping this check
	// would let a redirect, or a wrong URL in the configuration, hand us a key
	// set belonging to somebody else -- and every token it signed would then
	// verify.
	if strings.TrimSuffix(document.Issuer, "/") != issuer {
		return "", fmt.Errorf("the discovery document at %s claims issuer %q", url, document.Issuer)
	}
	if document.JWKSURI == "" {
		return "", fmt.Errorf("the discovery document at %s has no jwks_uri", url)
	}
	return document.JWKSURI, nil
}

// Verify checks a token and returns the caller it proves.
//
// Every failure comes back wrapped in app.ErrUnauthenticated, with the detail
// in the message for the log. The edge does not pass that message to the
// client: see the comment on Authenticator.refuse.
func (v *Verifier) Verify(ctx context.Context, raw string) (app.Identity, error) {
	claims := jwt.MapClaims{}

	// ParseWithClaims does signature, alg, iss, aud, exp and nbf. What is left
	// below is only what is specific to this service.
	if _, err := v.parser.ParseWithClaims(raw, claims, v.key(ctx)); err != nil {
		return app.Identity{}, fmt.Errorf("%w: %v", app.ErrUnauthenticated, err)
	}

	subject, err := claims.GetSubject()
	if err != nil {
		return app.Identity{}, fmt.Errorf("%w: reading the subject: %v", app.ErrUnauthenticated, err)
	}

	providerID, err := stringClaim(claims, v.providerClaim)
	if err != nil {
		return app.Identity{}, fmt.Errorf("%w: %v", app.ErrUnauthenticated, err)
	}

	return app.NewIdentity(app.IdentityParams{
		Subject:    subject,
		ProviderID: providerID,
		Scopes:     scopesOf(claims),
	})
}

// key hands the parser the public key the token's header points at.
//
// It is called *by* the parser, after the header is read and before the
// signature is checked, which is why the kid is untrusted input here: it
// selects a key, it never supplies one.
func (v *Verifier) key(ctx context.Context) jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("the token header has no key id")
		}
		return v.keys.key(ctx, kid)
	}
}

// stringClaim reads an optional string claim, refusing one of the wrong type.
//
// Absent is allowed: a credential with no provider is the internal service, and
// what it may do is decided by its scopes. Present but not a string is a
// misconfigured mapper, and guessing what it meant is how a provider identifier
// ends up being the number 1.
func stringClaim(claims jwt.MapClaims, name string) (string, error) {
	value, ok := claims[name]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("the %s claim is not a string", name)
	}
	return text, nil
}

// scopesOf reads the granted scopes.
//
// Two shapes, because issuers disagree: RFC 8693 puts a space-delimited string
// in "scope", and others send an array in "scp". Reading both costs a few lines
// and saves the day this service is pointed at a different provider.
func scopesOf(claims jwt.MapClaims) []string {
	var scopes []string

	if text, ok := claims["scope"].(string); ok {
		scopes = append(scopes, strings.Fields(text)...)
	}
	if list, ok := claims["scp"].([]any); ok {
		for _, item := range list {
			if text, ok := item.(string); ok {
				scopes = append(scopes, text)
			}
		}
	}
	return scopes
}
