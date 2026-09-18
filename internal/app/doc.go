// Package app holds the use cases and declares the ports they depend on.
//
// Interfaces are declared here, by the consumer, not by the adapter that
// implements them: repositories, unit of work, clock. Small interface, single
// role. Implementations live in internal/adapter.
//
// A use case orchestrates the domain and the transactional boundary; it carries
// no business rule. A rule that exists only in the use case is in the wrong
// place — it belongs to the domain, where it holds for every entry point.
package app
