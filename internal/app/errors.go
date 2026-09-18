package app

import (
	"errors"
	"fmt"
)

// The failures an adapter is allowed to report. Nothing above this layer ever
// inspects a driver error code, and no *pgconn.PgError travels past the
// adapter boundary -- translating at the edge is what keeps the use cases from
// being written in the vocabulary of one database.
var (
	// ErrNotFound means the row is not there. It is not an error on its own:
	// most callers decide what a missing wallet means.
	ErrNotFound = errors.New("not found")

	// ErrConflict means a uniqueness rule refused the write. Which rule is in
	// the [ConstraintError] that carries it, because "a wallet already exists
	// for this player" and "this idempotency key is taken" are different
	// answers to the caller.
	ErrConflict = errors.New("conflict")

	// ErrInvariantViolated means the database refused a write that the domain
	// should have refused first. Reaching it is a bug, not a business
	// rejection, and it is worth an alert rather than a friendly message.
	ErrInvariantViolated = errors.New("invariant violated")

	// ErrVersionMismatch means the conditional update matched no row: someone
	// else moved the wallet in between. This is the lost update, caught.
	ErrVersionMismatch = errors.New("version mismatch")

	// ErrSerializationFailure means the database could not order two concurrent
	// transactions and rolled one back. It is transient, and the unit of work
	// is where the retry belongs.
	ErrSerializationFailure = errors.New("serialization failure")

	// ErrNestedUnitOfWork means a unit of work was started inside another one.
	// Nesting silently as a savepoint is how "transaction" stops meaning
	// anything: the outer one commits while the inner already undid half.
	ErrNestedUnitOfWork = errors.New("nested unit of work")
)

// ConstraintError names the database rule that refused a write.
//
// The name matters. A conflict on wallets_one_per_player_and_currency is a
// player opening a second wallet; a conflict on
// ledger_one_per_transaction_and_wallet is a duplicate delivery that the
// database just absorbed. Same error class, opposite meanings.
type ConstraintError struct {
	Name string

	kind error
}

func (e *ConstraintError) Error() string {
	if e.Name == "" {
		return e.kind.Error()
	}
	return fmt.Sprintf("%s: %s", e.kind, e.Name)
}

// Unwrap is what makes errors.Is(err, ErrConflict) work against a named
// constraint failure.
func (e *ConstraintError) Unwrap() error { return e.kind }

// NewConflict reports a uniqueness violation, naming the rule that fired.
func NewConflict(constraint string) *ConstraintError {
	return &ConstraintError{Name: constraint, kind: ErrConflict}
}

// NewInvariantViolation reports a check, foreign key or trigger refusing a
// write that should never have been attempted.
func NewInvariantViolation(constraint string) *ConstraintError {
	return &ConstraintError{Name: constraint, kind: ErrInvariantViolated}
}
