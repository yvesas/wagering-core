// Package domain holds the entities, value objects, invariants and errors of the
// financial core: money, wallet, ledger entry and transaction.
//
// This package and its subpackages import the standard library only. No
// dependency injection framework, database driver, HTTP server or queue SDK
// belongs here. The rule is mechanically verifiable:
//
//	go list -deps ./internal/domain/... | grep -E 'go.uber.org/fx|jackc/pgx|net/http|aws-sdk-go'
//
// Empty output, or the rule has been broken.
//
// The reason is concrete: the service has two entry points — HTTP and queue —
// that must produce the same financial outcome. An invariant living inside a
// handler exists once per entry point and diverges the first time it is fixed on
// one side only. See docs/adr/0001-hexagonal-architecture.md.
package domain
