package http

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
	"github.com/yvesas/wagering-core/migrations"
)

// TestEveryDomainCodeHasAStatus reads the domain's source and fails if a code
// declared there is missing from the status table.
//
// Without this, adding a domain rejection silently makes it a 500: a client
// mistake reported as our fault, discovered whenever somebody wonders why the
// error rate moved. The table cannot be exhaustive by construction -- Go has no
// enum -- so it is exhaustive by test.
//
// It parses rather than greps, for the same reason the float scan does: the
// comments in that file name codes too.
func TestEveryDomainCodeHasAStatus(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join("..", "..", "domain", "errors.go"), nil, 0)
	if err != nil {
		t.Fatalf("parsing the domain errors: %v", err)
	}

	declared := map[domain.Code]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		// A code constant looks like: CodeSomething Code = "SOMETHING"
		ident, ok := spec.Type.(*ast.Ident)
		if !ok || ident.Name != "Code" {
			return true
		}
		for i, name := range spec.Names {
			if i >= len(spec.Values) {
				continue
			}
			lit, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			declared[domain.Code(value)] = name.Name
		}
		return true
	})

	if len(declared) < 10 {
		// The scan found almost nothing, which means it is not reading the
		// file rather than that the file is empty. A guard that quietly stops
		// looking reports success forever.
		t.Fatalf("found only %d codes; the scan is not reading the domain", len(declared))
	}

	for code, name := range declared {
		if _, ok := domainStatus[code]; !ok {
			t.Errorf("domain.%s (%q) has no HTTP status; it would fall through to 500", name, code)
		}
	}

	for code := range domainStatus {
		if _, ok := declared[code]; !ok {
			t.Errorf("the status table maps %q, which the domain no longer declares", code)
		}
	}
}

func TestStatusFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"malformed amount", &domain.Error{Code: domain.CodeInvalidAmount}, http.StatusBadRequest, "INVALID_AMOUNT"},
		{"negative amount", &domain.Error{Code: domain.CodeNegativeAmount}, http.StatusBadRequest, "NEGATIVE_AMOUNT"},
		// Understood, and refused by a rule. Resending it unchanged fails
		// again, but it was not a malformed request.
		{"insufficient funds", &domain.Error{Code: domain.CodeInsufficientFunds}, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS"},
		{"invalid transition", &domain.Error{Code: domain.CodeInvalidTransition}, http.StatusConflict, "INVALID_TRANSITION"},
		// Stored state the domain would never have produced: our bug.
		{"inconsistent entry", &domain.Error{Code: domain.CodeInconsistentEntry}, http.StatusInternalServerError, "INCONSISTENT_LEDGER_ENTRY"},

		{"not found", app.ErrNotFound, http.StatusNotFound, "NOT_FOUND"},
		{"invalid input", app.ErrInvalidInput, http.StatusBadRequest, "INVALID_INPUT"},
		{"version mismatch", app.ErrVersionMismatch, http.StatusConflict, "VERSION_MISMATCH"},
		{"invariant violated", app.ErrInvariantViolated, http.StatusInternalServerError, "INTERNAL"},
		// Transient. A client that reads this should retry, so it must not
		// look like a permanent refusal.
		{"serialization failure", app.ErrSerializationFailure, http.StatusServiceUnavailable, "TRY_AGAIN"},
		{"anything else", errors.New("boom"), http.StatusInternalServerError, "INTERNAL"},

		{"named conflict", app.NewConflict("wallets_one_per_player_and_currency"), http.StatusConflict, "WALLET_ALREADY_EXISTS"},
		{"opening conflict", app.NewConflict("wager_transactions_one_opening_per_wallet"), http.StatusConflict, "WALLET_ALREADY_OPENED"},
		{"unmapped conflict", app.NewConflict("some_future_constraint"), http.StatusConflict, "CONFLICT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status, code := statusFor(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

func TestStatusForSeesThroughWrapping(t *testing.T) {
	t.Parallel()
	// Errors arrive wrapped with context from two or three layers. Mapping has
	// to survive that, or every added %w turns a 404 into a 500.
	wrapped := fmt.Errorf("opening the wallet: %w",
		fmt.Errorf("inserting: %w", app.NewConflict("wallets_one_per_player_and_currency")))

	status, code := statusFor(wrapped)
	if status != http.StatusConflict || code != "WALLET_ALREADY_EXISTS" {
		t.Fatalf("got %d %q through two wraps", status, code)
	}
}

func TestConflictCodesNameRealConstraints(t *testing.T) {
	t.Parallel()

	// Every mapped name has to exist in the migration, or the mapping is
	// aspirational: the constraint fires under a different name and the client
	// gets the generic CONFLICT while this table says otherwise.
	sql, err := readMigration()
	if err != nil {
		t.Fatalf("reading the migration: %v", err)
	}
	for constraint := range conflictCodes {
		// Primary keys are named by PostgreSQL, not by the migration.
		if strings.HasSuffix(constraint, "_pkey") {
			continue
		}
		if !strings.Contains(sql, constraint) {
			t.Errorf("conflictCodes maps %q, which the schema does not define", constraint)
		}
	}
}

// readMigration loads the schema through the embedded filesystem, which is the
// same source the binary ships. Reading the file from disk would pass in a
// checkout and say nothing about what is actually embedded.
func readMigration() (string, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return "", err
	}
	var all strings.Builder
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		content, err := migrations.FS.ReadFile(entry.Name())
		if err != nil {
			return "", err
		}
		all.Write(content)
	}
	return all.String(), nil
}
