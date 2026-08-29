package server

import (
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// callDBTool runs a central database tool as Superuser and returns the text.
func callDBTool(t *testing.T, srv *Server, name string, args map[string]interface{}) string {
	t.Helper()
	res := callCentralTool(t, srv, name, args)
	return toolResultText(t, res)
}

func TestMCPDatabaseToolsReadOnly(t *testing.T) {
	srv := newAppDBServer(t)
	nonce, err := srv.store.CreateApp("notes", "Development", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	target := "app:" + nonce

	// db_query can read an empty schema; db_schema shows no tables yet.
	out := callDBTool(t, srv, "db_schema", map[string]interface{}{"target": target})
	if !strings.Contains(out, `"tables":[]`) {
		t.Errorf("schema on empty db: %s", out)
	}

	// db_execute in the default read-only mode is refused with the friendly
	// message naming the setting and the value to set.
	out = callDBTool(t, srv, "db_execute",
		map[string]interface{}{"target": target, "sql": "CREATE TABLE t (x)"})
	if !strings.Contains(out, "full access") || !strings.Contains(out, "MCP database mode") {
		t.Errorf("db_execute in read-only: %s", out)
	}

	// db_query sent a write also gets the friendly message, not SQLite's raw
	// "attempt to write a readonly database".
	out = callDBTool(t, srv, "db_query",
		map[string]interface{}{"target": target, "sql": "CREATE TABLE t (x)"})
	if !strings.Contains(out, "full access") {
		t.Errorf("db_query write: %s", out)
	}

	// A read-only db_query works.
	out = callDBTool(t, srv, "db_query",
		map[string]interface{}{"target": target, "sql": "SELECT 1 AS one"})
	if !strings.Contains(out, `"one"`) {
		t.Errorf("db_query select: %s", out)
	}
}

func TestMCPDatabaseToolsFullAccess(t *testing.T) {
	srv := newAppDBServer(t)
	nonce, err := srv.store.CreateApp("notes", "Development", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	target := "app:" + nonce

	// Flip the switch to full-access; db_execute now writes.
	mustSet(t, srv.store.SetSetting("mcp_database_mode", "full-access"))
	if mode := srv.mcpDBMode(); mode != "full-access" {
		t.Fatalf("mode = %q", mode)
	}

	out := callDBTool(t, srv, "db_execute",
		map[string]interface{}{"target": target, "sql": "CREATE TABLE tasks (id INTEGER PRIMARY KEY, title TEXT)"})
	if strings.Contains(out, "full access") {
		t.Errorf("db_execute under full-access refused: %s", out)
	}

	// db_query is STILL read-only — a write through it is refused even in
	// full-access mode (the friendly message names the mode, but the point is
	// db_query never writes).
	out = callDBTool(t, srv, "db_query",
		map[string]interface{}{"target": target, "sql": "INSERT INTO tasks (title) VALUES ('x')"})
	if !strings.Contains(out, "full access") {
		t.Errorf("db_query write under full-access: %s", out)
	}

	// A real read through db_query sees the row db_execute wrote.
	out = callDBTool(t, srv, "db_execute",
		map[string]interface{}{"target": target, "sql": "INSERT INTO tasks (title) VALUES ('first')"})
	out = callDBTool(t, srv, "db_query",
		map[string]interface{}{"target": target, "sql": "SELECT title FROM tasks"})
	if !strings.Contains(out, "first") {
		t.Errorf("db_query after insert: %s", out)
	}

	// db_list_databases shows the default "app" database.
	out = callDBTool(t, srv, "db_list_databases", map[string]interface{}{"target": target})
	if !strings.Contains(out, `"name":"app"`) {
		t.Errorf("db_list_databases: %s", out)
	}
}

func TestMCPDatabaseToolsGate(t *testing.T) {
	srv := newAppDBServer(t)
	// A Member naming a global database is refused at the gate — an admin+
	// door, regardless of the MCP mount being all-roles.
	srv.store.CreateUser("Jo", "jo@example.com", "Member", "Active")
	// mcpUser returns a synthetic Superuser when auth is off, so to exercise
	// the gate we call the core directly with a Member.
	member := &db.User{ID: 1, Role: "Member"}
	if err := srv.gateDBTarget(member, "global"); err == nil {
		t.Error("member on global: want 403")
	}
	nonce, err := srv.store.CreateApp("mine", "Development", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.gateDBTarget(member, "app:"+nonce); err == nil {
		t.Error("non-member: want 403")
	}
}

func mustSet(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSettingsMCPDatabaseMode(t *testing.T) {
	srv := newAppDBServer(t)
	// GET defaults to read-only.
	rr := testRequest(t, srv, "GET", "/api/settings", nil, nil)
	if !strings.Contains(rr.Body.String(), `"mcp_database_mode":"read-only"`) {
		t.Errorf("default mode: %s", rr.Body.String())
	}
	// Set full-access via the API.
	rr = testRequest(t, srv, "PUT", "/api/settings",
		strings.NewReader(`{"mcp_database_mode":"full-access"}`), nil)
	if rr.Code != 204 {
		t.Fatalf("put: %d %s", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, "GET", "/api/settings", nil, nil)
	if !strings.Contains(rr.Body.String(), `"mcp_database_mode":"full-access"`) {
		t.Errorf("full-access mode: %s", rr.Body.String())
	}
	// Invalid value rejected.
	rr = testRequest(t, srv, "PUT", "/api/settings",
		strings.NewReader(`{"mcp_database_mode":"yolo"}`), nil)
	if rr.Code != 400 {
		t.Errorf("invalid mode: status %d, want 400", rr.Code)
	}
	// The MCP update_settings tool surfaces it too.
	out := callDBTool(t, srv, "get_settings", nil)
	if !strings.Contains(out, "mcp_database_mode") {
		t.Errorf("get_settings: %s", out)
	}
}
