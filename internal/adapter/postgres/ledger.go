package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

type ledgerRepository struct {
	q querier
}

const ledgerColumns = `id, wallet_id, transaction_id, direction, amount_minor, ` +
	`balance_before_minor, balance_after_minor, currency, created_at`

// Append writes one entry. There is no update and no delete here, and the
// database refuses both anyway.
func (r *ledgerRepository) Append(ctx context.Context, entry domain.LedgerEntry) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO wallet_ledger_entries (`+ledgerColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		entry.ID().String(),
		entry.WalletID().String(),
		entry.TransactionID().String(),
		string(entry.Direction()),
		entry.Amount().Minor(),
		entry.BalanceBefore().Minor(),
		entry.BalanceAfter().Minor(),
		entry.Amount().Currency().String(),
		entry.CreatedAt(),
	)
	return translate(err)
}

// ListByWallet pages through a wallet's entries.
//
// The order is by seq, not by created_at. Two entries can share a timestamp --
// the same commit writes them with the same clock reading -- and a tie makes a
// cursor either skip a row or serve it twice. seq is unique and monotonic, so
// the order is total.
func (r *ledgerRepository) ListByWallet(ctx context.Context, wallet domain.WalletID, cursor app.LedgerCursor, limit int) (app.LedgerPage, error) {
	if limit <= 0 {
		return app.LedgerPage{}, fmt.Errorf("limit must be positive, got %d", limit)
	}

	// One row past the page, to learn whether another page exists. Asking for
	// exactly `limit` cannot tell a full last page from a full middle one.
	rows, err := r.q.Query(ctx, `
		SELECT seq, `+ledgerColumns+`
		  FROM wallet_ledger_entries
		 WHERE wallet_id = $1 AND seq > $2
		 ORDER BY seq
		 LIMIT $3`,
		wallet.String(), cursor.After(), limit+1)
	if err != nil {
		return app.LedgerPage{}, translate(err)
	}
	defer rows.Close()

	page := app.LedgerPage{Entries: make([]domain.LedgerEntry, 0, limit)}
	lastSeq := cursor.After()

	for rows.Next() {
		if len(page.Entries) == limit {
			page.HasMore = true
			break
		}
		seq, entry, err := scanLedgerEntry(rows)
		if err != nil {
			return app.LedgerPage{}, err
		}
		page.Entries = append(page.Entries, entry)
		lastSeq = seq
	}
	if err := rows.Err(); err != nil {
		return app.LedgerPage{}, translate(err)
	}

	page.Next = app.NewLedgerCursor(lastSeq)
	return page, nil
}

// SumByWallet rebuilds the balance from the entries: credits minus debits.
//
// The sum is cast to bigint in SQL. PostgreSQL widens SUM(bigint) to numeric to
// avoid overflowing, and letting that come back as a numeric would put the one
// type this system refuses -- a float -- one careless driver conversion away.
// The cast keeps it exact and fails loudly if a real overflow ever happens.
func (r *ledgerRepository) SumByWallet(ctx context.Context, wallet domain.WalletID, currency domain.Currency) (domain.Money, int, error) {
	var total int64
	var count int

	err := r.q.QueryRow(ctx, `
		SELECT COALESCE(SUM(
		           CASE WHEN direction = 'CREDIT' THEN amount_minor ELSE -amount_minor END
		       ), 0)::bigint,
		       COUNT(*)
		  FROM wallet_ledger_entries
		 WHERE wallet_id = $1`,
		wallet.String()).Scan(&total, &count)
	if err != nil {
		return domain.Money{}, 0, translate(err)
	}

	money, err := domain.NewMoneyFromMinor(total, currency)
	if err != nil {
		return domain.Money{}, 0, err
	}
	return money, count, nil
}

type scanner interface {
	Scan(dest ...any) error
}

// scanLedgerEntry rebuilds an entry through the domain constructor, which
// re-checks the arithmetic. A row whose balances no longer add up stops here
// instead of being served as a statement line.
func scanLedgerEntry(row scanner) (int64, domain.LedgerEntry, error) {
	var (
		seq                                            int64
		rawID, rawWallet, rawTx, rawDirection, rawCurr string
		amount, before, after                          int64
		createdAt                                      time.Time
	)

	if err := row.Scan(&seq, &rawID, &rawWallet, &rawTx, &rawDirection,
		&amount, &before, &after, &rawCurr, &createdAt); err != nil {
		return 0, domain.LedgerEntry{}, translate(err)
	}

	id, err := domain.ParseLedgerEntryID(rawID)
	if err != nil {
		return 0, domain.LedgerEntry{}, fmt.Errorf("stored entry id: %w", err)
	}
	walletID, err := domain.ParseWalletID(rawWallet)
	if err != nil {
		return 0, domain.LedgerEntry{}, fmt.Errorf("stored wallet id: %w", err)
	}
	txID, err := domain.ParseTransactionID(rawTx)
	if err != nil {
		return 0, domain.LedgerEntry{}, fmt.Errorf("stored transaction id: %w", err)
	}
	currency, err := domain.ParseCurrency(strings.TrimSpace(rawCurr))
	if err != nil {
		return 0, domain.LedgerEntry{}, fmt.Errorf("stored currency: %w", err)
	}

	amountMoney, err := domain.NewMoneyFromMinor(amount, currency)
	if err != nil {
		return 0, domain.LedgerEntry{}, err
	}
	beforeMoney, err := domain.NewMoneyFromMinor(before, currency)
	if err != nil {
		return 0, domain.LedgerEntry{}, err
	}
	afterMoney, err := domain.NewMoneyFromMinor(after, currency)
	if err != nil {
		return 0, domain.LedgerEntry{}, err
	}

	entry, err := domain.NewLedgerEntry(domain.LedgerEntryParams{
		ID:            id,
		WalletID:      walletID,
		TransactionID: txID,
		Direction:     domain.Direction(rawDirection),
		Amount:        amountMoney,
		BalanceBefore: beforeMoney,
		BalanceAfter:  afterMoney,
		CreatedAt:     createdAt,
	})
	return seq, entry, err
}
