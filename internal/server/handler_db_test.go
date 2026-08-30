package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// dbPost runs a query against the app-database HTTP mount and decodes the
// result.
func dbPost(t *testing.T, srv *Server, path string, body string) *dbQueryResult {
	t.Helper()
	rr := testRequest(t, srv, http.MethodPost, path, strings.NewReader(body), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST %s: status %d: %s", path, rr.Code, rr.Body.String())
	}
	var res dbQueryResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("POST %s: decode: %v", path, err)
	}
	return &res
}

func TestHandlerDBAppMount(t *testing.T) {
	srv := newAppDBServer(t)
	nonce, err := srv.store.CreateApp("notes", "Development", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/apps/" + nonce + "/db"

	// Create, insert, read back — through the HTTP mount.
	res := dbPost(t, srv, base+"/query", `{
		"sql": "CREATE TABLE tasks (id INTEGER PRIMARY KEY, title TEXT)"}
	}`)
	if res.RowsAffected != 0 {
		t.Errorf("create: rowsAffected = %d, want 0", res.RowsAffected)
	}
	res = dbPost(t, srv, base+"/query", `{
		"sql": "INSERT INTO tasks (title) VALUES (?)", "params": ["feed the frogs"]
	}`)
	if res.LastInsertID != 1 {
		t.Errorf("insert: lastInsertId = %d, want 1", res.LastInsertID)
	}
	res = dbPost(t, srv, base+"/query", `{"sql": "SELECT id, title FROM tasks"}`)
	if len(res.Rows) != 1 || res.Rows[0][1] != "feed the frogs" {
		t.Errorf("select: rows = %v", res.Rows)
	}

	// Second database by name, then the list shows both.
	dbPost(t, srv, base+"/query", `{"db": "scratch", "sql": "CREATE TABLE t (x)"}`)
	rr := testRequest(t, srv, http.MethodGet, base, nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status %d: %s", rr.Code, rr.Body.String())
	}
	var list struct {
		Databases []dbDBInfo `json:"databases"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Databases) != 2 || list.Databases[0].Name != "app" || list.Databases[1].Name != "scratch" {
		t.Errorf("list = %+v", list.Databases)
	}

	// DELETE drops the database; a follow-up query recreates it empty.
	rr = testRequest(t, srv, http.MethodDelete, base+"/scratch", nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", rr.Code, rr.Body.String())
	}
	res = dbPost(t, srv, base+"/query", `{"db": "scratch", "sql": "SELECT count(*) FROM sqlite_master"}`)
	if res.Rows[0][0] != float64(0) {
		t.Errorf("scratch after delete+recreate: %v", res.Rows)
	}

	// Bad name on delete → 400, not a filesystem adventure.
	rr = testRequest(t, srv, http.MethodDelete, base+"/BADNAME", nil, nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("delete bad name: status %d, want 400", rr.Code)
	}
	// Wrong method → 405.
	rr = testRequest(t, srv, http.MethodGet, base+"/query", nil, nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET query: status %d, want 405", rr.Code)
	}
	// Unknown sub-path → 404.
	rr = testRequest(t, srv, http.MethodPost, base+"/query/extra", strings.NewReader("{}"), nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("deep path: status %d, want 404", rr.Code)
	}
}

func TestHandlerDBGlobalMount(t *testing.T) {
	srv := newAppDBServer(t)

	res := dbPost(t, srv, "/api/db/query", `{"sql": "CREATE TABLE shared (k TEXT PRIMARY KEY, v TEXT)"}`)
	if res.RowsAffected != 0 {
		t.Errorf("create: rowsAffected = %d", res.RowsAffected)
	}
	dbPost(t, srv, "/api/db/query", `{"db": "other", "sql": "CREATE TABLE t (x)"}`)

	// List works at both spellings — with and without the trailing slash.
	for _, path := range []string{"/api/db", "/api/db/"} {
		rr := testRequest(t, srv, http.MethodGet, path, nil, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d: %s", path, rr.Code, rr.Body.String())
		}
		var list struct {
			Databases []dbDBInfo `json:"databases"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Databases) != 2 {
			t.Errorf("GET %s: databases = %+v", path, list.Databases)
		}
	}

	// Delete one of them.
	rr := testRequest(t, srv, http.MethodDelete, "/api/db/other", nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandlerDBWatch(t *testing.T) {
	srv := newAppDBServer(t)
	nonce, err := srv.store.CreateApp("notes", "Development", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/api/apps/"+nonce+"/db/watch", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch: status %d", resp.StatusCode)
	}

	// Read lines until the insert's change event arrives.
	admin := &db.User{ID: -1, Role: "Superuser", Status: "Active"}
	srv.coreDBQuery(admin, "app:"+nonce,
		dbQueryRequest{SQL: "CREATE TABLE tasks (id INTEGER PRIMARY KEY, title TEXT)"}, false)

	got := make(chan dbChangeEvent, 1)
	reader := bufio.NewReader(resp.Body)
	go func() {
		var ev dbChangeEvent
		var data string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "event:"):
				// event type; only "change" carries data we parse
			case strings.HasPrefix(line, "data:"):
				data = strings.TrimPrefix(line, "data:")
				data = strings.TrimPrefix(data, " ")
				if strings.Contains(data, `"op"`) && ev == (dbChangeEvent{}) {
					_ = json.Unmarshal([]byte(data), &ev)
					got <- ev
					return
				}
			}
		}
	}()

	srv.coreDBQuery(admin, "app:"+nonce,
		dbQueryRequest{SQL: "INSERT INTO tasks (title) VALUES ('watched')"}, false)

	select {
	case ev := <-got:
		if ev.Table != "tasks" || ev.Op != "insert" || ev.RowID != 1 {
			t.Errorf("change event = %+v", ev)
		}
		// The hub relabels SQLite's "main" to the database's name.
		if ev.DB != "app" {
			t.Errorf("change db = %q, want app", ev.DB)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change event within 5s")
	}
	cancel()
}

func TestHandlerDBWatchBadName(t *testing.T) {
	srv := newAppDBServer(t)
	nonce, _ := srv.store.CreateApp("notes", "Development", "", nil, nil)
	rr := testRequest(t, srv, http.MethodGet,
		"/api/apps/"+nonce+"/db/watch?db=BADNAME", nil, nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("watch bad name: status %d, want 400", rr.Code)
	}
}
