package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/formats"
)

// newVirtualSvcServer returns a test server with a virtual service whose
// tool file is written under a temp data dir, linked to the given app.
func newVirtualSvcServer(t *testing.T, toolFile string, d db.ServiceDescriptor) (*Server, *db.Service, string) {
	t.Helper()
	srv := newAppDBServer(t)
	if err := os.MkdirAll(filepath.Join(srv.config.DataDir, "virtual"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srv.config.DataDir, "virtual", "Keeper.txt"), []byte(toolFile), 0o644); err != nil {
		t.Fatalf("write tool file: %v", err)
	}
	if d.Type == "" {
		d.Type = "virtual"
	}
	if d.DatabaseName == "" {
		d.DatabaseName = ""
	}
	svc, err := srv.coreCreateService(&db.User{ID: 1, Role: "Admin"}, "Keeper", "/mcp/keeper", d, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	nonce, err := srv.store.CreateApp("notes", "Development", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.LinkAppService(nonce, svc.ID); err != nil {
		t.Fatal(err)
	}
	return srv, svc, nonce
}

const sqlToolFile = `[ensure-schema] Create the tasks table if needed.

CREATE TABLE IF NOT EXISTS tasks (id INTEGER PRIMARY KEY, title TEXT)
---
[add-task] Add a task.

INSERT INTO tasks (title)
  VALUES ($title)
---
[recent-tasks] The ten most recent tasks.

SELECT id, title
  FROM tasks
  ORDER BY id DESC
  LIMIT 10

{
  "tasks": $.rows
}
`

func TestBrowserSQLRunnerDefaultTarget(t *testing.T) {
	srv, svc, nonce := newVirtualSvcServer(t, sqlToolFile, db.ServiceDescriptor{})

	// Browser path: X-App-Nonce supplies the default target; the link is
	// the grant — no user involved.
	rr := testRequest(t, srv, http.MethodPost, "/service/call/keeper",
		strings.NewReader(`{"task": "ensure-schema", "args": {}}`),
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK {
		t.Fatalf("ensure-schema: status %d: %s", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, http.MethodPost, "/service/call/keeper",
		strings.NewReader(`{"task": "add-task", "args": {"title": "first"}}`),
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK {
		t.Fatalf("add-task: status %d: %s", rr.Code, rr.Body.String())
	}

	rr = testRequest(t, srv, http.MethodPost, "/service/call/keeper",
		strings.NewReader(`{"task": "recent-tasks", "args": {}}`),
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK {
		t.Fatalf("recent-tasks: status %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"first"`) {
		t.Errorf("recent-tasks body = %s", rr.Body.String())
	}

	// The data landed in the calling app's own database.
	admin := &db.User{ID: 1, Role: "Admin"}
	res, err := srv.coreDBQuery(admin, "app:"+nonce, dbQueryRequest{
		SQL: "SELECT title FROM tasks"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "first" {
		t.Errorf("app db rows = %v", res.Rows)
	}
	_ = svc
}

func TestBrowserSQLRunnerAppNonceLinkCheck(t *testing.T) {
	srv, svc, nonce := newVirtualSvcServer(t, sqlToolFile, db.ServiceDescriptor{})
	_ = svc

	// A second app, NOT linked to the service. An app_nonce naming it must
	// be refused — a public page can't aim a linked service at someone
	// else's data.
	other, err := srv.store.CreateApp("other", "Development", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := srv.browserSQLRunner(&db.Service{ID: 1, Descriptor: db.ServiceDescriptor{}}, nonce,
		map[string]interface{}{"app_nonce": other})
	_, err = runner("SELECT 1", nil)
	if err == nil || !strings.Contains(err.Error(), "not linked") {
		t.Errorf("want not-linked error, got %v", err)
	}

	// The header nonce's own app needs no extra check (link verified upstream).
	runner = srv.browserSQLRunner(&db.Service{ID: 1, Descriptor: db.ServiceDescriptor{}}, nonce,
		map[string]interface{}{"app_nonce": nonce})
	if _, err := runner("SELECT 1", nil); err != nil {
		t.Errorf("own nonce: %v", err)
	}

	// No nonce at all → a helpful error, not a panic.
	runner = srv.browserSQLRunner(&db.Service{ID: 1, Descriptor: db.ServiceDescriptor{}}, "",
		map[string]interface{}{})
	_, err = runner("SELECT 1", nil)
	if err == nil || !strings.Contains(err.Error(), "app_nonce") {
		t.Errorf("want app_nonce error, got %v", err)
	}
}

func TestMCPSQLRunnerGatesByMembership(t *testing.T) {
	srv, _, _ := newVirtualSvcServer(t, sqlToolFile, db.ServiceDescriptor{})

	// A member of one app; a second app they don't belong to.
	nonce1, err := srv.store.CreateApp("mine", "Development", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	nonce2, err := srv.store.CreateApp("theirs", "Development", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := srv.store.CreateUser("Jo", "jo@example.com", "Member", "Active")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.AddAppMember(nonce1, u.ID); err != nil {
		t.Fatal(err)
	}

	svc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{}}
	claims := &freshbreathClaims{UserEmail: "jo@example.com"}

	// Own app: passes the gate and runs.
	runner := srv.mcpSQLRunner(svc, claims, map[string]interface{}{"app_nonce": nonce1})
	if _, err := runner("CREATE TABLE t (x)", nil); err != nil {
		t.Errorf("member app: %v", err)
	}

	// Someone else's app: 403 through gateDBTarget → gateApp.
	runner = srv.mcpSQLRunner(svc, claims, map[string]interface{}{"app_nonce": nonce2})
	if _, err := runner("SELECT 1", nil); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("non-member app: want forbidden, got %v", err)
	}

	// No wrapped-token claims (raw upstream token): database access refused.
	runner = srv.mcpSQLRunner(svc, nil, map[string]interface{}{"app_nonce": nonce1})
	if _, err := runner("SELECT 1", nil); err == nil || !strings.Contains(err.Error(), "Freshbreath user token") {
		t.Errorf("no claims: want refusal, got %v", err)
	}

	// Global target: member refused, admin allowed — same gate as the API.
	globalSvc := &db.Service{ID: 1, Descriptor: db.ServiceDescriptor{DatabaseTarget: "global"}}
	runner = srv.mcpSQLRunner(globalSvc, claims, nil)
	if _, err := runner("SELECT 1", nil); err == nil {
		t.Errorf("member on global: want forbidden")
	}
	admin, err := srv.store.CreateUser("Boss", "boss@example.com", "Admin", "Active")
	if err != nil {
		t.Fatal(err)
	}
	_ = admin
	adminClaims := &freshbreathClaims{UserEmail: "boss@example.com"}
	runner = srv.mcpSQLRunner(globalSvc, adminClaims, nil)
	if _, err := runner("SELECT 1", nil); err != nil {
		t.Errorf("admin on global: %v", err)
	}
}

func TestBrowserSQLRunnerFixedTargets(t *testing.T) {
	srv, _, nonce := newVirtualSvcServer(t, sqlToolFile,
		db.ServiceDescriptor{DatabaseTarget: "global", DatabaseName: "shared"})

	// No app_nonce needed; the service is pinned to the global database.
	rr := testRequest(t, srv, http.MethodPost, "/service/call/keeper",
		strings.NewReader(`{"task": "ensure-schema", "args": {}}`),
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK {
		t.Fatalf("ensure-schema: status %d: %s", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, http.MethodPost, "/service/call/keeper",
		strings.NewReader(`{"task": "add-task", "args": {"title": "global row"}}`),
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK {
		t.Fatalf("add-task: status %d: %s", rr.Code, rr.Body.String())
	}
	admin := &db.User{ID: 1, Role: "Admin"}
	res, err := srv.coreDBQuery(admin, "global", dbQueryRequest{
		DB:  "shared",
		SQL: "SELECT title FROM tasks"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "global row" {
		t.Errorf("global db rows = %v", res.Rows)
	}
}

func TestValidateDBDescriptor(t *testing.T) {
	srv := newAppDBServer(t)
	nonce, err := srv.store.CreateApp("real", "Development", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		d    db.ServiceDescriptor
		want string // "" for ok, else substring of the error
	}{
		{db.ServiceDescriptor{}, ""},
		{db.ServiceDescriptor{DatabaseTarget: "global", DatabaseName: "shared"}, ""},
		{db.ServiceDescriptor{DatabaseTarget: "app:" + nonce}, ""},
		{db.ServiceDescriptor{DatabaseTarget: "app:nope"}, "unknown app"},
		{db.ServiceDescriptor{DatabaseTarget: "weird"}, `must be "global"`},
		{db.ServiceDescriptor{DatabaseName: "BAD NAME"}, "invalid database name"},
		{db.ServiceDescriptor{DatabaseName: "app"}, ""},
	}
	for i, c := range cases {
		err := srv.validateDBDescriptor(c.d)
		if c.want == "" && err != nil {
			t.Errorf("case %d: unexpected error %v", i, err)
		}
		if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("case %d: want %q, got %v", i, c.want, err)
		}
	}
}

func TestVirtualToolInputSchemaRequired(t *testing.T) {
	tools, err := formats.ParseVirtualFile([]byte(`[q] Query.

$state is string?
$owner is string
$host, $search is string?

SELECT id FROM issues
  WHERE state = $state AND owner = $owner AND host = $host AND search = $search
`))
	if err != nil {
		t.Fatal(err)
	}

	// Default target: app_nonce required alongside the non-optional params.
	schema := virtualToolInputSchema(tools[0], "")
	req, _ := schema["required"].([]string)
	want := []string{"app_nonce", "owner"}
	if strings.Join(req, ",") != strings.Join(want, ",") {
		t.Errorf("required = %v, want %v", req, want)
	}
	props := schema["properties"].(map[string]interface{})
	if _, ok := props["app_nonce"]; !ok {
		t.Errorf("app_nonce property missing: %v", props)
	}

	// Fixed target: no app_nonce; same required params.
	schema = virtualToolInputSchema(tools[0], "global")
	req, _ = schema["required"].([]string)
	if strings.Join(req, ",") != "owner" {
		t.Errorf("fixed-target required = %v", req)
	}
	props = schema["properties"].(map[string]interface{})
	if _, ok := props["app_nonce"]; ok {
		t.Errorf("app_nonce property should be absent for fixed target")
	}

	// Pure HTTP tool: no app_nonce even on a default-target service.
	httpTools, err := formats.ParseVirtualFile([]byte(`[get] Fetch.

$state is string?
GET https://api.example.com/issues?state=$state
`))
	if err != nil {
		t.Fatal(err)
	}
	schema = virtualToolInputSchema(httpTools[0], "")
	req, _ = schema["required"].([]string)
	if len(req) != 0 {
		t.Errorf("http tool required = %v, want empty", req)
	}
	props = schema["properties"].(map[string]interface{})
	if _, ok := props["app_nonce"]; ok {
		t.Errorf("app_nonce on HTTP-only tool")
	}
}

func TestVirtualAuthFromClaims(t *testing.T) {
	srv := newAppDBServer(t)

	// No claims (raw upstream token): identity built-ins all empty.
	auth := srv.virtualAuth("tok", nil)
	if auth.Token != "tok" || auth.Email != "" || auth.Sub != "" || auth.UserID != nil {
		t.Errorf("no-claims auth = %+v", auth)
	}

	// Claims for an ext: subject (no Fresh Breath account): email/sub
	// carried, no numerical ID.
	claims := &freshbreathClaims{Claims: josejwt.Claims{Subject: extSubject("idp", "ghost")}, UserEmail: "ghost@example.com"}
	auth = srv.virtualAuth("tok", claims)
	if auth.Email != "ghost@example.com" || auth.UserID != nil {
		t.Errorf("unknown-user auth = %+v", auth)
	}

	// Claims for a real user: the numerical ID resolves from the frbr: subject.
	kay, err := srv.store.CreateUser("Kay", "kay@example.com", "Member", "Active")
	if err != nil {
		t.Fatal(err)
	}
	claims = &freshbreathClaims{Claims: josejwt.Claims{Subject: subjectForUser(kay)}, UserEmail: "kay@example.com"}
	auth = srv.virtualAuth("tok", claims)
	if auth.UserID != kay.ID {
		t.Errorf("UserID = %v (%T), want %d", auth.UserID, auth.UserID, kay.ID)
	}
}

func TestVirtualCallIdentityBuiltins(t *testing.T) {
	srv := newAppDBServer(t)
	if err := os.MkdirAll(filepath.Join(srv.config.DataDir, "virtual"), 0o755); err != nil {
		t.Fatal(err)
	}
	toolFile := `[ensure] Create the stamps table.

CREATE TABLE IF NOT EXISTS stamps (email TEXT, uid INTEGER)
---
[stamp] Record who ran this.

INSERT INTO stamps (email, uid)
  VALUES ($token_email, $token_id)
`
	if err := os.WriteFile(filepath.Join(srv.config.DataDir, "virtual", "Keeper.txt"), []byte(toolFile), 0o644); err != nil {
		t.Fatal(err)
	}

	// An OIDC auth record gates the app door; Kay holds a token bound to it.
	idp := newAuthRecord(t, srv, "IdP", db.AuthOIDC,
		db.AuthDescriptor{Issuer: "https://idp.example", Provider: "idp"})
	kay, err := srv.store.CreateUser("Kay", "kay@example.com", "Member", "Active")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := srv.mintFreshbreathToken(subjectForUser(kay), "kay@example.com", "Member", "Kay", idp.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	svc, err := srv.coreCreateService(&db.User{ID: 1, Role: "Admin"}, "Keeper", "/mcp/keeper",
		db.ServiceDescriptor{Type: "virtual"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	nonce, err := srv.store.CreateApp("notes", "Development", "", nil, &idp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.LinkAppService(nonce, svc.ID); err != nil {
		t.Fatal(err)
	}

	headers := map[string]string{
		"X-App-Nonce":   nonce,
		"Authorization": "Bearer " + tok,
	}
	rr := testRequest(t, srv, http.MethodPost, "/service/call/keeper",
		strings.NewReader(`{"task": "ensure"}`), headers)
	if rr.Code != http.StatusOK {
		t.Fatalf("ensure: status %d: %s", rr.Code, rr.Body.String())
	}

	// Stamp with spoofed identity arguments — the built-ins must win.
	rr = testRequest(t, srv, http.MethodPost, "/service/call/keeper",
		strings.NewReader(`{"task": "stamp", "args": {"token_email": "spoof@evil.example", "token_id": 999}}`),
		headers)
	if rr.Code != http.StatusOK {
		t.Fatalf("stamp: status %d: %s", rr.Code, rr.Body.String())
	}

	admin := &db.User{ID: 1, Role: "Admin"}
	res, err := srv.coreDBQuery(admin, "app:"+nonce, dbQueryRequest{
		SQL: "SELECT email, uid FROM stamps"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("stamps rows = %v, want one", res.Rows)
	}
	if res.Rows[0][0] != "kay@example.com" {
		t.Errorf("stamp email = %v, want kay@example.com (injected, not spoofed)", res.Rows[0][0])
	}
	if got := fmt.Sprintf("%v", res.Rows[0][1]); got != strconv.FormatInt(kay.ID, 10) {
		t.Errorf("stamp uid = %v (%T), want kay's numerical ID %d", res.Rows[0][1], res.Rows[0][1], kay.ID)
	}

	// No bearer token: the auth service rejects the call outright.
	rr = testRequest(t, srv, http.MethodPost, "/service/call/keeper",
		strings.NewReader(`{"task": "stamp", "args": {}}`),
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("anonymous stamp: status %d, want 401", rr.Code)
	}
}

// A full-text search service, front to back: the migration builds the index,
// one tool feeds it, one tool searches it. `MATCH $query` is a binding like
// any other — the caller varies the search terms, never the query.
const fts5ToolFile = `[migrate] Build the note index.

CREATE VIRTUAL TABLE IF NOT EXISTS notes USING fts5(title, body)
---
[add-note] File a note.

INSERT INTO notes (title, body)
  VALUES ($title, $body)
---
[search-notes] Search notes, best match first.
# $query is FTS5 match syntax: bare words, "quoted phrases", prefix*, AND/OR/NOT.

SELECT title, snippet(notes, 1, '<b>', '</b>', '…', 10) AS excerpt
  FROM notes
  WHERE notes MATCH $query
  ORDER BY rank
  LIMIT 20

{
  "hits": $.rows
}
`

func TestBrowserSQLRunnerFTS5(t *testing.T) {
	requireFTS5(t)
	srv, _, nonce := newVirtualSvcServer(t, fts5ToolFile, db.ServiceDescriptor{})

	call := func(body string) string {
		t.Helper()
		rr := testRequest(t, srv, http.MethodPost, "/service/call/keeper",
			strings.NewReader(body), map[string]string{"X-App-Nonce": nonce})
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", body, rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}

	call(`{"task": "migrate", "args": {}}`)
	call(`{"task": "add-note", "args": {"title": "Frogs", "body": "the pond is full of frogs, jumping"}}`)
	call(`{"task": "add-note", "args": {"title": "Birds", "body": "the sky is full of birds"}}`)

	got := call(`{"task": "search-notes", "args": {"query": "frog*"}}`)
	if !strings.Contains(got, "Frogs") || strings.Contains(got, "Birds") {
		t.Errorf("search body = %s, want the frog note alone", got)
	}
	// The snippet markers come back HTML-escaped by Go's JSON encoder, so
	// this is `<b>frogs</b>` wearing its travelling clothes.
	if !strings.Contains(got, `\u003cb\u003efrogs\u003c/b\u003e`) {
		t.Errorf("search body = %s, want a highlighted snippet", got)
	}

	// The wildcard belongs to the caller, same as with LIKE: the tool never
	// splices anything into the SQL.
	if got := call(`{"task": "search-notes", "args": {"query": "sky"}}`); !strings.Contains(got, "Birds") {
		t.Errorf("search sky = %s", got)
	}
}
