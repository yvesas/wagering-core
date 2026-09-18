package http

import (
	"context"
	"net/http"
	"time"
)

// Prober reports whether a dependency answers.
type Prober interface {
	// Name is what appears in the readiness body.
	Name() string
	// Ping returns nil when the dependency is usable.
	Ping(ctx context.Context) error
}

type healthResponse struct {
	Status     string            `json:"status"`
	Dependents map[string]string `json:"dependencies,omitempty"`
}

// HealthHandler answers the two health endpoints.
//
// They mean different things and a load balancer treats them differently.
// Liveness says the process is not wedged; readiness says it can serve. Wiring
// a database check into liveness is a classic way to turn a brief database
// blip into every replica being restarted at once.
type HealthHandler struct {
	probes  []Prober
	timeout time.Duration
}

func NewHealthHandler(probes ...Prober) *HealthHandler {
	return &HealthHandler{probes: probes, timeout: 2 * time.Second}
}

// Live reports that the process is running. It touches no dependency, on
// purpose: if this needs a database to answer, it is not a liveness check.
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, healthResponse{Status: "ok"})
}

// Ready reports whether the dependencies answer.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	// A readiness probe that can hang is worse than one that fails: the prober
	// times out, the replica is pulled, and nothing says why.
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	body := healthResponse{Status: "ok", Dependents: make(map[string]string, len(h.probes))}
	status := http.StatusOK

	for _, probe := range h.probes {
		if err := probe.Ping(ctx); err != nil {
			body.Dependents[probe.Name()] = "unavailable"
			body.Status = "unavailable"
			status = http.StatusServiceUnavailable
			continue
		}
		body.Dependents[probe.Name()] = "ok"
	}

	writeJSON(w, r, status, body)
}
