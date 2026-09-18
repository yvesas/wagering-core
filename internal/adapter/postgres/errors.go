package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yvesas/wagering-core/internal/app"
)

// PostgreSQL SQLSTATE codes this adapter knows how to read.
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	codeRestrictViolation   = "23001" // what the append-only trigger raises
	codeForeignKeyViolation = "23503"
	codeNotNullViolation    = "23502"
	codeUniqueViolation     = "23505"
	codeCheckViolation      = "23514"
	codeSerializationFailed = "40001"
	codeDeadlockDetected    = "40P01"
)

// translate turns a driver error into one of the failures declared in app.
//
// This is the only place in the codebase that knows what "23505" means. Above
// it, a caller asks errors.Is(err, app.ErrConflict) and never learns which
// database answered.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	switch pgErr.Code {
	case codeUniqueViolation:
		return app.NewConflict(pgErr.ConstraintName)

	case codeCheckViolation, codeForeignKeyViolation, codeNotNullViolation, codeRestrictViolation:
		// The domain should have refused these before the statement was ever
		// sent. Reaching one means the two copies of an invariant disagree,
		// which is worth an alert rather than a friendly message.
		return app.NewInvariantViolation(constraintOf(pgErr))

	case codeSerializationFailed, codeDeadlockDetected:
		return app.ErrSerializationFailure

	default:
		return err
	}
}

// constraintOf names what refused the write.
//
// A trigger raising an exception carries no constraint name, so the table is
// the best identifier available -- and for the append-only guard the table is
// exactly what the reader needs to know.
func constraintOf(pgErr *pgconn.PgError) string {
	if pgErr.ConstraintName != "" {
		return pgErr.ConstraintName
	}
	if pgErr.TableName != "" {
		return pgErr.TableName
	}
	return pgErr.Message
}
