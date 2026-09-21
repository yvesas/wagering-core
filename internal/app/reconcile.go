package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// Reconciliation is what a check found.
//
// It reports both numbers rather than only whether they matched. "They
// disagree" is the start of an investigation, not the end of one, and the two
// values plus the entry count are what the person doing it needs first.
type Reconciliation struct {
	WalletID domain.WalletID

	// Stored is the balance the wallet row carries: the number every request
	// has been answered with.
	Stored domain.Money

	// Rebuilt is the balance the ledger adds up to: credits minus debits, the
	// opening credit included, because the opening is an ordinary entry.
	Rebuilt domain.Money

	// Difference is Stored minus Rebuilt. Zero is the only acceptable value,
	// and the sign says which way the stored balance is wrong.
	Difference domain.Money

	// Entries is how many ledger rows were summed. A drift with zero entries
	// is a different problem from a drift over ten thousand.
	Entries int

	// Version is the wallet's version at the moment of the check, so a later
	// investigation can tell what had happened by then.
	Version int64

	CheckedAt time.Time
}

// Drifted reports whether the two numbers disagree.
func (r Reconciliation) Drifted() bool { return !r.Difference.IsZero() }

// ReconcileWallet rebuilds a wallet's balance from its ledger and compares.
//
// It writes nothing. That is the entire contract: the point of a reconciliation
// is to be a second opinion, and one that corrects what it finds would destroy
// the evidence of how the two ever came apart. A correction is a new ledger
// entry, raised by a person who looked.
type ReconcileWallet struct {
	snapshot Snapshot
	clock    Clock
	metrics  ReconciliationMetrics
	logger   *slog.Logger
}

func NewReconcileWallet(snapshot Snapshot, clock Clock, metrics ReconciliationMetrics, logger *slog.Logger) *ReconcileWallet {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReconcileWallet{
		snapshot: snapshot,
		clock:    clock,
		metrics:  reconciliationMetricsOr(metrics),
		logger:   logger,
	}
}

// Execute checks one wallet.
func (uc *ReconcileWallet) Execute(ctx context.Context, walletID string) (Reconciliation, error) {
	// Reconciliation reads every movement a wallet ever had. It is the
	// platform's own audit, not something a provider runs against a player --
	// REQ-SEC-004, and the same scope that opens a wallet.
	if _, err := caller(ctx, ScopeWallets); err != nil {
		return Reconciliation{}, err
	}

	id, err := domain.ParseWalletID(walletID)
	if err != nil {
		return Reconciliation{}, err
	}

	var result Reconciliation
	err = uc.snapshot.Do(ctx, func(ctx context.Context, queries Queries) error {
		// Both reads happen against one unchanging view. Without that, a bet
		// committing between them would produce a difference that never
		// existed -- and a reconciliation that cries wolf is a reconciliation
		// people learn to ignore, which is worse than not having one.
		wallet, err := queries.Wallets().FindByID(ctx, id)
		if err != nil {
			return err
		}

		rebuilt, entries, err := queries.Ledger().SumByWallet(ctx, id, wallet.Balance().Currency())
		if err != nil {
			return err
		}

		difference, err := wallet.Balance().Sub(rebuilt)
		if err != nil {
			return err
		}

		result = Reconciliation{
			WalletID:   id,
			Stored:     wallet.Balance(),
			Rebuilt:    rebuilt,
			Difference: difference,
			Entries:    entries,
			Version:    wallet.Version(),
			CheckedAt:  uc.clock.Now(),
		}
		return nil
	})
	if err != nil {
		return Reconciliation{}, err
	}

	uc.report(ctx, result)
	return result, nil
}

// report puts the outcome where an operator will see it.
//
// Both a log line and a metric, and they answer different questions. The metric
// says "something drifted" and is what a page fires on; the log line says which
// wallet and by how much, and is what someone reads next. Either one alone
// leaves half the incident unanswerable.
func (uc *ReconcileWallet) report(ctx context.Context, result Reconciliation) {
	uc.metrics.ReconciliationRun(result.Drifted())

	if !result.Drifted() {
		uc.logger.DebugContext(ctx, "wallet reconciled",
			slog.String("walletId", result.WalletID.String()),
			slog.Int("entries", result.Entries),
			slog.String("correlationId", CorrelationIDFrom(ctx)))
		return
	}

	// Error level, not warn. The stored balance and the ledger disagreeing
	// means one of them is wrong about money, and there is no volume of this
	// that is normal.
	uc.logger.ErrorContext(ctx, "wallet balance does not match its ledger",
		slog.String("walletId", result.WalletID.String()),
		slog.String("stored", result.Stored.String()),
		slog.String("rebuilt", result.Rebuilt.String()),
		slog.String("difference", result.Difference.String()),
		slog.Int("entries", result.Entries),
		slog.Int64("version", result.Version),
		slog.String("correlationId", CorrelationIDFrom(ctx)))
}
