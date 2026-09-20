package app

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/yvesas/wagering-core/internal/domain"
)

// Scope is a permission carried by a credential.
//
// The names are the ones the identity provider grants, so they are part of the
// contract with whoever operates the realm and are not renamed lightly.
type Scope string

const (
	// ScopeSubmit lets a provider send operations.
	ScopeSubmit Scope = "wagering:submit"

	// ScopeRead lets a provider read back what it submitted. It is separate
	// from ScopeSubmit so a reporting credential can exist that cannot move
	// money.
	ScopeRead Scope = "wagering:read"

	// ScopeWallets is the internal service's. Opening a wallet and reading a
	// balance or a ledger are operations of the platform, not of a provider:
	// see REQ-SEC-004.
	ScopeWallets Scope = "wallets:manage"
)

// Identity is the caller, already reduced to what authorisation needs.
//
// It is a value with unexported fields: the only way to obtain a non-zero one
// is [NewIdentity], and the zero value is "nobody", which every check refuses.
// That is what makes forgetting to authenticate fail closed rather than fail
// open.
//
// It deliberately carries no token, no claim set and no expiry. Whatever proved
// the caller is who they say stays in the adapter that verified it; what
// travels inwards is the decision, not the evidence.
type Identity struct {
	subject    string
	providerID domain.ProviderID
	scopes     []Scope
}

// IdentityParams is what a verified credential yields.
type IdentityParams struct {
	// Subject identifies the caller to the issuer. It goes in the log line, so
	// a rejected call can be traced to a client without the token.
	Subject string

	// ProviderID is empty when the caller is not a provider -- the internal
	// service is not one, and neither is a human operator.
	ProviderID string

	Scopes []string
}

// NewIdentity builds an identity from a verified credential.
//
// It is in the app layer, not in the adapter, because who may do what is a
// business rule: the two entry ports have to agree on it, and a rule that lives
// in the HTTP adapter does not exist for the queue.
func NewIdentity(p IdentityParams) (Identity, error) {
	if p.Subject == "" {
		// Every credential has a subject. One without it cannot be named in a
		// log or an audit, and an unnameable caller is not an authenticated
		// caller.
		return Identity{}, fmt.Errorf("%w: the credential has no subject", ErrUnauthenticated)
	}

	identity := Identity{subject: p.Subject}
	if p.ProviderID != "" {
		providerID, err := domain.ParseProviderID(p.ProviderID)
		if err != nil {
			return Identity{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
		}
		identity.providerID = providerID
	}

	for _, raw := range p.Scopes {
		scope := Scope(strings.TrimSpace(raw))
		// Unknown scopes are dropped rather than refused. An identity provider
		// serves more than this service, and a token carrying "profile" is not
		// a malformed token -- it is a token with a scope we have no use for.
		if scope.known() && !slices.Contains(identity.scopes, scope) {
			identity.scopes = append(identity.scopes, scope)
		}
	}
	return identity, nil
}

func (s Scope) known() bool {
	switch s {
	case ScopeSubmit, ScopeRead, ScopeWallets:
		return true
	default:
		return false
	}
}

// Subject returns who the issuer says this is.
func (i Identity) Subject() string { return i.subject }

// ProviderID returns the provider this identity acts for, zero when it acts for
// none.
func (i Identity) ProviderID() domain.ProviderID { return i.providerID }

// IsProvider reports whether this caller submits on a provider's behalf.
func (i Identity) IsProvider() bool { return !i.providerID.IsZero() }

// Has reports whether the credential granted a scope.
func (i Identity) Has(scope Scope) bool { return slices.Contains(i.scopes, scope) }

// Scopes returns the granted scopes, for logging.
func (i Identity) Scopes() []Scope { return slices.Clone(i.scopes) }

// LogValue makes an identity loggable as a group, which is the whole point:
// slog calls this instead of reflecting over the struct, so what reaches a log
// aggregator is who called -- never a credential, because the struct does not
// hold one and this method could not print it if it did.
func (i Identity) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("subject", i.subject)}
	if i.IsProvider() {
		attrs = append(attrs, slog.String("providerId", i.providerID.String()))
	}
	return slog.GroupValue(attrs...)
}

// authorise checks a scope.
//
// The message names the scope that was missing. That is deliberate: the caller
// owns its own token and telling it what it lacks is how a misconfigured client
// gets fixed, whereas saying nothing produces a support ticket.
func (i Identity) authorise(scope Scope) error {
	if !i.Has(scope) {
		return fmt.Errorf("%w: the credential does not carry %s", ErrForbidden, scope)
	}
	return nil
}

// provider returns the provider this identity acts for, refusing when it acts
// for none.
//
// An internal credential submitting an operation would be the service raising a
// bet on a provider's behalf, with no provider to answer for it. The schema
// refuses that row too -- wager_transactions_internal_shape -- and this is the
// same rule, stated where the caller can be told why.
func (i Identity) provider() (domain.ProviderID, error) {
	if !i.IsProvider() {
		return domain.ProviderID{}, fmt.Errorf(
			"%w: this credential does not act for a provider", ErrForbidden)
	}
	return i.providerID, nil
}

// mayActAs refuses an operation submitted under someone else's provider.
//
// This is the whole of REQ-SEC-002 on the write path: the credential decides
// which provider an operation belongs to, and the body only gets to agree. It
// is a refusal rather than a silent overwrite for the reason the idempotency
// key is never substituted either -- a server that quietly corrects a request
// answers a question the client did not ask.
//
// It is also, by consequence, most of REQ-SEC-003. An operation is identified
// by (provider, externalId); a caller that cannot name another provider cannot
// address another provider's operation, so a replay cannot reach one either.
// The isolation on replay is not a second check -- there is nothing to check.
func (i Identity) mayActAs(providerID domain.ProviderID) error {
	provider, err := i.provider()
	if err != nil {
		return err
	}
	if provider != providerID {
		// The message names the credential's own provider and never the one
		// that was asked for: telling the caller "acme exists, you are not it"
		// is an existence oracle for the price of one wrong request.
		return fmt.Errorf("%w: this credential submits as provider %s", ErrForbidden, provider)
	}
	return nil
}

// owns reports whether an operation belongs to this caller.
//
// The internal credential sees everything: reconciliation and support answer
// questions about operations they did not submit. A provider sees only its own,
// and an internal operation -- an OPENING, which has no provider -- belongs to
// no provider and so is visible to none of them.
func (i Identity) owns(providerID domain.ProviderID) bool {
	if !i.IsProvider() {
		return i.Has(ScopeWallets)
	}
	return !providerID.IsZero() && providerID == i.providerID
}

type identityKey struct{}

// WithIdentity puts the caller in the context.
//
// The identity travels in the context rather than in every command because it
// is not part of what the caller asked for: it is who asked. Putting it in
// SubmitCommand would make it a field an entry port can forget to fill, and a
// zero field is indistinguishable from an anonymous call. In the context it is
// either there or it is not, and [caller] refuses when it is not.
//
// This is the same argument that docs/adr/0003 makes for the unit of work, with
// the sign reversed: there, a repository that failed to find its transaction
// fell back to the pool and wrote outside it *without failing*. Here nothing
// falls back. Every use case starts by asking for the caller, and a context
// without one ends the call.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFrom returns the caller, and whether there was one.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok
}

// caller is how a use case starts: it names the scope it needs and gets the
// identity, or an error that is already the right answer to send back.
func caller(ctx context.Context, scope Scope) (Identity, error) {
	identity, ok := IdentityFrom(ctx)
	if !ok {
		return Identity{}, fmt.Errorf("%w: the request carries no identity", ErrUnauthenticated)
	}
	if err := identity.authorise(scope); err != nil {
		return Identity{}, err
	}
	return identity, nil
}
