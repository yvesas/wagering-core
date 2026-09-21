package app

import (
	"context"
	"errors"
	"testing"

	"github.com/yvesas/wagering-core/internal/domain"
)

// memorySnapshot is the in-memory stand-in for a repeatable-read transaction.
//
// It cannot prove the isolation level -- only PostgreSQL can, and the
// integration test is where that happens. What it does prove is everything
// above it: that the use case reads both numbers from one place, compares them
// with domain arithmetic, and writes nothing.
type memorySnapshot struct {
	store *memoryStore

	// between runs after the wallet has been read and before the ledger is
	// summed, which is the window a reconciliation without a single view would
	// fall into.
	between func()
}

func (s *memorySnapshot) Do(ctx context.Context, fn func(context.Context, Queries) error) error {
	return fn(ctx, &snapshotQueries{store: s.store, between: s.between})
}

type snapshotQueries struct {
	store   *memoryStore
	between func()
}

func (q *snapshotQueries) Wallets() WalletReader {
	return &hookedWallets{inner: &memoryWallets{store: q.store}, after: q.between}
}
func (q *snapshotQueries) Ledger() LedgerReader { return &memoryLedger{store: q.store} }
func (q *snapshotQueries) Transactions() TransactionReader {
	return (&memoryQueries{store: q.store}).Transactions()
}

type hookedWallets struct {
	inner WalletReader
	after func()
}

func (h *hookedWallets) FindByID(ctx context.Context, id domain.WalletID) (domain.Wallet, error) {
	wallet, err := h.inner.FindByID(ctx, id)
	if h.after != nil {
		h.after()
	}
	return wallet, err
}

func (h *hookedWallets) FindByPlayerAndCurrency(ctx context.Context, p domain.PlayerID, c domain.Currency) (domain.Wallet, error) {
	return h.inner.FindByPlayerAndCurrency(ctx, p, c)
}

func newReconciler(f submitFixture, between func()) *ReconcileWallet {
	return NewReconcileWallet(
		&memorySnapshot{store: f.store, between: between},
		f.clock, f.metrics, discardLogger())
}

func TestReconciliationAgreesWithTheLedger(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	if _, err := f.submit.Execute(callerContext(), f.command("BET", "25.00", "tx-1")); err != nil {
		t.Fatalf("the bet: %v", err)
	}

	result, err := newReconciler(f, nil).Execute(internalContext(), f.wallet.ID().String())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Drifted() {
		t.Fatalf("drift on a healthy wallet: stored %s, rebuilt %s",
			result.Stored, result.Rebuilt)
	}
	if result.Stored.String() != "75.00" || result.Rebuilt.String() != "75.00" {
		t.Errorf("stored %s, rebuilt %s, want both 75.00", result.Stored, result.Rebuilt)
	}
	// The opening credit and the bet. The opening is an ordinary ledger entry,
	// which is why "including the opening" needs no special case.
	if result.Entries != 2 {
		t.Errorf("entries = %d, want 2", result.Entries)
	}
	if got := f.metrics.reconciliations("match"); got != 1 {
		t.Errorf("match counter = %d, want 1", got)
	}
}

func TestReconciliationReportsADrift(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// The balance moved without a ledger entry. Nothing in this system does
	// that -- the two are written in one commit, and the database revokes
	// UPDATE on the ledger -- which is exactly why the check has to exist: it
	// is the second opinion for the day something we did not foresee does it.
	f.corruptBalance(t, "40.00")

	result, err := newReconciler(f, nil).Execute(internalContext(), f.wallet.ID().String())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !result.Drifted() {
		t.Fatal("a wallet 60.00 short of its ledger reconciled clean")
	}
	if result.Stored.String() != "40.00" || result.Rebuilt.String() != "100.00" {
		t.Errorf("stored %s, rebuilt %s", result.Stored, result.Rebuilt)
	}
	// The sign says which way it is wrong, and that is the first thing an
	// investigation asks: money missing, or money invented.
	if result.Difference.String() != "-60.00" {
		t.Errorf("difference = %s, want -60.00", result.Difference)
	}
	if got := f.metrics.reconciliations("drift"); got != 1 {
		t.Errorf("drift counter = %d, want 1", got)
	}
}

func TestReconciliationReadsOneView(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	// A bet lands between the two reads. Against a real database the snapshot
	// hides it; here the point is narrower and still worth holding: the use
	// case must take both numbers from the Queries it was handed, so that the
	// adapter's isolation is the only thing deciding what it sees. If it read
	// the wallet from somewhere else, this test would report a drift.
	var once bool
	between := func() {
		if once {
			return
		}
		once = true
		if _, err := f.submit.Execute(callerContext(), f.command("BET", "25.00", "tx-mid")); err != nil {
			t.Errorf("the interleaved bet: %v", err)
		}
	}

	result, err := newReconciler(f, between).Execute(internalContext(), f.wallet.ID().String())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The wallet was read before the bet and the ledger after it, so the two
	// disagree by exactly that bet. That is the failure mode the repeatable
	// read exists to remove, and seeing it here is what makes the isolation
	// level in snapshot.go a requirement rather than a preference.
	if result.Difference.String() != "25.00" {
		t.Fatalf("difference = %s, want 25.00 -- the interleaving did not happen",
			result.Difference)
	}
}

func TestReconciliationIsTheInternalServicesAlone(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")
	reconciler := newReconciler(f, nil)

	// It reads every movement a wallet ever had. A provider running it against
	// a player would be reading a statement it has no claim to.
	provider := contextFor("provider-a", ScopeSubmit, ScopeRead)
	if _, err := reconciler.Execute(provider, f.wallet.ID().String()); !errors.Is(err, ErrForbidden) {
		t.Errorf("as a provider = %v, want ErrForbidden", err)
	}
	if _, err := reconciler.Execute(context.Background(), f.wallet.ID().String()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("with no identity = %v, want ErrUnauthenticated", err)
	}
	if _, err := reconciler.Execute(internalContext(), f.wallet.ID().String()); err != nil {
		t.Errorf("as the platform: %v", err)
	}
}

func TestReconcilingAWalletThatIsNotThere(t *testing.T) {
	t.Parallel()
	f := newSubmitFixture(t, "100.00")

	_, err := newReconciler(f, nil).Execute(internalContext(), "no-such-wallet")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Execute = %v, want ErrNotFound", err)
	}
	// Nothing was checked, so nothing is counted. A counter that moved here
	// would make a monitoring rule on "reconciliations that found nothing
	// wrong" quietly include the ones that never ran.
	if got := f.metrics.reconciliations("match"); got != 0 {
		t.Errorf("match counter = %d after a failed lookup, want 0", got)
	}
}

// corruptBalance rewrites the stored balance behind the domain's back, which is
// the only way to produce a drift: every path through the real code writes the
// balance and the ledger entry in one commit.
func (f submitFixture) corruptBalance(t *testing.T, amount string) {
	t.Helper()

	money, err := domain.ParseMoney(amount, f.wallet.Balance().Currency())
	if err != nil {
		t.Fatalf("parsing %s: %v", amount, err)
	}

	for i, wallet := range f.store.wallets {
		if wallet.ID() != f.wallet.ID() {
			continue
		}
		rehydrated, err := domain.RehydrateWallet(domain.RehydrateWalletParams{
			ID:        wallet.ID(),
			PlayerID:  wallet.PlayerID(),
			Balance:   money,
			Version:   wallet.Version(),
			CreatedAt: wallet.CreatedAt(),
			UpdatedAt: wallet.UpdatedAt(),
		})
		if err != nil {
			t.Fatalf("rehydrating the wallet: %v", err)
		}
		f.store.wallets[i] = rehydrated
		return
	}
	t.Fatal("the fixture wallet is not in the store")
}
