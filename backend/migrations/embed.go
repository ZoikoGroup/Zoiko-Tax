// Package migrations embeds the cell's schema.
//
// The SQL files are the artifact under review (ADR-0008 §2.3). Embedding them
// rather than reading a directory at runtime means ztax-migrate carries exactly
// the migrations it was built with — a container that finds a different set of
// files on a mounted volume is a container that can apply a schema nobody
// released.
package migrations

import "embed"

// FS holds the versioned migration files.
//
//go:embed *.sql
var FS embed.FS
