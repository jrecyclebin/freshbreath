package server

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// ── App ?file= single-file ops ──
//
// /api/apps/{nonce}/web grows a ?file=<relpath> query that selects single-file
// GET/PUT/DELETE on the web dir; without ?file= the existing bulk archive ops
// (zip GET, multipart POST, nuke DELETE) run unchanged. PUT is only valid
// with ?file= — it's a full-file raw-body replace; patches stay MCP-only.

func TestAppWebFileGet(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "getapp")
	createAppFile(t, srv, nonce, "index.html", []byte("<html><body>hi</body></html>"))

	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web?file=index.html", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got != "<html><body>hi</body></html>" {
		t.Fatalf("body = %q, want the file contents", got)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html (sniffed)", ct)
	}
}

func TestAppWebFileGetNested(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "nested")
	createAppFile(t, srv, nonce, "css/site.css", []byte("body{color:red}"))

	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web?file=css/site.css", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got != "body{color:red}" {
		t.Fatalf("body = %q, want nested file contents", got)
	}
}

func TestAppWebFileGetMissing(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "miss")
	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web?file=nope.html", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for missing file", rr.Code)
	}
}

func TestAppWebFileGetBadPath(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "badpath")
	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web?file=../etc/passwd", nil, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for traversal (cleanAppFilePath)", rr.Code)
	}
}

func TestAppWebFilePut(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "putapp")
	rr := testRequest(t, srv, http.MethodPut, "/api/apps/"+nonce+"/web?file=notes.txt",
		bytes.NewReader([]byte("hello put\n")), map[string]string{"Content-Type": "text/plain"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
	// read back through the same ?file= GET path — round-trips
	rr2 := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web?file=notes.txt", nil, nil)
	if rr2.Code != http.StatusOK || rr2.Body.String() != "hello put\n" {
		t.Fatalf("readback = %d %q", rr2.Code, rr2.Body.String())
	}
}

func TestAppWebFilePutCreatesMissingDirs(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "putdirs")
	rr := testRequest(t, srv, http.MethodPut, "/api/apps/"+nonce+"/web?file=new/deep/page.html",
		bytes.NewReader([]byte("<p>ok</p>")), map[string]string{"Content-Type": "text/html"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
	rr2 := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web?file=new/deep/page.html", nil, nil)
	if rr2.Code != http.StatusOK || rr2.Body.String() != "<p>ok</p>" {
		t.Fatalf("readback = %d %q", rr2.Code, rr2.Body.String())
	}
}

func TestAppWebFileDelete(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "delapp")
	createAppFile(t, srv, nonce, "gone.txt", []byte("bye"))

	rr := testRequest(t, srv, http.MethodDelete, "/api/apps/"+nonce+"/web?file=gone.txt", nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	rr2 := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web?file=gone.txt", nil, nil)
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("readback status = %d, want 404 after delete", rr2.Code)
	}
}

func TestAppWebFileDeleteMissing(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "delmiss")
	rr := testRequest(t, srv, http.MethodDelete, "/api/apps/"+nonce+"/web?file=absent.txt", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for deleting a missing file", rr.Code)
	}
}

func TestAppWebPutWithoutFile(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "putnofile")
	rr := testRequest(t, srv, http.MethodPut, "/api/apps/"+nonce+"/web", bytes.NewReader([]byte("x")), nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (PUT only valid with ?file=)", rr.Code)
	}
}

// TestAppWebGetWithoutFileStillZip pins the bulk-download path: ?file= unset
// keeps returning a zip, so the single-file branch can't have disturbed it.
func TestAppWebGetWithoutFileStillZip(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "zipapp")
	createAppFile(t, srv, nonce, "a.txt", []byte("aaa"))

	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type = %q, want application/zip (bulk path unchanged)", ct)
	}
}

// ── Service PUT raw-body ──
//
// /api/services/{id}/files grows a PUT (raw body) for full-definition write;
// existing GET/POST/DELETE stay. PUT = full replace (coreWriteServiceFile with
// empty oldText); POST stays the multipart control-panel upload. Services
// have no ?file= — the definition path is server-derived.

func TestServiceFilesPutTasks(t *testing.T) {
	srv := newTestServer(t)
	id := registerService(t, srv, "tasks-svc", "http://tasks", db.ServiceDescriptor{Type: "tasks"})

	rr := testRequest(t, srv, http.MethodPut, "/api/services/"+id+"/files",
		bytes.NewReader([]byte("- do stuff\n- do more")), map[string]string{"Content-Type": "text/plain"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
	data, _, err := srv.coreReadServiceFile(&db.User{ID: 1, Role: "Superuser"}, parseServiceID(t, id), 0, 0)
	if err != nil {
		t.Fatalf("readback core: %v", err)
	}
	if string(data) != "- do stuff\n- do more" {
		t.Fatalf("readback = %q, want the PUT body", string(data))
	}
}

func TestServiceFilesPutVirtual(t *testing.T) {
	srv := newTestServer(t)
	id := registerService(t, srv, "virt-svc", "http://virt", db.ServiceDescriptor{Type: "virtual"})

	rr := testRequest(t, srv, http.MethodPut, "/api/services/"+id+"/files",
		bytes.NewReader([]byte("virtual body")), map[string]string{"Content-Type": "text/plain"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
}

func TestServiceFilesPutNonPublishable(t *testing.T) {
	srv := newTestServer(t)
	id := registerService(t, srv, "mcp-svc", "http://mcp", db.ServiceDescriptor{Type: "mcp"})

	rr := testRequest(t, srv, http.MethodPut, "/api/services/"+id+"/files", bytes.NewReader([]byte("x")), nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (mcp services don't publish files)", rr.Code)
	}
}

// ── Act-token equivalence ──
//
// These are the deferred Phase-1 dispatch tests: the ?file=/PUT path must
// behave the same whether the request comes in directly (auth-off synthetic
// superuser here) or via an act-token dispatch (real Admin user Ada). Same
// handler, same authz gates, same bytes — the act token is just a second way
// to get a user into the context.

func TestActTokenAppFileGet(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "actget")
	createAppFile(t, srv, nonce, "index.html", []byte("<h1>act</h1>"))
	ada := createActUser(t, srv)

	tok, err := srv.mintActToken(ada, http.MethodGet, "/api/apps/"+nonce+"/web?file=index.html", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	direct := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web?file=index.html", nil, nil)
	viaAct := testRequest(t, srv, http.MethodGet, "/api/act/"+tok, nil, nil)

	if viaAct.Code != direct.Code {
		t.Fatalf("act status %d != direct %d", viaAct.Code, direct.Code)
	}
	if viaAct.Body.String() != direct.Body.String() {
		t.Fatalf("act body %q != direct %q", viaAct.Body.String(), direct.Body.String())
	}
	if viaAct.Header().Get("Content-Type") != direct.Header().Get("Content-Type") {
		t.Fatalf("act Content-Type %q != direct %q", viaAct.Header().Get("Content-Type"), direct.Header().Get("Content-Type"))
	}
}

func TestActTokenAppFilePut(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "actput")
	ada := createActUser(t, srv)

	tok, err := srv.mintActToken(ada, http.MethodPut, "/api/apps/"+nonce+"/web?file=via-act.txt", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rr := testRequest(t, srv, http.MethodPut, "/api/act/"+tok,
		bytes.NewReader([]byte("written via act token\n")), map[string]string{"Content-Type": "text/plain"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("act PUT status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
	// read back via a direct GET — proves the bytes landed on disk
	rr2 := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web?file=via-act.txt", nil, nil)
	if rr2.Code != http.StatusOK || rr2.Body.String() != "written via act token\n" {
		t.Fatalf("readback = %d %q", rr2.Code, rr2.Body.String())
	}
}

func TestActTokenAppFileDelete(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "actdel")
	createAppFile(t, srv, nonce, "killme.txt", []byte("doomed"))
	ada := createActUser(t, srv)

	tok, err := srv.mintActToken(ada, http.MethodDelete, "/api/apps/"+nonce+"/web?file=killme.txt", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rr := testRequest(t, srv, http.MethodDelete, "/api/act/"+tok, nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("act DELETE status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
	rr2 := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web?file=killme.txt", nil, nil)
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("readback = %d, want 404 after act-token delete", rr2.Code)
	}
}

func TestActTokenServiceFilePut(t *testing.T) {
	srv := newTestServer(t)
	id := registerService(t, srv, "act-tasks", "http://act-tasks", db.ServiceDescriptor{Type: "tasks"})
	ada := createActUser(t, srv)

	tok, err := srv.mintActToken(ada, http.MethodPut, "/api/services/"+id+"/files", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rr := testRequest(t, srv, http.MethodPut, "/api/act/"+tok,
		bytes.NewReader([]byte("via act\n")), map[string]string{"Content-Type": "text/plain"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("act service PUT status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
	data, _, err := srv.coreReadServiceFile(&db.User{ID: 1, Role: "Superuser"}, parseServiceID(t, id), 0, 0)
	if err != nil || string(data) != "via act\n" {
		t.Fatalf("readback = %q err=%v", string(data), err)
	}
}

// TestActTokenServiceFilePutMemberDenied pins the trust boundary: an act
// token authenticates (the holder is a real user) but does not bypass
// authorization. A Member-rank user's act token reaches the handler — the
// authWrap short-circuit honors the pre-set userKey — but coreWriteServiceFile's
// gate(actor, rolesAdminPlus) still rejects with 403. The capability gets you
// in the door; your role decides the room.
func TestActTokenServiceFilePutMemberDenied(t *testing.T) {
	srv := newTestServer(t)
	id := registerService(t, srv, "act-tasks-member", "http://act-tasks-m", db.ServiceDescriptor{Type: "tasks"})
	member, err := srv.store.CreateUser("Melly", "melly@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	tok, err := srv.mintActToken(member, http.MethodPut, "/api/services/"+id+"/files", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rr := testRequest(t, srv, http.MethodPut, "/api/act/"+tok,
		bytes.NewReader([]byte("nope")), map[string]string{"Content-Type": "text/plain"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (Member can't write service files; gate intact)", rr.Code)
	}
}

// parseServiceID converts the string id registerService returns back to int64.
func parseServiceID(t *testing.T, id string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("parse service id %q: %v", id, err)
	}
	return n
}
