// Command migrate applies or rolls back the database schema.
//
//	migrate up       bring the schema to the latest version
//	migrate down     roll back the most recent migration
//	migrate status   print the applied version
//
// The migrations travel inside the binary, so this needs a database and nothing
// else on disk.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/yvesas/wagering-core/internal/adapter/postgres"
	"github.com/yvesas/wagering-core/internal/platform"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := platform.DatabaseConfigFromEnv()
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		return fmt.Errorf("opening %s: %w", cfg.Redacted(), err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("reaching %s: %w", cfg.Redacted(), err)
	}

	switch command {
	case "up":
		if err := postgres.Migrate(ctx, db); err != nil {
			return err
		}
	case "down":
		if err := postgres.MigrateDown(ctx, db); err != nil {
			return err
		}
	case "status":
	default:
		return fmt.Errorf("unknown command %q; want up, down or status", command)
	}

	version, err := postgres.MigrationStatus(ctx, db)
	if err != nil {
		return err
	}
	fmt.Printf("%s at version %d\n", cfg.Redacted(), version)
	return nil
}
