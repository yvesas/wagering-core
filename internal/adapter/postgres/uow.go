package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yvesas/wagering-core/internal/app"
)

// querier is the part of pgx that a pool and a transaction have in common. The
// repositories take one of these and never learn which they got, so the same
// SQL runs inside a unit of work and outside it.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// unitOfWork runs a callback inside one transaction.
type unitOfWork struct {
	pool *pgxpool.Pool
}

// NewUnitOfWork builds the transactional boundary over a pool.
func NewUnitOfWork(pool *pgxpool.Pool) app.UnitOfWork { return &unitOfWork{pool: pool} }

// insideUnitOfWork marks a context that is already inside a transaction.
//
// This is a marker, not the mechanism -- and the difference is the whole reason
// ADR 0003 rejected carrying the transaction in the context. The repositories
// still arrive through the callback, so losing this value cannot cause a silent
// write outside the transaction; the worst it does is let a nested Do go
// undetected. A value that can only fail by being too permissive about an
// error message is a very different thing from one that can fail by writing to
// the wrong connection.
type insideUnitOfWork struct{}

func (u *unitOfWork) Do(ctx context.Context, fn func(context.Context, app.Repositories) error) error {
	if ctx.Value(insideUnitOfWork{}) != nil {
		return app.ErrNestedUnitOfWork
	}

	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return translate(err)
	}

	// Rollback runs on a context that cannot be cancelled. If the caller's
	// context is already done -- a client that hung up, a worker being shut
	// down -- a rollback on it fails immediately, and the transaction stays
	// open until the connection is reaped. That is how a cancelled request
	// turns into a lock nobody can explain.
	rollback := func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }

	inner := context.WithValue(ctx, insideUnitOfWork{}, struct{}{})
	repos := &repositories{q: tx}

	// A panic must not commit, and must not be swallowed either: the caller
	// above still needs to see it.
	committed := false
	defer func() {
		if !committed {
			rollback()
		}
	}()

	if err := fn(inner, repos); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return translate(err)
	}
	committed = true
	return nil
}

// repositories is the bundle bound to one transaction.
type repositories struct {
	q querier
}

func (r *repositories) Wallets() app.WalletRepository { return &walletRepository{q: r.q} }
func (r *repositories) Ledger() app.LedgerRepository  { return &ledgerRepository{q: r.q} }
func (r *repositories) Transactions() app.TransactionRepository {
	return &transactionRepository{q: r.q}
}

// queries is read-only access straight to the pool.
type queries struct {
	pool *pgxpool.Pool
}

// NewQueries builds the read-only side, for lookups that do not need atomicity.
func NewQueries(pool *pgxpool.Pool) app.Queries { return &queries{pool: pool} }

func (q *queries) Wallets() app.WalletReader           { return &walletRepository{q: q.pool} }
func (q *queries) Ledger() app.LedgerReader            { return &ledgerRepository{q: q.pool} }
func (q *queries) Transactions() app.TransactionReader { return &transactionRepository{q: q.pool} }
