// Package migrations carries the versioned SQL that builds the schema.
//
// It is a Go package rather than a bare folder because go:embed only reaches
// files inside the package directory. Embedding them means the binary carries
// its own schema: there is no "did you copy the sql folder?" step between a
// build and a working database.
package migrations

import "embed"

// FS holds every migration, in name order.
//
//go:embed *.sql
var FS embed.FS
