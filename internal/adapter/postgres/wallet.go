package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

type walletRepository struct {
	q querier
}

const walletColumns = `id, player_id, currency, balance_minor, version, created_at, updated_at`

func (r *walletRepository) FindByID(ctx context.Context, id domain.WalletID) (domain.Wallet, error) {
	row := r.q.QueryRow(ctx,
		`SELECT `+walletColumns+` FROM wallets WHERE id = $1`,
		id.String())
	return scanWallet(row)
}

func (r *walletRepository) FindByPlayerAndCurrency(ctx context.Context, player domain.PlayerID, currency domain.Currency) (domain.Wallet, error) {
	row := r.q.QueryRow(ctx,
		`SELECT `+walletColumns+` FROM wallets WHERE player_id = $1 AND currency = $2`,
		player.String(), currency.String())
	return scanWallet(row)
}

func (r *walletRepository) Insert(ctx context.Context, wallet domain.Wallet) error {
	if !wallet.IsInitialised() {
		return fmt.Errorf("inserting an uninitialised wallet")
	}
	_, err := r.q.Exec(ctx, `
		INSERT INTO wallets (`+walletColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		wallet.ID().String(),
		wallet.PlayerID().String(),
		wallet.Currency().String(),
		wallet.Balance().Minor(),
		wallet.Version(),
		wallet.CreatedAt(),
		wallet.UpdatedAt(),
	)
	return translate(err)
}

// UpdateBalance writes the new balance only while the stored version is still
// the one the caller read.
//
// The condition and the write are one statement on purpose. Reading the version
// and then updating would leave a window between them, and that window is
// exactly where a lost update lives.
func (r *walletRepository) UpdateBalance(ctx context.Context, wallet domain.Wallet, expectedVersion int64) error {
	if !wallet.IsInitialised() {
		return fmt.Errorf("updating an uninitialised wallet")
	}

	tag, err := r.q.Exec(ctx, `
		UPDATE wallets
		   SET balance_minor = $1, version = $2, updated_at = $3
		 WHERE id = $4 AND version = $5`,
		wallet.Balance().Minor(),
		wallet.Version(),
		wallet.UpdatedAt(),
		wallet.ID().String(),
		expectedVersion,
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	// No row matched, and there are two reasons for that. Telling them apart
	// costs one query on a path that already failed, and saves whoever reads
	// the log from guessing.
	var stored int64
	err = r.q.QueryRow(ctx, `SELECT version FROM wallets WHERE id = $1`, wallet.ID().String()).Scan(&stored)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return app.ErrNotFound
	case err != nil:
		return translate(err)
	default:
		return fmt.Errorf("%w: wallet %s is at version %d, expected %d",
			app.ErrVersionMismatch, wallet.ID(), stored, expectedVersion)
	}
}

// scanWallet rebuilds a wallet from a row.
//
// It goes through the domain's rehydration constructor rather than filling a
// struct, so a row that violates an invariant stops here. A corrupted balance
// read back as fact is worse than a failed read.
func scanWallet(row pgx.Row) (domain.Wallet, error) {
	var (
		rawID, rawPlayer, rawCurrency string
		balanceMinor, version         int64
		createdAt, updatedAt          time.Time
	)

	if err := row.Scan(&rawID, &rawPlayer, &rawCurrency, &balanceMinor, &version, &createdAt, &updatedAt); err != nil {
		return domain.Wallet{}, translate(err)
	}

	id, err := domain.ParseWalletID(rawID)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("stored wallet id: %w", err)
	}
	player, err := domain.ParsePlayerID(rawPlayer)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("stored player id: %w", err)
	}
	currency, err := domain.ParseCurrency(strings.TrimSpace(rawCurrency))
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("stored currency: %w", err)
	}
	balance, err := domain.NewMoneyFromMinor(balanceMinor, currency)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("stored balance: %w", err)
	}

	return domain.RehydrateWallet(domain.RehydrateWalletParams{
		ID:        id,
		PlayerID:  player,
		Balance:   balance,
		Version:   version,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	})
}
