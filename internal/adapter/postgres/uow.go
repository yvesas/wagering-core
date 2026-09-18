package postgres

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

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

// RetryPolicy bounds how hard a transient conflict is retried.
type RetryPolicy struct {
	// MaxAttempts includes the first try. Unbounded retrying turns a contended
	// wallet into a handful of requests that never answer and never give up.
	MaxAttempts int

	// BaseBackoff is doubled each attempt and then jittered.
	BaseBackoff time.Duration
}

// DefaultRetryPolicy is deliberately short. The lock means real contention
// queues rather than collides, so these retries exist for what the database
// raises anyway -- a serialization failure, a detected deadlock -- and those
// clear immediately or not at all.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 3, BaseBackoff: 5 * time.Millisecond}

func (p RetryPolicy) normalised() RetryPolicy {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = DefaultRetryPolicy.MaxAttempts
	}
	if p.BaseBackoff <= 0 {
		p.BaseBackoff = DefaultRetryPolicy.BaseBackoff
	}
	return p
}

// unitOfWork runs a callback inside one transaction.
type unitOfWork struct {
	pool  *pgxpool.Pool
	retry RetryPolicy
}

// NewUnitOfWork builds the transactional boundary over a pool.
func NewUnitOfWork(pool *pgxpool.Pool) app.UnitOfWork {
	return &unitOfWork{pool: pool, retry: DefaultRetryPolicy}
}

// NewUnitOfWorkWithRetry is the same thing with an explicit policy, so a test
// can make the retry observable instead of inferring it from timing.
func NewUnitOfWorkWithRetry(pool *pgxpool.Pool, policy RetryPolicy) app.UnitOfWork {
	return &unitOfWork{pool: pool, retry: policy.normalised()}
}

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

// Do runs fn in a transaction, retrying it while the failure is transient.
//
// Only a transient failure is retried: a serialization failure, a detected
// deadlock, a version that no longer matches. A business rejection is never
// retried -- insufficient funds does not improve on a second attempt, and
// repeating it would turn a refusal into a wait.
//
// fn must be safe to run again, which this design already forces: it keeps no
// state between attempts, and any read it needs happens inside it. A read done
// outside and reused would make the second attempt decide on the numbers that
// already lost, which is the bug the retry is supposed to prevent.
func (u *unitOfWork) Do(ctx context.Context, fn func(context.Context, app.Repositories) error) error {
	if ctx.Value(insideUnitOfWork{}) != nil {
		return app.ErrNestedUnitOfWork
	}

	policy := u.retry.normalised()
	var err error
	for attempt := 1; ; attempt++ {
		err = u.attempt(ctx, fn)
		if err == nil || !transient(err) || attempt >= policy.MaxAttempts {
			return err
		}
		if waitErr := backoff(ctx, policy, attempt); waitErr != nil {
			// The caller went away while we were waiting. Reporting why the
			// attempt failed is more useful than reporting the cancellation.
			return err
		}
	}
}

// transient says whether trying again could plausibly produce a different
// answer.
func transient(err error) bool {
	return errors.Is(err, app.ErrSerializationFailure) ||
		errors.Is(err, app.ErrVersionMismatch)
}

// backoff waits, doubling each attempt, with jitter.
//
// The jitter is the point. Without it, the writers that collided together sleep
// the same amount and collide again at the same instant: the backoff would
// synchronise exactly what it is supposed to spread out.
func backoff(ctx context.Context, policy RetryPolicy, attempt int) error {
	window := policy.BaseBackoff << (attempt - 1)
	wait := window/2 + time.Duration(rand.Int64N(int64(window/2)+1))

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (u *unitOfWork) attempt(ctx context.Context, fn func(context.Context, app.Repositories) error) error {
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
