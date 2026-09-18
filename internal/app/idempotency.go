package app

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/yvesas/wagering-core/internal/domain"
)

// hashPrefix names the algorithm in the stored value, so a future change can be
// told apart from what is already on disk instead of being compared against it.
const hashPrefix = "sha256:"

// SubmitCommand is an operation as it arrived, before anything is parsed.
//
// The fields stay strings because this is also what gets hashed: parsing first
// and hashing the parsed values would make the hash depend on how lenient the
// parser happens to be, and a parser change would silently invalidate every
// stored record.
type SubmitCommand struct {
	IdempotencyKey      string
	ProviderID          string
	ExternalID          string
	PlayerID            string
	WalletID            string
	RoundID             string
	GameID              string
	Kind                string
	Amount              string
	Currency            string
	ReferenceExternalID string
}

// PayloadHash is the fingerprint of the business fields.
//
// What goes in and what stays out is the whole decision; see
// docs/adr/0006-idempotency-hash.md. Two things stay out:
//
//   - The idempotency key. It is the label of this content, not part of it.
//     Including it would make every resend match by construction and the hash
//     would stop detecting the one thing it exists for: the same key carrying
//     different content.
//   - Transport metadata -- headers, message ids, delivery timestamps. A queue
//     redelivery carries a new message id and is the same operation, so
//     including any of it would stop HTTP and the queue from ever agreeing.
//
// The amount is normalised through the domain's canonical form first, so "25",
// "25.0" and "025.00" hash alike: they are the same money. That is the only
// normalisation applied, and it is documented because a hash whose input is
// quietly rewritten is a hash nobody can reproduce.
func (c SubmitCommand) PayloadHash() (domain.PayloadHash, error) {
	amount := c.Amount
	if currency, err := domain.ParseCurrency(c.Currency); err == nil {
		if money, err := domain.ParseMoney(c.Amount, currency); err == nil {
			amount = money.String()
		}
		// A malformed amount is hashed exactly as it arrived. It will be
		// rejected a moment later anyway, and inventing a normalised form for
		// something the domain refuses would be a second opinion about what it
		// meant.
	}

	fields := map[string]string{
		"providerId": c.ProviderID,
		"externalId": c.ExternalID,
		"playerId":   c.PlayerID,
		"walletId":   c.WalletID,
		"roundId":    c.RoundID,
		"gameId":     c.GameID,
		"kind":       c.Kind,
		"amount":     amount,
		"currency":   c.Currency,
	}
	// An absent reference and an empty one are the same absence, and only one
	// of them should reach the hash.
	if c.ReferenceExternalID != "" {
		fields["referenceExternalId"] = c.ReferenceExternalID
	}

	sum := sha256.Sum256([]byte(canonicalise(fields)))
	return domain.ParsePayloadHash(hashPrefix + hex.EncodeToString(sum[:]))
}

// canonicalise renders the fields in a form that cannot be two things at once.
//
// Each key and each value carries its byte length:
//
//	len(key) ":" key "=" len(value) ":" value ";"
//
// Plain "key=value;" concatenation is ambiguous, because an external id comes
// from a provider and may contain any character, separators included: "a=b;c"
// and "a=b" plus "c" would collide. A length prefix removes that without
// escaping anything, which also means no escaping rule to get subtly wrong.
//
// This is written by hand rather than delegated to encoding/json. Sorted map
// keys are documented behaviour there, but this hash has to stay byte-identical
// for as long as the records exist, and twenty lines that depend only on us are
// cheaper than a dependency on someone else's promise.
func canonicalise(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, key := range keys {
		value := fields[key]
		b.WriteString(strconv.Itoa(len(key)))
		b.WriteByte(':')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(len(value)))
		b.WriteByte(':')
		b.WriteString(value)
		b.WriteByte(';')
	}
	return b.String()
}
