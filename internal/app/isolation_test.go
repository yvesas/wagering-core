package app

import (
	"context"
	"errors"
	"testing"

	"github.com/yvesas/wagering-core/internal/domain"
)

// contextFor is a caller with a provider and a set of scopes.
func contextFor(providerID string, scopes ...Scope) context.Context {
	raw := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		raw = append(raw, string(scope))
	}
	return WithIdentity(context.Background(), mustIdentity(IdentityParams{
		Subject:    "sa-" + providerID,
		ProviderID: providerID,
		Scopes:     raw,
	}))
}

// internalContext is the platform's own credential: no provider, and the scope
// that opens wallets.
func internalContext() context.Context {
	return WithIdentity(context.Background(), mustIdentity(IdentityParams{
		Subject: "sa-platform",
		Scopes:  []string{string(ScopeWallets), string(ScopeRead)},
	}))
}

func TestSubmitRefusesACallerWithNoIdentity(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// The fail-closed path, exercised through the use case rather than through
	// the helper: an entry port that forgets to authenticate moves no money.
	_, err := f.submit.Execute(context.Background(), f.command("BET", "25.00", "tx-1"))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Execute = %v, want ErrUnauthenticated", err)
	}
	if got := f.storedWallet(t); got.Balance().String() != "100.00" {
		t.Fatalf("the balance moved: %s", got.Balance())
	}
}

func TestSubmitRefusesACredentialWithoutTheScope(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// A credential that may read is not a credential that may move money.
	ctx := contextFor("provider-a", ScopeRead)
	_, err := f.submit.Execute(ctx, f.command("BET", "25.00", "tx-1"))
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Execute = %v, want ErrForbidden", err)
	}
	if got := f.storedWallet(t); got.Balance().String() != "100.00" {
		t.Fatalf("the balance moved: %s", got.Balance())
	}
}

func TestAProviderCannotSubmitUnderAnotherProvidersName(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// The command says provider-a. The credential says provider-b. The
	// credential is the authority, and the disagreement is refused rather than
	// quietly corrected -- a server that rewrites a request answers a question
	// the client never asked.
	ctx := contextFor("provider-b", ScopeSubmit)
	_, err := f.submit.Execute(ctx, f.command("BET", "25.00", "tx-1"))
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Execute = %v, want ErrForbidden", err)
	}
	if got := f.storedWallet(t); got.Balance().String() != "100.00" {
		t.Fatalf("the balance moved: %s", got.Balance())
	}
}

func TestReplayCannotReachAnotherProvidersOperation(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	if _, err := f.submit.Execute(contextFor("provider-a", ScopeSubmit),
		f.command("BET", "25.00", "shared-id")); err != nil {
		t.Fatalf("the first submission: %v", err)
	}

	// provider-b resends exactly what provider-a sent, identifiers and all.
	// This is REQ-SEC-003, and the answer is a refusal that says nothing about
	// whether the operation exists.
	_, err := f.submit.Execute(contextFor("provider-b", ScopeSubmit),
		f.command("BET", "25.00", "shared-id"))
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Execute = %v, want ErrForbidden", err)
	}

	if got := f.storedWallet(t); got.Balance().String() != "75.00" {
		t.Fatalf("balance = %s, want 75.00 -- one bet, not two", got.Balance())
	}
}

func TestTheSameExternalIDUnderTwoProvidersIsTwoOperations(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// An operation is identified by (provider, externalId), so two providers
	// choosing the same string is not a collision. Anything else would make one
	// provider's numbering visible to another.
	first, err := f.submit.Execute(contextFor("provider-a", ScopeSubmit),
		f.command("BET", "25.00", "bet-1"))
	if err != nil {
		t.Fatalf("provider-a: %v", err)
	}

	cmd := f.command("BET", "25.00", "bet-1")
	cmd.ProviderID = "provider-b"
	cmd.IdempotencyKey = "provider-b:bet-1"

	second, err := f.submit.Execute(contextFor("provider-b", ScopeSubmit), cmd)
	if err != nil {
		t.Fatalf("provider-b: %v", err)
	}

	if second.Replay {
		t.Error("provider-b's operation was answered as provider-a's replay")
	}
	if first.Transaction.ID() == second.Transaction.ID() {
		t.Error("the two operations share a transaction id")
	}
	if got := f.storedWallet(t); got.Balance().String() != "50.00" {
		t.Fatalf("balance = %s, want 50.00 -- both bets applied", got.Balance())
	}
}

func TestReadingAnotherProvidersOperationIsIndistinguishableFromAMiss(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	result, err := f.submit.Execute(contextFor("provider-a", ScopeSubmit),
		f.command("BET", "25.00", "bet-1"))
	if err != nil {
		t.Fatalf("submitting: %v", err)
	}
	queries := NewTransactionQueries(&memoryQueries{store: f.store})

	rival := contextFor("provider-b", ScopeRead)

	// 403 here would be a lookup service for other people's transaction ids:
	// refusing is itself a confirmation that there is something to refuse. The
	// two answers have to be the same error, not merely the same status.
	_, hidden := queries.Get(rival, result.Transaction.ID().String())
	_, missing := queries.Get(rival, "a-transaction-that-never-existed")

	if !errors.Is(hidden, ErrNotFound) {
		t.Fatalf("reading another provider's operation = %v, want ErrNotFound", hidden)
	}
	if hidden.Error() != missing.Error() {
		t.Fatalf("the two answers differ: %q and %q", hidden, missing)
	}

	// And the owner still reads it.
	if _, err := queries.Get(contextFor("provider-a", ScopeRead), result.Transaction.ID().String()); err != nil {
		t.Fatalf("the owner cannot read its own operation: %v", err)
	}
}

func TestTheBusinessLookupIsScopedToTheCaller(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	if _, err := f.submit.Execute(contextFor("provider-a", ScopeSubmit),
		f.command("BET", "25.00", "bet-1")); err != nil {
		t.Fatalf("submitting: %v", err)
	}
	queries := NewTransactionQueries(&memoryQueries{store: f.store})

	if _, err := queries.GetByBusinessID(contextFor("provider-b", ScopeRead),
		"provider-a", "bet-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByBusinessID = %v, want ErrNotFound", err)
	}
	if _, err := queries.GetByBusinessID(contextFor("provider-a", ScopeRead),
		"provider-a", "bet-1"); err != nil {
		t.Fatalf("the owner cannot read its own operation: %v", err)
	}
}

func TestAProviderCannotSeeAnInternalOperation(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// The OPENING that funded the wallet. It has no provider, so it belongs to
	// the platform -- and a provider reading it would be reading how somebody
	// else's wallet was funded.
	opening := f.openingTransaction(t)
	queries := NewTransactionQueries(&memoryQueries{store: f.store})

	if _, err := queries.Get(contextFor("provider-a", ScopeRead), opening.String()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
	if _, err := queries.Get(internalContext(), opening.String()); err != nil {
		t.Fatalf("the platform cannot read its own opening: %v", err)
	}
}

func TestWalletOperationsAreTheInternalServicesAlone(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	store := f.store
	uow := &memoryUnitOfWork{store: store}
	queries := NewWalletQueries(&memoryQueries{store: store})
	open := NewOpenWallet(uow, f.ids, f.clock)

	// Every scope a provider can hold. None of them opens a wallet, because
	// opening one mints the initial balance: it is the single operation here
	// that creates money rather than moving it.
	provider := contextFor("provider-a", ScopeSubmit, ScopeRead)

	_, err := open.Execute(provider, OpenWalletCommand{PlayerID: "player-2", Amount: "500.00", Currency: "BRL"})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("opening a wallet as a provider = %v, want ErrForbidden", err)
	}
	if _, err := queries.Get(provider, f.wallet.ID().String()); !errors.Is(err, ErrForbidden) {
		t.Errorf("reading a wallet as a provider = %v, want ErrForbidden", err)
	}
	if _, err := queries.Ledger(provider, f.wallet.ID().String(), LedgerCursor{}, 10); !errors.Is(err, ErrForbidden) {
		t.Errorf("reading a ledger as a provider = %v, want ErrForbidden", err)
	}

	// The internal credential does all three.
	internal := internalContext()
	if _, err := open.Execute(internal, OpenWalletCommand{PlayerID: "player-2", Amount: "500.00", Currency: "BRL"}); err != nil {
		t.Errorf("opening a wallet as the platform: %v", err)
	}
	if _, err := queries.Get(internal, f.wallet.ID().String()); err != nil {
		t.Errorf("reading a wallet as the platform: %v", err)
	}
	if _, err := queries.Ledger(internal, f.wallet.ID().String(), LedgerCursor{}, 10); err != nil {
		t.Errorf("reading a ledger as the platform: %v", err)
	}
}

// openingTransaction finds the OPENING the fixture's wallet was funded with.
func (f submitFixture) openingTransaction(t *testing.T) domain.TransactionID {
	t.Helper()
	for _, transaction := range f.store.transactions {
		if transaction.Kind() == domain.KindOpening {
			return transaction.ID()
		}
	}
	t.Fatal("the fixture wallet was never opened")
	return domain.TransactionID{}
}
