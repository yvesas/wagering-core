package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

// ErrorBody is what a failed request returns. The code is stable and is what a
// client branches on; the message is for a human reading a log.
type ErrorBody struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlationId,omitempty"`
}

// domainStatus maps every domain rejection onto a status.
//
// It is exhaustive on purpose, and a test reads the domain's source to prove it
// stays that way. A code missing from here would fall through to 500, turning a
// client mistake into "our fault" -- and nobody would notice until someone
// looked at why a 500 rate went up.
var domainStatus = map[domain.Code]int{
	// The request could not be understood. The client has to change it.
	domain.CodeInvalidCurrency:      http.StatusBadRequest,
	domain.CodeInvalidAmount:        http.StatusBadRequest,
	domain.CodeNegativeAmount:       http.StatusBadRequest,
	domain.CodeAmountOverflow:       http.StatusBadRequest,
	domain.CodeCurrencyMismatch:     http.StatusBadRequest,
	domain.CodeInvalidIdentifier:    http.StatusBadRequest,
	domain.CodeInvalidTimestamp:     http.StatusBadRequest,
	domain.CodeInvalidDirection:     http.StatusBadRequest,
	domain.CodeNonPositiveAmount:    http.StatusBadRequest,
	domain.CodeInvalidKind:          http.StatusBadRequest,
	domain.CodeInvalidAmountForKind: http.StatusBadRequest,
	domain.CodeMissingReference:     http.StatusBadRequest,
	domain.CodeUnexpectedReference:  http.StatusBadRequest,

	// The request was understood and a business rule refused it. Resending it
	// unchanged will be refused again, but it was not malformed.
	domain.CodeInsufficientFunds: http.StatusUnprocessableEntity,

	// Reversal refusals. All of them are recorded outcomes, so they normally
	// reach a client through the transaction's status rather than as an error;
	// the mapping exists because a code without one would fall through to 500.
	domain.CodeReferenceNotFound:       http.StatusUnprocessableEntity,
	domain.CodeReferenceMismatch:       http.StatusUnprocessableEntity,
	domain.CodeReferenceAmountMismatch: http.StatusUnprocessableEntity,
	domain.CodeReferenceNotReversible:  http.StatusUnprocessableEntity,
	domain.CodeAlreadyReversed:         http.StatusUnprocessableEntity,
	// Money already handed over that cannot be taken back. Deliberately its own
	// code, and not INSUFFICIENT_FUNDS: this one needs a person to look.
	domain.CodeReversalExceedsBalance: http.StatusUnprocessableEntity,
	domain.CodeInvalidTransition:      http.StatusConflict,
	domain.CodeInvalidStatus:          http.StatusConflict,

	// The stored state is inconsistent with what the domain allows. This is a
	// bug on our side, not a bad request.
	domain.CodeInconsistentEntry: http.StatusInternalServerError,
	domain.CodeInvalidVersion:    http.StatusInternalServerError,
}

// conflictCodes turn a database constraint into something a client can branch
// on.
//
// Returning the constraint name would leak schema naming and, worse, would make
// a client's error handling depend on it: renaming a constraint is a migration,
// not an API change, and it must not break anyone. Anything not in this map
// stays the generic CONFLICT.
var conflictCodes = map[string]string{
	"wallets_one_per_player_and_currency":       "WALLET_ALREADY_EXISTS",
	"ledger_one_per_transaction_and_wallet":     "TRANSACTION_ALREADY_APPLIED",
	"wager_transactions_business_identity":      "TRANSACTION_ALREADY_SUBMITTED",
	"wager_transactions_idempotency_key":        "IDEMPOTENCY_KEY_REUSED",
	"wager_transactions_one_opening_per_wallet": "WALLET_ALREADY_OPENED",
	"wager_transactions_pkey":                   "TRANSACTION_ALREADY_SUBMITTED",
	"wallets_pkey":                              "WALLET_ALREADY_EXISTS",
}

// conflictCode names the conflict, falling back to the generic one.
func conflictCode(err error) string {
	var ce *app.ConstraintError
	if errors.As(err, &ce) {
		if code, ok := conflictCodes[ce.Name]; ok {
			return code
		}
	}
	return "CONFLICT"
}

// statusFor decides the status and the code for an error.
func statusFor(err error) (int, string) {
	var de *domain.Error
	if errors.As(err, &de) {
		if status, ok := domainStatus[de.Code]; ok {
			return status, string(de.Code)
		}
		// Unreachable while the exhaustiveness test passes. Kept because
		// "unreachable" is not a guarantee, and 400 is the safer guess for a
		// domain rejection than 500.
		return http.StatusBadRequest, string(de.Code)
	}

	switch {
	case errors.Is(err, app.ErrNotFound):
		return http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, app.ErrConflict):
		return http.StatusConflict, conflictCode(err)
	case errors.Is(err, app.ErrVersionMismatch):
		// Someone else moved the wallet in between. The client may retry, and
		// the state it read is stale -- which is what 409 says.
		return http.StatusConflict, "VERSION_MISMATCH"
	case errors.Is(err, app.ErrInvalidInput):
		return http.StatusBadRequest, "INVALID_INPUT"
	case errors.Is(err, app.ErrNotImplemented):
		// The request is valid and this build cannot serve it. 501 says that
		// without pretending the client got something wrong.
		return http.StatusNotImplemented, "NOT_IMPLEMENTED"
	case errors.Is(err, app.ErrSerializationFailure):
		// Transient: the database could not order two transactions. Retrying
		// the same request is the right move, so it must not look permanent.
		return http.StatusServiceUnavailable, "TRY_AGAIN"
	case errors.Is(err, app.ErrInvariantViolated):
		return http.StatusInternalServerError, "INTERNAL"
	default:
		return http.StatusInternalServerError, "INTERNAL"
	}
}

// writeError renders a failure.
//
// A 5xx never echoes the underlying message to the client: it can carry a
// constraint name, a column, or a fragment of SQL. The full error goes to the
// log with the correlation id, so the two can be matched without exposing
// anything.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := statusFor(err)
	correlationID := CorrelationIDFrom(r.Context())

	message := err.Error()
	if status == http.StatusConflict {
		// The underlying message carries the constraint name. The code above
		// already says what happened in terms the client owns.
		message = "the request conflicts with the current state"
	}
	if status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request failed",
			slog.String("code", code),
			slog.String("error", err.Error()),
			slog.String("correlationId", correlationID),
		)
		message = "internal error"
	}

	writeJSON(w, r, status, ErrorBody{
		Code:          code,
		Message:       message,
		CorrelationID: correlationID,
	})
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already out, so the response cannot be repaired.
		// Logging is all that is left, and silence here would hide a broken
		// endpoint behind a 200.
		slog.ErrorContext(r.Context(), "writing the response body",
			slog.String("error", err.Error()),
			slog.String("correlationId", CorrelationIDFrom(r.Context())),
		)
	}
}
