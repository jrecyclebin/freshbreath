package server

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"

	"poggers.institute/freshbreath/internal/db"
)

// newAppDBServer returns a test server whose DataDir is a temp dir, so app
// databases never touch the repo tree.
func newAppDBServer(t *testing.T) *Server {
	t.Helper()
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	return srv
}

func TestDBPathValidation(t *testing.T) {
	srv := newAppDBServer(t)

	// Good names.
	for _, name := range []string{"app", "scratch", "a", "db-2", "x_9"} {
		if _, err := srv.dbPath("app:nonce1", name); err != nil {
			t.Errorf("dbPath(app, %q) error: %v", name, err)
		}
		if _, err := srv.dbPath("global", name); err != nil {
			t.Errorf("dbPath(global, %q) error: %v", name, err)
		}
	}

	// Bad names: traversal, case, length, emptiness.
	for _, name := range []string{"", "App", "app.db", "../freshbreath", "a b", strings.Repeat("x", 65), "-lead", "_lead"} {
		if _, err := srv.dbPath("app:nonce1", name); err == nil {
			t.Errorf("dbPath(app, %q): expected error, got none", name)
		}
	}

	// @slot targets are reserved, rejected with a clear message.
	if _, err := srv.dbPath("app:nonce1@staging", "app"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("dbPath(@staging): want reserved error, got %v", err)
	}
	if _, err := srv.dbPath("bogus", "app"); err == nil {
		t.Errorf("dbPath(bogus): expected error")
	}

	// Resolution: app target under the app's db/, global under top-level db/.
	p, err := srv.dbPath("app:nonce1", "app")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(srv.config.DataDir, "apps", "nonce1", "db", "app.db"); p != want {
		t.Errorf("app path = %q, want %q", p, want)
	}
	p, err = srv.dbPath("global", "shared")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(srv.config.DataDir, "db", "shared.db"); p != want {
		t.Errorf("global path = %q, want %q", p, want)
	}
}

func TestGateDBTarget(t *testing.T) {
	srv := newAppDBServer(t)

	// global requires admin+.
	admin := &db.User{ID: 1, Role: "Admin"}
	member := &db.User{ID: 2, Role: "Member"}
	if err := srv.gateDBTarget(admin, "global"); err != nil {
		t.Errorf("global as admin: %v", err)
	}
	if err := srv.gateDBTarget(member, "global"); err == nil {
		t.Errorf("global as member: want 403")
	}

	// app: requires membership (or admin+).
	nonce, err := srv.store.CreateApp("notes", "Development", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := srv.store.CreateUser("Jo", "jo@example.com", "Member", "Active")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.gateDBTarget(u, "app:"+nonce); err == nil {
		t.Errorf("non-member: want 403")
	}
	if err := srv.store.AddAppMember(nonce, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := srv.gateDBTarget(u, "app:"+nonce); err != nil {
		t.Errorf("member: %v", err)
	}
	if err := srv.gateDBTarget(admin, "app:"+nonce); err != nil {
		t.Errorf("admin+ on any app: %v", err)
	}

	// Unknown target shape.
	if err := srv.gateDBTarget(admin, "weird"); err == nil {
		t.Errorf("weird target: want error")
	}
}

func TestCoreDBQueryLifecycle(t *testing.T) {
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	// Databases are created on first touch.
	res, err := srv.coreDBQuery(admin, target, dbQueryRequest{
		SQL: "CREATE TABLE tasks (id INTEGER PRIMARY KEY, title TEXT, done INT)",
	}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.RowsAffected != 0 {
		t.Errorf("create rowsAffected = %d, want 0", res.RowsAffected)
	}

	// Positional params, lastInsertId.
	res, err = srv.coreDBQuery(admin, target, dbQueryRequest{
		SQL:    "INSERT INTO tasks (title, done) VALUES (?, ?)",
		Params: []interface{}{"Feed the frogs", 0},
	}, false)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if res.LastInsertID != 1 || res.RowsAffected != 1 {
		t.Errorf("insert: lastID=%d affected=%d, want 1/1", res.LastInsertID, res.RowsAffected)
	}

	// Named params.
	res, err = srv.coreDBQuery(admin, target, dbQueryRequest{
		SQL:    "INSERT INTO tasks (title, done) VALUES (:t, :d)",
		Params: map[string]interface{}{"t": "Ship the thing", "d": 1},
	}, false)
	if err != nil {
		t.Fatalf("named insert: %v", err)
	}

	// Select: columns + rows-as-arrays shape.
	res, err = srv.coreDBQuery(admin, target, dbQueryRequest{
		SQL: "SELECT id, title, done FROM tasks ORDER BY id",
	}, false)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if strings.Join(res.Columns, ",") != "id,title,done" {
		t.Errorf("columns = %v", res.Columns)
	}
	if len(res.Rows) != 2 || res.Rows[0][1] != "Feed the frogs" {
		t.Errorf("rows = %v", res.Rows)
	}
	if res.Truncated {
		t.Errorf("truncated = true on a 2-row result")
	}
}

func TestCoreDBQueryRowCap(t *testing.T) {
	srv := newAppDBServer(t)
	srv.dbRowCap = 5 // lowered for the test; production default is 10000
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "CREATE TABLE t (n)"}, false); err != nil {
		t.Fatal(err)
	}
	// generate_series is an extension and not present by default; insert a
	// dozen rows with one multi-VALUES statement instead.
	vals := make([]string, 12)
	for i := range vals {
		vals[i] = fmt.Sprintf("(%d)", i+1)
	}
	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{
		SQL: "INSERT INTO t (n) VALUES " + strings.Join(vals, ","),
	}, false); err != nil {
		t.Fatal(err)
	}
	res, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "SELECT n FROM t"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 5 || !res.Truncated {
		t.Errorf("rows=%d truncated=%v, want 5/true", len(res.Rows), res.Truncated)
	}
}

func TestCoreDBBatchAtomicity(t *testing.T) {
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "CREATE TABLE m (k TEXT)"}, false); err != nil {
		t.Fatal(err)
	}

	// Statement 2 references a missing table: the whole batch must roll back.
	_, err := srv.coreDBQuery(admin, target, dbQueryRequest{
		Batch: []dbStmtRequest{
			{SQL: "INSERT INTO m (k) VALUES ('first')"},
			{SQL: "INSERT INTO missing (x) VALUES (1)"},
		},
	}, false)
	if err == nil {
		t.Fatalf("batch with bad stmt: want error")
	}
	res, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "SELECT COUNT(*) FROM m"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0] != int64(0) {
		t.Errorf("partial write survived: count = %v, want 0", res.Rows[0][0])
	}

	// A good batch commits all-or-nothing.
	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{
		Batch: []dbStmtRequest{
			{SQL: "INSERT INTO m (k) VALUES ('a')"},
			{SQL: "INSERT INTO m (k) VALUES ('b')"},
		},
	}, false); err != nil {
		t.Fatalf("good batch: %v", err)
	}
}

func TestCoreDBBlobRoundTrip(t *testing.T) {
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "CREATE TABLE b (data BLOB)"}, false); err != nil {
		t.Fatal(err)
	}
	// Inbound $blob wrapper.
	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{
		SQL:    "INSERT INTO b (data) VALUES (?)",
		Params: []interface{}{map[string]interface{}{"$blob": "ZnJvZw=="}}, // "frog"
	}, false); err != nil {
		t.Fatal(err)
	}
	// Outbound $blob wrapper.
	res, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "SELECT data FROM b"}, false)
	if err != nil {
		t.Fatal(err)
	}
	blob, ok := res.Rows[0][0].(map[string]interface{})
	if !ok || blob["$blob"] != "ZnJvZw==" {
		t.Errorf("blob = %#v, want {$blob: ZnJvZw==}", res.Rows[0][0])
	}
}

func TestAppDBAuthorizer(t *testing.T) {
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "CREATE TABLE t (x)"}, false); err != nil {
		t.Fatal(err)
	}

	// ATTACH is the whole ballgame — denied at the engine level.
	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{
		SQL: "ATTACH '/tmp/evil.db' AS boss",
	}, false); err == nil {
		t.Errorf("ATTACH: want error")
	}

	// Arbitrary PRAGMAs are denied.
	for _, p := range []string{"PRAGMA journal_mode = DELETE", "PRAGMA writable_schema = 1"} {
		if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: p}, false); err == nil {
			t.Errorf("%s: want error", p)
		}
	}

	// load_extension is denied by name. SQLite also refuses it whenever an
	// authorizer is registered, so this asserts our own branch fires: for
	// SQLITE_FUNCTION the name arrives in arg2, and reading arg1 matched
	// nothing at all.
	if appDBAuthorizer(sqlite3.SQLITE_FUNCTION, "", "load_extension", "") != sqlite3.SQLITE_DENY {
		t.Errorf("load_extension: want DENY")
	}
	if appDBAuthorizer(sqlite3.SQLITE_FUNCTION, "", "bm25", "") != sqlite3.SQLITE_OK {
		t.Errorf("bm25: want OK — FTS5 aux functions have to pass")
	}

	// Read-only schema pragmas are allowlisted (db_schema depends on them;
	// data_version is FTS5's, and every full-text write needs it).
	for _, p := range []string{"PRAGMA table_info(t)", "PRAGMA index_list(t)", "PRAGMA table_xinfo(t)", "PRAGMA foreign_key_list(t)", "PRAGMA table_list", "PRAGMA data_version"} {
		if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: p}, false); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestAppDBReadOnlyPool(t *testing.T) {
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "CREATE TABLE t (x)"}, false); err != nil {
		t.Fatal(err)
	}

	// Read-only handles refuse writes at the engine level.
	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "INSERT INTO t (x) VALUES (1)"}, true); err == nil {
		t.Errorf("ro write: want error")
	} else if !isReadonlyWriteErr(err) {
		t.Errorf("ro write error = %v, want a readonly refusal", err)
	}
	// ...and read fine.
	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "SELECT COUNT(*) FROM t"}, true); err != nil {
		t.Errorf("ro read: %v", err)
	}
}

func TestCoreDBListAndDelete(t *testing.T) {
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	// Touch two databases; the default "app" is not auto-created.
	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{DB: "scratch", SQL: "CREATE TABLE t (x)"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{DB: "scratch2", SQL: "CREATE TABLE t (x)"}, false); err != nil {
		t.Fatal(err)
	}
	list, err := srv.coreDBList(admin, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %d entries, want 2", len(list))
	}
	if list[0].Name != "scratch" || list[1].Name != "scratch2" {
		t.Errorf("list order: %v %v", list[0].Name, list[1].Name)
	}
	if list[0].Size <= 0 {
		t.Errorf("size = %d, want > 0", list[0].Size)
	}
	if list[0].ModTime.IsZero() {
		t.Errorf("mtime zero")
	}

	if err := srv.coreDBDelete(admin, target, "scratch"); err != nil {
		t.Fatal(err)
	}
	list, err = srv.coreDBList(admin, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "scratch2" {
		t.Errorf("after delete: %v", list)
	}

	// Deleting a database that doesn't exist is a 404.
	if err := srv.coreDBDelete(admin, target, "nope"); err == nil {
		t.Errorf("delete missing: want error")
	}
}

func TestCoreDBSchema(t *testing.T) {
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{
		Batch: []dbStmtRequest{
			{SQL: "CREATE TABLE tasks (id INTEGER PRIMARY KEY, title TEXT NOT NULL, done INT DEFAULT 0)"},
			{SQL: "CREATE INDEX tasks_done ON tasks (done)"},
		},
	}, false); err != nil {
		t.Fatal(err)
	}
	schema, err := srv.coreDBSchema(admin, target, "app")
	if err != nil {
		t.Fatal(err)
	}
	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		t.Fatalf("schema type = %T", schema)
	}
	tables, ok := schemaMap["tables"].([]map[string]interface{})
	if !ok || len(tables) != 1 {
		t.Fatalf("tables = %#v", schemaMap["tables"])
	}
	tasks := tables[0]
	if tasks["name"] != "tasks" {
		t.Errorf("table name = %v", tasks["name"])
	}
	cols, _ := tasks["columns"].([]map[string]interface{})
	if len(cols) != 3 || cols[1]["name"] != "title" {
		t.Errorf("columns = %#v", cols)
	}
	if nn, _ := cols[1]["notnull"].(int64); nn != 1 {
		t.Errorf("title notnull = %v, want 1", cols[1]["notnull"])
	}
	idx, _ := tasks["indexes"].([]map[string]interface{})
	if len(idx) != 1 || idx[0]["name"] != "tasks_done" {
		t.Errorf("indexes = %#v", idx)
	}
	if ddl, _ := tasks["ddl"].(string); !strings.Contains(ddl, "CREATE TABLE tasks") {
		t.Errorf("ddl = %q", ddl)
	}
}

func TestAppDBWatch(t *testing.T) {
	srv := newAppDBServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	const target = "app:nonce1"

	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{SQL: "CREATE TABLE tasks (id INTEGER PRIMARY KEY, title TEXT)"}, false); err != nil {
		t.Fatal(err)
	}

	ch, stop, err := srv.appDBWatchCh(target, "app")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if _, err := srv.coreDBQuery(admin, target, dbQueryRequest{
		SQL: "INSERT INTO tasks (title) VALUES ('watch me')",
	}, false); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Table != "tasks" || ev.Op != "insert" || ev.RowID != 1 {
			t.Errorf("event = %+v, want tasks/insert/1", ev)
		}
		if ev.DB != "app" {
			t.Errorf("db = %q, want app", ev.DB)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no change event arrived")
	}
}
