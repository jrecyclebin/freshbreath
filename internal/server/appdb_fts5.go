//go:build sqlite_fts5 || fts5

package server

// FTS5Enabled reports whether this binary's SQLite carries the FTS5 module.
// mattn/go-sqlite3 leaves full-text search out unless it is compiled in, so
// the answer is a build-tag question, not a runtime one — see the GOFLAGS
// line in mise.toml, which is where the tag actually comes from.
//
// A binary built without it runs fine right up until an app says CREATE
// VIRTUAL TABLE … USING fts5 and gets "no such module: fts5". main.go says
// so at startup rather than letting that be the first hint.
const FTS5Enabled = true
