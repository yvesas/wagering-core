// Package metrics records what operators need to see, in Prometheus's format.
//
// It is an outbound adapter behind the small interfaces in internal/app: the
// use cases say what happened, and nothing above this package knows what a
// counter, a histogram or a label is. That is what makes the choice of backend
// reversible -- swapping it is rewriting this package, and `make app-check`
// keeps it that way.
//
// It may not be imported by internal/app or internal/domain.
//
// Prometheus rather than OpenTelemetry: OTel's value is correlating signals
// across services and this is one service, and OTel metrics in Go usually end
// up exported to Prometheus anyway. Tracing stays a separate study. See
// docs/adr/0012-observability-and-reconciliation.md.
package metrics
