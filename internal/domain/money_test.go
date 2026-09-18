package domain

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

// brl is the currency used across the tests. It goes through the real parser so
// no test can fabricate a Currency the production code could not build.
func brl(t *testing.T) Currency {
	t.Helper()
	c, err := ParseCurrency("BRL")
	if err != nil {
		t.Fatalf("ParseCurrency(BRL): %v", err)
	}
	return c
}

func usd(t *testing.T) Currency {
	t.Helper()
	c, err := ParseCurrency("USD")
	if err != nil {
		t.Fatalf("ParseCurrency(USD): %v", err)
	}
	return c
}

func mustParse(t *testing.T, amount string, c Currency) Money {
	t.Helper()
	m, err := ParseMoney(amount, c)
	if err != nil {
		t.Fatalf("ParseMoney(%q): %v", amount, err)
	}
	return m
}

func TestParseCurrency(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string // empty means the input must be rejected
	}{
		{"three uppercase letters", "BRL", "BRL"},
		{"another code", "USD", "USD"},
		{"lowercase is not upcased silently", "brl", ""},
		{"mixed case", "Brl", ""},
		{"too short", "BR", ""},
		{"too long", "BRLX", ""},
		{"empty", "", ""},
		{"digits", "123", ""},
		{"padded", " BR", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCurrency(tc.input)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("ParseCurrency(%q) = %v, want rejection", tc.input, got)
				}
				if !errors.Is(err, ErrInvalidCurrency) {
					t.Fatalf("error = %v, want ErrInvalidCurrency", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCurrency(%q): %v", tc.input, err)
			}
			if got.String() != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCurrencyZeroValueIsDetectable(t *testing.T) {
	t.Parallel()
	var c Currency
	if !c.IsZero() {
		t.Fatal("zero Currency should report IsZero")
	}
	if _, err := ZeroMoney(c); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("ZeroMoney(zero currency) = %v, want ErrInvalidCurrency", err)
	}
}

func TestParseMoneyAccepts(t *testing.T) {
	t.Parallel()
	c := brl(t)
	tests := []struct {
		name      string
		input     string
		wantMinor int64
		wantStr   string
	}{
		{"two decimal places", "25.00", 2500, "25.00"},
		{"cents", "0.50", 50, "0.50"},
		{"single cent", "0.01", 1, "0.01"},
		{"zero", "0.00", 0, "0.00"},
		{"no decimal point is padded", "25", 2500, "25.00"},
		{"one decimal place is padded", "25.0", 2500, "25.00"},
		{"leading zeros are equivalent", "025.00", 2500, "25.00"},
		{"many leading zeros", "0000.50", 50, "0.50"},
		{"thousands", "1000.00", 100000, "1000.00"},
		{"negative is allowed for internal differences", "-0.50", -50, "-0.50"},
		{"negative whole", "-25", -2500, "-25.00"},
		{"negative zero normalises", "-0.00", 0, "0.00"},
		{"largest positive", "92233720368547758.07", math.MaxInt64, "92233720368547758.07"},
		{"smallest negative", "-92233720368547758.08", math.MinInt64, "-92233720368547758.08"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseMoney(tc.input, c)
			if err != nil {
				t.Fatalf("ParseMoney(%q): %v", tc.input, err)
			}
			if got.Minor() != tc.wantMinor {
				t.Errorf("minor = %d, want %d", got.Minor(), tc.wantMinor)
			}
			if got.String() != tc.wantStr {
				t.Errorf("String() = %q, want %q", got, tc.wantStr)
			}
			if got.Currency() != c {
				t.Errorf("currency = %v, want %v", got.Currency(), c)
			}
		})
	}
}

func TestParseMoneyRejects(t *testing.T) {
	t.Parallel()
	c := brl(t)
	tests := []struct {
		name     string
		input    string
		wantCode Code
	}{
		{"empty", "", CodeInvalidAmount},
		{"NaN", "NaN", CodeInvalidAmount},
		{"lowercase nan", "nan", CodeInvalidAmount},
		{"Infinity", "Infinity", CodeInvalidAmount},
		{"Inf", "Inf", CodeInvalidAmount},
		{"negative infinity", "-Inf", CodeInvalidAmount},
		{"scientific notation", "1e5", CodeInvalidAmount},
		{"scientific notation uppercase", "1E5", CodeInvalidAmount},
		{"scientific with decimals", "2.5e3", CodeInvalidAmount},
		{"hexadecimal", "0x10", CodeInvalidAmount},
		{"explicit plus", "+25.00", CodeInvalidAmount},
		{"scale of three", "25.000", CodeInvalidAmount},
		{"scale of four", "0.1234", CodeInvalidAmount},
		{"trailing point", "25.", CodeInvalidAmount},
		{"leading point", ".50", CodeInvalidAmount},
		{"negative leading point", "-.50", CodeInvalidAmount},
		{"comma separator", "25,00", CodeInvalidAmount},
		{"thousands separator", "1,000.00", CodeInvalidAmount},
		{"leading space", " 25.00", CodeInvalidAmount},
		{"trailing space", "25.00 ", CodeInvalidAmount},
		{"underscore", "1_000.00", CodeInvalidAmount},
		{"lone minus", "-", CodeInvalidAmount},
		{"double minus", "--5.00", CodeInvalidAmount},
		{"two points", "25.0.0", CodeInvalidAmount},
		{"currency inside amount", "BRL 25.00", CodeInvalidAmount},
		{"one past the positive limit", "92233720368547758.08", CodeAmountOverflow},
		{"one past the negative limit", "-92233720368547758.09", CodeAmountOverflow},
		{"far past the limit", "99999999999999999999.00", CodeAmountOverflow},
		{"overflow in the integer digits", "92233720368547758071", CodeAmountOverflow},
		// Fits while the digits are read, then overflows when padded to scale:
		// 922337203685477580 * 100 is past the top. The padding multiplies, so it
		// has to be checked exactly like the digits are.
		{"overflow while padding to scale", "922337203685477580", CodeAmountOverflow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseMoney(tc.input, c)
			if err == nil {
				t.Fatalf("ParseMoney(%q) = %v, want rejection", tc.input, got)
			}
			var de *Error
			if !errors.As(err, &de) {
				t.Fatalf("error %v is not a *domain.Error", err)
			}
			if de.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q (%v)", de.Code, tc.wantCode, err)
			}
		})
	}
}

func TestParseMoneyNeverRoundsSilently(t *testing.T) {
	t.Parallel()
	c := brl(t)
	// The whole point of rejecting excess scale: "0.005" must not become 0 or 1
	// cent. An amount the system cannot represent exactly is an error, and the
	// caller has to say what it meant.
	for _, input := range []string{"0.005", "0.004", "25.999", "1.0000000001"} {
		if got, err := ParseMoney(input, c); err == nil {
			t.Errorf("ParseMoney(%q) = %v, want rejection instead of rounding", input, got)
		}
	}
}

func TestParseExternalMoneyRejectsNegative(t *testing.T) {
	t.Parallel()
	c := brl(t)

	if _, err := ParseExternalMoney("25.00", c); err != nil {
		t.Fatalf("positive external amount: %v", err)
	}
	if _, err := ParseExternalMoney("0.00", c); err != nil {
		t.Fatalf("zero external amount: %v", err)
	}

	got, err := ParseExternalMoney("-0.01", c)
	if err == nil {
		t.Fatalf("ParseExternalMoney(-0.01) = %v, want rejection", got)
	}
	if !errors.Is(err, ErrNegativeAmount) {
		t.Fatalf("error = %v, want ErrNegativeAmount", err)
	}
}

func TestParseMoneyRequiresInitialisedCurrency(t *testing.T) {
	t.Parallel()
	var none Currency
	if _, err := ParseMoney("25.00", none); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("error = %v, want ErrInvalidCurrency", err)
	}
}

func TestZeroMoney(t *testing.T) {
	t.Parallel()
	c := brl(t)
	m, err := ZeroMoney(c)
	if err != nil {
		t.Fatalf("ZeroMoney: %v", err)
	}
	if !m.IsZero() {
		t.Error("ZeroMoney is not zero")
	}
	if m.String() != "0.00" {
		t.Errorf("String() = %q, want %q", m, "0.00")
	}
	if m.Currency() != c {
		t.Error("ZeroMoney lost its currency")
	}
}

func TestNewMoneyFromMinorAcceptsStoredNegatives(t *testing.T) {
	t.Parallel()
	c := brl(t)
	// Rehydration takes what the database holds, including a stored difference.
	m, err := NewMoneyFromMinor(-2500, c)
	if err != nil {
		t.Fatalf("NewMoneyFromMinor: %v", err)
	}
	if m.String() != "-25.00" {
		t.Errorf("String() = %q, want %q", m, "-25.00")
	}
}

func TestAdd(t *testing.T) {
	t.Parallel()
	c := brl(t)
	tests := []struct {
		name string
		a, b string
		want string
	}{
		{"whole amounts", "25.00", "75.00", "100.00"},
		{"carrying cents", "0.99", "0.01", "1.00"},
		{"adding zero", "25.00", "0.00", "25.00"},
		{"adding a negative", "25.00", "-30.00", "-5.00"},
		{"two negatives", "-1.50", "-2.50", "-4.00"},
		// The canonical float trap: 0.1 + 0.2 != 0.3 in binary.
		{"the tenth that floats get wrong", "0.10", "0.20", "0.30"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := mustParse(t, tc.a, c).Add(mustParse(t, tc.b, c))
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
			if got.String() != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSub(t *testing.T) {
	t.Parallel()
	c := brl(t)
	tests := []struct {
		name string
		a, b string
		want string
	}{
		{"simple", "100.00", "25.00", "75.00"},
		{"to zero", "25.00", "25.00", "0.00"},
		{"below zero is allowed for differences", "25.00", "30.00", "-5.00"},
		{"borrowing cents", "1.00", "0.01", "0.99"},
		{"subtracting a negative", "25.00", "-5.00", "30.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := mustParse(t, tc.a, c).Sub(mustParse(t, tc.b, c))
			if err != nil {
				t.Fatalf("Sub: %v", err)
			}
			if got.String() != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNeg(t *testing.T) {
	t.Parallel()
	c := brl(t)
	for _, tc := range []struct{ in, want string }{
		{"25.00", "-25.00"},
		{"-25.00", "25.00"},
		{"0.00", "0.00"},
		{"0.01", "-0.01"},
	} {
		got, err := mustParse(t, tc.in, c).Neg()
		if err != nil {
			t.Fatalf("Neg(%s): %v", tc.in, err)
		}
		if got.String() != tc.want {
			t.Errorf("Neg(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestArithmeticOverflow(t *testing.T) {
	t.Parallel()
	c := brl(t)
	max, err := NewMoneyFromMinor(math.MaxInt64, c)
	if err != nil {
		t.Fatal(err)
	}
	min, err := NewMoneyFromMinor(math.MinInt64, c)
	if err != nil {
		t.Fatal(err)
	}
	cent := mustParse(t, "0.01", c)

	t.Run("add past the top", func(t *testing.T) {
		t.Parallel()
		if got, err := max.Add(cent); !errors.Is(err, ErrAmountOverflow) {
			t.Fatalf("got (%v, %v), want ErrAmountOverflow", got, err)
		}
	})
	t.Run("subtract past the bottom", func(t *testing.T) {
		t.Parallel()
		if got, err := min.Sub(cent); !errors.Is(err, ErrAmountOverflow) {
			t.Fatalf("got (%v, %v), want ErrAmountOverflow", got, err)
		}
	})
	t.Run("add two negatives past the bottom", func(t *testing.T) {
		t.Parallel()
		negCent, err := cent.Neg()
		if err != nil {
			t.Fatal(err)
		}
		if got, err := min.Add(negCent); !errors.Is(err, ErrAmountOverflow) {
			t.Fatalf("got (%v, %v), want ErrAmountOverflow", got, err)
		}
	})
	t.Run("negate the asymmetric bound", func(t *testing.T) {
		t.Parallel()
		// math.MinInt64 has no positive counterpart; this is the only input
		// where the int64 range is not symmetric.
		if got, err := min.Neg(); !errors.Is(err, ErrAmountOverflow) {
			t.Fatalf("got (%v, %v), want ErrAmountOverflow", got, err)
		}
	})
	t.Run("results at the bound are still exact", func(t *testing.T) {
		t.Parallel()
		got, err := max.Sub(cent)
		if err != nil {
			t.Fatalf("Sub: %v", err)
		}
		if got.Minor() != math.MaxInt64-1 {
			t.Fatalf("minor = %d, want %d", got.Minor(), math.MaxInt64-1)
		}
	})
}

func TestCurrencyMismatch(t *testing.T) {
	t.Parallel()
	real := mustParse(t, "25.00", brl(t))
	dollar := mustParse(t, "25.00", usd(t))

	t.Run("add", func(t *testing.T) {
		t.Parallel()
		if _, err := real.Add(dollar); !errors.Is(err, ErrCurrencyMismatch) {
			t.Fatalf("error = %v, want ErrCurrencyMismatch", err)
		}
	})
	t.Run("sub", func(t *testing.T) {
		t.Parallel()
		if _, err := real.Sub(dollar); !errors.Is(err, ErrCurrencyMismatch) {
			t.Fatalf("error = %v, want ErrCurrencyMismatch", err)
		}
	})
	t.Run("cmp", func(t *testing.T) {
		t.Parallel()
		// Ordering across currencies must be an error, never a silent answer:
		// "25.00 BRL < 25.00 USD" has no meaning the domain can defend.
		if _, err := real.Cmp(dollar); !errors.Is(err, ErrCurrencyMismatch) {
			t.Fatalf("error = %v, want ErrCurrencyMismatch", err)
		}
	})
	t.Run("equal amounts in different currencies are not equal", func(t *testing.T) {
		t.Parallel()
		if real == dollar {
			t.Fatal("25.00 BRL == 25.00 USD")
		}
	})
}

func TestUninitialisedMoneyIsRejected(t *testing.T) {
	t.Parallel()
	c := brl(t)
	var none Money
	some := mustParse(t, "25.00", c)

	if none.IsInitialised() {
		t.Error("zero Money should not report as initialised")
	}
	for name, err := range map[string]error{
		"add to":        mustErr(some.Add(none)),
		"add from":      mustErr(none.Add(some)),
		"sub":           mustErr(some.Sub(none)),
		"neg":           mustErr(none.Neg()),
		"marshal":       mustMarshalErr(none),
		"cmp":           mustCmpErr(some.Cmp(none)),
		"zero currency": mustErr(ZeroMoney(Currency{})),
	} {
		if !errors.Is(err, ErrInvalidCurrency) {
			t.Errorf("%s: error = %v, want ErrInvalidCurrency", name, err)
		}
	}
}

func mustErr(_ Money, err error) error  { return err }
func mustCmpErr(_ int, err error) error { return err }
func mustMarshalErr(m Money) error      { _, err := m.MarshalJSON(); return err }

func TestCmp(t *testing.T) {
	t.Parallel()
	c := brl(t)
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"less", "25.00", "75.00", -1},
		{"greater", "75.00", "25.00", 1},
		{"equal", "25.00", "25.00", 0},
		{"equal across spellings", "25", "25.00", 0},
		{"one cent apart", "0.01", "0.02", -1},
		{"negative below zero", "-0.01", "0.00", -1},
		{"negatives ordered", "-5.00", "-1.00", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := mustParse(t, tc.a, c).Cmp(mustParse(t, tc.b, c))
			if err != nil {
				t.Fatalf("Cmp: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Cmp(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestEqualityUsesTheLanguageOperator(t *testing.T) {
	t.Parallel()
	c := brl(t)
	// Money is comparable, which is half the reason it is an int64 and not a
	// pointer type: == cannot silently compare identities instead of values.
	if mustParse(t, "25", c) != mustParse(t, "25.00", c) {
		t.Error(`"25" and "25.00" should be the same value`)
	}
	if mustParse(t, "25.00", c) == mustParse(t, "25.01", c) {
		t.Error("different amounts compared equal")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	t.Parallel()
	c := brl(t)
	for _, input := range []string{"25.00", "0.00", "0.01", "1000.00", "-5.00"} {
		original := mustParse(t, input, c)

		encoded, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("Marshal(%s): %v", input, err)
		}

		var decoded Money
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("Unmarshal(%s): %v", encoded, err)
		}
		if decoded != original {
			t.Errorf("round trip of %s gave %s", original, decoded)
		}
	}
}

func TestMarshalJSONShape(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(mustParse(t, "25", brl(t)))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// The amount must be a JSON string. A number would invite the decoder on
	// the other side to read it as a float, which is the failure this type
	// exists to prevent.
	want := `{"amount":"25.00","currency":"BRL"}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestUnmarshalJSONRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"amount as a number", `{"amount":25.00,"currency":"BRL"}`},
		{"amount as a float literal", `{"amount":0.1,"currency":"BRL"}`},
		{"unknown currency", `{"amount":"25.00","currency":"XX"}`},
		{"lowercase currency", `{"amount":"25.00","currency":"brl"}`},
		{"missing currency", `{"amount":"25.00"}`},
		{"missing amount", `{"currency":"BRL"}`},
		{"excess scale", `{"amount":"25.000","currency":"BRL"}`},
		{"scientific notation", `{"amount":"2.5e1","currency":"BRL"}`},
		{"not an object", `"25.00"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var m Money
			if err := json.Unmarshal([]byte(tc.input), &m); err == nil {
				t.Fatalf("Unmarshal(%s) = %v, want rejection", tc.input, m)
			}
		})
	}
}

func TestErrorsCarryAStableCode(t *testing.T) {
	t.Parallel()
	_, err := ParseMoney("nope", brl(t))

	var de *Error
	if !errors.As(err, &de) {
		t.Fatalf("errors.As failed for %v", err)
	}
	if de.Code != CodeInvalidAmount {
		t.Errorf("code = %q, want %q", de.Code, CodeInvalidAmount)
	}
	if de.Detail == "" {
		t.Error("detail is empty; the message should say which input failed")
	}
	// The sentinel matches on code alone, so a detailed error still answers to
	// errors.Is — that is what lets a caller branch without string matching.
	if !errors.Is(err, ErrInvalidAmount) {
		t.Error("errors.Is failed against the sentinel")
	}
	if errors.Is(err, ErrCurrencyMismatch) {
		t.Error("errors.Is matched the wrong sentinel")
	}
}
