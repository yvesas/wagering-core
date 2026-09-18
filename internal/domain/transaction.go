package domain

import "time"

// Kind is what an operation does to a wallet.
type Kind string

const (
	// KindOpening is the internal credit that opens a wallet. It never arrives
	// from outside; see [Origin].
	KindOpening Kind = "OPENING"

	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

func (k Kind) Valid() bool {
	switch k {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	default:
		return false
	}
}

// IsReversal reports whether the operation exists to undo another one, and so
// has to resolve a reference before it can be applied.
func (k Kind) IsReversal() bool { return k == KindRefund || k == KindRollback }

// MovesMoney reports whether a processed operation of this kind changes the
// balance. Only LOSS does not: it records how a round ended and produces no
// ledger entry and no version bump.
func (k Kind) MovesMoney() bool { return k != KindLoss }

// validateAmount applies the amount policy of the kind.
//
// LOSS is the odd one and the reason this is a method rather than a blanket
// "must be positive": it requires exactly zero, because a loss that moved money
// would be a bet that was already debited.
func (k Kind) validateAmount(m Money) error {
	if !m.IsInitialised() {
		return fail(CodeInvalidCurrency, "amount is not initialised")
	}
	if k == KindLoss {
		if !m.IsZero() {
			return fail(CodeInvalidAmountForKind, "%s requires 0.00, got %s", k, m)
		}
		return nil
	}
	if !m.IsPositive() {
		return fail(CodeInvalidAmountForKind, "%s requires a positive amount, got %s", k, m)
	}
	return nil
}

// Origin says where an operation came from. It is what keeps provider metadata
// off an internal opening and demands it on everything else.
type Origin string

const (
	OriginInternal Origin = "INTERNAL"
	OriginExternal Origin = "EXTERNAL"
)

// Status is where an operation is in its life.
type Status string

const (
	// StatusPending means the operation was accepted and recorded but not yet
	// applied. Every durable PENDING has to be resumable by another instance.
	StatusPending Status = "PENDING"

	// StatusPendingReference means it is waiting on a reversal target that has
	// not arrived. A worker retries with backoff until a deadline.
	StatusPendingReference Status = "PENDING_REFERENCE"

	StatusProcessed Status = "PROCESSED"
	StatusRejected  Status = "REJECTED"
	StatusFailed    Status = "FAILED"
)

func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether the status admits no further transition. A replay
// of a terminal operation reads its stored result instead of reapplying it.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusProcessed, StatusRejected, StatusFailed:
		return true
	default:
		return false
	}
}

// WagerTransaction is one operation against a wallet: what was asked, who asked,
// where it stands and how it ended.
//
// One type covers both origins rather than two. The transitions are the risky
// part, and two types would mean two copies of them, drifting apart on the first
// fix applied to only one. The safety that separate types would have bought is
// kept anyway: the fields are unexported and the two constructors take different
// parameter structs, so an opening has nowhere to put a provider id.
type WagerTransaction struct {
	id       TransactionID
	origin   Origin
	kind     Kind
	status   Status
	walletID WalletID
	playerID PlayerID
	money    Money

	// External metadata. All zero on an internal opening.
	providerID     ProviderID
	externalID     ExternalTransactionID
	idempotencyKey IdempotencyKey
	payloadHash    PayloadHash
	roundID        RoundID
	gameID         GameID

	// referenceExternalID is what a reversal names; resolvedReferenceID is the
	// internal transaction it turned out to be, once found.
	referenceExternalID ExternalTransactionID
	resolvedReferenceID TransactionID

	// failureCode explains a rejection or a failure. It is empty otherwise, and
	// it is the stable code a provider branches on.
	failureCode Code

	// The wait for a reference. These live on the row rather than in a
	// worker's memory, so a process that restarts finds the pending work where
	// it left it.
	referenceAttempts      int
	referenceNextAttemptAt time.Time
	referenceDeadlineAt    time.Time

	// balanceAfter is the balance observed when the operation was processed.
	// A replay returns this, not the balance the wallet has now — the wallet
	// may well have moved on since.
	balanceAfter Money

	createdAt time.Time
	updatedAt time.Time
}

// OpeningTransactionParams carries the internal opening credit. There is no
// field here for a provider, an external id, a key, a hash, a round, a game or
// a reference, because none of them applies to an operation the system raised
// for itself.
type OpeningTransactionParams struct {
	ID        TransactionID
	WalletID  WalletID
	PlayerID  PlayerID
	Money     Money
	CreatedAt time.Time
}

// NewOpeningTransaction builds the internal credit that opens a wallet.
//
// It starts PROCESSED, not PENDING: the opening is committed together with the
// wallet and its ledger entry, so there is no window in which it is accepted
// but not yet applied.
func NewOpeningTransaction(p OpeningTransactionParams) (WagerTransaction, error) {
	switch {
	case p.ID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "transaction id is empty")
	case p.WalletID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "wallet id is empty")
	case p.PlayerID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "player id is empty")
	case p.CreatedAt.IsZero():
		return WagerTransaction{}, fail(CodeInvalidTimestamp, "created at is not set")
	}
	if err := KindOpening.validateAmount(p.Money); err != nil {
		return WagerTransaction{}, err
	}

	at := p.CreatedAt.UTC()
	return WagerTransaction{
		id:           p.ID,
		origin:       OriginInternal,
		kind:         KindOpening,
		status:       StatusProcessed,
		walletID:     p.WalletID,
		playerID:     p.PlayerID,
		money:        p.Money,
		balanceAfter: p.Money,
		createdAt:    at,
		updatedAt:    at,
	}, nil
}

// ExternalTransactionParams carries an operation submitted by a provider.
type ExternalTransactionParams struct {
	ID             TransactionID
	Kind           Kind
	ProviderID     ProviderID
	ExternalID     ExternalTransactionID
	IdempotencyKey IdempotencyKey
	PayloadHash    PayloadHash
	WalletID       WalletID
	PlayerID       PlayerID
	RoundID        RoundID
	GameID         GameID
	Money          Money

	// ReferenceExternalID is required for a reversal and rejected for anything
	// else.
	ReferenceExternalID ExternalTransactionID

	CreatedAt time.Time
}

// NewExternalTransaction records an operation from a provider, in PENDING.
//
// OPENING is refused here. It is the one kind the system raises for itself, and
// accepting it from outside would let a provider mint balance.
func NewExternalTransaction(p ExternalTransactionParams) (WagerTransaction, error) {
	if !p.Kind.Valid() {
		return WagerTransaction{}, fail(CodeInvalidKind, "unknown kind %q", string(p.Kind))
	}
	if p.Kind == KindOpening {
		return WagerTransaction{}, fail(CodeInvalidKind,
			"%s is internal and cannot be submitted", KindOpening)
	}

	switch {
	case p.ID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "transaction id is empty")
	case p.ProviderID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "provider id is empty")
	case p.ExternalID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "external transaction id is empty")
	case p.IdempotencyKey.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "idempotency key is empty")
	case p.PayloadHash.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "payload hash is empty")
	case p.WalletID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "wallet id is empty")
	case p.PlayerID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "player id is empty")
	case p.RoundID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "round id is empty")
	case p.GameID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "game id is empty")
	case p.CreatedAt.IsZero():
		return WagerTransaction{}, fail(CodeInvalidTimestamp, "created at is not set")
	}

	if err := p.Kind.validateAmount(p.Money); err != nil {
		return WagerTransaction{}, err
	}

	switch {
	case p.Kind.IsReversal() && p.ReferenceExternalID.IsZero():
		return WagerTransaction{}, fail(CodeMissingReference,
			"%s requires a reference", p.Kind)
	case !p.Kind.IsReversal() && !p.ReferenceExternalID.IsZero():
		return WagerTransaction{}, fail(CodeUnexpectedReference,
			"%s does not take a reference", p.Kind)
	}

	at := p.CreatedAt.UTC()
	return WagerTransaction{
		id:                  p.ID,
		origin:              OriginExternal,
		kind:                p.Kind,
		status:              StatusPending,
		walletID:            p.WalletID,
		playerID:            p.PlayerID,
		money:               p.Money,
		providerID:          p.ProviderID,
		externalID:          p.ExternalID,
		idempotencyKey:      p.IdempotencyKey,
		payloadHash:         p.PayloadHash,
		roundID:             p.RoundID,
		gameID:              p.GameID,
		referenceExternalID: p.ReferenceExternalID,
		createdAt:           at,
		updatedAt:           at,
	}, nil
}

func (t WagerTransaction) ID() TransactionID              { return t.id }
func (t WagerTransaction) Origin() Origin                 { return t.origin }
func (t WagerTransaction) Kind() Kind                     { return t.kind }
func (t WagerTransaction) Status() Status                 { return t.status }
func (t WagerTransaction) WalletID() WalletID             { return t.walletID }
func (t WagerTransaction) PlayerID() PlayerID             { return t.playerID }
func (t WagerTransaction) Money() Money                   { return t.money }
func (t WagerTransaction) ProviderID() ProviderID         { return t.providerID }
func (t WagerTransaction) RoundID() RoundID               { return t.roundID }
func (t WagerTransaction) GameID() GameID                 { return t.gameID }
func (t WagerTransaction) FailureCode() Code              { return t.failureCode }
func (t WagerTransaction) BalanceAfter() Money            { return t.balanceAfter }
func (t WagerTransaction) CreatedAt() time.Time           { return t.createdAt }
func (t WagerTransaction) UpdatedAt() time.Time           { return t.updatedAt }
func (t WagerTransaction) IsInitialised() bool            { return !t.id.IsZero() }
func (t WagerTransaction) IsTerminal() bool               { return t.status.IsTerminal() }
func (t WagerTransaction) IdempotencyKey() IdempotencyKey { return t.idempotencyKey }
func (t WagerTransaction) PayloadHash() PayloadHash       { return t.payloadHash }

func (t WagerTransaction) ExternalID() ExternalTransactionID { return t.externalID }

func (t WagerTransaction) ReferenceExternalID() ExternalTransactionID {
	return t.referenceExternalID
}

func (t WagerTransaction) ResolvedReferenceID() TransactionID { return t.resolvedReferenceID }

// MarkPendingReference parks a reversal whose target has not arrived.
//
// The schedule is passed in rather than computed here: backoff and jitter are
// policy, and the domain has no business owning a clock or a random source.
func (t WagerTransaction) MarkPendingReference(at, nextAttemptAt, deadlineAt time.Time) (WagerTransaction, error) {
	if !t.kind.IsReversal() {
		return WagerTransaction{}, fail(CodeInvalidTransition,
			"%s never waits on a reference", t.kind)
	}
	if nextAttemptAt.IsZero() || deadlineAt.IsZero() {
		return WagerTransaction{}, fail(CodeInvalidTimestamp,
			"a wait needs both a next attempt and a deadline")
	}
	return t.transition(StatusPendingReference, at, func(next *WagerTransaction) {
		next.referenceAttempts++
		next.referenceNextAttemptAt = nextAttemptAt.UTC()
		next.referenceDeadlineAt = deadlineAt.UTC()
	})
}

// RescheduleReference records another failed lookup and when to try again.
//
// It stays in PENDING_REFERENCE, so it is not a transition -- the state machine
// has no self-edge and adding one would make "terminal" harder to reason about.
func (t WagerTransaction) RescheduleReference(at, nextAttemptAt time.Time) (WagerTransaction, error) {
	if t.status != StatusPendingReference {
		return WagerTransaction{}, fail(CodeInvalidTransition,
			"%s is not waiting on a reference", t.status)
	}
	if at.IsZero() || nextAttemptAt.IsZero() {
		return WagerTransaction{}, fail(CodeInvalidTimestamp, "reschedule needs both instants")
	}

	next := t
	next.referenceAttempts++
	next.referenceNextAttemptAt = nextAttemptAt.UTC()
	next.updatedAt = at.UTC()
	return next, nil
}

// ReferenceDeadlinePassed reports whether the wait is over.
func (t WagerTransaction) ReferenceDeadlinePassed(now time.Time) bool {
	return !t.referenceDeadlineAt.IsZero() && !now.Before(t.referenceDeadlineAt)
}

func (t WagerTransaction) ReferenceAttempts() int            { return t.referenceAttempts }
func (t WagerTransaction) ReferenceNextAttemptAt() time.Time { return t.referenceNextAttemptAt }
func (t WagerTransaction) ReferenceDeadlineAt() time.Time    { return t.referenceDeadlineAt }

// ResolveReference records the internal transaction a reversal turned out to
// name. It does not itself move the status: resolving tells the caller what to
// apply, and applying is what ends in PROCESSED.
func (t WagerTransaction) ResolveReference(id TransactionID, at time.Time) (WagerTransaction, error) {
	if !t.kind.IsReversal() {
		return WagerTransaction{}, fail(CodeUnexpectedReference,
			"%s has no reference to resolve", t.kind)
	}
	if id.IsZero() {
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "resolved reference id is empty")
	}
	if t.IsTerminal() {
		return WagerTransaction{}, fail(CodeInvalidTransition,
			"%s is terminal and cannot resolve a reference", t.status)
	}
	if at.IsZero() {
		return WagerTransaction{}, fail(CodeInvalidTimestamp, "transition time is not set")
	}

	next := t
	next.resolvedReferenceID = id
	next.updatedAt = at.UTC()
	return next, nil
}

// MarkProcessed completes the operation and stores the balance observed at that
// moment.
//
// That balance is what a replay returns later. Returning the wallet's current
// balance instead would be easy to write and would pass every happy-path test,
// and it would be wrong the first time another operation landed in between.
func (t WagerTransaction) MarkProcessed(balanceAfter Money, at time.Time) (WagerTransaction, error) {
	if !balanceAfter.IsInitialised() {
		return WagerTransaction{}, fail(CodeInvalidCurrency, "observed balance is not initialised")
	}
	if t.money.IsInitialised() && balanceAfter.Currency() != t.money.Currency() {
		return WagerTransaction{}, fail(CodeCurrencyMismatch,
			"balance in %s for an operation in %s", balanceAfter.Currency(), t.money.Currency())
	}
	return t.transition(StatusProcessed, at, func(next *WagerTransaction) {
		next.balanceAfter = balanceAfter
	})
}

// Reject ends the operation on a business rule. The code is what the provider
// reads, so it is required.
func (t WagerTransaction) Reject(code Code, at time.Time) (WagerTransaction, error) {
	if code == "" {
		return WagerTransaction{}, fail(CodeInvalidStatus, "a rejection needs a failure code")
	}
	return t.transition(StatusRejected, at, func(next *WagerTransaction) {
		next.failureCode = code
	})
}

// Fail ends the operation on a permanent infrastructure problem, recorded for
// audit. A transient problem is retried and does not come here.
func (t WagerTransaction) Fail(code Code, at time.Time) (WagerTransaction, error) {
	if code == "" {
		return WagerTransaction{}, fail(CodeInvalidStatus, "a failure needs a failure code")
	}
	return t.transition(StatusFailed, at, func(next *WagerTransaction) {
		next.failureCode = code
	})
}

// allowedTransitions is the state machine, written once so no method can invent
// an edge of its own.
var allowedTransitions = map[Status]map[Status]bool{
	StatusPending: {
		StatusPendingReference: true,
		StatusProcessed:        true,
		StatusRejected:         true,
		StatusFailed:           true,
	},
	StatusPendingReference: {
		StatusProcessed: true,
		StatusRejected:  true,
		StatusFailed:    true,
	},
	// The terminal states have no outgoing edges at all. That is the whole
	// point: a replay reads the stored result rather than moving anything.
	StatusProcessed: {},
	StatusRejected:  {},
	StatusFailed:    {},
}

func (t WagerTransaction) transition(to Status, at time.Time, apply func(*WagerTransaction)) (WagerTransaction, error) {
	if !t.IsInitialised() {
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "transaction is not initialised")
	}
	if at.IsZero() {
		return WagerTransaction{}, fail(CodeInvalidTimestamp, "transition time is not set")
	}
	if !allowedTransitions[t.status][to] {
		return WagerTransaction{}, fail(CodeInvalidTransition,
			"cannot go from %s to %s", t.status, to)
	}

	next := t
	next.status = to
	next.updatedAt = at.UTC()
	apply(&next)
	return next, nil
}

// RehydrateTransactionParams carries a transaction as it was stored.
type RehydrateTransactionParams struct {
	ID       TransactionID
	Origin   Origin
	Kind     Kind
	Status   Status
	WalletID WalletID
	PlayerID PlayerID
	Money    Money

	ProviderID     ProviderID
	ExternalID     ExternalTransactionID
	IdempotencyKey IdempotencyKey
	PayloadHash    PayloadHash
	RoundID        RoundID
	GameID         GameID

	ReferenceExternalID ExternalTransactionID
	ResolvedReferenceID TransactionID

	ReferenceAttempts      int
	ReferenceNextAttemptAt time.Time
	ReferenceDeadlineAt    time.Time

	FailureCode  Code
	BalanceAfter Money

	CreatedAt time.Time
	UpdatedAt time.Time
}

// RehydrateTransaction rebuilds a transaction from storage without replaying
// anything: no movement is applied, no transition is raised and no event is
// emitted. It still checks the shape, so a row that mixes an internal origin
// with provider metadata stops here instead of being read as fact.
func RehydrateTransaction(p RehydrateTransactionParams) (WagerTransaction, error) {
	switch {
	case p.ID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "transaction id is empty")
	case !p.Kind.Valid():
		return WagerTransaction{}, fail(CodeInvalidKind, "unknown kind %q", string(p.Kind))
	case !p.Status.Valid():
		return WagerTransaction{}, fail(CodeInvalidStatus, "unknown status %q", string(p.Status))
	case p.WalletID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "wallet id is empty")
	case p.PlayerID.IsZero():
		return WagerTransaction{}, fail(CodeInvalidIdentifier, "player id is empty")
	case p.CreatedAt.IsZero(), p.UpdatedAt.IsZero():
		return WagerTransaction{}, fail(CodeInvalidTimestamp, "timestamps are not set")
	}

	if err := p.Kind.validateAmount(p.Money); err != nil {
		return WagerTransaction{}, err
	}

	switch p.Origin {
	case OriginInternal:
		if p.Kind != KindOpening {
			return WagerTransaction{}, fail(CodeInvalidKind,
				"internal origin carries %s, only %s is internal", p.Kind, KindOpening)
		}
		if !p.ProviderID.IsZero() || !p.ExternalID.IsZero() || !p.IdempotencyKey.IsZero() ||
			!p.PayloadHash.IsZero() || !p.RoundID.IsZero() || !p.GameID.IsZero() ||
			!p.ReferenceExternalID.IsZero() {
			return WagerTransaction{}, fail(CodeInvalidKind,
				"internal transaction carries external metadata")
		}
	case OriginExternal:
		if p.Kind == KindOpening {
			return WagerTransaction{}, fail(CodeInvalidKind,
				"%s cannot have an external origin", KindOpening)
		}
		switch {
		case p.ProviderID.IsZero(), p.ExternalID.IsZero(), p.IdempotencyKey.IsZero(),
			p.PayloadHash.IsZero(), p.RoundID.IsZero(), p.GameID.IsZero():
			return WagerTransaction{}, fail(CodeInvalidIdentifier,
				"external transaction is missing provider metadata")
		}
		if p.Kind.IsReversal() && p.ReferenceExternalID.IsZero() {
			return WagerTransaction{}, fail(CodeMissingReference, "%s without a reference", p.Kind)
		}
		if !p.Kind.IsReversal() && !p.ReferenceExternalID.IsZero() {
			return WagerTransaction{}, fail(CodeUnexpectedReference, "%s with a reference", p.Kind)
		}
	default:
		return WagerTransaction{}, fail(CodeInvalidKind, "unknown origin %q", string(p.Origin))
	}

	return WagerTransaction{
		id:                     p.ID,
		origin:                 p.Origin,
		kind:                   p.Kind,
		status:                 p.Status,
		walletID:               p.WalletID,
		playerID:               p.PlayerID,
		money:                  p.Money,
		providerID:             p.ProviderID,
		externalID:             p.ExternalID,
		idempotencyKey:         p.IdempotencyKey,
		payloadHash:            p.PayloadHash,
		roundID:                p.RoundID,
		gameID:                 p.GameID,
		referenceExternalID:    p.ReferenceExternalID,
		resolvedReferenceID:    p.ResolvedReferenceID,
		referenceAttempts:      p.ReferenceAttempts,
		referenceNextAttemptAt: utcOrZero(p.ReferenceNextAttemptAt),
		referenceDeadlineAt:    utcOrZero(p.ReferenceDeadlineAt),
		failureCode:            p.FailureCode,
		balanceAfter:           p.BalanceAfter,
		createdAt:              p.CreatedAt.UTC(),
		updatedAt:              p.UpdatedAt.UTC(),
	}, nil
}

// Direction is which way a processed operation of this kind moves money. The
// second return is false for a kind that moves nothing.
//
// This is the single definition, and the reversal rules derive from it rather
// than repeating it. A hand-written table of reversal directions would be a
// second source of truth about what a bet does.
func (k Kind) Direction() (Direction, bool) {
	switch k {
	case KindOpening, KindWin, KindRefund:
		return Credit, true
	case KindBet:
		return Debit, true
	case KindRollback:
		// A rollback's direction depends on what it reverses, so it has none of
		// its own. That is also why a rollback cannot be rolled back: there is
		// nothing to invert without walking the chain, and walking it would
		// just be reapplying the original operation by a longer route.
		return "", false
	default: // KindLoss
		return "", false
	}
}

// CanReverse says whether this kind may undo an operation of the target kind.
//
//	REFUND   → BET
//	ROLLBACK → BET, WIN, REFUND
//
// LOSS moved nothing, so there is nothing to undo. OPENING is internal, and
// reversing it would be deleting a wallet by another name. See
// docs/adr/0008-reversals.md.
func (k Kind) CanReverse(target Kind) bool {
	switch k {
	case KindRefund:
		return target == KindBet
	case KindRollback:
		return target == KindBet || target == KindWin || target == KindRefund
	default:
		return false
	}
}

// ReversalDirection is which way a reversal of the target moves money: the
// opposite of what the target did.
func ReversalDirection(target Kind) (Direction, error) {
	direction, moves := target.Direction()
	if !moves {
		return "", fail(CodeReferenceNotReversible, "%s moves no money", target)
	}
	return direction.Opposite(), nil
}

// utcOrZero normalises an optional instant without turning the zero value into
// a real time in 1 AD, which is what a bare .UTC() would do.
func utcOrZero(at time.Time) time.Time {
	if at.IsZero() {
		return time.Time{}
	}
	return at.UTC()
}
