package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

type stubOpener struct {
	wallet domain.Wallet
	err    error
	got    app.OpenWalletCommand

	// inspect sees the caller the edge established, which is how a test proves
	// the identity travelled rather than that the handler compiled.
	inspect func(app.Identity)
}

func (s *stubOpener) Execute(ctx context.Context, cmd app.OpenWalletCommand) (domain.Wallet, error) {
	s.got = cmd
	if s.inspect != nil {
		identity, _ := app.IdentityFrom(ctx)
		s.inspect(identity)
	}
	return s.wallet, s.err
}

type stubReader struct {
	wallet domain.Wallet
	page   app.LedgerPage
	err    error

	gotCursor app.LedgerCursor
	gotLimit  int
}

func (s *stubReader) Get(context.Context, string) (domain.Wallet, error) {
	return s.wallet, s.err
}

func (s *stubReader) Ledger(_ context.Context, _ string, cursor app.LedgerCursor, limit int) (app.LedgerPage, error) {
	s.gotCursor, s.gotLimit = cursor, limit
	return s.page, s.err
}

func sampleWallet(t *testing.T) domain.Wallet {
	t.Helper()
	currency, err := domain.ParseCurrency("BRL")
	if err != nil {
		t.Fatal(err)
	}
	balance, err := domain.ParseMoney("1000.00", currency)
	if err != nil {
		t.Fatal(err)
	}
	id, err := domain.ParseWalletID("wallet-1")
	if err != nil {
		t.Fatal(err)
	}
	player, err := domain.ParsePlayerID("player-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	wallet, err := domain.RehydrateWallet(domain.RehydrateWalletParams{
		ID: id, PlayerID: player, Balance: balance, Version: 1, CreatedAt: at, UpdatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return wallet
}

func serve(t *testing.T, handler *WalletHandler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := Routes(testAuth(t), handler, NewTransactionHandler(&stubSubmitter{}, &stubTxReader{}), NewHealthHandler())

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := authenticated(httptest.NewRequest(method, target, reader))
	rec := httptest.NewRecorder()
	Handler(mux).ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	return out
}

func TestOpenWalletHandler(t *testing.T) {
	t.Parallel()
	opener := &stubOpener{wallet: sampleWallet(t)}
	handler := NewWalletHandler(opener, &stubReader{})

	rec := serve(t, handler, http.MethodPost, "/wallets",
		`{"playerId":"player-1","initialBalance":{"amount":"1000.00","currency":"BRL"}}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	// The strings reach the use case untouched. Any cleverness here -- trimming,
	// upcasing a currency -- would be a normalisation the domain never agreed to.
	if opener.got.Amount != "1000.00" || opener.got.Currency != "BRL" || opener.got.PlayerID != "player-1" {
		t.Fatalf("the command was altered on the way: %+v", opener.got)
	}

	body := decodeBody[walletResponse](t, rec)
	if body.Balance.Amount != "1000.00" || body.Balance.Currency != "BRL" {
		t.Errorf("balance = %+v", body.Balance)
	}
	if body.Version != 1 {
		t.Errorf("version = %d", body.Version)
	}
}

func TestMoneyIsAlwaysAStringOnTheWire(t *testing.T) {
	t.Parallel()
	handler := NewWalletHandler(&stubOpener{wallet: sampleWallet(t)}, &stubReader{})
	rec := serve(t, handler, http.MethodPost, "/wallets",
		`{"playerId":"player-1","initialBalance":{"amount":"1000.00","currency":"BRL"}}`)

	// A JSON number here would invite the decoder on the other side to read it
	// as a float, which is the one thing this system refuses. Checking the raw
	// bytes is the only way to see it: a struct field would hide the difference.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	balance, ok := raw["balance"].(map[string]any)
	if !ok {
		t.Fatalf("balance is %T", raw["balance"])
	}
	if _, ok := balance["amount"].(string); !ok {
		t.Fatalf("amount is %T, want a string", balance["amount"])
	}
}

func TestOpenWalletHandlerRejectsBadBodies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		code string
	}{
		{"not json", `{`, "INVALID_INPUT"},
		{"empty body", ``, "INVALID_INPUT"},
		{"amount as a number", `{"playerId":"p","initialBalance":{"amount":25.00,"currency":"BRL"}}`, "INVALID_INPUT"},
		// A client that sends "ammount" has a bug, and dropping the field
		// silently would turn that bug into a wrong balance nobody can explain.
		{"unknown field", `{"playerId":"p","ammount":"1.00"}`, "INVALID_INPUT"},
		{"missing initialBalance", `{"playerId":"p"}`, "INVALID_INPUT"},
		{"two json values", `{"playerId":"p","initialBalance":{"amount":"1.00","currency":"BRL"}}{}`, "INVALID_INPUT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler := NewWalletHandler(&stubOpener{wallet: sampleWallet(t)}, &stubReader{})
			rec := serve(t, handler, http.MethodPost, "/wallets", tc.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			if got := decodeBody[ErrorBody](t, rec).Code; got != tc.code {
				t.Errorf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestGetWalletNotFound(t *testing.T) {
	t.Parallel()
	handler := NewWalletHandler(&stubOpener{}, &stubReader{err: app.ErrNotFound})
	rec := serve(t, handler, http.MethodGet, "/wallets/missing", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := decodeBody[ErrorBody](t, rec).Code; got != "NOT_FOUND" {
		t.Errorf("code = %q", got)
	}
}

func TestConflictDoesNotLeakTheConstraintName(t *testing.T) {
	t.Parallel()
	handler := NewWalletHandler(
		&stubOpener{err: app.NewConflict("wallets_one_per_player_and_currency")},
		&stubReader{})

	rec := serve(t, handler, http.MethodPost, "/wallets",
		`{"playerId":"p","initialBalance":{"amount":"1.00","currency":"BRL"}}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	body := decodeBody[ErrorBody](t, rec)
	// The code is stable and belongs to the API; the constraint name is a
	// schema detail, and renaming it is a migration rather than a breaking
	// change for every client.
	if body.Code != "WALLET_ALREADY_EXISTS" {
		t.Errorf("code = %q", body.Code)
	}
	if strings.Contains(rec.Body.String(), "wallets_one_per_player") {
		t.Errorf("the constraint name reached the client: %s", rec.Body)
	}
}

func TestInternalErrorsDoNotReachTheClient(t *testing.T) {
	t.Parallel()
	handler := NewWalletHandler(
		&stubOpener{err: app.NewInvariantViolation("ledger_arithmetic")},
		&stubReader{})

	rec := serve(t, handler, http.MethodPost, "/wallets",
		`{"playerId":"p","initialBalance":{"amount":"1.00","currency":"BRL"}}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	// A 5xx message can carry a constraint, a column, a fragment of SQL. The
	// detail goes to the log with the correlation id instead.
	if strings.Contains(rec.Body.String(), "ledger_arithmetic") {
		t.Errorf("an internal detail reached the client: %s", rec.Body)
	}
	if decodeBody[ErrorBody](t, rec).CorrelationID == "" {
		t.Error("no correlation id to match the response against the log")
	}
}

func TestCorrelationIDIsEchoedAndReused(t *testing.T) {
	t.Parallel()
	handler := NewWalletHandler(&stubOpener{wallet: sampleWallet(t)}, &stubReader{})
	mux := Handler(Routes(testAuth(t), handler, NewTransactionHandler(&stubSubmitter{}, &stubTxReader{}), NewHealthHandler()))

	t.Run("generated when absent", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))
		if rec.Header().Get(CorrelationIDHeader) == "" {
			t.Error("no correlation id was assigned")
		}
	})

	t.Run("an upstream id is kept", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		req.Header.Set(CorrelationIDHeader, "from-upstream")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// A trace that started upstream keeps its identity through this
		// service, which is the only way the two logs can be joined later.
		if got := rec.Header().Get(CorrelationIDHeader); got != "from-upstream" {
			t.Errorf("correlation id = %q", got)
		}
	})

	t.Run("an absurd id is replaced", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		req.Header.Set(CorrelationIDHeader, strings.Repeat("x", maxCorrelationIDLength+1))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// The value lands in every log line for the request. Without a cap the
		// client decides how much it costs to log one call.
		if got := rec.Header().Get(CorrelationIDHeader); len(got) > maxCorrelationIDLength {
			t.Errorf("the oversized id was kept (%d bytes)", len(got))
		}
	})
}

func TestLedgerCursorRoundTrip(t *testing.T) {
	t.Parallel()
	for _, after := range []int64{0, 1, 42, 1 << 40} {
		encoded := encodeCursor(app.NewLedgerCursor(after))
		decoded, err := decodeCursor(encoded)
		if err != nil {
			t.Fatalf("decodeCursor(%q): %v", encoded, err)
		}
		if decoded.After() != after {
			t.Errorf("round trip of %d gave %d", after, decoded.After())
		}
		// Opaque means opaque: the number must not be readable in the token,
		// or a client will start constructing its own.
		if strings.Contains(encoded, "42") && after == 42 {
			t.Errorf("the cursor %q exposes its position", encoded)
		}
	}
}

func TestLedgerCursorRejectsGarbage(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"not-base64!!", "YWJj", "djE6", "djE6LTE"} {
		if _, err := decodeCursor(raw); err == nil {
			t.Errorf("decodeCursor(%q) was accepted", raw)
		}
	}
	// An absent cursor is the first page, not an error.
	if c, err := decodeCursor(""); err != nil || c.After() != 0 {
		t.Errorf("empty cursor gave (%v, %v)", c, err)
	}
}

func TestLedgerHandlerPassesTheCursorThrough(t *testing.T) {
	t.Parallel()
	reader := &stubReader{page: app.LedgerPage{Next: app.NewLedgerCursor(7), HasMore: true}}
	handler := NewWalletHandler(&stubOpener{}, reader)

	cursor := encodeCursor(app.NewLedgerCursor(3))
	rec := serve(t, handler, http.MethodGet, "/wallets/w1/ledger?cursor="+cursor+"&limit=5", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if reader.gotCursor.After() != 3 || reader.gotLimit != 5 {
		t.Fatalf("the query got cursor %d limit %d", reader.gotCursor.After(), reader.gotLimit)
	}

	body := decodeBody[ledgerResponse](t, rec)
	if !body.HasMore || body.NextCursor == "" {
		t.Fatalf("a page with more did not offer a next cursor: %+v", body)
	}
	next, err := decodeCursor(body.NextCursor)
	if err != nil || next.After() != 7 {
		t.Fatalf("the next cursor decoded to (%v, %v)", next, err)
	}
}

func TestLedgerHandlerRejectsABadLimit(t *testing.T) {
	t.Parallel()
	handler := NewWalletHandler(&stubOpener{}, &stubReader{})
	rec := serve(t, handler, http.MethodGet, "/wallets/w1/ledger?limit=abc", "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUnknownMethodAnswers405(t *testing.T) {
	t.Parallel()
	handler := NewWalletHandler(&stubOpener{}, &stubReader{})
	rec := serve(t, handler, http.MethodDelete, "/wallets/w1", "")

	// The ServeMux does this on its own because the patterns carry a method.
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if rec.Header().Get("Allow") == "" {
		t.Error("no Allow header")
	}
}

func TestPanicBecomesA500(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("a handler lost its mind")
	})

	rec := httptest.NewRecorder()
	Handler(mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	// A panic must not take the process down, and must not leak its message.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "lost its mind") {
		t.Errorf("the panic value reached the client: %s", rec.Body)
	}
}

func TestHealthLiveTouchesNoDependency(t *testing.T) {
	t.Parallel()
	// A liveness check that needs a database turns a brief database blip into
	// every replica being restarted at once.
	health := NewHealthHandler(failingProbe{})
	mux := Handler(Routes(testAuth(t), NewWalletHandler(&stubOpener{}, &stubReader{}),
		NewTransactionHandler(&stubSubmitter{}, &stubTxReader{}), health))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("live = %d with a failing dependency, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready = %d with a failing dependency, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unavailable") {
		t.Errorf("readiness did not name the state: %s", rec.Body)
	}
}

type failingProbe struct{}

func (failingProbe) Name() string               { return "postgres" }
func (failingProbe) Ping(context.Context) error { return context.DeadlineExceeded }
