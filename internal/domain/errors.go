package domain

import "fmt"

// Code is the stable identifier of a domain rejection. It is what a provider
// reads to decide what to do next, so it is part of the contract: renaming one
// is a breaking change, not a refactor.
type Code string

const (
	CodeInvalidCurrency  Code = "INVALID_CURRENCY"
	CodeInvalidAmount    Code = "INVALID_AMOUNT"
	CodeNegativeAmount   Code = "NEGATIVE_AMOUNT"
	CodeAmountOverflow   Code = "AMOUNT_OVERFLOW"
	CodeCurrencyMismatch Code = "CURRENCY_MISMATCH"

	CodeInvalidIdentifier Code = "INVALID_IDENTIFIER"
	CodeInvalidTimestamp  Code = "INVALID_TIMESTAMP"
	CodeInvalidDirection  Code = "INVALID_DIRECTION"
	CodeNonPositiveAmount Code = "NON_POSITIVE_AMOUNT"
	CodeInconsistentEntry Code = "INCONSISTENT_LEDGER_ENTRY"
	CodeInsufficientFunds Code = "INSUFFICIENT_FUNDS"
	CodeInvalidVersion    Code = "INVALID_VERSION"

	CodeInvalidKind          Code = "INVALID_KIND"
	CodeInvalidStatus        Code = "INVALID_STATUS"
	CodeInvalidTransition    Code = "INVALID_TRANSITION"
	CodeInvalidAmountForKind Code = "INVALID_AMOUNT_FOR_KIND"
	CodeMissingReference     Code = "MISSING_REFERENCE"
	CodeUnexpectedReference  Code = "UNEXPECTED_REFERENCE"
)

// Error is a domain rejection: a business rule said no. It is never used for
// an infrastructure failure, and a rejection never travels as a panic.
//
// Sentinels below are Errors carrying only a Code, and [Error.Is] matches on
// that Code alone. One concept covers both roles — comparing with errors.Is
// and extracting the code with errors.As — instead of two parallel lists that
// drift apart.
type Error struct {
	Code   Code
	Detail string

	cause error
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Detail
}

// Is reports whether target is a domain error with the same Code, which is what
// makes errors.Is(err, ErrCurrencyMismatch) work against a detailed error.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Unwrap exposes a non-domain cause when one was wrapped. It is nil for the
// rejections raised by this package, which have no underlying error.
func (e *Error) Unwrap() error { return e.cause }

// Rejections raised by this package. Compare with errors.Is; read the Code with
// errors.As when the caller has to map it onto a transport status.
var (
	ErrInvalidCurrency  = &Error{Code: CodeInvalidCurrency}
	ErrInvalidAmount    = &Error{Code: CodeInvalidAmount}
	ErrNegativeAmount   = &Error{Code: CodeNegativeAmount}
	ErrAmountOverflow   = &Error{Code: CodeAmountOverflow}
	ErrCurrencyMismatch = &Error{Code: CodeCurrencyMismatch}

	ErrInvalidIdentifier = &Error{Code: CodeInvalidIdentifier}
	ErrInvalidTimestamp  = &Error{Code: CodeInvalidTimestamp}
	ErrInvalidDirection  = &Error{Code: CodeInvalidDirection}
	ErrNonPositiveAmount = &Error{Code: CodeNonPositiveAmount}
	ErrInconsistentEntry = &Error{Code: CodeInconsistentEntry}
	ErrInsufficientFunds = &Error{Code: CodeInsufficientFunds}
	ErrInvalidVersion    = &Error{Code: CodeInvalidVersion}

	ErrInvalidKind          = &Error{Code: CodeInvalidKind}
	ErrInvalidStatus        = &Error{Code: CodeInvalidStatus}
	ErrInvalidTransition    = &Error{Code: CodeInvalidTransition}
	ErrInvalidAmountForKind = &Error{Code: CodeInvalidAmountForKind}
	ErrMissingReference     = &Error{Code: CodeMissingReference}
	ErrUnexpectedReference  = &Error{Code: CodeUnexpectedReference}
)

// fail builds a detailed rejection for a code. The detail explains the instance;
// the code identifies the class.
func fail(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}
