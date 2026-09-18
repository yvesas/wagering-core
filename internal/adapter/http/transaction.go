package http

import (
	"context"
	"fmt"
	"net/http"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

// IdempotencyKeyHeader carries the key. It is required, and the server never
// substitutes one of its own: a client may well build it as
// "{providerId}:{externalTransactionId}", but quietly computing that when the
// header is missing would mean accepting a request the client never made
// idempotent.
const IdempotencyKeyHeader = "Idempotency-Key"

type submitRequest struct {
	ProviderID          string    `json:"providerId"`
	ExternalID          string    `json:"externalTransactionId"`
	PlayerID            string    `json:"playerId"`
	WalletID            string    `json:"walletId"`
	RoundID             string    `json:"roundId"`
	GameID              string    `json:"gameId"`
	Kind                string    `json:"kind"`
	Money               *moneyDTO `json:"money"`
	ReferenceExternalID string    `json:"referenceExternalTransactionId,omitempty"`
}

type submitResponse struct {
	TransactionID    string   `json:"transactionId"`
	Status           string   `json:"status"`
	Balance          moneyDTO `json:"balance"`
	IdempotentReplay bool     `json:"idempotentReplay"`
	FailureCode      string   `json:"failureCode,omitempty"`
}

type transactionResponse struct {
	TransactionID       string   `json:"transactionId"`
	ExternalID          string   `json:"externalTransactionId,omitempty"`
	ProviderID          string   `json:"providerId,omitempty"`
	WalletID            string   `json:"walletId"`
	PlayerID            string   `json:"playerId"`
	RoundID             string   `json:"roundId,omitempty"`
	GameID              string   `json:"gameId,omitempty"`
	Kind                string   `json:"kind"`
	Status              string   `json:"status"`
	Money               moneyDTO `json:"money"`
	ReferenceExternalID string   `json:"referenceExternalTransactionId,omitempty"`
	FailureCode         string   `json:"failureCode,omitempty"`
	Balance             string   `json:"balanceAfter,omitempty"`
	CreatedAt           string   `json:"createdAt"`
	UpdatedAt           string   `json:"updatedAt"`
}

// TransactionSubmitter and TransactionReader are the small interfaces this
// handler consumes, for the same reason the wallet handler declares its own.
type TransactionSubmitter interface {
	Execute(ctx context.Context, cmd app.SubmitCommand) (app.SubmitResult, error)
}

type TransactionReader interface {
	Get(ctx context.Context, transactionID string) (domain.WagerTransaction, error)
	GetByBusinessID(ctx context.Context, providerID, externalID string) (domain.WagerTransaction, error)
}

// TransactionHandler serves the wagering endpoints.
type TransactionHandler struct {
	submit  TransactionSubmitter
	queries TransactionReader
}

func NewTransactionHandler(submit TransactionSubmitter, queries TransactionReader) *TransactionHandler {
	return &TransactionHandler{submit: submit, queries: queries}
}

// Submit handles POST /wagering/transactions.
func (h *TransactionHandler) Submit(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get(IdempotencyKeyHeader)
	if key == "" {
		writeError(w, r, fmt.Errorf("%w: the %s header is required",
			app.ErrInvalidInput, IdempotencyKeyHeader))
		return
	}

	var req submitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Money == nil {
		writeError(w, r, fmt.Errorf("%w: money is required", app.ErrInvalidInput))
		return
	}

	result, err := h.submit.Execute(r.Context(), app.SubmitCommand{
		IdempotencyKey:      key,
		ProviderID:          req.ProviderID,
		ExternalID:          req.ExternalID,
		PlayerID:            req.PlayerID,
		WalletID:            req.WalletID,
		RoundID:             req.RoundID,
		GameID:              req.GameID,
		Kind:                req.Kind,
		Amount:              req.Money.Amount,
		Currency:            req.Money.Currency,
		ReferenceExternalID: req.ReferenceExternalID,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, r, submitStatus(result), submitResponse{
		TransactionID:    result.Transaction.ID().String(),
		Status:           string(result.Transaction.Status()),
		Balance:          toMoneyDTO(result.Balance),
		IdempotentReplay: result.Replay,
		FailureCode:      string(result.Transaction.FailureCode()),
	})
}

// submitStatus turns the recorded outcome into a status code.
//
// A rejection is 422: the request was understood and a rule refused it.
// Crucially, a replay of that rejection answers 422 too -- the stored outcome is
// the answer, and a resend that suddenly returned 200 would tell a provider its
// bet went through.
func submitStatus(result app.SubmitResult) int {
	switch result.Transaction.Status() {
	case domain.StatusRejected, domain.StatusFailed:
		return http.StatusUnprocessableEntity
	case domain.StatusPending, domain.StatusPendingReference:
		// Accepted and not yet applied: a reversal whose target has not arrived
		// is waiting for it. 202 rather than 200 because nothing has moved yet,
		// and rather than an error because it still might.
		return http.StatusAccepted
	default:
		return http.StatusOK
	}
}

// Get handles GET /wagering/transactions/{transactionId}.
func (h *TransactionHandler) Get(w http.ResponseWriter, r *http.Request) {
	transaction, err := h.queries.Get(r.Context(), r.PathValue("transactionId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, toTransactionResponse(transaction))
}

// GetByProvider handles
// GET /providers/{providerId}/wagering/transactions/{externalTransactionId}.
func (h *TransactionHandler) GetByProvider(w http.ResponseWriter, r *http.Request) {
	transaction, err := h.queries.GetByBusinessID(r.Context(),
		r.PathValue("providerId"), r.PathValue("externalTransactionId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, toTransactionResponse(transaction))
}

func toTransactionResponse(t domain.WagerTransaction) transactionResponse {
	body := transactionResponse{
		TransactionID:       t.ID().String(),
		ExternalID:          t.ExternalID().String(),
		ProviderID:          t.ProviderID().String(),
		WalletID:            t.WalletID().String(),
		PlayerID:            t.PlayerID().String(),
		RoundID:             t.RoundID().String(),
		GameID:              t.GameID().String(),
		Kind:                string(t.Kind()),
		Status:              string(t.Status()),
		Money:               toMoneyDTO(t.Money()),
		ReferenceExternalID: t.ReferenceExternalID().String(),
		FailureCode:         string(t.FailureCode()),
		CreatedAt:           t.CreatedAt().Format(rfc3339Millis),
		UpdatedAt:           t.UpdatedAt().Format(rfc3339Millis),
	}
	// An operation that has not been processed has no observed balance, and
	// rendering zero would be a lie about what the wallet held.
	if t.BalanceAfter().IsInitialised() {
		body.Balance = t.BalanceAfter().String()
	}
	return body
}
