package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig is what this adapter needs to open a pool.
//
// It deliberately does not reuse the composition layer's config type. An
// adapter that imports the layer wiring it cannot be wired by anything else --
// and the compiler said so, with an import cycle, the moment the module file
// started providing this constructor.
type PoolConfig struct {
	// DSN carries the password and is never logged.
	DSN string

	// SafeLabel identifies the database in errors and logs. It must already be
	// redacted: this type has no way to tell a safe string from a leaky one,
	// so the caller is the one that must not hand over a password.
	SafeLabel string

	MaxConns int32
}

// NewPool opens the connection pool and proves it can reach the database.
//
// It pings on the way out on purpose. A pool created lazily reports success
// here and fails on the first query instead, which moves a configuration
// mistake from start-up -- where it is obvious -- into the first request, where
// it looks like a bug in the request.
func NewPool(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parsing the connection string for %s: %w", cfg.SafeLabel, err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", cfg.SafeLabel, err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reaching %s: %w", cfg.SafeLabel, err)
	}
	return pool, nil
}
