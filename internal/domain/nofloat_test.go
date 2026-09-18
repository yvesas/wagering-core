package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestNoFloatingPointInTheDomain reads this package's own source and fails if
// any float appears in it — the type float32 or float64, or a float literal.
//
// This is the invariant that is easiest to break by accident and most expensive
// to discover late: a rounding error does not crash, it just makes a balance
// quietly wrong, and in an append-only ledger the correction is a new entry
// that stays visible forever. So it is not left to review.
//
// It parses rather than greps. A grep would flag the doc comments that say
// "no float32 or float64", and a test that has to be explained away every time
// it fires stops being read. The AST carries no comments, so only real code
// counts.
func TestNoFloatingPointInTheDomain(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}
	if len(packages) == 0 {
		t.Fatal("parsed no package; the scan would pass by doing nothing")
	}

	banned := map[string]bool{"float32": true, "float64": true}
	found := 0

	for _, pkg := range packages {
		for name, file := range pkg.Files {
			// This file names the types it bans, so it would report itself.
			if name == "nofloat_test.go" {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.Ident:
					if banned[node.Name] {
						t.Errorf("%s: %s appears in the domain", fset.Position(node.Pos()), node.Name)
						found++
					}
				case *ast.BasicLit:
					// A float literal is the other way in: 0.1 is float64 even
					// when nothing names the type.
					if node.Kind == token.FLOAT || node.Kind == token.IMAG {
						t.Errorf("%s: floating-point literal %s", fset.Position(node.Pos()), node.Value)
						found++
					}
				}
				return true
			})
		}
	}

	if found > 0 {
		t.Logf("money must never pass through a float: not parsing, not arithmetic, "+
			"not serialisation, not persistence, not tests (%d occurrences)", found)
	}
}

// TestTheFloatScanActuallyLooks guards the guard.
//
// A scan that silently parses nothing reports success forever, which is exactly
// how the two dead hooks in this repository failed. This asserts the scan sees
// real declarations, so "no floats found" means it looked.
func TestTheFloatScanActuallyLooks(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}

	declarations := 0
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			declarations += len(file.Decls)
		}
	}
	if declarations < 50 {
		t.Fatalf("the scan found only %d declarations; it is not reading the package", declarations)
	}
}
