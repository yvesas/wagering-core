// Package oidc verifies bearer tokens against an external identity provider.
//
// It is an inbound adapter: it turns a credential into an [app.Identity] and
// hands that inwards. Nothing above it sees a token, a claim set or a signing
// key, which is what keeps "who may do what" out of the vocabulary of one
// identity provider -- the same argument docs/adr/0001 makes for the database.
//
// It may not be imported by internal/app or internal/domain.
//
// What it does not do: issue tokens, register clients, or hold a user. The
// service has no account of its own and no password to check; the issuer is
// someone else's, and this package only ever reads from it.
package oidc
