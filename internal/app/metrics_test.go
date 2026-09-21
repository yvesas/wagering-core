package app

import (
	"sync"
	"time"
)

// recordingMetrics remembers what was recorded, so a test can assert on the
// numbers an operator would see rather than on the fact that a call compiled.
//
// It is safe for concurrent use because the consumer and the publisher record
// from their own goroutines, and a test that races its own instrumentation
// would fail under -race for a reason that has nothing to do with the code.
type recordingMetrics struct {
	mu sync.Mutex

	settled    map[string]int
	latencies  []time.Duration
	contention int
	retries    int

	deduplicated int
	discarded    map[string]int
	released     int

	publishLag    []time.Duration
	publishFailed int

	reconciled map[string]int
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{
		settled:    map[string]int{},
		discarded:  map[string]int{},
		reconciled: map[string]int{},
	}
}

func (m *recordingMetrics) OperationSettled(source, kind, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settled[source+":"+kind+":"+status]++
}

func (m *recordingMetrics) OperationLatency(_ string, took time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latencies = append(m.latencies, took)
}

func (m *recordingMetrics) WalletContention() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.contention++
}

func (m *recordingMetrics) TransactionRetried() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retries++
}

func (m *recordingMetrics) MessageDeduplicated() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deduplicated++
}

func (m *recordingMetrics) MessageDiscarded(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.discarded[reason]++
}

func (m *recordingMetrics) MessageReleased() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.released++
}

func (m *recordingMetrics) EventPublished(lag time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishLag = append(m.publishLag, lag)
}

func (m *recordingMetrics) EventPublishFailed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishFailed++
}

func (m *recordingMetrics) ReconciliationRun(drifted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := "match"
	if drifted {
		result = "drift"
	}
	m.reconciled[result]++
}

// count reads one settled counter.
func (m *recordingMetrics) count(source, kind, status string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settled[source+":"+kind+":"+status]
}

func (m *recordingMetrics) discardedFor(reason string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.discarded[reason]
}

func (m *recordingMetrics) reconciliations(result string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconciled[result]
}

func (m *recordingMetrics) observations() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.latencies)
}

func (m *recordingMetrics) duplicates() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deduplicated
}

var _ Metrics = (*recordingMetrics)(nil)
