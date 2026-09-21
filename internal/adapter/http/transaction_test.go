package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

func sampleTransaction(t *testing.T, status domain.Status, failureCode domain.Code) domain.WagerTransaction {
	t.Helper()
	currency, err := domain.ParseCurrency("BRL")
	if err != nil {
		t.Fatal(err)
	}
	money, err := domain.ParseMoney("25.00", currency)
	if err != nil {
		t.Fatal(err)
	}
	balance, err := domain.ParseMoney("975.00", currency)
	if err != nil {
		t.Fatal(err)
	}

	parse := func(f func() error) {
		t.Helper()
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	var p domain.RehydrateTransactionParams
	parse(func() (err error) { p.ID, err = domain.ParseTransactionID("tx-1"); return })
	parse(func() (err error) { p.WalletID, err = domain.ParseWalletID("wallet-1"); return })
	parse(func() (err error) { p.PlayerID, err = domain.ParsePlayerID("player-1"); return })
	parse(func() (err error) { p.ProviderID, err = domain.ParseProviderID("provider-a"); return })
	parse(func() (err error) { p.ExternalID, err = domain.ParseExternalTransactionID("transaction-123"); return })
	parse(func() (err error) {
		p.IdempotencyKey, err = domain.ParseIdempotencyKey("provider-a:transaction-123")
		return
	})
	parse(func() (err error) { p.PayloadHash, err = domain.ParsePayloadHash("sha256:abc"); return })
	parse(func() (err error) { p.RoundID, err = domain.ParseRoundID("round-987"); return })
	parse(func() (err error) { p.GameID, err = domain.ParseGameID("fortune-chimp"); return })

	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	p.Origin = domain.OriginExternal
	p.Kind = domain.KindBet
	p.Status = status
	if status == domain.StatusPendingReference {
		// Only a reversal waits, and the schema demands it carry a schedule.
		p.Kind = domain.KindRefund
		ref, err := domain.ParseExternalTransactionID("transaction-1")
		if err != nil {
			t.Fatal(err)
		}
		p.ReferenceExternalID = ref
		p.ReferenceAttempts = 1
		p.ReferenceNextAttemptAt = at.Add(time.Second)
		p.ReferenceDeadlineAt = at.Add(time.Minute)
	}
	p.Money = money
	p.FailureCode = failureCode
	p.CreatedAt, p.UpdatedAt = at, at
	if status == domain.StatusProcessed {
		p.BalanceAfter = balance
	}

	transaction, err := domain.RehydrateTransaction(p)
	if err != nil {
		t.Fatal(err)
	}
	return transaction
}

const validSubmitBody = `{
  "providerId":"provider-a",
  "externalTransactionId":"transaction-123",
  "playerId":"player-1",
  "walletId":"wallet-1",
  "roundId":"round-987",
  "gameId":"fortune-chimp",
  "kind":"BET",
  "money":{"amount":"25.00","currency":"BRL"}
}`

func submitRequestFor(t *testing.T, submitter *stubSubmitter, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewTransactionHandler(submitter, &stubTxReader{})
	mux := Handler(testRoutes(t, NewWalletHandler(&stubOpener{}, &stubReader{}), handler))

	req := authenticated(httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(body)))
	if key != "" {
		req.Header.Set(IdempotencyKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSubmitRequiresTheIdempotencyKeyHeader(t *testing.T) {
	t.Parallel()
	submitter := &stubSubmitter{}
	rec := submitRequestFor(t, submitter, "", validSubmitBody)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	// Computing "{providerId}:{externalTransactionId}" here would be accepting
	// a request the client never made idempotent, under a key it never chose.
	if submitter.got.ProviderID != "" {
		t.Error("the request reached the use case without a key")
	}
}

func TestSubmitPassesTheHeaderKeyThrough(t *testing.T) {
	t.Parallel()
	submitter := &stubSubmitter{result: app.SubmitResult{
		Transaction: sampleTransaction(t, domain.StatusProcessed, ""),
	}}
	rec := submitRequestFor(t, submitter, "a-key-the-client-chose", validSubmitBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	// The server never substitutes a key of its own.
	if submitter.got.IdempotencyKey != "a-key-the-client-chose" {
		t.Errorf("key = %q", submitter.got.IdempotencyKey)
	}
}

func TestSubmitRendersAProcessedOperation(t *testing.T) {
	t.Parallel()
	transaction := sampleTransaction(t, domain.StatusProcessed, "")
	submitter := &stubSubmitter{result: app.SubmitResult{
		Transaction: transaction,
		Balance:     transaction.BalanceAfter(),
	}}

	rec := submitRequestFor(t, submitter, "provider-a:transaction-123", validSubmitBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	body := decodeBody[submitResponse](t, rec)
	if body.Status != "PROCESSED" || body.Balance.Amount != "975.00" || body.IdempotentReplay {
		t.Fatalf("body = %+v", body)
	}
}

func TestSubmitRendersAReplay(t *testing.T) {
	t.Parallel()
	transaction := sampleTransaction(t, domain.StatusProcessed, "")
	submitter := &stubSubmitter{result: app.SubmitResult{
		Transaction: transaction,
		Balance:     transaction.BalanceAfter(),
		Replay:      true,
	}}

	rec := submitRequestFor(t, submitter, "provider-a:transaction-123", validSubmitBody)
	if !decodeBody[submitResponse](t, rec).IdempotentReplay {
		t.Fatal("the replay flag did not reach the client")
	}
}

func TestARejectionAnswers422WithItsCode(t *testing.T) {
	t.Parallel()
	transaction := sampleTransaction(t, domain.StatusRejected, domain.CodeInsufficientFunds)
	submitter := &stubSubmitter{result: app.SubmitResult{Transaction: transaction}}

	rec := submitRequestFor(t, submitter, "provider-a:transaction-123", validSubmitBody)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body)
	}

	body := decodeBody[submitResponse](t, rec)
	if body.Status != "REJECTED" {
		t.Errorf("status = %q", body.Status)
	}
	// The failure code is what a provider branches on to decide what to do.
	if body.FailureCode != string(domain.CodeInsufficientFunds) {
		t.Errorf("failure code = %q", body.FailureCode)
	}
}

func TestAReplayedRejectionIsStill422(t *testing.T) {
	t.Parallel()
	transaction := sampleTransaction(t, domain.StatusRejected, domain.CodeInsufficientFunds)
	submitter := &stubSubmitter{result: app.SubmitResult{Transaction: transaction, Replay: true}}

	rec := submitRequestFor(t, submitter, "provider-a:transaction-123", validSubmitBody)
	// A resend that suddenly answered 200 would tell a provider its bet went
	// through. The stored outcome is the answer, both times.
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if !decodeBody[submitResponse](t, rec).IdempotentReplay {
		t.Error("the replay flag is missing")
	}
}

func TestSubmitConflictAnswers409(t *testing.T) {
	t.Parallel()
	submitter := &stubSubmitter{err: app.NewConflict("wager_transactions_idempotency_key")}

	rec := submitRequestFor(t, submitter, "reused-key", validSubmitBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got := decodeBody[ErrorBody](t, rec).Code; got != "IDEMPOTENCY_KEY_REUSED" {
		t.Errorf("code = %q", got)
	}
}

// Reversals are implemented now. The mapping stays, and this asserts it still
// works, because "valid request this build cannot serve" is a thing that will
// happen again -- 400 would blame the client for something it got right.
func TestAnUnsupportedRequestAnswers501(t *testing.T) {
	t.Parallel()
	submitter := &stubSubmitter{err: app.ErrNotImplemented}

	rec := submitRequestFor(t, submitter, "a-key", validSubmitBody)
	// The request is valid and this build cannot serve it. Saying 400 would
	// blame the client for something it got right.
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
	if got := decodeBody[ErrorBody](t, rec).Code; got != "NOT_IMPLEMENTED" {
		t.Errorf("code = %q", got)
	}
}

func TestSubmitRejectsAMissingMoney(t *testing.T) {
	t.Parallel()
	rec := submitRequestFor(t, &stubSubmitter{}, "a-key",
		`{"providerId":"p","externalTransactionId":"e","playerId":"pl","walletId":"w","roundId":"r","gameId":"g","kind":"BET"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestGetTransactionByProviderUsesBothPathValues(t *testing.T) {
	t.Parallel()
	reader := &stubTxReader{transaction: sampleTransaction(t, domain.StatusProcessed, "")}
	handler := NewTransactionHandler(&stubSubmitter{}, reader)
	mux := Handler(testRoutes(t, NewWalletHandler(&stubOpener{}, &stubReader{}), handler))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authenticated(httptest.NewRequest(http.MethodGet,
		"/providers/provider-a/wagering/transactions/transaction-123", nil)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if reader.gotProvider != "provider-a" || reader.gotExternal != "transaction-123" {
		t.Fatalf("got provider %q external %q", reader.gotProvider, reader.gotExternal)
	}
}

func TestAnUnprocessedTransactionHasNoBalance(t *testing.T) {
	t.Parallel()
	reader := &stubTxReader{transaction: sampleTransaction(t, domain.StatusRejected, domain.CodeInsufficientFunds)}
	handler := NewTransactionHandler(&stubSubmitter{}, reader)
	mux := Handler(testRoutes(t, NewWalletHandler(&stubOpener{}, &stubReader{}), handler))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authenticated(httptest.NewRequest(http.MethodGet, "/wagering/transactions/tx-1", nil)))

	body := decodeBody[transactionResponse](t, rec)
	// Rendering zero would be a lie about what the wallet held.
	if body.Balance != "" {
		t.Errorf("balanceAfter = %q on a rejected operation", body.Balance)
	}
	if body.FailureCode != string(domain.CodeInsufficientFunds) {
		t.Errorf("failure code = %q", body.FailureCode)
	}
}

func TestAWaitingReversalAnswers202(t *testing.T) {
	t.Parallel()
	transaction := sampleTransaction(t, domain.StatusPendingReference, "")
	submitter := &stubSubmitter{result: app.SubmitResult{Transaction: transaction}}

	rec := submitRequestFor(t, submitter, "provider-a:transaction-123", validSubmitBody)
	// Nothing moved yet, so not 200; it still might, so not an error.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if got := decodeBody[submitResponse](t, rec).Status; got != "PENDING_REFERENCE" {
		t.Errorf("status = %q", got)
	}
}
