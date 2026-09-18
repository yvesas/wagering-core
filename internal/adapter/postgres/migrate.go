package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/yvesas/wagering-core/migrations"
)

// Migrate brings the schema up to the latest version.
//
// goose takes an advisory lock before running, which is what makes this safe to
// call from every instance at start-up: several processes racing to migrate is
// the normal case with more than one replica, not an edge case.
func Migrate(ctx context.Context, db *sql.DB) error {
	provider, err := newProvider(db)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

// MigrateDown rolls back the most recent migration. It exists so the rollback
// path is exercised rather than merely documented: a down migration nobody runs
// is a down migration that does not work.
func MigrateDown(ctx context.Context, db *sql.DB) error {
	provider, err := newProvider(db)
	if err != nil {
		return err
	}
	if _, err := provider.Down(ctx); err != nil {
		return fmt.Errorf("rolling back migration: %w", err)
	}
	return nil
}

// MigrationStatus reports the applied version.
func MigrationStatus(ctx context.Context, db *sql.DB) (int64, error) {
	provider, err := newProvider(db)
	if err != nil {
		return 0, err
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading migration version: %w", err)
	}
	return version, nil
}

func newProvider(db *sql.DB) (*goose.Provider, error) {
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return nil, fmt.Errorf("building migration provider: %w", err)
	}
	return provider, nil
}
