package server

import (
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// requireFTS5 skips a test on a binary built without the module. mise puts
// -tags=sqlite_fts5 in GOFLAGS; a bare `go test` outside mise won't have it,
// and the skip line says so rather than failing mysteriously.
func requireFTS5(t *testing.T) {
	t.Helper()
	if !FTS5Enabled {
		t.Skip("built without FTS5 — run under mise, or `go test -tags sqlite_fts5`")
	}
}

// TestAppDBFTS5 walks the whole shape of a full-text index through the same
// hardened path an app uses: create, write, search, rank, snippet. The
// writes are the interesting part — FTS5's xUpdate reads PRAGMA data_version
// on every one of them, so without that pragma allowlisted this test fails
// at the first INSERT with "authorization denied".
func TestAppDBFTS5(t *testing.T) {
	requireFTS5(t)
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	exec := func(sqlText string, params interface{}) *dbQueryResult {
		t.Helper()
		res, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: sqlText, Params: params}, false)
		if err != nil {
			t.Fatalf("%.40s…: %v", sqlText, err)
		}
		return res
	}

	exec(`CREATE VIRTUAL TABLE notes USING fts5(title, body, tokenize='porter unicode61')`, nil)
	exec(`INSERT INTO notes (title, body) VALUES ('frogs', 'the pond is full of frogs, jumping')`, nil)
	exec(`INSERT INTO notes (title, body) VALUES ('birds', 'the sky is full of birds')`, nil)
	exec(`UPDATE notes SET body = 'the pond is full of frogs' WHERE title = 'frogs'`, nil)
	exec(`DELETE FROM notes WHERE title = 'birds'`, nil)

	// A bound MATCH query, ranked, with a highlighted snippet — the three
	// things every search tool ends up wanting.
	res := exec(`SELECT title, snippet(notes, 1, '[', ']', '…', 8), bm25(notes)
		FROM notes WHERE notes MATCH :q ORDER BY rank LIMIT 5`,
		map[string]interface{}{"q": "frog*"})
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %v, want one hit", res.Rows)
	}
	if res.Rows[0][0] != "frogs" {
		t.Errorf("title = %v, want frogs", res.Rows[0][0])
	}
	if snip, _ := res.Rows[0][1].(string); !strings.Contains(snip, "[frogs]") {
		t.Errorf("snippet = %v, want the term highlighted", res.Rows[0][1])
	}
	if _, ok := res.Rows[0][2].(float64); !ok {
		t.Errorf("bm25 = %#v, want a number", res.Rows[0][2])
	}

	// The maintenance commands are ordinary INSERTs into the table's own name.
	exec(`INSERT INTO notes(notes) VALUES('optimize')`, nil)

	// A malformed match expression is a caller's mistake, not a 500 waiting
	// to happen — it comes back as an error with SQLite's own words.
	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{
		SQL:    `SELECT title FROM notes WHERE notes MATCH :q`,
		Params: map[string]interface{}{"q": `"unclosed`},
	}, false); err == nil {
		t.Errorf("malformed MATCH: want an error")
	}
}

// TestAppDBFTS5ExternalContent covers the other common shape: a real table
// with an index shadowing it, kept in sync by triggers.
func TestAppDBFTS5ExternalContent(t *testing.T) {
	requireFTS5(t)
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	for _, stmt := range []string{
		`CREATE TABLE tasks (id INTEGER PRIMARY KEY, title TEXT, body TEXT)`,
		`CREATE VIRTUAL TABLE tasks_fts USING fts5(title, body, content='tasks', content_rowid='id')`,
		`CREATE TRIGGER tasks_ai AFTER INSERT ON tasks BEGIN
			INSERT INTO tasks_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
		END`,
		`INSERT INTO tasks (title, body) VALUES ('feed the frogs', 'they are hungry')`,
	} {
		if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: stmt}, false); err != nil {
			t.Fatalf("%.40s…: %v", stmt, err)
		}
	}

	res, err := srv.coreDBQuery(admin, target, dbQueryRequest{
		SQL:    `SELECT title FROM tasks_fts WHERE tasks_fts MATCH :q`,
		Params: map[string]interface{}{"q": "hungry"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "feed the frogs" {
		t.Errorf("rows = %v, want the trigger-indexed row", res.Rows)
	}
}

// TestAppDBSchemaHidesShadowTables: one FTS5 index adds five internal tables.
// db_schema is what an MCP model reads before writing a query blind, so it
// shows the virtual table and none of its innards.
func TestAppDBSchemaHidesShadowTables(t *testing.T) {
	requireFTS5(t)
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	for _, stmt := range []string{
		`CREATE TABLE tasks (id INTEGER PRIMARY KEY, title TEXT)`,
		`CREATE VIRTUAL TABLE notes USING fts5(title, body)`,
	} {
		if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: stmt}, false); err != nil {
			t.Fatal(err)
		}
	}

	out, err := srv.coreDBSchema(admin, target, "app")
	if err != nil {
		t.Fatal(err)
	}
	tables, _ := out.(map[string]interface{})["tables"].([]map[string]interface{})
	seen := map[string]bool{}
	for _, tbl := range tables {
		name, _ := tbl["name"].(string)
		seen[name] = true
	}
	for _, want := range []string{"tasks", "notes"} {
		if !seen[want] {
			t.Errorf("schema is missing %q: %v", want, seen)
		}
	}
	for _, hide := range []string{"notes_data", "notes_idx", "notes_content", "notes_docsize", "notes_config"} {
		if seen[hide] {
			t.Errorf("schema exposes shadow table %q", hide)
		}
	}
}
