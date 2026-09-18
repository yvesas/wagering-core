// Package http is the inbound HTTP edge: routing, request decoding, response
// rendering and the mapping from a domain rejection onto a status code.
//
// It uses net/http and no router dependency. Since Go 1.22 the ServeMux matches
// on method and extracts path wildcards, which were the two reasons a third
// party was needed. See docs/adr/0005-stdlib-http.md.
package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Routes builds the mux.
//
// Patterns carry their method, so an unregistered method on a known path
// answers 405 with an Allow header and no code of ours. Specificity decides
// between overlapping patterns, so the order here is for a reader, not for the
// router.
func Routes(wallets *WalletHandler, health *HealthHandler) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /wallets", wallets.Open)
	mux.HandleFunc("GET /wallets/{walletId}", wallets.Get)
	mux.HandleFunc("GET /wallets/{walletId}/ledger", wallets.Ledger)

	mux.HandleFunc("GET /health/live", health.Live)
	mux.HandleFunc("GET /health/ready", health.Ready)

	return mux
}

// Handler wraps the mux in the middleware chain.
//
// The chain is an expression rather than a list of Use() calls, so it reads
// outside-in and there is no accumulated state deciding what runs when.
// Recovery is outermost on purpose: it has to catch a panic raised by anything
// below it, logging included.
func Handler(mux *http.ServeMux) http.Handler {
	return withRecovery(withCorrelationID(withRequestLog(mux)))
}

type correlationIDKey struct{}

// CorrelationIDFrom returns the id assigned to this request, or "" outside one.
func CorrelationIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey{}).(string)
	return id
}

// CorrelationIDHeader is read from the caller when present, so a trace that
// started upstream keeps its identity through this service.
const CorrelationIDHeader = "X-Correlation-Id"

// maxCorrelationIDLength bounds what a caller can inject. The value ends up in
// every log line for the request; without a cap, a client controls how much it
// costs to log one call.
const maxCorrelationIDLength = 128

func withCorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationIDHeader)
		if id == "" || len(id) > maxCorrelationIDLength {
			id = uuid.NewString()
		}
		w.Header().Set(CorrelationIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationIDKey{}, id)))
	})
}

// statusRecorder remembers what was written, because http.ResponseWriter does
// not expose it and the log line needs it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(recorder, r)

		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}
		// The log carries identifiers and shape, never a body. A financial
		// payload in a log aggregator is a data leak with extra steps.
		slog.InfoContext(r.Context(), "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", recorder.status),
			slog.Duration("took", time.Since(started)),
			slog.String("correlationId", CorrelationIDFrom(r.Context())),
		)
	})
}

func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			// http.ErrAbortHandler is the standard way to abort a response on
			// purpose. Treating it as a crash would fill the log with noise
			// every time a client hangs up mid-response.
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			slog.ErrorContext(r.Context(), "panic serving a request",
				slog.Any("panic", recovered),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("correlationId", CorrelationIDFrom(r.Context())),
			)
			writeJSON(w, r, http.StatusInternalServerError, ErrorBody{
				Code:          "INTERNAL",
				Message:       "internal error",
				CorrelationID: CorrelationIDFrom(r.Context()),
			})
		}()
		next.ServeHTTP(w, r)
	})
}

// ServerConfig is what the HTTP server needs.
type ServerConfig struct {
	Addr            string
	ShutdownTimeout time.Duration

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// NewServer builds the server without starting it. Starting and stopping is the
// composition layer's job, because the order of that relative to the pool is
// what keeps a drain from being useless.
func NewServer(cfg ServerConfig, handler http.Handler) *http.Server {
	// Every timeout has a value. The zero value of http.Server means "wait
	// forever", and a single slow client is then enough to hold a connection
	// and eventually a goroutine indefinitely.
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: orDefault(cfg.ReadHeaderTimeout, 5*time.Second),
		ReadTimeout:       orDefault(cfg.ReadTimeout, 15*time.Second),
		WriteTimeout:      orDefault(cfg.WriteTimeout, 15*time.Second),
		IdleTimeout:       orDefault(cfg.IdleTimeout, 60*time.Second),
	}
}

func orDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}
