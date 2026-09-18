# Development targets. `make check` is the gate before any commit.

GO ?= go

.DEFAULT_GOAL := help

.PHONY: help setup fmt fmt-check vet domain-check app-check test race cover check up up-test down logs test-integration

help: ## List the available targets
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

setup: ## Point git at .githooks — run this right after cloning
	@git config core.hooksPath .githooks
	@echo "core.hooksPath -> .githooks"
	@printf 'chore: hook check\n\nCo-Authored-By: X <x@y.z>\n' > /tmp/wc-hook-check && \
		if .githooks/commit-msg /tmp/wc-hook-check >/dev/null 2>&1; then \
			echo "FAIL: the commit-msg hook is NOT blocking AI attribution"; exit 1; \
		else echo "OK: commit-msg rejects AI attribution"; fi

fmt: ## Format the code
	$(GO) fmt ./...

fmt-check: ## Fail if any file is unformatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "FAIL: unformatted files:"; echo "$$out"; exit 1; fi

vet: ## Static analysis from the toolchain
	$(GO) vet ./...

domain-check: ## Assert the domain imports no infrastructure
	@if $(GO) list -deps ./internal/domain/... | grep -E 'go.uber.org/fx|jackc/pgx|net/http|aws-sdk-go'; then \
		echo "FAIL: the domain imports infrastructure — see docs/adr/0001-hexagonal-architecture.md"; exit 1; \
	else echo "OK: domain free of infrastructure"; fi

app-check: ## Assert the use-case layer imports no driver, HTTP or queue SDK
	@if $(GO) list -deps ./internal/app/... | grep -E 'jackc/pgx|net/http|aws-sdk-go|go.uber.org/fx'; then \
		echo "FAIL: internal/app imports infrastructure - see docs/adr/0003-transactional-boundary.md"; exit 1; \
	else echo "OK: app free of infrastructure"; fi

test: ## Run the tests
	$(GO) test ./...

race: ## Run the tests with the race detector
	$(GO) test -race ./...

test-integration: ## Run the integration tests (needs `make up-test`)
	TEST_DB_PORT=5433 $(GO) test -tags integration -count=1 ./...

cover: ## Run the tests with a coverage report
	$(GO) test -coverprofile=coverage.out ./... && $(GO) tool cover -html=coverage.out -o coverage.html

check: fmt-check vet domain-check app-check test race ## Full gate before committing

up: ## Start the local dependencies
	docker compose up --build

up-test: ## Start the isolated database used by the integration tests
	docker compose --profile test up -d postgres-test

down: ## Stop the local dependencies and drop the volumes
	docker compose down -v

logs: ## Follow the compose logs
	docker compose logs -f
