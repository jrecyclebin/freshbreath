//go:build !sqlite_fts5 && !fts5

package server

// See appdb_fts5.go — this is the same constant for a binary built without
// the tag.
const FTS5Enabled = false
