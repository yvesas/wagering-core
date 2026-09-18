package domain

import (
	"encoding/json"
	"math"
	"strconv"
)

// Scale is the number of decimal places every amount carries.
//
// It belongs to the type, not to the currency: two places for all of them. That
// excludes JPY (zero places) and KWD (three), and is accepted because the flows
// are in BRL. The type still carries its currency, so the day a different scale
// is needed what changes is this factor, not the design.
const Scale = 2

// scaleFactor is 10**Scale: the number of minor units in one major unit.
const scaleFactor = 100

// Currency is an ISO 4217 code. The string is unexported so the only way to get
// a non-zero Currency is [ParseCurrency] — which means an invalid one cannot be
// constructed elsewhere, and the zero value stays detectable as "not set".
type Currency struct{ code string }

// ParseCurrency accepts exactly three uppercase letters.
//
// Lowercase is rejected rather than upcased. Silent normalisation is what makes
// two spellings of the same input hash differently later, and the idempotency
// key is built from these fields.
func ParseCurrency(s string) (Currency, error) {
	if len(s) != 3 {
		return Currency{}, fail(CodeInvalidCurrency, "want 3 letters, got %q", s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return Currency{}, fail(CodeInvalidCurrency, "want uppercase letters, got %q", s)
		}
	}
	return Currency{code: s}, nil
}

func (c Currency) String() string { return c.code }

// IsZero reports whether the currency was never initialised.
func (c Currency) IsZero() bool { return c.code == "" }

// Money is an immutable amount in a single currency, held as a count of minor
// units — 2500 is "25.00". See docs/adr/0002-money-representation.md.
//
// No float32 or float64 takes part in any of it: not parsing, not arithmetic,
// not serialisation, not persistence.
//
// Money is comparable, so `a == b` is true exactly when both the amount and the
// currency match. Use it freely; [Money.Cmp] exists for ordering, where a
// currency mismatch has to be an error rather than a silent false.
type Money struct {
	minor    int64
	currency Currency
}

// ZeroMoney is the zero amount of a currency.
func ZeroMoney(c Currency) (Money, error) {
	if c.IsZero() {
		return Money{}, fail(CodeInvalidCurrency, "currency is not initialised")
	}
	return Money{currency: c}, nil
}

// NewMoneyFromMinor rebuilds an amount from stored minor units. It is the
// rehydration path: the database already holds a value this type produced, so
// any int64 is accepted, negative included.
func NewMoneyFromMinor(minor int64, c Currency) (Money, error) {
	if c.IsZero() {
		return Money{}, fail(CodeInvalidCurrency, "currency is not initialised")
	}
	return Money{minor: minor, currency: c}, nil
}

// ParseMoney reads a decimal string such as "25.00" or "-0.50".
//
// A leading minus is accepted because internal differences are legitimately
// negative — reconciliation reports one when the stored balance is below the
// balance rebuilt from the ledger. External financial input has the stricter
// rule and goes through [ParseExternalMoney].
//
// Accepted: an optional minus, at least one digit, and at most two decimal
// places. "25", "25.0" and "025.00" all yield 2500. Rejected, never rounded:
// the empty string, "NaN", "Infinity", "1e5", "+25.00", "25.", "25,00" and any
// scale beyond two places.
//
// Normalisation is defined by [Money.String], which is the canonical form and
// the one that feeds the idempotency hash.
func ParseMoney(amount string, c Currency) (Money, error) {
	if c.IsZero() {
		return Money{}, fail(CodeInvalidCurrency, "currency is not initialised")
	}
	minor, err := parseMinor(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: c}, nil
}

// ParseExternalMoney is [ParseMoney] plus the rule that money arriving from
// outside is never negative.
func ParseExternalMoney(amount string, c Currency) (Money, error) {
	m, err := ParseMoney(amount, c)
	if err != nil {
		return Money{}, err
	}
	if m.minor < 0 {
		return Money{}, fail(CodeNegativeAmount, "external amount %q is negative", amount)
	}
	return m, nil
}

// parseMinor turns a decimal string into minor units without touching a float.
//
// It scans byte by byte and accumulates in uint64 so the magnitude of
// math.MinInt64 stays representable — that value has no positive counterpart in
// int64, and it is the one case where the range is asymmetric.
func parseMinor(s string) (int64, error) {
	if s == "" {
		return 0, fail(CodeInvalidAmount, "amount is empty")
	}

	i, negative := 0, false
	switch s[0] {
	case '-':
		negative, i = true, 1
	case '+':
		// Rejected rather than ignored: "+25.00" and "25.00" would be two
		// spellings of one amount, and the idempotency hash would tell them
		// apart even though the domain could not.
		return 0, fail(CodeInvalidAmount, "explicit plus sign in %q", s)
	}

	// The magnitude of math.MinInt64 is one above math.MaxInt64, so a negative
	// amount is allowed exactly one more minor unit than a positive one.
	limit := uint64(math.MaxInt64)
	if negative {
		limit++
	}

	var acc uint64
	intDigits := 0
	for ; i < len(s) && s[i] != '.'; i++ {
		d, ok := digit(s[i])
		if !ok {
			// Also where "NaN", "Infinity", "1e5" and "0x10" die: none of them
			// is a run of digits.
			return 0, fail(CodeInvalidAmount, "unexpected %q in %q", string(s[i]), s)
		}
		acc, ok = pushDigit(acc, d, limit)
		if !ok {
			return 0, fail(CodeAmountOverflow, "amount %q does not fit in int64 minor units", s)
		}
		intDigits++
	}
	if intDigits == 0 {
		return 0, fail(CodeInvalidAmount, "no integer part in %q", s)
	}

	fracDigits := 0
	if i < len(s) { // s[i] is '.'
		for i++; i < len(s); i++ {
			d, ok := digit(s[i])
			if !ok {
				return 0, fail(CodeInvalidAmount, "unexpected %q in %q", string(s[i]), s)
			}
			if fracDigits == Scale {
				return 0, fail(CodeInvalidAmount, "scale of %q exceeds %d places", s, Scale)
			}
			acc, ok = pushDigit(acc, d, limit)
			if !ok {
				return 0, fail(CodeAmountOverflow, "amount %q does not fit in int64 minor units", s)
			}
			fracDigits++
		}
		if fracDigits == 0 {
			return 0, fail(CodeInvalidAmount, "trailing decimal point in %q", s)
		}
	}

	// Pad "25" and "25.0" up to the fixed scale. The padding multiplies, so it
	// can overflow just like the digits did.
	for ; fracDigits < Scale; fracDigits++ {
		var ok bool
		acc, ok = pushDigit(acc, 0, limit)
		if !ok {
			return 0, fail(CodeAmountOverflow, "amount %q does not fit in int64 minor units", s)
		}
	}

	if !negative {
		return int64(acc), nil
	}
	if acc == uint64(math.MaxInt64)+1 {
		return math.MinInt64, nil
	}
	return -int64(acc), nil
}

func digit(b byte) (uint64, bool) {
	if b < '0' || b > '9' {
		return 0, false
	}
	return uint64(b - '0'), true
}

// pushDigit computes acc*10+d, reporting failure instead of wrapping around.
// Go's integer overflow is silent, so every multiply on this path is checked
// before it happens rather than inspected afterwards.
func pushDigit(acc, d, limit uint64) (uint64, bool) {
	if acc > (limit-d)/10 {
		return 0, false
	}
	return acc*10 + d, true
}

// Currency returns the currency of the amount.
func (m Money) Currency() Currency { return m.currency }

// Minor returns the amount in minor units. It is what persistence stores, in a
// BIGINT column alongside the currency.
func (m Money) Minor() int64 { return m.minor }

// IsZero reports whether the amount is zero. It says nothing about whether the
// currency was set; see [Money.IsInitialised].
func (m Money) IsZero() bool { return m.minor == 0 }

// IsPositive reports whether the amount is strictly above zero.
func (m Money) IsPositive() bool { return m.minor > 0 }

// IsNegative reports whether the amount is strictly below zero.
func (m Money) IsNegative() bool { return m.minor < 0 }

// IsInitialised reports whether the value came from a constructor. The zero
// Money carries no currency, and every operation rejects it.
func (m Money) IsInitialised() bool { return !m.currency.IsZero() }

// String renders the canonical form: a minus when negative, at least one
// integer digit, a point, and exactly [Scale] decimal places.
//
// This is the normalised spelling. Whatever the input looked like, this is what
// leaves the system and what the idempotency hash sees.
func (m Money) String() string {
	// -(math.MinInt64) overflows, so the magnitude is taken in uint64.
	var magnitude uint64
	sign := ""
	if m.minor < 0 {
		sign = "-"
		magnitude = uint64(-(m.minor + 1)) + 1
	} else {
		magnitude = uint64(m.minor)
	}

	units := magnitude / scaleFactor
	fraction := magnitude % scaleFactor

	out := make([]byte, 0, 24)
	out = append(out, sign...)
	out = strconv.AppendUint(out, units, 10)
	out = append(out, '.')
	if fraction < 10 {
		out = append(out, '0')
	}
	out = strconv.AppendUint(out, fraction, 10)
	return string(out)
}

// Add returns the sum. Currencies must match, and the result must fit.
func (m Money) Add(other Money) (Money, error) {
	if err := m.requireComparable(other); err != nil {
		return Money{}, err
	}
	sum := m.minor + other.minor
	if (other.minor > 0 && sum < m.minor) || (other.minor < 0 && sum > m.minor) {
		return Money{}, fail(CodeAmountOverflow, "%s + %s overflows", m, other)
	}
	return Money{minor: sum, currency: m.currency}, nil
}

// Sub returns the difference, which may be negative.
func (m Money) Sub(other Money) (Money, error) {
	if err := m.requireComparable(other); err != nil {
		return Money{}, err
	}
	diff := m.minor - other.minor
	if (other.minor < 0 && diff < m.minor) || (other.minor > 0 && diff > m.minor) {
		return Money{}, fail(CodeAmountOverflow, "%s - %s overflows", m, other)
	}
	return Money{minor: diff, currency: m.currency}, nil
}

// Neg returns the amount with the opposite sign.
//
// math.MinInt64 has no positive counterpart in int64, so it is the one input
// that fails. It is unreachable with real balances and is handled anyway,
// because "unreachable" is not a guarantee.
func (m Money) Neg() (Money, error) {
	if !m.IsInitialised() {
		return Money{}, fail(CodeInvalidCurrency, "amount is not initialised")
	}
	if m.minor == math.MinInt64 {
		return Money{}, fail(CodeAmountOverflow, "negating %s overflows", m)
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

// Cmp returns -1, 0 or 1. Comparing across currencies is an error, never a
// silent ordering.
func (m Money) Cmp(other Money) (int, error) {
	if err := m.requireComparable(other); err != nil {
		return 0, err
	}
	switch {
	case m.minor < other.minor:
		return -1, nil
	case m.minor > other.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) requireComparable(other Money) error {
	if !m.IsInitialised() || !other.IsInitialised() {
		return fail(CodeInvalidCurrency, "amount is not initialised")
	}
	if m.currency != other.currency {
		return fail(CodeCurrencyMismatch, "%s and %s", m.currency, other.currency)
	}
	return nil
}

// moneyJSON is the wire shape: {"amount":"25.00","currency":"BRL"}.
type moneyJSON struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// MarshalJSON writes the canonical decimal string, never a JSON number. A
// number would invite the decoder on the other side to parse it as a float,
// which is the exact failure this type exists to prevent.
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.IsInitialised() {
		return nil, fail(CodeInvalidCurrency, "amount is not initialised")
	}
	return json.Marshal(moneyJSON{Amount: m.String(), Currency: m.currency.code})
}

// UnmarshalJSON decodes the wire shape.
//
// It uses [ParseMoney], so a negative amount decodes fine: the reconciliation
// difference is a Money and is legitimately negative. Refusing negative amounts
// is a rule about financial *input*, and it belongs to the use case that
// accepts the operation — not to the decoder.
func (m *Money) UnmarshalJSON(data []byte) error {
	var raw moneyJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return fail(CodeInvalidAmount, "malformed money object: %v", err)
	}
	currency, err := ParseCurrency(raw.Currency)
	if err != nil {
		return err
	}
	parsed, err := ParseMoney(raw.Amount, currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}
