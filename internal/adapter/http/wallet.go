package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

// moneyDTO is how money crosses the wire: a decimal string and a currency,
// never a JSON number. A number invites the decoder on the other side to read
// it as a float, which is the one thing this system refuses.
type moneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type openWalletRequest struct {
	PlayerID       string    `json:"playerId"`
	InitialBalance *moneyDTO `json:"initialBalance"`
}

type walletResponse struct {
	ID       string   `json:"id"`
	PlayerID string   `json:"playerId"`
	Balance  moneyDTO `json:"balance"`
	Version  int64    `json:"version"`
}

type ledgerEntryResponse struct {
	ID            string   `json:"id"`
	TransactionID string   `json:"transactionId"`
	Direction     string   `json:"direction"`
	Money         moneyDTO `json:"money"`
	BalanceBefore moneyDTO `json:"balanceBefore"`
	BalanceAfter  moneyDTO `json:"balanceAfter"`
	CreatedAt     string   `json:"createdAt"`
}

type ledgerResponse struct {
	Entries    []ledgerEntryResponse `json:"entries"`
	NextCursor string                `json:"nextCursor,omitempty"`
	HasMore    bool                  `json:"hasMore"`
}

// WalletOpener and WalletReader are declared here, by the handler, rather than
// taking the concrete use-case types. They are small and single-purpose, and
// the practical payoff is that a handler test needs two stubs instead of a
// database, a pool and a dependency graph.
type WalletOpener interface {
	Execute(ctx context.Context, cmd app.OpenWalletCommand) (domain.Wallet, error)
}

type WalletReader interface {
	Get(ctx context.Context, walletID string) (domain.Wallet, error)
	Ledger(ctx context.Context, walletID string, cursor app.LedgerCursor, limit int) (app.LedgerPage, error)
}

// WalletHandler serves the wallet endpoints.
type WalletHandler struct {
	open    WalletOpener
	queries WalletReader
}

func NewWalletHandler(open WalletOpener, queries WalletReader) *WalletHandler {
	return &WalletHandler{open: open, queries: queries}
}

// Open handles POST /wallets.
func (h *WalletHandler) Open(w http.ResponseWriter, r *http.Request) {
	var req openWalletRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.InitialBalance == nil {
		writeError(w, r, fmt.Errorf("%w: initialBalance is required", app.ErrInvalidInput))
		return
	}

	wallet, err := h.open.Execute(r.Context(), app.OpenWalletCommand{
		PlayerID: req.PlayerID,
		Amount:   req.InitialBalance.Amount,
		Currency: req.InitialBalance.Currency,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, r, http.StatusCreated, toWalletResponse(wallet))
}

// Get handles GET /wallets/{walletId}.
func (h *WalletHandler) Get(w http.ResponseWriter, r *http.Request) {
	wallet, err := h.queries.Get(r.Context(), r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, toWalletResponse(wallet))
}

// Ledger handles GET /wallets/{walletId}/ledger.
func (h *WalletHandler) Ledger(w http.ResponseWriter, r *http.Request) {
	cursor, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, err)
		return
	}

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, r, fmt.Errorf("%w: limit %q is not a number", app.ErrInvalidInput, raw))
			return
		}
	}

	page, err := h.queries.Ledger(r.Context(), r.PathValue("walletId"), cursor, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}

	body := ledgerResponse{
		Entries: make([]ledgerEntryResponse, 0, len(page.Entries)),
		HasMore: page.HasMore,
	}
	for _, entry := range page.Entries {
		body.Entries = append(body.Entries, toLedgerEntryResponse(entry))
	}
	if page.HasMore {
		body.NextCursor = encodeCursor(page.Next)
	}

	writeJSON(w, r, http.StatusOK, body)
}

func toWalletResponse(wallet domain.Wallet) walletResponse {
	return walletResponse{
		ID:       wallet.ID().String(),
		PlayerID: wallet.PlayerID().String(),
		Balance:  toMoneyDTO(wallet.Balance()),
		Version:  wallet.Version(),
	}
}

func toLedgerEntryResponse(entry domain.LedgerEntry) ledgerEntryResponse {
	return ledgerEntryResponse{
		ID:            entry.ID().String(),
		TransactionID: entry.TransactionID().String(),
		Direction:     string(entry.Direction()),
		Money:         toMoneyDTO(entry.Amount()),
		BalanceBefore: toMoneyDTO(entry.BalanceBefore()),
		BalanceAfter:  toMoneyDTO(entry.BalanceAfter()),
		CreatedAt:     entry.CreatedAt().Format(rfc3339Millis),
	}
}

func toMoneyDTO(m domain.Money) moneyDTO {
	return moneyDTO{Amount: m.String(), Currency: m.Currency().String()}
}

// rfc3339Millis is the timestamp format on the wire: UTC, RFC 3339, with
// milliseconds so two events in the same second stay ordered for a reader.
const rfc3339Millis = "2006-01-02T15:04:05.000Z07:00"

// maxRequestBody caps what a handler will read. Without it, one request can
// hold an arbitrary amount of memory before the first field is even parsed.
const maxRequestBody = 1 << 20 // 1 MiB

func decodeJSON(r *http.Request, into any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxRequestBody))

	// An unknown field is a rejection, not something to ignore. A client that
	// sends "ammount" has a bug, and silently dropping it would turn that bug
	// into a wrong balance nobody can explain later.
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("%w: %s", app.ErrInvalidInput, cleanDecodeError(err))
	}
	// Exactly one JSON value, so a body with trailing content is refused too.
	if err := decoder.Decode(new(struct{})); err == nil {
		return fmt.Errorf("%w: the body carries more than one JSON value", app.ErrInvalidInput)
	}
	return nil
}

// cleanDecodeError keeps the decoder's message useful without leaking the
// offset of a reader the client never saw.
func cleanDecodeError(err error) string {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return fmt.Sprintf("the body is larger than %d bytes", maxErr.Limit)
	}
	return err.Error()
}

// The cursor is opaque: the client hands back what it was given. The prefix is
// there so a future change of encoding can be told apart from a corrupted one,
// rather than being parsed as garbage.
const cursorPrefix = "v1:"

func encodeCursor(c app.LedgerCursor) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(cursorPrefix + strconv.FormatInt(c.After(), 10)))
}

func decodeCursor(raw string) (app.LedgerCursor, error) {
	if raw == "" {
		return app.NewLedgerCursor(0), nil
	}

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return app.LedgerCursor{}, fmt.Errorf("%w: the cursor is not valid", app.ErrInvalidInput)
	}
	text, found := strings.CutPrefix(string(decoded), cursorPrefix)
	if !found {
		return app.LedgerCursor{}, fmt.Errorf("%w: the cursor is not valid", app.ErrInvalidInput)
	}
	after, err := strconv.ParseInt(text, 10, 64)
	if err != nil || after < 0 {
		return app.LedgerCursor{}, fmt.Errorf("%w: the cursor is not valid", app.ErrInvalidInput)
	}
	return app.NewLedgerCursor(after), nil
}
