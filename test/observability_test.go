//go:build integration

package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// These run the real thing: three processes, a real database, a real token, and
// the exposition endpoint each instance serves on a port of its own.
//
//	make up-test && make test-scenarios

func TestTheMetricsEndpointIsSeparateAndNeedsNoCredential(t *testing.T) {
	c := startCluster(t)

	// No Authorization header anywhere in this test. A scraper is not a
	// provider; it has no client in the realm and nothing to renew, which is
	// the whole reason this listener is not the business one.
	body, status := get(t, c.metrics[0]+"/metrics", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, body)
	}
	// A counter with labels does not exist until some label combination is
	// incremented, and this instance has settled nothing yet -- so the series
	// to look for is one of the unlabelled ones, which is exported at zero from
	// the first scrape.
	if !strings.Contains(body, "wagering_wallet_contention_total 0") {
		t.Errorf("the scrape has none of this service's metrics:\n%s", firstLines(body, 20))
	}
	if !strings.Contains(body, "go_goroutines") {
		t.Error("the runtime collectors are not registered")
	}

	// And the business port does not serve it: the two surfaces are separate,
	// not the same mux on two addresses.
	body, status = get(t, c.next(0)+"/metrics", "")
	if status != http.StatusNotFound {
		t.Errorf("the business port answered %d for /metrics: %s", status, body)
	}
}

func TestABetMovesTheCounters(t *testing.T) {
	c := startCluster(t)

	suffix := time.Now().Format("150405.000000000")
	player := "player-obs-" + suffix
	walletID := openWallet(t, c.next(0), player, "100.00")

	before := counterValue(t, c.metrics[0], `wagering_operations_total{kind="BET",source="http",status="PROCESSED"}`)

	external := "obs-bet-" + suffix
	body, status := submit(t, c.next(0), "provider-a:"+external,
		bet(player, walletID, external, "25.00"))
	if status != http.StatusOK {
		t.Fatalf("the bet: %d %s", status, body)
	}

	after := counterValue(t, c.metrics[0], `wagering_operations_total{kind="BET",source="http",status="PROCESSED"}`)
	if after != before+1 {
		t.Fatalf("the counter went from %v to %v, want one more", before, after)
	}

	// The route label is the pattern, not the path. One series per wallet is
	// how a metrics backend is taken down by the instrumentation meant to watch
	// it, and the wallet ids here are unique per run -- so if the path leaked
	// in, this run would have planted its own.
	scrape, _ := get(t, c.metrics[0]+"/metrics", "")
	if strings.Contains(scrape, walletID) {
		t.Errorf("a wallet id reached a metric label: %s", walletID)
	}
	// The pattern this test actually exercised. A wildcard route is covered by
	// the wallet id check above: if the path had leaked in, the unique id this
	// run minted would be sitting in a label right now.
	if !strings.Contains(scrape, `route="/wagering/transactions"`) {
		t.Errorf("the route label is not the registered pattern:\n%s",
			linesMatching(scrape, "wagering_http_request_duration_seconds_count"))
	}
}

func TestReconciliationOverTheWholeStack(t *testing.T) {
	c := startCluster(t)

	suffix := time.Now().Format("150405.000000000")
	player := "player-rec-" + suffix
	walletID := openWallet(t, c.next(0), player, "100.00")

	external := "rec-bet-" + suffix
	if body, status := submit(t, c.next(0), "provider-a:"+external,
		bet(player, walletID, external, "25.00")); status != http.StatusOK {
		t.Fatalf("the bet: %d %s", status, body)
	}

	t.Run("the platform gets a verdict", func(t *testing.T) {
		// A different instance from the one that wrote, so the answer comes
		// from the database and not from anything one process remembers.
		body, status := post(t, c.next(1)+"/wallets/"+walletID+"/reconciliation",
			platformToken(t), "")
		if status != http.StatusOK {
			t.Fatalf("status = %d: %s", status, body)
		}

		var result struct {
			Consistent bool                    `json:"consistent"`
			Entries    int                     `json:"entries"`
			Stored     struct{ Amount string } `json:"storedBalance"`
			Rebuilt    struct{ Amount string } `json:"rebuiltBalance"`
			Difference struct{ Amount string } `json:"difference"`
		}
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Fatalf("decoding %s: %v", body, err)
		}

		if !result.Consistent {
			t.Errorf("a healthy wallet drifted: stored %s, rebuilt %s",
				result.Stored.Amount, result.Rebuilt.Amount)
		}
		if result.Stored.Amount != "75.00" || result.Rebuilt.Amount != "75.00" {
			t.Errorf("stored %s, rebuilt %s", result.Stored.Amount, result.Rebuilt.Amount)
		}
		// The opening credit and the bet: the opening is an ordinary ledger
		// entry, which is why "including the opening" needs no special case.
		if result.Entries != 2 {
			t.Errorf("entries = %d, want 2", result.Entries)
		}
		if result.Difference.Amount != "0.00" {
			t.Errorf("difference = %s", result.Difference.Amount)
		}
	})

	t.Run("a provider cannot run it", func(t *testing.T) {
		// It reads every movement a wallet ever had. A provider running it
		// against a player would be reading a statement it has no claim to.
		body, status := post(t, c.next(2)+"/wallets/"+walletID+"/reconciliation",
			providerToken(t, "provider-a"), "")
		if status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", status, body)
		}
	})

	t.Run("it is counted", func(t *testing.T) {
		if got := counterValue(t, c.metrics[1], `wagering_reconciliations_total{result="match"}`); got < 1 {
			t.Fatalf("match counter = %v, want at least 1", got)
		}
	})
}

// counterValue reads one series out of a scrape. It returns zero when the
// series is not there yet, which is what a counter that has never moved looks
// like to Prometheus anyway.
func counterValue(t *testing.T, base, series string) float64 {
	t.Helper()

	body, status := get(t, base+"/metrics", "")
	if status != http.StatusOK {
		t.Fatalf("scraping %s: %d", base, status)
	}

	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, series) {
			continue
		}
		var value float64
		if _, err := fmt.Sscanf(strings.TrimPrefix(line, series), "%g", &value); err != nil {
			t.Fatalf("reading %q: %v", line, err)
		}
		return value
	}
	return 0
}

// linesMatching picks the lines of a scrape that mention a series, so a failure
// shows what was exported instead of the whole page.
func linesMatching(body, series string) string {
	var out []string
	for line := range strings.Lines(body) {
		if strings.Contains(line, series) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.Join(out, "\n")
}

func firstLines(body string, n int) string {
	lines := strings.SplitN(body, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
