//go:build integration

// These tests run against a real PostgreSQL. Mocking it would defeat the
// purpose: what is under test here is the schema -- constraints, triggers,
// transaction semantics -- and a mock has none of that.
//
//	make up-test          start the isolated database
//	make test-integration run these
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
	"github.com/yvesas/wagering-core/internal/platform"
)

var testPool *pgxpool.Pool

// ids are unique per run. The ledger is append-only in the database, so a test
// cannot clean up after itself by deleting rows -- that is the design working,
// not a problem. Unique identifiers give isolation without needing to.
var idCounter atomic.Int64

func uniqueSuffix() string { return fmt.Sprintf("%d-%d", time.Now().UnixNano(), idCounter.Add(1)) }

func TestMain(m *testing.M) {
	ctx := context.Background()

	cfg := platform.DatabaseConfig{
		Host:     envOr("TEST_DB_HOST", "localhost"),
		Port:     envOr("TEST_DB_PORT", "5433"),
		Name:     envOr("TEST_DB_NAME", "wagering_test"),
		User:     envOr("TEST_DB_USER", "wagering"),
		Password: envOr("TEST_DB_PASSWORD", "local-dev-only"),
		SSLMode:  "disable",
	}

	db, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening %s: %v\n", cfg.Redacted(), err)
		os.Exit(1)
	}
	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cannot reach %s: %v\nrun `make up-test` first\n", cfg.Redacted(), err)
		os.Exit(1)
	}
	if err := Migrate(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "migrating: %v\n", err)
		os.Exit(1)
	}
	// Running it twice proves the migration is idempotent, which is what makes
	// it safe for every replica to call at start-up.
	if err := Migrate(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "migrating twice: %v\n", err)
		os.Exit(1)
	}
	_ = db.Close()

	testPool, err = NewPool(ctx, cfg, PoolConfig{MaxConns: 8})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pool: %v\n", err)
		os.Exit(1)
	}
	defer testPool.Close()

	os.Exit(m.Run())
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// --- helpers ---------------------------------------------------------------

func brl(t *testing.T) domain.Currency {
	t.Helper()
	c, err := domain.ParseCurrency("BRL")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func money(t *testing.T, amount string) domain.Money {
	t.Helper()
	m, err := domain.ParseMoney(amount, brl(t))
	if err != nil {
		t.Fatalf("ParseMoney(%q): %v", amount, err)
	}
	return m
}

func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// newOpening builds a wallet with its opening transaction and entry, all with
// identifiers unique to this call.
func newOpening(t *testing.T, balance string) (domain.Opening, domain.WagerTransaction) {
	t.Helper()
	suffix := uniqueSuffix()

	walletID, err := domain.ParseWalletID("wallet-" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := domain.ParsePlayerID("player-" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	txID, err := domain.ParseTransactionID("tx-opening-" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	entryID, err := domain.ParseLedgerEntryID("entry-opening-" + suffix)
	if err != nil {
		t.Fatal(err)
	}

	at := now()
	opening, err := domain.OpenWallet(domain.OpenWalletParams{
		ID:                   walletID,
		PlayerID:             playerID,
		InitialBalance:       money(t, balance),
		OpeningTransactionID: txID,
		OpeningEntryID:       entryID,
		CreatedAt:            at,
	})
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}

	tx, err := domain.NewOpeningTransaction(domain.OpeningTransactionParams{
		ID:        txID,
		WalletID:  walletID,
		PlayerID:  playerID,
		Money:     money(t, balance),
		CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("NewOpeningTransaction: %v", err)
	}
	return opening, tx
}

// persistOpening commits a wallet, its opening transaction and its entry.
func persistOpening(t *testing.T, uow app.UnitOfWork, opening domain.Opening, tx domain.WagerTransaction) {
	t.Helper()
	err := uow.Do(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		if err := repos.Wallets().Insert(ctx, opening.Wallet); err != nil {
			return err
		}
		if err := repos.Transactions().Insert(ctx, tx); err != nil {
			return err
		}
		for _, entry := range opening.Entries {
			if err := repos.Ledger().Append(ctx, entry); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("persisting the opening: %v", err)
	}
}

func externalTx(t *testing.T, walletID domain.WalletID, playerID domain.PlayerID, kind domain.Kind, amount string) domain.WagerTransaction {
	t.Helper()
	suffix := uniqueSuffix()

	parse := func(f func(string) error, value string) {
		t.Helper()
		if err := f(value); err != nil {
			t.Fatal(err)
		}
	}

	var p domain.ExternalTransactionParams
	var err error
	parse(func(s string) error { p.ID, err = domain.ParseTransactionID(s); return err }, "tx-"+suffix)
	parse(func(s string) error { p.ProviderID, err = domain.ParseProviderID(s); return err }, "provider-a")
	parse(func(s string) error { p.ExternalID, err = domain.ParseExternalTransactionID(s); return err }, "ext-"+suffix)
	parse(func(s string) error { p.IdempotencyKey, err = domain.ParseIdempotencyKey(s); return err }, "provider-a:ext-"+suffix)
	parse(func(s string) error { p.PayloadHash, err = domain.ParsePayloadHash(s); return err }, "sha256-"+suffix)
	parse(func(s string) error { p.RoundID, err = domain.ParseRoundID(s); return err }, "round-"+suffix)
	parse(func(s string) error { p.GameID, err = domain.ParseGameID(s); return err }, "fortune-chimp")

	p.Kind = kind
	p.WalletID = walletID
	p.PlayerID = playerID
	p.Money = money(t, amount)
	p.CreatedAt = now()

	tx, err := domain.NewExternalTransaction(p)
	if err != nil {
		t.Fatalf("NewExternalTransaction: %v", err)
	}
	return tx
}

// --- wallets ---------------------------------------------------------------

func TestWalletRoundTrip(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	queries := NewQueries(testPool)
	opening, tx := newOpening(t, "1000.00")
	persistOpening(t, uow, opening, tx)

	ctx := context.Background()
	got, err := queries.Wallets().FindByID(ctx, opening.Wallet.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	// The whole value has to survive the round trip, amount and currency alike.
	if got != opening.Wallet {
		t.Fatalf("round trip changed the wallet:\n got %+v\nwant %+v", got, opening.Wallet)
	}

	byPlayer, err := queries.Wallets().FindByPlayerAndCurrency(ctx, opening.Wallet.PlayerID(), brl(t))
	if err != nil {
		t.Fatalf("FindByPlayerAndCurrency: %v", err)
	}
	if byPlayer != opening.Wallet {
		t.Error("lookup by player and currency returned a different wallet")
	}
}

func TestWalletNotFound(t *testing.T) {
	id, err := domain.ParseWalletID("missing-" + uniqueSuffix())
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewQueries(testPool).Wallets().FindByID(context.Background(), id)
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("error = %v, want app.ErrNotFound", err)
	}
}

func TestSecondWalletForTheSamePlayerAndCurrencyConflicts(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, tx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, tx)

	// Same player, same currency, different wallet id.
	secondID, err := domain.ParseWalletID("wallet-second-" + uniqueSuffix())
	if err != nil {
		t.Fatal(err)
	}
	second, err := domain.OpenWallet(domain.OpenWalletParams{
		ID:             secondID,
		PlayerID:       opening.Wallet.PlayerID(),
		InitialBalance: money(t, "0.00"),
		CreatedAt:      now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	err = uow.Do(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		return repos.Wallets().Insert(ctx, second.Wallet)
	})
	if !errors.Is(err, app.ErrConflict) {
		t.Fatalf("error = %v, want app.ErrConflict", err)
	}

	var ce *app.ConstraintError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v does not name a constraint", err)
	}
	// The name is the point: this conflict means "that player already has a
	// wallet", and a caller has to be able to say so.
	if ce.Name != "wallets_one_per_player_and_currency" {
		t.Errorf("constraint = %q", ce.Name)
	}
}

func TestUpdateBalanceHonoursTheVersion(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, tx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, tx)

	entryID, err := domain.ParseLedgerEntryID("entry-" + uniqueSuffix())
	if err != nil {
		t.Fatal(err)
	}
	moved, err := opening.Wallet.Debit(entryID, tx.ID(), money(t, "25.00"), now())
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	t.Run("matching version writes", func(t *testing.T) {
		err := uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
			return repos.Wallets().UpdateBalance(ctx, moved.Wallet, opening.Wallet.Version())
		})
		if err != nil {
			t.Fatalf("UpdateBalance: %v", err)
		}
		stored, err := NewQueries(testPool).Wallets().FindByID(ctx, opening.Wallet.ID())
		if err != nil {
			t.Fatal(err)
		}
		if stored.Balance().String() != "75.00" || stored.Version() != 2 {
			t.Fatalf("stored %s at version %d", stored.Balance(), stored.Version())
		}
	})

	t.Run("stale version is refused", func(t *testing.T) {
		// This is the lost update, caught: the caller read version 1, someone
		// else already moved the wallet to 2, and this write would erase it.
		err := uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
			return repos.Wallets().UpdateBalance(ctx, moved.Wallet, opening.Wallet.Version())
		})
		if !errors.Is(err, app.ErrVersionMismatch) {
			t.Fatalf("error = %v, want app.ErrVersionMismatch", err)
		}
	})

	t.Run("unknown wallet is not a version mismatch", func(t *testing.T) {
		ghostID, err := domain.ParseWalletID("ghost-" + uniqueSuffix())
		if err != nil {
			t.Fatal(err)
		}
		ghost, err := domain.RehydrateWallet(domain.RehydrateWalletParams{
			ID:        ghostID,
			PlayerID:  opening.Wallet.PlayerID(),
			Balance:   money(t, "1.00"),
			Version:   2,
			CreatedAt: now(),
			UpdatedAt: now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		err = uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
			return repos.Wallets().UpdateBalance(ctx, ghost, 1)
		})
		if !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("error = %v, want app.ErrNotFound", err)
		}
	})
}

func TestDatabaseRefusesANegativeBalance(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, tx := newOpening(t, "10.00")
	persistOpening(t, uow, opening, tx)

	// Straight SQL, going around the domain on purpose. The domain would never
	// produce this; the point is that the database refuses it anyway.
	_, err := testPool.Exec(context.Background(),
		`UPDATE wallets SET balance_minor = -1 WHERE id = $1`, opening.Wallet.ID().String())
	if translate(err) == nil {
		t.Fatal("the database accepted a negative balance")
	}
	if !errors.Is(translate(err), app.ErrInvariantViolated) {
		t.Fatalf("error = %v, want app.ErrInvariantViolated", translate(err))
	}
}

// --- ledger ----------------------------------------------------------------

func TestLedgerIsAppendOnlyInTheDatabase(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, tx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, tx)

	entryID := opening.Entries[0].ID().String()
	ctx := context.Background()

	for name, statement := range map[string]string{
		"update":   `UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = $1`,
		"delete":   `DELETE FROM wallet_ledger_entries WHERE id = $1`,
		"truncate": `TRUNCATE wallet_ledger_entries`,
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			if name == "truncate" {
				_, err = testPool.Exec(ctx, statement)
			} else {
				_, err = testPool.Exec(ctx, statement, entryID)
			}
			if err == nil {
				t.Fatalf("the database allowed a %s on the ledger", name)
			}
			if !errors.Is(translate(err), app.ErrInvariantViolated) {
				t.Fatalf("error = %v, want app.ErrInvariantViolated", translate(err))
			}
		})
	}
}

func TestLedgerRefusesASecondEntryForTheSameTransaction(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, tx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, tx)

	// A different entry id, but the same (wallet, transaction) pair: this is
	// what a duplicate delivery looks like once it reaches the database.
	duplicateID, err := domain.ParseLedgerEntryID("entry-dup-" + uniqueSuffix())
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := domain.NewLedgerEntry(domain.LedgerEntryParams{
		ID:            duplicateID,
		WalletID:      opening.Wallet.ID(),
		TransactionID: tx.ID(),
		Direction:     domain.Credit,
		Amount:        money(t, "1.00"),
		BalanceBefore: money(t, "0.00"),
		BalanceAfter:  money(t, "1.00"),
		CreatedAt:     now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	err = uow.Do(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		return repos.Ledger().Append(ctx, duplicate)
	})
	var ce *app.ConstraintError
	if !errors.As(err, &ce) || !errors.Is(err, app.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if ce.Name != "ledger_one_per_transaction_and_wallet" {
		t.Errorf("constraint = %q", ce.Name)
	}
}

func TestDatabaseRefusesBrokenLedgerArithmetic(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, tx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, tx)

	// Around the domain again: a debit whose balances do not add up.
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO wallet_ledger_entries
		  (id, wallet_id, transaction_id, direction, amount_minor,
		   balance_before_minor, balance_after_minor, currency, created_at)
		VALUES ($1, $2, $3, 'DEBIT', 100, 10000, 9999, 'BRL', now())`,
		"entry-broken-"+uniqueSuffix(), opening.Wallet.ID().String(), tx.ID().String())
	if !errors.Is(translate(err), app.ErrInvariantViolated) {
		t.Fatalf("error = %v, want app.ErrInvariantViolated", translate(err))
	}
}

func TestLedgerPaginationIsStable(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, openingTx := newOpening(t, "1000.00")
	persistOpening(t, uow, opening, openingTx)

	ctx := context.Background()
	wallet := opening.Wallet

	// Seven movements committed in the same instant, so created_at ties. If the
	// ordering used the timestamp, this is where a cursor would skip or repeat.
	const movements = 7
	instant := now()
	for i := 0; i < movements; i++ {
		tx := externalTx(t, wallet.ID(), wallet.PlayerID(), domain.KindBet, "1.00")
		entryID, err := domain.ParseLedgerEntryID("entry-" + uniqueSuffix())
		if err != nil {
			t.Fatal(err)
		}
		moved, err := wallet.Debit(entryID, tx.ID(), money(t, "1.00"), instant)
		if err != nil {
			t.Fatal(err)
		}
		previous := wallet.Version()
		err = uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
			if err := repos.Transactions().Insert(ctx, tx); err != nil {
				return err
			}
			if err := repos.Wallets().UpdateBalance(ctx, moved.Wallet, previous); err != nil {
				return err
			}
			return repos.Ledger().Append(ctx, moved.Entry)
		})
		if err != nil {
			t.Fatalf("movement %d: %v", i, err)
		}
		wallet = moved.Wallet
	}

	// Page through in threes and check every entry is seen exactly once.
	seen := map[string]int{}
	cursor := app.NewLedgerCursor(0)
	pages := 0
	for {
		page, err := NewQueries(testPool).Ledger().ListByWallet(ctx, wallet.ID(), cursor, 3)
		if err != nil {
			t.Fatalf("ListByWallet: %v", err)
		}
		for _, e := range page.Entries {
			seen[e.ID().String()]++
		}
		pages++
		if !page.HasMore {
			break
		}
		cursor = page.Next
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}

	// The opening credit plus the seven debits.
	if len(seen) != movements+1 {
		t.Fatalf("saw %d distinct entries, want %d", len(seen), movements+1)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("entry %s served %d times", id, count)
		}
	}
}

func TestStoredBalanceMatchesTheLedgerSum(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, openingTx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, openingTx)

	ctx := context.Background()
	wallet := opening.Wallet

	steps := []struct {
		direction domain.Direction
		amount    string
	}{
		{domain.Debit, "25.00"},
		{domain.Credit, "50.00"},
		{domain.Debit, "0.01"},
		{domain.Credit, "0.99"},
	}
	for _, s := range steps {
		tx := externalTx(t, wallet.ID(), wallet.PlayerID(), domain.KindBet, s.amount)
		entryID, err := domain.ParseLedgerEntryID("entry-" + uniqueSuffix())
		if err != nil {
			t.Fatal(err)
		}

		var moved domain.Movement
		if s.direction == domain.Debit {
			moved, err = wallet.Debit(entryID, tx.ID(), money(t, s.amount), now())
		} else {
			moved, err = wallet.Credit(entryID, tx.ID(), money(t, s.amount), now())
		}
		if err != nil {
			t.Fatal(err)
		}

		previous := wallet.Version()
		err = uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
			if err := repos.Transactions().Insert(ctx, tx); err != nil {
				return err
			}
			if err := repos.Wallets().UpdateBalance(ctx, moved.Wallet, previous); err != nil {
				return err
			}
			return repos.Ledger().Append(ctx, moved.Entry)
		})
		if err != nil {
			t.Fatalf("%s %s: %v", s.direction, s.amount, err)
		}
		wallet = moved.Wallet
	}

	// The property the whole system rests on, checked through the database.
	sum, count, err := NewQueries(testPool).Ledger().SumByWallet(ctx, wallet.ID(), brl(t))
	if err != nil {
		t.Fatalf("SumByWallet: %v", err)
	}
	stored, err := NewQueries(testPool).Wallets().FindByID(ctx, wallet.ID())
	if err != nil {
		t.Fatal(err)
	}
	if sum != stored.Balance() {
		t.Fatalf("ledger sums to %s but the stored balance is %s", sum, stored.Balance())
	}
	if want := len(steps) + 1; count != want {
		t.Errorf("counted %d entries, want %d", count, want)
	}
	if stored.Balance().String() != "125.98" {
		t.Errorf("balance = %s, want 125.98", stored.Balance())
	}
}

// --- transactions ----------------------------------------------------------

func TestTransactionRoundTrip(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	queries := NewQueries(testPool)
	opening, openingTx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, openingTx)

	ctx := context.Background()

	stored, err := queries.Transactions().FindByID(ctx, openingTx.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored != openingTx {
		t.Fatalf("round trip changed the opening:\n got %+v\nwant %+v", stored, openingTx)
	}

	external := externalTx(t, opening.Wallet.ID(), opening.Wallet.PlayerID(), domain.KindBet, "25.00")
	if err := uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
		return repos.Transactions().Insert(ctx, external)
	}); err != nil {
		t.Fatalf("inserting the external transaction: %v", err)
	}

	byBusiness, err := queries.Transactions().FindByBusinessID(ctx, external.ProviderID(), external.ExternalID())
	if err != nil {
		t.Fatalf("FindByBusinessID: %v", err)
	}
	if byBusiness != external {
		t.Error("business-id lookup returned a different transaction")
	}
}

func TestDatabaseRefusesAnExternalOpening(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, openingTx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, openingTx)

	// The domain refuses this too. Here it is sent straight to the database,
	// because "a provider can mint balance" has to be impossible at both
	// layers, not just the one with tests around it.
	suffix := uniqueSuffix()
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO wager_transactions
		  (id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
		   provider_id, external_id, idempotency_key, payload_hash, round_id, game_id,
		   created_at, updated_at)
		VALUES ($1, 'EXTERNAL', 'OPENING', 'PENDING', $2, $3, 100, 'BRL',
		        'provider-a', $4, $5, 'hash', 'round', 'game', now(), now())`,
		"tx-evil-"+suffix, opening.Wallet.ID().String(), opening.Wallet.PlayerID().String(),
		"ext-"+suffix, "key-"+suffix)
	if !errors.Is(translate(err), app.ErrInvariantViolated) {
		t.Fatalf("error = %v, want app.ErrInvariantViolated", translate(err))
	}
}

func TestDatabaseRefusesASecondOpeningForAWallet(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, openingTx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, openingTx)

	second, err := domain.NewOpeningTransaction(domain.OpeningTransactionParams{
		ID:        mustTransactionID(t, "tx-opening-second-"+uniqueSuffix()),
		WalletID:  opening.Wallet.ID(),
		PlayerID:  opening.Wallet.PlayerID(),
		Money:     money(t, "50.00"),
		CreatedAt: now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// A second initial credit would be balance out of nowhere.
	err = uow.Do(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		return repos.Transactions().Insert(ctx, second)
	})
	var ce *app.ConstraintError
	if !errors.As(err, &ce) || !errors.Is(err, app.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if ce.Name != "wager_transactions_one_opening_per_wallet" {
		t.Errorf("constraint = %q", ce.Name)
	}
}

func TestDuplicateIdempotencyKeyConflicts(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, openingTx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, openingTx)

	ctx := context.Background()
	first := externalTx(t, opening.Wallet.ID(), opening.Wallet.PlayerID(), domain.KindBet, "10.00")
	if err := uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
		return repos.Transactions().Insert(ctx, first)
	}); err != nil {
		t.Fatal(err)
	}

	// Same provider, same key, different operation. This is a client reusing a
	// key for different content -- the conflict the contract promises.
	second := externalTx(t, opening.Wallet.ID(), opening.Wallet.PlayerID(), domain.KindBet, "20.00")
	reused := domain.ExternalTransactionParams{
		ID:                  second.ID(),
		Kind:                domain.KindBet,
		ProviderID:          second.ProviderID(),
		ExternalID:          second.ExternalID(),
		IdempotencyKey:      first.IdempotencyKey(),
		PayloadHash:         second.PayloadHash(),
		WalletID:            second.WalletID(),
		PlayerID:            second.PlayerID(),
		RoundID:             second.RoundID(),
		GameID:              second.GameID(),
		Money:               second.Money(),
		CreatedAt:           second.CreatedAt(),
		ReferenceExternalID: domain.ExternalTransactionID{},
	}
	clashing, err := domain.NewExternalTransaction(reused)
	if err != nil {
		t.Fatal(err)
	}

	err = uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
		return repos.Transactions().Insert(ctx, clashing)
	})
	var ce *app.ConstraintError
	if !errors.As(err, &ce) || !errors.Is(err, app.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if ce.Name != "wager_transactions_idempotency_key" {
		t.Errorf("constraint = %q", ce.Name)
	}
}

func TestUpdateRefusesToMoveATerminalTransaction(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, openingTx := newOpening(t, "100.00")
	persistOpening(t, uow, opening, openingTx)

	ctx := context.Background()
	tx := externalTx(t, opening.Wallet.ID(), opening.Wallet.PlayerID(), domain.KindBet, "10.00")
	if err := uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
		return repos.Transactions().Insert(ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}

	processed, err := tx.MarkProcessed(money(t, "90.00"), now())
	if err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
		return repos.Transactions().Update(ctx, processed)
	}); err != nil {
		t.Fatalf("first transition: %v", err)
	}

	// A worker that woke up late still holds the PENDING value and would
	// happily overwrite the result another instance committed. The WHERE clause
	// is what stops it -- the domain check cannot, because this caller's copy
	// looks perfectly transitionable.
	stale, err := tx.Reject(domain.CodeInsufficientFunds, now())
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
		return repos.Transactions().Update(ctx, stale)
	})
	if !errors.Is(err, app.ErrConflict) {
		t.Fatalf("error = %v, want app.ErrConflict", err)
	}

	stored, err := NewQueries(testPool).Transactions().FindByID(ctx, tx.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status() != domain.StatusProcessed {
		t.Fatalf("stored status = %q, want it untouched at PROCESSED", stored.Status())
	}
}

// --- unit of work ----------------------------------------------------------

func TestUnitOfWorkCommitsEverythingTogether(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, tx := newOpening(t, "500.00")
	persistOpening(t, uow, opening, tx)

	ctx := context.Background()
	wallet, err := NewQueries(testPool).Wallets().FindByID(ctx, opening.Wallet.ID())
	if err != nil {
		t.Fatalf("the wallet did not survive the commit: %v", err)
	}
	page, err := NewQueries(testPool).Ledger().ListByWallet(ctx, wallet.ID(), app.NewLedgerCursor(0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("got %d entries, want the opening credit", len(page.Entries))
	}
}

func TestUnitOfWorkRollsBackEverythingOnError(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, tx := newOpening(t, "500.00")

	sentinel := errors.New("the use case changed its mind")
	err := uow.Do(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		if err := repos.Wallets().Insert(ctx, opening.Wallet); err != nil {
			return err
		}
		if err := repos.Transactions().Insert(ctx, tx); err != nil {
			return err
		}
		if err := repos.Ledger().Append(ctx, opening.Entries[0]); err != nil {
			return err
		}
		// Everything above succeeded. Failing here is the interesting case: a
		// partial commit would leave a wallet without its ledger entry.
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the sentinel back unchanged", err)
	}

	ctx := context.Background()
	if _, err := NewQueries(testPool).Wallets().FindByID(ctx, opening.Wallet.ID()); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("the wallet survived the rollback: %v", err)
	}
	if _, err := NewQueries(testPool).Transactions().FindByID(ctx, tx.ID()); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("the transaction survived the rollback: %v", err)
	}
	page, err := NewQueries(testPool).Ledger().ListByWallet(ctx, opening.Wallet.ID(), app.NewLedgerCursor(0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 0 {
		t.Errorf("%d ledger entries survived the rollback", len(page.Entries))
	}
}

func TestUnitOfWorkRollsBackOnPanicAndRepropagates(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, tx := newOpening(t, "500.00")

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		_ = uow.Do(context.Background(), func(ctx context.Context, repos app.Repositories) error {
			if err := repos.Wallets().Insert(ctx, opening.Wallet); err != nil {
				return err
			}
			panic("something went very wrong")
		})
	}()

	if _, err := NewQueries(testPool).Wallets().FindByID(context.Background(), opening.Wallet.ID()); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("the wallet survived a panic: %v", err)
	}
	_ = tx
}

func TestNestedUnitOfWorkIsRefused(t *testing.T) {
	uow := NewUnitOfWork(testPool)

	err := uow.Do(context.Background(), func(ctx context.Context, _ app.Repositories) error {
		// Opening a savepoint quietly here is how "transaction" stops meaning
		// anything: the outer one commits while the inner already undid half.
		return uow.Do(ctx, func(context.Context, app.Repositories) error { return nil })
	})
	if !errors.Is(err, app.ErrNestedUnitOfWork) {
		t.Fatalf("error = %v, want app.ErrNestedUnitOfWork", err)
	}
}

func TestRepositoriesSeeTheirOwnUncommittedWrites(t *testing.T) {
	uow := NewUnitOfWork(testPool)
	opening, tx := newOpening(t, "100.00")

	// Read-your-writes inside the transaction is what makes a use case able to
	// insert a wallet and then move it in the same commit.
	err := uow.Do(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		if err := repos.Wallets().Insert(ctx, opening.Wallet); err != nil {
			return err
		}
		if err := repos.Transactions().Insert(ctx, tx); err != nil {
			return err
		}
		found, err := repos.Wallets().FindByID(ctx, opening.Wallet.ID())
		if err != nil {
			return fmt.Errorf("reading back inside the transaction: %w", err)
		}
		if found != opening.Wallet {
			return errors.New("the wallet read back differently")
		}
		return errors.New("rolling this back on purpose")
	})
	if err == nil || !strings.Contains(err.Error(), "on purpose") {
		t.Fatalf("error = %v", err)
	}
}

func mustTransactionID(t *testing.T, s string) domain.TransactionID {
	t.Helper()
	id, err := domain.ParseTransactionID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
