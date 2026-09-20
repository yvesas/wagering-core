package app

import "time"

// The metrics this layer records, split by who records them.
//
// Four small interfaces rather than one wide one, for the reason every port
// here is narrow: a use case should be able to say what it needs without
// dragging in what it does not. The adapter satisfies all four with one type,
// and that is the adapter's business.
//
// What is *not* here is as deliberate. No method takes a wallet id, a provider
// or an amount: those are labels with no bound, and an unbounded label is how a
// metrics backend falls over. Identifiers belong in the log line, where they
// are a string and not a new time series. See
// docs/adr/0012-observability-and-reconciliation.md.

// Source says which entry port an operation arrived through. Two values, ever,
// which is what makes it safe as a label.
const (
	SourceHTTP  = "http"
	SourceQueue = "queue"
)

// OperationMetrics is what the submission path records.
type OperationMetrics interface {
	// OperationSettled counts an operation that reached a recorded outcome.
	// Kind and status are closed sets defined by the domain.
	OperationSettled(source, kind, status string)

	// OperationLatency times one submission, whichever port it arrived on.
	OperationLatency(source string, took time.Duration)

	// WalletContention counts a write that lost a race over one wallet. It is
	// the number that says whether the lock strategy is costing availability --
	// which is exactly what ADR 0007 measured by hand.
	WalletContention()
}

// QueueMetrics is what the consumer records.
type QueueMetrics interface {
	// MessageDeduplicated counts a delivery the inbox had already handled.
	// Steady non-zero is normal -- at-least-once delivery is the contract --
	// and a spike means something upstream is redelivering.
	MessageDeduplicated()

	// MessageDiscarded counts a message dropped for good, by reason. The
	// reasons are ours and there are few of them.
	MessageDiscarded(reason string)

	// MessageReleased counts a delivery handed back for redelivery.
	MessageReleased()
}

// OutboxMetrics is what the publisher records.
type OutboxMetrics interface {
	// EventPublished records how long an event waited between being written
	// and being sent. This is the one number that says whether the outbox is
	// keeping up; a queue depth would not, because a stuck publisher and an
	// idle one both show an empty queue.
	EventPublished(lag time.Duration)

	// EventPublishFailed counts a publish the broker refused.
	EventPublishFailed()
}

// ReconciliationMetrics is what the reconciliation use case records.
type ReconciliationMetrics interface {
	// ReconciliationRun counts a check and whether it found a difference.
	// Drift is the alert: it means the stored balance and the ledger disagree,
	// and one of them is wrong about money.
	ReconciliationRun(drifted bool)
}

// StorageMetrics is what the persistence adapter records.
type StorageMetrics interface {
	// TransactionRetried counts a unit of work replayed after the database
	// refused to order it. Transient by nature, so the count matters and a
	// single occurrence does not.
	TransactionRetried()
}

// Metrics is every recorder at once, which is what the composition layer wires
// and what a single adapter provides.
type Metrics interface {
	OperationMetrics
	QueueMetrics
	OutboxMetrics
	ReconciliationMetrics
	StorageMetrics
}

// NoMetrics records nothing.
//
// It exists so a nil recorder is never reachable. A metric call that panicked
// would take down a financial operation in order to report that the operation
// happened, which is the wrong way round; every constructor here turns a nil
// into this.
type NoMetrics struct{}

func (NoMetrics) OperationSettled(string, string, string) {}
func (NoMetrics) OperationLatency(string, time.Duration)  {}
func (NoMetrics) WalletContention()                       {}
func (NoMetrics) MessageDeduplicated()                    {}
func (NoMetrics) MessageDiscarded(string)                 {}
func (NoMetrics) MessageReleased()                        {}
func (NoMetrics) EventPublished(time.Duration)            {}
func (NoMetrics) EventPublishFailed()                     {}
func (NoMetrics) ReconciliationRun(bool)                  {}
func (NoMetrics) TransactionRetried()                     {}

// The reasons a message is dropped for good. They are a closed set because they
// are a label, and they name what a person would have to fix.
const (
	// DiscardMalformed is an envelope we could not read.
	DiscardMalformed = "malformed"

	// DiscardUnusable is an envelope we read and the domain refuses -- a
	// currency that does not exist, an amount it will not accept. Redelivery
	// would repeat the same refusal forever.
	DiscardUnusable = "unusable"

	// DiscardNoProvider is an envelope that names no provider, so it acts as
	// nobody.
	DiscardNoProvider = "no-provider"

	// DiscardReusedID is a message id seen before carrying different content,
	// which is a producer reusing an identifier.
	DiscardReusedID = "reused-message-id"
)

// operationMetricsOr is how a constructor refuses a nil recorder.
func operationMetricsOr(m OperationMetrics) OperationMetrics {
	if m == nil {
		return NoMetrics{}
	}
	return m
}

func queueMetricsOr(m QueueMetrics) QueueMetrics {
	if m == nil {
		return NoMetrics{}
	}
	return m
}

func outboxMetricsOr(m OutboxMetrics) OutboxMetrics {
	if m == nil {
		return NoMetrics{}
	}
	return m
}

func reconciliationMetricsOr(m ReconciliationMetrics) ReconciliationMetrics {
	if m == nil {
		return NoMetrics{}
	}
	return m
}
