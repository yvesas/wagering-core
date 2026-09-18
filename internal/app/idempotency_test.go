package app

import "testing"

func baseCommand() SubmitCommand {
	return SubmitCommand{
		IdempotencyKey: "provider-a:transaction-123",
		ProviderID:     "provider-a",
		ExternalID:     "transaction-123",
		PlayerID:       "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		WalletID:       "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID:        "round-987",
		GameID:         "fortune-chimp",
		Kind:           "BET",
		Amount:         "25.00",
		Currency:       "BRL",
	}
}

func hashOf(t *testing.T, cmd SubmitCommand) string {
	t.Helper()
	h, err := cmd.PayloadHash()
	if err != nil {
		t.Fatalf("PayloadHash: %v", err)
	}
	return h.String()
}

// TestPayloadHashIsFrozen pins the algorithm to exact values.
//
// This test exists to fail loudly. A change to the field set, the ordering, the
// separators or the normalisation would otherwise pass every other test here
// and silently invalidate every record already stored: the same operation would
// hash differently than it did last week, the replay check would miss, and the
// symptom in production is a duplicated financial movement rather than a red
// test.
//
// If this fails and the change was intended, the stored hashes have to be
// migrated -- which is the conversation the failure is meant to start.
func TestPayloadHashIsFrozen(t *testing.T) {
	t.Parallel()

	const (
		wantBase     = "sha256:7fba4b18304a221f9912091a69f6cf74be638eb3a003ff78d7ec00eac6dcbb68"
		wantReversal = "sha256:fc59e8cda346a52076701720d8b8d5eaaa6e94fbc335345ca7a69933c448a762"
	)

	if got := hashOf(t, baseCommand()); got != wantBase {
		t.Errorf("the hash of the reference payload changed\n got %s\nwant %s", got, wantBase)
	}

	reversal := baseCommand()
	reversal.Kind = "REFUND"
	reversal.ReferenceExternalID = "transaction-1"
	if got := hashOf(t, reversal); got != wantReversal {
		t.Errorf("the hash of the reference reversal changed\n got %s\nwant %s", got, wantReversal)
	}
}

func TestPayloadHashIgnoresTheIdempotencyKey(t *testing.T) {
	t.Parallel()
	// The key is the label of this content, not part of it. Including it would
	// make every resend match by construction, and the hash would stop
	// detecting the one thing it exists for.
	other := baseCommand()
	other.IdempotencyKey = "a completely different key"

	if hashOf(t, baseCommand()) != hashOf(t, other) {
		t.Fatal("the idempotency key reached the hash")
	}
}

func TestPayloadHashNormalisesTheAmount(t *testing.T) {
	t.Parallel()
	// The same money spelled three ways is the same operation.
	want := hashOf(t, baseCommand())
	for _, spelling := range []string{"25", "25.0", "025.00", "25.00"} {
		cmd := baseCommand()
		cmd.Amount = spelling
		if got := hashOf(t, cmd); got != want {
			t.Errorf("%q hashed differently from 25.00", spelling)
		}
	}
}

func TestPayloadHashDoesNotNormaliseAnythingElse(t *testing.T) {
	t.Parallel()
	// Whitespace around an identifier is a difference in content, not noise,
	// and a lowercase currency is rejected rather than upcased. Anything the
	// hash quietly rewrote would be a second opinion about what the caller
	// meant.
	base := hashOf(t, baseCommand())

	for name, mutate := range map[string]func(*SubmitCommand){
		"padded external id": func(c *SubmitCommand) { c.ExternalID = " transaction-123" },
		"lowercase currency": func(c *SubmitCommand) { c.Currency = "brl" },
		"padded round":       func(c *SubmitCommand) { c.RoundID = "round-987 " },
	} {
		cmd := baseCommand()
		mutate(&cmd)
		if hashOf(t, cmd) == base {
			t.Errorf("%s hashed the same as the untouched payload", name)
		}
	}
}

func TestPayloadHashChangesWithEveryBusinessField(t *testing.T) {
	t.Parallel()
	base := hashOf(t, baseCommand())

	mutations := map[string]func(*SubmitCommand){
		"provider":   func(c *SubmitCommand) { c.ProviderID = "provider-b" },
		"externalId": func(c *SubmitCommand) { c.ExternalID = "transaction-124" },
		"player":     func(c *SubmitCommand) { c.PlayerID = "another-player" },
		"wallet":     func(c *SubmitCommand) { c.WalletID = "another-wallet" },
		"round":      func(c *SubmitCommand) { c.RoundID = "round-988" },
		"game":       func(c *SubmitCommand) { c.GameID = "another-game" },
		"kind":       func(c *SubmitCommand) { c.Kind = "WIN" },
		"amount":     func(c *SubmitCommand) { c.Amount = "25.01" },
		"currency":   func(c *SubmitCommand) { c.Currency = "USD" },
		"reference":  func(c *SubmitCommand) { c.ReferenceExternalID = "transaction-1" },
	}
	for name, mutate := range mutations {
		cmd := baseCommand()
		mutate(&cmd)
		if hashOf(t, cmd) == base {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}

func TestPayloadHashIsUnambiguousAcrossFieldBoundaries(t *testing.T) {
	t.Parallel()
	// An external id comes from a provider and may contain any character,
	// separators included. Without the length prefix, moving a character across
	// a field boundary would produce the same canonical string -- and two
	// different operations would share a hash.
	a := baseCommand()
	a.ExternalID = "ab"
	a.RoundID = "c"

	b := baseCommand()
	b.ExternalID = "a"
	b.RoundID = "bc"

	if hashOf(t, a) == hashOf(t, b) {
		t.Fatal("two different payloads collide across a field boundary")
	}

	// And with the separators themselves inside a value.
	c := baseCommand()
	c.ExternalID = "x=3:y;"
	d := baseCommand()
	d.ExternalID = "x=3:y"

	if hashOf(t, c) == hashOf(t, d) {
		t.Fatal("a value containing separators collides with its prefix")
	}
}

func TestAbsentReferenceIsNotAnEmptyOne(t *testing.T) {
	t.Parallel()
	// An absent reference and an empty string are the same absence, and only
	// one of them should reach the canonical form.
	empty := baseCommand()
	empty.ReferenceExternalID = ""

	if hashOf(t, baseCommand()) != hashOf(t, empty) {
		t.Fatal("an empty reference hashed differently from an absent one")
	}
}

func TestPayloadHashIsDeterministic(t *testing.T) {
	t.Parallel()
	// Map iteration in Go is randomised on purpose. Hashing the fields without
	// sorting them would pass a single run and fail in production, which is the
	// worst possible way for this to break.
	want := hashOf(t, baseCommand())
	for i := 0; i < 200; i++ {
		if got := hashOf(t, baseCommand()); got != want {
			t.Fatalf("run %d produced a different hash", i)
		}
	}
}

func TestCanonicaliseShape(t *testing.T) {
	t.Parallel()
	got := canonicalise(map[string]string{"b": "two", "a": "one"})
	// Sorted, and every part carrying its length.
	if want := "1:a=3:one;1:b=3:two;"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
