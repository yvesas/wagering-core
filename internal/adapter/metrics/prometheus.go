package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/yvesas/wagering-core/internal/app"
)

// namespace prefixes every series, so this service's numbers are one selector
// away in a shared Prometheus.
const namespace = "wagering"

// Recorder implements every metrics port in internal/app.
//
// One type for all of them because there is one registry and one exposition
// endpoint; the interfaces are narrow so a use case depends on the two methods
// it calls rather than on all of this.
type Recorder struct {
	registry *prometheus.Registry

	operations  *prometheus.CounterVec
	latency     *prometheus.HistogramVec
	contention  prometheus.Counter
	retries     prometheus.Counter
	deduped     prometheus.Counter
	discarded   *prometheus.CounterVec
	released    prometheus.Counter
	publishLag  prometheus.Histogram
	publishFail prometheus.Counter
	reconciled  *prometheus.CounterVec

	requests *prometheus.HistogramVec
}

// New builds the recorder and its own registry.
//
// Its own, rather than prometheus.DefaultRegisterer: a package-level registry is
// global state that any dependency can write to, and a duplicate registration
// from one of them panics at start-up. With a registry we own, what is exported
// is what this file says and nothing else.
func New() *Recorder {
	registry := prometheus.NewRegistry()

	// The Go runtime and process collectors, added explicitly. Heap, goroutine
	// count and file descriptors answer "is the process healthy" at three in
	// the morning, and they are free.
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	r := &Recorder{
		registry: registry,

		operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "operations_total",
			Help:      "Operations that reached a recorded outcome, by entry port, kind and status.",
		}, []string{"source", "kind", "status"}),

		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "operation_duration_seconds",
			Help:      "Time to settle one operation, by entry port.",
			// Buckets down to a millisecond and up to five seconds. The
			// interesting region for a wallet write is the few milliseconds a
			// locked row takes, and the default buckets start too coarse to
			// show it.
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"source"}),

		contention: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "wallet_contention_total",
			Help:      "Writes that lost a race over a single wallet.",
		}),

		retries: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "transaction_retries_total",
			Help:      "Units of work replayed after the database refused to order them.",
		}),

		deduped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "queue_duplicates_total",
			Help:      "Deliveries the inbox had already handled.",
		}),

		discarded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "queue_discarded_total",
			Help:      "Messages dropped for good, by reason.",
		}, []string{"reason"}),

		released: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "queue_released_total",
			Help:      "Deliveries handed back for redelivery.",
		}),

		publishLag: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "outbox_publish_lag_seconds",
			Help:      "Time between an event being recorded and being published.",
			// Wider than request latency and deliberately so: this one is
			// healthy in the hundreds of milliseconds and interesting in the
			// minutes, because minutes mean the publisher is behind or the
			// broker is refusing.
			Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 300},
		}),

		publishFail: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbox_publish_failures_total",
			Help:      "Publishes the broker refused.",
		}),

		reconciled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "reconciliations_total",
			Help:      "Reconciliation checks, by whether the balance and the ledger agreed.",
		}, []string{"result"}),

		requests: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "http_request_duration_seconds",
			Help:      "HTTP requests, by matched route and status.",
			Buckets:   []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"route", "method", "status"}),
	}

	registry.MustRegister(
		r.operations, r.latency, r.contention, r.retries,
		r.deduped, r.discarded, r.released,
		r.publishLag, r.publishFail, r.reconciled, r.requests,
	)
	return r
}

// Handler serves the exposition endpoint.
func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{
		// A broken collector must not take the scrape down; the error is
		// reported as a metric of its own and the rest is still served.
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// Gather exposes the registry for a test to read back what was recorded.
func (r *Recorder) Gather() prometheus.Gatherer { return r.registry }

// --- app.OperationMetrics --------------------------------------------------

func (r *Recorder) OperationSettled(source, kind, status string) {
	r.operations.WithLabelValues(source, kind, status).Inc()
}

func (r *Recorder) OperationLatency(source string, took time.Duration) {
	r.latency.WithLabelValues(source).Observe(took.Seconds())
}

func (r *Recorder) WalletContention() { r.contention.Inc() }

// --- app.StorageMetrics ----------------------------------------------------

func (r *Recorder) TransactionRetried() { r.retries.Inc() }

// --- app.QueueMetrics ------------------------------------------------------

func (r *Recorder) MessageDeduplicated() { r.deduped.Inc() }

func (r *Recorder) MessageDiscarded(reason string) {
	r.discarded.WithLabelValues(reason).Inc()
}

func (r *Recorder) MessageReleased() { r.released.Inc() }

// --- app.OutboxMetrics -----------------------------------------------------

func (r *Recorder) EventPublished(lag time.Duration) {
	// A negative lag means the clocks disagree, not that an event was published
	// before it existed. Recording it would drag the histogram's sum below
	// zero, where no rate() makes sense again.
	if lag < 0 {
		lag = 0
	}
	r.publishLag.Observe(lag.Seconds())
}

func (r *Recorder) EventPublishFailed() { r.publishFail.Inc() }

// --- app.ReconciliationMetrics ---------------------------------------------

func (r *Recorder) ReconciliationRun(drifted bool) {
	// Both outcomes are counted, so a dashboard can show the ratio. A counter
	// that only moved on failure could not tell "nothing is wrong" from
	// "nothing has run".
	result := "match"
	if drifted {
		result = "drift"
	}
	r.reconciled.WithLabelValues(result).Inc()
}

// --- the HTTP edge ---------------------------------------------------------

// RequestObserved times one request.
//
// Route is the *matched pattern*, never the path that arrived. A path carries
// wallet ids, and one series per wallet is how a metrics backend is taken down
// by its own instrumentation.
func (r *Recorder) RequestObserved(route, method, status string, took time.Duration) {
	r.requests.WithLabelValues(route, method, status).Observe(took.Seconds())
}

// Recorder satisfies every port it is handed to. The assertion is here so a
// method that drifts from an interface fails to compile in this package, rather
// than at whichever call site happens to be wired first.
var _ app.Metrics = (*Recorder)(nil)
