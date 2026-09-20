//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

// This is the claim snapshot.go makes that only a real database can settle:
// that the whole transaction sees one view, not one view per statement.
//
// The in-memory test in internal/app deliberately shows the opposite -- an
// interleaved bet does produce a difference there -- because that is the
// failure mode this isolation level removes. The two tests are a pair, and the
// day someone changes pgx.RepeatableRead to something cheaper, this one is what
// notices.
//
//	make up-test && make test-integration

func TestASnapshotSeesOneViewForItsWholeLife(t *testing.T) {
	ctx := context.Background()

	uow := NewUnitOfWork(testPool, nil)
	opening, openingTx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, openingTx)
	wallet := opening.Wallet

	var first, second domain.Money
	var summed domain.Money

	err := NewSnapshot(testPool).Do(ctx, func(ctx context.Context, queries app.Queries) error {
		before, err := queries.Wallets().FindByID(ctx, wallet.ID())
		if err != nil {
			return err
		}
		first = before.Balance()

		// Somebody else commits, on another connection, while this snapshot is
		// open. Under the default isolation level the next statement in here
		// would see it -- which is exactly how a reconciliation reports a
		// difference that never existed.
		debitOutside(t, uow, wallet, "25.00")

		after, err := queries.Wallets().FindByID(ctx, wallet.ID())
		if err != nil {
			return err
		}
		second = after.Balance()

		// And the ledger, which is the other half of what reconciliation reads.
		// It has to be the same view as the balance or the comparison is
		// between two different moments.
		summed, _, err = queries.Ledger().SumByWallet(ctx, wallet.ID(), wallet.Balance().Currency())
		return err
	})
	if err != nil {
		t.Fatalf("the snapshot: %v", err)
	}

	if first.String() != second.String() {
		t.Fatalf("the view moved under the snapshot: %s then %s -- repeatable read is not in effect",
			first, second)
	}
	if first.String() != "100.00" {
		t.Fatalf("balance inside the snapshot = %s, want 100.00", first)
	}
	if summed.String() != "100.00" {
		t.Fatalf("ledger inside the snapshot = %s, want 100.00 -- the two reads straddled a commit",
			summed)
	}

	// The commit really happened. Without this, the assertions above would also
	// pass if the write had simply failed.
	reread, err := NewQueries(testPool).Wallets().FindByID(ctx, wallet.ID())
	if err != nil {
		t.Fatalf("reading the wallet back: %v", err)
	}
	if reread.Balance().String() != "75.00" {
		t.Fatalf("balance after the snapshot = %s, want 75.00", reread.Balance())
	}
}

// debitOutside moves money on its own connection and commits, which is what
// makes it visible to everyone whose snapshot started after it.
func debitOutside(t *testing.T, uow app.UnitOfWork, wallet domain.Wallet, amount string) {
	t.Helper()

	err := uow.Do(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		current, err := repos.Wallets().FindByIDForUpdate(ctx, wallet.ID())
		if err != nil {
			return err
		}

		money, err := domain.ParseMoney(amount, current.Balance().Currency())
		if err != nil {
			return err
		}
		entryID, err := domain.ParseLedgerEntryID("entry-outside-" + uniqueSuffix())
		if err != nil {
			return err
		}
		txID, err := domain.ParseTransactionID("tx-outside-" + uniqueSuffix())
		if err != nil {
			return err
		}

		movement, err := current.Debit(entryID, txID, money, current.UpdatedAt())
		if err != nil {
			return err
		}
		if err := repos.Wallets().UpdateBalance(ctx, movement.Wallet, current.Version()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("the interleaved debit: %v", err)
	}
}
