package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

func reconciliationFor(t *testing.T, stored, rebuilt string) app.Reconciliation {
	t.Helper()

	currency, err := domain.ParseCurrency("BRL")
	if err != nil {
		t.Fatalf("parsing the currency: %v", err)
	}
	walletID, err := domain.ParseWalletID("wallet-1")
	if err != nil {
		t.Fatalf("parsing the wallet id: %v", err)
	}

	money := func(amount string) domain.Money {
		t.Helper()
		value, err := domain.ParseMoney(amount, currency)
		if err != nil {
			t.Fatalf("parsing %s: %v", amount, err)
		}
		return value
	}

	difference, err := money(stored).Sub(money(rebuilt))
	if err != nil {
		t.Fatalf("subtracting: %v", err)
	}

	return app.Reconciliation{
		WalletID:   walletID,
		Stored:     money(stored),
		Rebuilt:    money(rebuilt),
		Difference: difference,
		Entries:    3,
		Version:    4,
		CheckedAt:  time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
	}
}

func checkReconciliation(t *testing.T, reconciler *stubReconciler) *httptest.ResponseRecorder {
	t.Helper()

	mux := Handler(Routes(testAuth(t), nil,
		NewWalletHandler(&stubOpener{}, &stubReader{}),
		NewTransactionHandler(&stubSubmitter{}, &stubTxReader{}),
		NewReconciliationHandler(reconciler),
		NewHealthHandler()))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authenticated(httptest.NewRequest(
		http.MethodPost, "/wallets/wallet-1/reconciliation", nil)))
	return rec
}

func TestReconciliationReportsAgreement(t *testing.T) {
	t.Parallel()
	reconciler := &stubReconciler{result: reconciliationFor(t, "75.00", "75.00")}

	rec := checkReconciliation(t, reconciler)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	body := decodeBody[reconciliationResponse](t, rec)
	if !body.Consistent {
		t.Error("a wallet that matches its ledger was reported inconsistent")
	}
	if body.Stored.Amount != "75.00" || body.Rebuilt.Amount != "75.00" {
		t.Errorf("stored %q, rebuilt %q", body.Stored.Amount, body.Rebuilt.Amount)
	}
	if reconciler.got != "wallet-1" {
		t.Errorf("the use case was asked about %q", reconciler.got)
	}
}

func TestADriftIsStillASuccessfulCheck(t *testing.T) {
	t.Parallel()
	reconciler := &stubReconciler{result: reconciliationFor(t, "40.00", "100.00")}

	rec := checkReconciliation(t, reconciler)

	// 200, not 409. The caller asked whether the two agree, and answering
	// "they do not" is this endpoint doing its job -- a non-2xx would make
	// every monitor treat a working check as a broken request, which is
	// backwards for the one endpoint whose failure mode is being ignored.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	body := decodeBody[reconciliationResponse](t, rec)
	if body.Consistent {
		t.Fatal("a wallet 60.00 short of its ledger was reported consistent")
	}
	// Both numbers and the sign, because "they disagree" is the beginning of
	// an investigation and not the end of one.
	if body.Difference.Amount != "-60.00" {
		t.Errorf("difference = %q, want -60.00", body.Difference.Amount)
	}
	if body.Entries != 3 || body.Version != 4 {
		t.Errorf("entries = %d, version = %d", body.Entries, body.Version)
	}
}

func TestReconciliationPassesTheUseCaseRefusalThrough(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "a wallet that is not there", err: app.ErrNotFound, want: http.StatusNotFound},
		{name: "a credential without the scope", err: app.ErrForbidden, want: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := checkReconciliation(t, &stubReconciler{err: tc.err})
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestReconciliationIsNotReachableByGET(t *testing.T) {
	t.Parallel()
	mux := Handler(Routes(testAuth(t), nil,
		NewWalletHandler(&stubOpener{}, &stubReader{}),
		NewTransactionHandler(&stubSubmitter{}, &stubTxReader{}),
		NewReconciliationHandler(&stubReconciler{}),
		NewHealthHandler()))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authenticated(httptest.NewRequest(
		http.MethodGet, "/wallets/wallet-1/reconciliation", nil)))

	// It reads every ledger entry a wallet ever had. A GET invites a cache, a
	// prefetch and a retry-on-timeout, and none of those should decide how
	// often this runs.
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
