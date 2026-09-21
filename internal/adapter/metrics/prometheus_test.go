package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/app"
)

func scrape(t *testing.T, recorder *Recorder) string {
	t.Helper()

	rec := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("the exposition endpoint answered %d: %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func TestWhatWasRecordedIsWhatIsExposed(t *testing.T) {
	t.Parallel()
	recorder := New()

	recorder.OperationSettled(app.SourceHTTP, "BET", "PROCESSED")
	recorder.OperationSettled(app.SourceQueue, "BET", "REJECTED")
	recorder.OperationLatency(app.SourceHTTP, 12*time.Millisecond)
	recorder.WalletContention()
	recorder.TransactionRetried()
	recorder.MessageDeduplicated()
	recorder.MessageDiscarded(app.DiscardMalformed)
	recorder.MessageReleased()
	recorder.EventPublished(300 * time.Millisecond)
	recorder.EventPublishFailed()
	recorder.ReconciliationRun(true)
	recorder.RequestObserved("/wallets/{walletId}", "GET", "200", time.Millisecond)

	body := scrape(t, recorder)

	// The exact series names, because they are the contract with whatever
	// dashboard and alert rule is written against them. Renaming one is not a
	// refactor: it is a silently broken alert.
	for _, want := range []string{
		`wagering_operations_total{kind="BET",source="http",status="PROCESSED"} 1`,
		`wagering_operations_total{kind="BET",source="queue",status="REJECTED"} 1`,
		`wagering_wallet_contention_total 1`,
		`wagering_transaction_retries_total 1`,
		`wagering_queue_duplicates_total 1`,
		`wagering_queue_discarded_total{reason="malformed"} 1`,
		`wagering_queue_released_total 1`,
		`wagering_outbox_publish_failures_total 1`,
		`wagering_reconciliations_total{result="drift"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape does not contain %s", want)
		}
	}

	for _, want := range []string{
		"wagering_operation_duration_seconds_bucket",
		"wagering_outbox_publish_lag_seconds_bucket",
		"wagering_http_request_duration_seconds_bucket",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape has no histogram %s", want)
		}
	}
}

func TestBothReconciliationOutcomesAreCounted(t *testing.T) {
	t.Parallel()
	recorder := New()

	recorder.ReconciliationRun(false)
	recorder.ReconciliationRun(false)
	recorder.ReconciliationRun(true)

	body := scrape(t, recorder)

	// A counter that only moved on failure could not tell "nothing is wrong"
	// from "nothing has run", and those need very different responses at three
	// in the morning.
	if !strings.Contains(body, `wagering_reconciliations_total{result="match"} 2`) {
		t.Error("matches are not counted")
	}
	if !strings.Contains(body, `wagering_reconciliations_total{result="drift"} 1`) {
		t.Error("drifts are not counted")
	}
}

func TestTheRuntimeCollectorsAreRegistered(t *testing.T) {
	t.Parallel()
	body := scrape(t, New())

	// Heap and goroutine count answer "is the process healthy" before any
	// business metric does, and they are free.
	for _, want := range []string{"go_goroutines", "go_memstats_alloc_bytes"} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape has no %s", want)
		}
	}
}

func TestAClockSkewedLagIsNotRecordedAsNegative(t *testing.T) {
	t.Parallel()
	recorder := New()

	// An event cannot be published before it existed; a negative lag means two
	// clocks disagree. Recording it would drag the histogram's sum below zero,
	// where no rate() over it makes sense again.
	recorder.EventPublished(-5 * time.Second)

	if !strings.Contains(scrape(t, recorder), "wagering_outbox_publish_lag_seconds_sum 0") {
		t.Error("a negative lag reached the histogram")
	}
}

func TestTwoRecordersDoNotCollide(t *testing.T) {
	t.Parallel()

	// Each recorder owns its registry. Sharing the default one would make a
	// second instance -- a test, a second graph in one process -- panic on a
	// duplicate registration at start-up.
	first, second := New(), New()
	first.WalletContention()

	if !strings.Contains(scrape(t, first), "wagering_wallet_contention_total 1") {
		t.Error("the first recorder lost its count")
	}
	if !strings.Contains(scrape(t, second), "wagering_wallet_contention_total 0") {
		t.Error("the second recorder saw the first one's count")
	}
}
