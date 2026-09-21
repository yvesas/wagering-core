package http

import (
	"context"
	"net/http"

	"github.com/yvesas/wagering-core/internal/app"
)

// WalletReconciler is the use case this handler consumes.
type WalletReconciler interface {
	Execute(ctx context.Context, walletID string) (app.Reconciliation, error)
}

type reconciliationResponse struct {
	WalletID string `json:"walletId"`

	// Both numbers, always, and not only when they differ. "They disagree" is
	// the beginning of an investigation; the two values and the entry count are
	// what the person doing it needs before anything else.
	Stored     moneyDTO `json:"storedBalance"`
	Rebuilt    moneyDTO `json:"rebuiltBalance"`
	Difference moneyDTO `json:"difference"`

	Entries    int    `json:"entries"`
	Version    int64  `json:"walletVersion"`
	Consistent bool   `json:"consistent"`
	CheckedAt  string `json:"checkedAt"`
}

// ReconciliationHandler serves the reconciliation endpoint.
type ReconciliationHandler struct {
	reconcile WalletReconciler
}

func NewReconciliationHandler(reconcile WalletReconciler) *ReconciliationHandler {
	return &ReconciliationHandler{reconcile: reconcile}
}

// Check handles POST /wallets/{walletId}/reconciliation.
//
// POST rather than GET even though it changes nothing, because it is not free:
// it reads every ledger entry a wallet ever had. A GET invites a cache, a
// prefetch and a retry-on-timeout, and none of those should decide how often
// this runs.
//
// A drift answers 200, not 409. The caller asked whether the two agree, and
// answering "they do not" is this endpoint succeeding at its job -- a non-2xx
// would make every monitor treat a working check as a broken request, which is
// exactly backwards for the one endpoint whose failure mode is being ignored.
// The verdict is in the body, in the log and on a counter.
func (h *ReconciliationHandler) Check(w http.ResponseWriter, r *http.Request) {
	result, err := h.reconcile.Execute(r.Context(), r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, r, http.StatusOK, reconciliationResponse{
		WalletID:   result.WalletID.String(),
		Stored:     toMoneyDTO(result.Stored),
		Rebuilt:    toMoneyDTO(result.Rebuilt),
		Difference: toMoneyDTO(result.Difference),
		Entries:    result.Entries,
		Version:    result.Version,
		Consistent: !result.Drifted(),
		CheckedAt:  result.CheckedAt.Format(rfc3339Millis),
	})
}
