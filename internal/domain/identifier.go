package domain

// maxIdentifierLength bounds every identifier. External ones come from a
// provider, so they are untrusted input: without a bound, a multi-megabyte
// "transaction id" would travel all the way to a database column.
const maxIdentifierLength = 128

// identifier is the shared behaviour of the opaque IDs below. The string is
// unexported, so the only way to obtain a non-zero value is through a parser in
// this package, and the zero value stays detectable as "not set".
type identifier struct{ value string }

func (i identifier) String() string { return i.value }

// IsZero reports whether the identifier was never set.
func (i identifier) IsZero() bool { return i.value == "" }

// The identifiers are distinct types on purpose: passing a PlayerID where a
// WalletID belongs is a compile error, not a runtime mystery. All of them are
// comparable, so == works.
//
// They are opaque strings rather than UUIDs because the domain does not get to
// decide how an identity is generated — internal ones are UUIDv7 minted by an
// adapter, and external ones are whatever the provider sent ("transaction-123").
// Constraining the shape here would reject legitimate provider input.
type (
	WalletID      struct{ identifier }
	PlayerID      struct{ identifier }
	TransactionID struct{ identifier }
	LedgerEntryID struct{ identifier }
	ProviderID    struct{ identifier }
	RoundID       struct{ identifier }
	GameID        struct{ identifier }

	// ExternalTransactionID is the provider's own id for an operation. Together
	// with ProviderID it identifies the business operation, which is what a
	// reversal resolves against.
	ExternalTransactionID struct{ identifier }

	// IdempotencyKey is transport-level: it says "this is the same request as
	// before". It is deliberately not the same thing as ExternalTransactionID,
	// and the server never silently substitutes one for the other.
	IdempotencyKey struct{ identifier }

	// PayloadHash fingerprints the business fields of a request, so a genuine
	// retry can be told apart from a conflicting reuse of the same key. The
	// algorithm is decided when idempotency is built; here it is opaque.
	PayloadHash struct{ identifier }
)

func parseIdentifier(kind, s string) (identifier, error) {
	if s == "" {
		return identifier{}, fail(CodeInvalidIdentifier, "%s id is empty", kind)
	}
	if len(s) > maxIdentifierLength {
		return identifier{}, fail(CodeInvalidIdentifier,
			"%s id is %d bytes, over the %d limit", kind, len(s), maxIdentifierLength)
	}
	return identifier{value: s}, nil
}

func ParseWalletID(s string) (WalletID, error) {
	v, err := parseIdentifier("wallet", s)
	return WalletID{v}, err
}

func ParsePlayerID(s string) (PlayerID, error) {
	v, err := parseIdentifier("player", s)
	return PlayerID{v}, err
}

func ParseTransactionID(s string) (TransactionID, error) {
	v, err := parseIdentifier("transaction", s)
	return TransactionID{v}, err
}

func ParseLedgerEntryID(s string) (LedgerEntryID, error) {
	v, err := parseIdentifier("ledger entry", s)
	return LedgerEntryID{v}, err
}

func ParseProviderID(s string) (ProviderID, error) {
	v, err := parseIdentifier("provider", s)
	return ProviderID{v}, err
}

func ParseRoundID(s string) (RoundID, error) {
	v, err := parseIdentifier("round", s)
	return RoundID{v}, err
}

func ParseGameID(s string) (GameID, error) {
	v, err := parseIdentifier("game", s)
	return GameID{v}, err
}

func ParseExternalTransactionID(s string) (ExternalTransactionID, error) {
	v, err := parseIdentifier("external transaction", s)
	return ExternalTransactionID{v}, err
}

func ParseIdempotencyKey(s string) (IdempotencyKey, error) {
	v, err := parseIdentifier("idempotency key", s)
	return IdempotencyKey{v}, err
}

func ParsePayloadHash(s string) (PayloadHash, error) {
	v, err := parseIdentifier("payload hash", s)
	return PayloadHash{v}, err
}
