package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yvesas/wagering-core/internal/platform"
)

// PoolConfig tunes the connection pool.
type PoolConfig struct {
	MaxConns     int32
	QueryTimeout time.Duration
}

// NewPool opens the connection pool and proves it can reach the database.
//
// It pings on the way out on purpose. A pool that is created lazily reports
// success here and fails on the first query instead, which moves a
// configuration mistake from start-up -- where it is obvious -- into the first
// request, where it looks like a bug in the request.
func NewPool(ctx context.Context, db platform.DatabaseConfig, cfg PoolConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(db.DSN())
	if err != nil {
		// db.DSN() carries the password, so the error names the redacted form.
		return nil, fmt.Errorf("parsing the connection string for %s: %w", db.Redacted(), err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", db.Redacted(), err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reaching %s: %w", db.Redacted(), err)
	}
	return pool, nil
}
