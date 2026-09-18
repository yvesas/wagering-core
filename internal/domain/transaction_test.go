package domain

import (
	"errors"
	"testing"
	"time"
)

func providerID(t *testing.T) ProviderID {
	t.Helper()
	id, err := ParseProviderID("provider-a")
	if err != nil {
		t.Fatalf("ParseProviderID: %v", err)
	}
	return id
}

func externalID(t *testing.T, s string) ExternalTransactionID {
	t.Helper()
	id, err := ParseExternalTransactionID(s)
	if err != nil {
		t.Fatalf("ParseExternalTransactionID(%q): %v", s, err)
	}
	return id
}

// externalParams is a valid BET. Each test bends one field, so the rejected
// case differs from the accepted one in exactly one place.
func externalParams(t *testing.T) ExternalTransactionParams {
	t.Helper()
	key, err := ParseIdempotencyKey("provider-a:transaction-123")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := ParsePayloadHash("sha256:deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	round, err := ParseRoundID("round-987")
	if err != nil {
		t.Fatal(err)
	}
	game, err := ParseGameID("fortune-chimp")
	if err != nil {
		t.Fatal(err)
	}
	return ExternalTransactionParams{
		ID:             txID(t),
		Kind:           KindBet,
		ProviderID:     providerID(t),
		ExternalID:     externalID(t, "transaction-123"),
		IdempotencyKey: key,
		PayloadHash:    hash,
		WalletID:       walletID(t),
		PlayerID:       playerID(t),
		RoundID:        round,
		GameID:         game,
		Money:          mustParse(t, "25.00", brl(t)),
		CreatedAt:      at(t),
	}
}

func TestKindPolicy(t *testing.T) {
	t.Parallel()
	for k, want := range map[Kind]struct{ valid, reversal, moves bool }{
		KindOpening:  {true, false, true},
		KindBet:      {true, false, true},
		KindWin:      {true, false, true},
		KindLoss:     {true, false, false},
		KindRefund:   {true, true, true},
		KindRollback: {true, true, true},
		"":           {false, false, true},
		"TRANSFER":   {false, false, true},
	} {
		if k.Valid() != want.valid {
			t.Errorf("%q.Valid() = %v, want %v", string(k), k.Valid(), want.valid)
		}
		if k.IsReversal() != want.reversal {
			t.Errorf("%q.IsReversal() = %v, want %v", string(k), k.IsReversal(), want.reversal)
		}
		if k.MovesMoney() != want.moves {
			t.Errorf("%q.MovesMoney() = %v, want %v", string(k), k.MovesMoney(), want.moves)
		}
	}
}

func TestAmountPolicyPerKind(t *testing.T) {
	t.Parallel()
	c := brl(t)
	tests := []struct {
		name   string
		kind   Kind
		amount string
		ok     bool
	}{
		{"bet is positive", KindBet, "25.00", true},
		{"bet cannot be zero", KindBet, "0.00", false},
		{"win is positive", KindWin, "25.00", true},
		{"win cannot be zero", KindWin, "0.00", false},
		{"refund is positive", KindRefund, "25.00", true},
		{"rollback is positive", KindRollback, "25.00", true},
		// LOSS is the only kind that demands zero: it records how a round ended
		// and moves nothing, because the bet was already debited.
		{"loss must be zero", KindLoss, "0.00", true},
		{"loss cannot carry money", KindLoss, "0.01", false},
		{"loss cannot carry a large amount", KindLoss, "25.00", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := externalParams(t)
			p.Kind = tc.kind
			p.Money = mustParse(t, tc.amount, c)
			if tc.kind.IsReversal() {
				p.ReferenceExternalID = externalID(t, "transaction-1")
			}

			got, err := NewExternalTransaction(p)
			if tc.ok {
				if err != nil {
					t.Fatalf("NewExternalTransaction: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("NewExternalTransaction = %v, want rejection", got)
			}
			if !errors.Is(err, ErrInvalidAmountForKind) {
				t.Fatalf("error = %v, want ErrInvalidAmountForKind", err)
			}
		})
	}
}

func TestNewExternalTransactionStartsPending(t *testing.T) {
	t.Parallel()
	tx, err := NewExternalTransaction(externalParams(t))
	if err != nil {
		t.Fatalf("NewExternalTransaction: %v", err)
	}
	if tx.Status() != StatusPending {
		t.Errorf("status = %q, want PENDING", tx.Status())
	}
	if tx.Origin() != OriginExternal {
		t.Errorf("origin = %q, want EXTERNAL", tx.Origin())
	}
	if tx.IsTerminal() {
		t.Error("a new transaction should not be terminal")
	}
	if tx.FailureCode() != "" {
		t.Errorf("failure code = %q, want empty", tx.FailureCode())
	}
}

func TestOpeningCannotArriveFromOutside(t *testing.T) {
	t.Parallel()
	p := externalParams(t)
	p.Kind = KindOpening

	got, err := NewExternalTransaction(p)
	if err == nil {
		t.Fatalf("NewExternalTransaction = %v, want rejection", got)
	}
	// Accepting this from a provider would let them mint balance.
	if !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("error = %v, want ErrInvalidKind", err)
	}
}

func TestReversalReferenceRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		kind      Kind
		reference string
		wantCode  Code // empty means it must be accepted
	}{
		{"refund with a reference", KindRefund, "transaction-1", ""},
		{"rollback with a reference", KindRollback, "transaction-1", ""},
		{"refund without a reference", KindRefund, "", CodeMissingReference},
		{"rollback without a reference", KindRollback, "", CodeMissingReference},
		{"bet with a reference", KindBet, "transaction-1", CodeUnexpectedReference},
		{"win with a reference", KindWin, "transaction-1", CodeUnexpectedReference},
		{"bet without a reference", KindBet, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := externalParams(t)
			p.Kind = tc.kind
			if tc.reference != "" {
				p.ReferenceExternalID = externalID(t, tc.reference)
			}

			got, err := NewExternalTransaction(p)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("NewExternalTransaction: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("NewExternalTransaction = %v, want rejection", got)
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

func TestNewExternalTransactionRequiresProviderMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*ExternalTransactionParams)
	}{
		{"no transaction id", func(p *ExternalTransactionParams) { p.ID = TransactionID{} }},
		{"no provider", func(p *ExternalTransactionParams) { p.ProviderID = ProviderID{} }},
		{"no external id", func(p *ExternalTransactionParams) { p.ExternalID = ExternalTransactionID{} }},
		{"no idempotency key", func(p *ExternalTransactionParams) { p.IdempotencyKey = IdempotencyKey{} }},
		{"no payload hash", func(p *ExternalTransactionParams) { p.PayloadHash = PayloadHash{} }},
		{"no wallet", func(p *ExternalTransactionParams) { p.WalletID = WalletID{} }},
		{"no player", func(p *ExternalTransactionParams) { p.PlayerID = PlayerID{} }},
		{"no round", func(p *ExternalTransactionParams) { p.RoundID = RoundID{} }},
		{"no game", func(p *ExternalTransactionParams) { p.GameID = GameID{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := externalParams(t)
			tc.mutate(&p)
			got, err := NewExternalTransaction(p)
			if err == nil {
				t.Fatalf("NewExternalTransaction = %v, want rejection", got)
			}
			if !errors.Is(err, ErrInvalidIdentifier) {
				t.Fatalf("error = %v, want ErrInvalidIdentifier", err)
			}
		})
	}
}

func TestNewOpeningTransaction(t *testing.T) {
	t.Parallel()
	tx, err := NewOpeningTransaction(OpeningTransactionParams{
		ID:        txID(t),
		WalletID:  walletID(t),
		PlayerID:  playerID(t),
		Money:     mustParse(t, "1000.00", brl(t)),
		CreatedAt: at(t),
	})
	if err != nil {
		t.Fatalf("NewOpeningTransaction: %v", err)
	}

	if tx.Origin() != OriginInternal {
		t.Errorf("origin = %q, want INTERNAL", tx.Origin())
	}
	// It is committed together with the wallet and its entry, so there is no
	// window in which it is accepted but not yet applied.
	if tx.Status() != StatusProcessed {
		t.Errorf("status = %q, want PROCESSED", tx.Status())
	}
	if tx.BalanceAfter().String() != "1000.00" {
		t.Errorf("balance after = %s", tx.BalanceAfter())
	}

	// None of the provider metadata applies, and the params struct has nowhere
	// to put it in the first place.
	if !tx.ProviderID().IsZero() || !tx.ExternalID().IsZero() ||
		!tx.IdempotencyKey().IsZero() || !tx.PayloadHash().IsZero() ||
		!tx.RoundID().IsZero() || !tx.GameID().IsZero() ||
		!tx.ReferenceExternalID().IsZero() {
		t.Error("an opening carries external metadata")
	}
}

func TestNewOpeningTransactionRejectsZero(t *testing.T) {
	t.Parallel()
	// A wallet opened at zero creates no opening transaction at all, so an
	// opening of zero should never be built.
	got, err := NewOpeningTransaction(OpeningTransactionParams{
		ID:        txID(t),
		WalletID:  walletID(t),
		PlayerID:  playerID(t),
		Money:     mustParse(t, "0.00", brl(t)),
		CreatedAt: at(t),
	})
	if err == nil {
		t.Fatalf("NewOpeningTransaction = %v, want rejection", got)
	}
	if !errors.Is(err, ErrInvalidAmountForKind) {
		t.Fatalf("error = %v, want ErrInvalidAmountForKind", err)
	}
}

func TestStatusIsTerminal(t *testing.T) {
	t.Parallel()
	for s, want := range map[Status]bool{
		StatusPending:          false,
		StatusPendingReference: false,
		StatusProcessed:        true,
		StatusRejected:         true,
		StatusFailed:           true,
	} {
		if s.IsTerminal() != want {
			t.Errorf("%q.IsTerminal() = %v, want %v", string(s), s.IsTerminal(), want)
		}
	}
}

func TestTransitionsFromPending(t *testing.T) {
	t.Parallel()
	c := brl(t)

	t.Run("to processed", func(t *testing.T) {
		t.Parallel()
		tx, err := NewExternalTransaction(externalParams(t))
		if err != nil {
			t.Fatal(err)
		}
		done, err := tx.MarkProcessed(mustParse(t, "975.00", c), at(t))
		if err != nil {
			t.Fatalf("MarkProcessed: %v", err)
		}
		if done.Status() != StatusProcessed {
			t.Errorf("status = %q", done.Status())
		}
		if done.BalanceAfter().String() != "975.00" {
			t.Errorf("balance after = %s", done.BalanceAfter())
		}
	})

	t.Run("to rejected", func(t *testing.T) {
		t.Parallel()
		tx, err := NewExternalTransaction(externalParams(t))
		if err != nil {
			t.Fatal(err)
		}
		done, err := tx.Reject(CodeInsufficientFunds, at(t))
		if err != nil {
			t.Fatalf("Reject: %v", err)
		}
		if done.Status() != StatusRejected {
			t.Errorf("status = %q", done.Status())
		}
		if done.FailureCode() != CodeInsufficientFunds {
			t.Errorf("failure code = %q", done.FailureCode())
		}
	})

	t.Run("to failed", func(t *testing.T) {
		t.Parallel()
		tx, err := NewExternalTransaction(externalParams(t))
		if err != nil {
			t.Fatal(err)
		}
		done, err := tx.Fail(CodeInvalidStatus, at(t))
		if err != nil {
			t.Fatalf("Fail: %v", err)
		}
		if done.Status() != StatusFailed {
			t.Errorf("status = %q", done.Status())
		}
	})

	t.Run("to pending reference, for a reversal", func(t *testing.T) {
		t.Parallel()
		p := externalParams(t)
		p.Kind = KindRefund
		p.ReferenceExternalID = externalID(t, "transaction-1")
		tx, err := NewExternalTransaction(p)
		if err != nil {
			t.Fatal(err)
		}
		parked, err := tx.MarkPendingReference(at(t))
		if err != nil {
			t.Fatalf("MarkPendingReference: %v", err)
		}
		if parked.Status() != StatusPendingReference {
			t.Errorf("status = %q", parked.Status())
		}
	})
}

func TestOnlyReversalsWaitOnAReference(t *testing.T) {
	t.Parallel()
	tx, err := NewExternalTransaction(externalParams(t)) // a BET
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.MarkPendingReference(at(t)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition", err)
	}
}

func TestTerminalTransactionsDoNotMove(t *testing.T) {
	t.Parallel()
	c := brl(t)
	base, err := NewExternalTransaction(externalParams(t))
	if err != nil {
		t.Fatal(err)
	}

	terminal := map[string]WagerTransaction{}
	processed, err := base.MarkProcessed(mustParse(t, "975.00", c), at(t))
	if err != nil {
		t.Fatal(err)
	}
	terminal["processed"] = processed
	rejected, err := base.Reject(CodeInsufficientFunds, at(t))
	if err != nil {
		t.Fatal(err)
	}
	terminal["rejected"] = rejected
	failed, err := base.Fail(CodeInvalidStatus, at(t))
	if err != nil {
		t.Fatal(err)
	}
	terminal["failed"] = failed

	for name, tx := range terminal {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// This is what makes a replay safe: there is no edge out, so the
			// stored result is the only possible answer.
			if _, err := tx.MarkProcessed(mustParse(t, "1.00", c), at(t)); !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("MarkProcessed: error = %v, want ErrInvalidTransition", err)
			}
			if _, err := tx.Reject(CodeInsufficientFunds, at(t)); !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("Reject: error = %v, want ErrInvalidTransition", err)
			}
			if _, err := tx.Fail(CodeInvalidStatus, at(t)); !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("Fail: error = %v, want ErrInvalidTransition", err)
			}
		})
	}
}

func TestRejectedTransitionLeavesTheTransactionUntouched(t *testing.T) {
	t.Parallel()
	c := brl(t)
	tx, err := NewExternalTransaction(externalParams(t))
	if err != nil {
		t.Fatal(err)
	}
	processed, err := tx.MarkProcessed(mustParse(t, "975.00", c), at(t))
	if err != nil {
		t.Fatal(err)
	}
	before := processed

	if _, err := processed.Reject(CodeInsufficientFunds, at(t)); err == nil {
		t.Fatal("expected the transition to be refused")
	}
	if processed != before {
		t.Fatal("the transaction changed despite the refused transition")
	}
}

func TestTransitionsRequireTheirInputs(t *testing.T) {
	t.Parallel()
	c := brl(t)
	tx, err := NewExternalTransaction(externalParams(t))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tx.MarkProcessed(mustParse(t, "1.00", c), time.Time{}); !errors.Is(err, ErrInvalidTimestamp) {
		t.Errorf("no timestamp: error = %v, want ErrInvalidTimestamp", err)
	}
	if _, err := tx.MarkProcessed(Money{}, at(t)); !errors.Is(err, ErrInvalidCurrency) {
		t.Errorf("uninitialised balance: error = %v, want ErrInvalidCurrency", err)
	}
	if _, err := tx.MarkProcessed(mustParse(t, "1.00", usd(t)), at(t)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("foreign balance: error = %v, want ErrCurrencyMismatch", err)
	}
	// A rejection a provider cannot branch on is not a rejection.
	if _, err := tx.Reject("", at(t)); !errors.Is(err, ErrInvalidStatus) {
		t.Errorf("empty failure code: error = %v, want ErrInvalidStatus", err)
	}
	if _, err := tx.Fail("", at(t)); !errors.Is(err, ErrInvalidStatus) {
		t.Errorf("empty failure code: error = %v, want ErrInvalidStatus", err)
	}

	var none WagerTransaction
	if _, err := none.MarkProcessed(mustParse(t, "1.00", c), at(t)); !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("uninitialised transaction: error = %v, want ErrInvalidIdentifier", err)
	}
}

func TestResolveReference(t *testing.T) {
	t.Parallel()
	p := externalParams(t)
	p.Kind = KindRollback
	p.ReferenceExternalID = externalID(t, "transaction-1")
	tx, err := NewExternalTransaction(p)
	if err != nil {
		t.Fatal(err)
	}

	target, err := ParseTransactionID("tx-original")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := tx.ResolveReference(target, at(t))
	if err != nil {
		t.Fatalf("ResolveReference: %v", err)
	}
	if resolved.ResolvedReferenceID() != target {
		t.Errorf("resolved = %v, want %v", resolved.ResolvedReferenceID(), target)
	}
	// Resolving says what to apply; applying is what ends in PROCESSED.
	if resolved.Status() != StatusPending {
		t.Errorf("status = %q, want it unchanged at PENDING", resolved.Status())
	}
}

func TestResolveReferenceRejects(t *testing.T) {
	t.Parallel()
	c := brl(t)
	target, err := ParseTransactionID("tx-original")
	if err != nil {
		t.Fatal(err)
	}

	bet, err := NewExternalTransaction(externalParams(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bet.ResolveReference(target, at(t)); !errors.Is(err, ErrUnexpectedReference) {
		t.Errorf("bet: error = %v, want ErrUnexpectedReference", err)
	}

	p := externalParams(t)
	p.Kind = KindRefund
	p.ReferenceExternalID = externalID(t, "transaction-1")
	refund, err := NewExternalTransaction(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refund.ResolveReference(TransactionID{}, at(t)); !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("empty target: error = %v, want ErrInvalidIdentifier", err)
	}

	done, err := refund.MarkProcessed(mustParse(t, "1.00", c), at(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := done.ResolveReference(target, at(t)); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("terminal: error = %v, want ErrInvalidTransition", err)
	}
}

func TestRehydrateTransaction(t *testing.T) {
	t.Parallel()
	c := brl(t)
	original, err := NewExternalTransaction(externalParams(t))
	if err != nil {
		t.Fatal(err)
	}
	processed, err := original.MarkProcessed(mustParse(t, "975.00", c), at(t))
	if err != nil {
		t.Fatal(err)
	}

	back, err := RehydrateTransaction(RehydrateTransactionParams{
		ID:             processed.ID(),
		Origin:         processed.Origin(),
		Kind:           processed.Kind(),
		Status:         processed.Status(),
		WalletID:       processed.WalletID(),
		PlayerID:       processed.PlayerID(),
		Money:          processed.Money(),
		ProviderID:     processed.ProviderID(),
		ExternalID:     processed.ExternalID(),
		IdempotencyKey: processed.IdempotencyKey(),
		PayloadHash:    processed.PayloadHash(),
		RoundID:        processed.RoundID(),
		GameID:         processed.GameID(),
		BalanceAfter:   processed.BalanceAfter(),
		CreatedAt:      processed.CreatedAt(),
		UpdatedAt:      processed.UpdatedAt(),
	})
	if err != nil {
		t.Fatalf("RehydrateTransaction: %v", err)
	}
	// Rehydration replays nothing: no movement, no transition, no event.
	if back != processed {
		t.Fatal("rehydration did not reproduce the stored transaction")
	}
}

func TestRehydrateTransactionRejectsMixedOrigins(t *testing.T) {
	t.Parallel()
	c := brl(t)
	internalValid := RehydrateTransactionParams{
		ID:        txID(t),
		Origin:    OriginInternal,
		Kind:      KindOpening,
		Status:    StatusProcessed,
		WalletID:  walletID(t),
		PlayerID:  playerID(t),
		Money:     mustParse(t, "100.00", c),
		CreatedAt: at(t),
		UpdatedAt: at(t),
	}

	if _, err := RehydrateTransaction(internalValid); err != nil {
		t.Fatalf("valid internal row: %v", err)
	}

	t.Run("internal row carrying provider metadata", func(t *testing.T) {
		t.Parallel()
		p := internalValid
		p.ProviderID = providerID(t)
		if _, err := RehydrateTransaction(p); !errors.Is(err, ErrInvalidKind) {
			t.Fatalf("error = %v, want ErrInvalidKind", err)
		}
	})

	t.Run("internal row that is not an opening", func(t *testing.T) {
		t.Parallel()
		p := internalValid
		p.Kind = KindBet
		if _, err := RehydrateTransaction(p); !errors.Is(err, ErrInvalidKind) {
			t.Fatalf("error = %v, want ErrInvalidKind", err)
		}
	})

	t.Run("external row missing provider metadata", func(t *testing.T) {
		t.Parallel()
		p := internalValid
		p.Origin = OriginExternal
		p.Kind = KindBet
		if _, err := RehydrateTransaction(p); !errors.Is(err, ErrInvalidIdentifier) {
			t.Fatalf("error = %v, want ErrInvalidIdentifier", err)
		}
	})

	t.Run("unknown origin", func(t *testing.T) {
		t.Parallel()
		p := internalValid
		p.Origin = "SOMEWHERE"
		if _, err := RehydrateTransaction(p); !errors.Is(err, ErrInvalidKind) {
			t.Fatalf("error = %v, want ErrInvalidKind", err)
		}
	})

	t.Run("unknown status", func(t *testing.T) {
		t.Parallel()
		p := internalValid
		p.Status = "ALMOST"
		if _, err := RehydrateTransaction(p); !errors.Is(err, ErrInvalidStatus) {
			t.Fatalf("error = %v, want ErrInvalidStatus", err)
		}
	})
}

func TestTransactionTimestampsAreUTC(t *testing.T) {
	t.Parallel()
	zone := time.FixedZone("UTC-3", -3*60*60)
	p := externalParams(t)
	p.CreatedAt = time.Date(2026, 9, 18, 9, 0, 0, 0, zone)

	tx, err := NewExternalTransaction(p)
	if err != nil {
		t.Fatal(err)
	}
	if tx.CreatedAt().Location() != time.UTC {
		t.Errorf("location = %v, want UTC", tx.CreatedAt().Location())
	}
}

func TestNewOpeningTransactionRejects(t *testing.T) {
	t.Parallel()
	valid := OpeningTransactionParams{
		ID:        txID(t),
		WalletID:  walletID(t),
		PlayerID:  playerID(t),
		Money:     mustParse(t, "100.00", brl(t)),
		CreatedAt: at(t),
	}
	tests := []struct {
		name     string
		mutate   func(*OpeningTransactionParams)
		wantCode Code
	}{
		{"no transaction id", func(p *OpeningTransactionParams) { p.ID = TransactionID{} }, CodeInvalidIdentifier},
		{"no wallet id", func(p *OpeningTransactionParams) { p.WalletID = WalletID{} }, CodeInvalidIdentifier},
		{"no player id", func(p *OpeningTransactionParams) { p.PlayerID = PlayerID{} }, CodeInvalidIdentifier},
		{"no timestamp", func(p *OpeningTransactionParams) { p.CreatedAt = time.Time{} }, CodeInvalidTimestamp},
		{"uninitialised amount", func(p *OpeningTransactionParams) { p.Money = Money{} }, CodeInvalidCurrency},
		{"negative amount", func(p *OpeningTransactionParams) {
			p.Money = mustParse(t, "-1.00", brl(t))
		}, CodeInvalidAmountForKind},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := valid
			tc.mutate(&p)
			got, err := NewOpeningTransaction(p)
			if err == nil {
				t.Fatalf("NewOpeningTransaction = %v, want rejection", got)
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

func TestRehydrateTransactionRejectsMalformedRows(t *testing.T) {
	t.Parallel()
	valid := RehydrateTransactionParams{
		ID:        txID(t),
		Origin:    OriginInternal,
		Kind:      KindOpening,
		Status:    StatusProcessed,
		WalletID:  walletID(t),
		PlayerID:  playerID(t),
		Money:     mustParse(t, "100.00", brl(t)),
		CreatedAt: at(t),
		UpdatedAt: at(t),
	}
	tests := []struct {
		name     string
		mutate   func(*RehydrateTransactionParams)
		wantCode Code
	}{
		{"no transaction id", func(p *RehydrateTransactionParams) { p.ID = TransactionID{} }, CodeInvalidIdentifier},
		{"no wallet id", func(p *RehydrateTransactionParams) { p.WalletID = WalletID{} }, CodeInvalidIdentifier},
		{"no player id", func(p *RehydrateTransactionParams) { p.PlayerID = PlayerID{} }, CodeInvalidIdentifier},
		{"unknown kind", func(p *RehydrateTransactionParams) { p.Kind = "TRANSFER" }, CodeInvalidKind},
		{"no created at", func(p *RehydrateTransactionParams) { p.CreatedAt = time.Time{} }, CodeInvalidTimestamp},
		{"no updated at", func(p *RehydrateTransactionParams) { p.UpdatedAt = time.Time{} }, CodeInvalidTimestamp},
		{"amount breaks the kind policy", func(p *RehydrateTransactionParams) {
			p.Money = mustParse(t, "0.00", brl(t))
		}, CodeInvalidAmountForKind},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := valid
			tc.mutate(&p)
			got, err := RehydrateTransaction(p)
			if err == nil {
				t.Fatalf("RehydrateTransaction = %v, want rejection", got)
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

func TestRehydrateExternalReversalReferenceRules(t *testing.T) {
	t.Parallel()
	key, err := ParseIdempotencyKey("provider-a:transaction-123")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := ParsePayloadHash("sha256:deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	round, err := ParseRoundID("round-987")
	if err != nil {
		t.Fatal(err)
	}
	game, err := ParseGameID("fortune-chimp")
	if err != nil {
		t.Fatal(err)
	}
	base := RehydrateTransactionParams{
		ID:             txID(t),
		Origin:         OriginExternal,
		Kind:           KindRefund,
		Status:         StatusPending,
		WalletID:       walletID(t),
		PlayerID:       playerID(t),
		Money:          mustParse(t, "25.00", brl(t)),
		ProviderID:     providerID(t),
		ExternalID:     externalID(t, "transaction-123"),
		IdempotencyKey: key,
		PayloadHash:    hash,
		RoundID:        round,
		GameID:         game,
		CreatedAt:      at(t),
		UpdatedAt:      at(t),
	}

	if _, err := RehydrateTransaction(base); !errors.Is(err, ErrMissingReference) {
		t.Errorf("reversal without a reference: error = %v, want ErrMissingReference", err)
	}

	withRef := base
	withRef.ReferenceExternalID = externalID(t, "transaction-1")
	if _, err := RehydrateTransaction(withRef); err != nil {
		t.Errorf("reversal with a reference: %v", err)
	}

	bet := withRef
	bet.Kind = KindBet
	if _, err := RehydrateTransaction(bet); !errors.Is(err, ErrUnexpectedReference) {
		t.Errorf("bet with a reference: error = %v, want ErrUnexpectedReference", err)
	}
}
