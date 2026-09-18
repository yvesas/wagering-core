package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Probe reports whether the database answers, for the readiness endpoint.
type Probe struct {
	pool *pgxpool.Pool
}

func NewProbe(pool *pgxpool.Pool) *Probe { return &Probe{pool: pool} }

func (p *Probe) Name() string { return "postgres" }

// Ping asks for a connection and a round trip. Checking only that the pool
// object exists would report ready while every query failed.
func (p *Probe) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }
