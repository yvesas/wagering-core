package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yvesas/wagering-core/internal/app"
)

// snapshot runs reads against one unchanging view of the database.
type snapshot struct{ pool *pgxpool.Pool }

// NewSnapshot builds the read-only, single-view boundary over a pool.
func NewSnapshot(pool *pgxpool.Pool) app.Snapshot { return &snapshot{pool: pool} }

// Do opens a repeatable-read, read-only transaction and runs fn in it.
//
// Both options earn their place:
//
// RepeatableRead is the one that matters. PostgreSQL's default is
// ReadCommitted, where every *statement* takes a fresh snapshot -- so two reads
// in one transaction can straddle somebody else's commit. Reconciliation reads
// a balance and then sums a ledger, and under ReadCommitted a bet landing
// between them would be counted in one and not the other: a difference the
// database would report and that never existed. RepeatableRead pins the whole
// transaction to one snapshot and the question goes away.
//
// ReadOnly says to the database what the use case says in prose. It costs
// nothing, and it means a write that somehow reached here is refused by
// PostgreSQL rather than by a code review.
//
// There is no retry. A read-only repeatable-read transaction has nothing to
// serialise against -- it takes a snapshot and reads it -- so the failures the
// unit of work retries cannot arise here.
func (s *snapshot) Do(ctx context.Context, fn func(context.Context, app.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return translate(err)
	}
	// Rollback on a context that cannot be cancelled, for the reason the unit
	// of work does the same: a rollback on an already-cancelled context fails
	// at once and leaves the transaction open until the connection is reaped.
	//
	// Rolling back rather than committing is not a detail either. Nothing here
	// wrote anything, so there is nothing to commit, and ending the only way a
	// read-only transaction can end removes the question entirely.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	return fn(ctx, &queries{q: tx})
}
