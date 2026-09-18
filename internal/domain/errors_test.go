package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorMessage(t *testing.T) {
	t.Parallel()

	// A sentinel carries only its code, and that is what it prints.
	if got := ErrInsufficientFunds.Error(); got != string(CodeInsufficientFunds) {
		t.Errorf("sentinel message = %q, want %q", got, CodeInsufficientFunds)
	}

	// A detailed rejection prints the code and then the instance, so a log line
	// says both what class of thing went wrong and which input did it.
	detailed := fail(CodeInvalidAmount, "unexpected %q in %q", "x", "2x.00")
	want := `INVALID_AMOUNT: unexpected "x" in "2x.00"`
	if got := detailed.Error(); got != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	// It has to survive being wrapped by a caller, or errors.Is is useless one
	// layer up.
	wrapped := fmt.Errorf("handling request: %w", detailed)
	if !errors.Is(wrapped, ErrInvalidAmount) {
		t.Error("errors.Is failed through fmt.Errorf")
	}
	var de *Error
	if !errors.As(wrapped, &de) || de.Code != CodeInvalidAmount {
		t.Error("errors.As failed through fmt.Errorf")
	}
}

func TestSentinelsMatchOnCodeAlone(t *testing.T) {
	t.Parallel()
	// Two rejections of the same class compare equal however different their
	// details are. That is what lets a caller branch on the class without
	// matching strings.
	a := fail(CodeCurrencyMismatch, "BRL and USD")
	b := fail(CodeCurrencyMismatch, "EUR and GBP")
	if !errors.Is(a, b) {
		t.Error("two CURRENCY_MISMATCH errors should match")
	}
	if errors.Is(a, ErrInvalidAmount) {
		t.Error("CURRENCY_MISMATCH matched INVALID_AMOUNT")
	}
	if errors.Is(a, errors.New("some other error")) {
		t.Error("matched an unrelated error")
	}
}

func TestEveryCodeIsDistinct(t *testing.T) {
	t.Parallel()
	// Two rejections sharing a code would be indistinguishable to a provider,
	// which is the one thing a stable failure code must never be.
	sentinels := map[string]*Error{
		"ErrInvalidCurrency":      ErrInvalidCurrency,
		"ErrInvalidAmount":        ErrInvalidAmount,
		"ErrNegativeAmount":       ErrNegativeAmount,
		"ErrAmountOverflow":       ErrAmountOverflow,
		"ErrCurrencyMismatch":     ErrCurrencyMismatch,
		"ErrInvalidIdentifier":    ErrInvalidIdentifier,
		"ErrInvalidTimestamp":     ErrInvalidTimestamp,
		"ErrInvalidDirection":     ErrInvalidDirection,
		"ErrNonPositiveAmount":    ErrNonPositiveAmount,
		"ErrInconsistentEntry":    ErrInconsistentEntry,
		"ErrInsufficientFunds":    ErrInsufficientFunds,
		"ErrInvalidVersion":       ErrInvalidVersion,
		"ErrInvalidKind":          ErrInvalidKind,
		"ErrInvalidStatus":        ErrInvalidStatus,
		"ErrInvalidTransition":    ErrInvalidTransition,
		"ErrInvalidAmountForKind": ErrInvalidAmountForKind,
		"ErrMissingReference":     ErrMissingReference,
		"ErrUnexpectedReference":  ErrUnexpectedReference,
	}

	seen := map[Code]string{}
	for name, sentinel := range sentinels {
		if sentinel.Code == "" {
			t.Errorf("%s has an empty code", name)
			continue
		}
		if other, clash := seen[sentinel.Code]; clash {
			t.Errorf("%s and %s share the code %q", name, other, sentinel.Code)
		}
		seen[sentinel.Code] = name
	}
}
