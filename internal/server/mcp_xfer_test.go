package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"poggers.institute/freshbreath/internal/db"
)

// callCentralTool invokes a tool on the central MCP server via an in-memory
// transport. With no admin auth service configured, the call runs as the
// synthetic Superuser.
func callCentralTool(t *testing.T, srv *Server, name string, args map[string]interface{}) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	mcps := srv.buildCentralMCPServerForRole("Superuser")
	ss, err := mcps.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })

	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func toolResultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		return ""
	}
	txt, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	return txt.Text
}

func createAppFile(t *testing.T, srv *Server, nonce, path string, content []byte) {
	t.Helper()
	webDir := filepath.Join(srv.config.DataDir, "apps", nonce, "web")
	fullPath := filepath.Join(webDir, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		t.Fatalf("write app file: %v", err)
	}
}

func TestMCPListAppFiles(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "list-app", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Empty initially.
	res := callCentralTool(t, srv, "list_app_files", map[string]interface{}{"nonce": nonce})
	if res.IsError {
		t.Fatalf("list_app_files failed: %s", toolResultText(t, res))
	}
	if !strings.Contains(toolResultText(t, res), `"files":[]`) {
		t.Errorf("expected empty files, got %s", toolResultText(t, res))
	}

	createAppFile(t, srv, nonce, "index.html", []byte("<h1>hello</h1>"))
	createAppFile(t, srv, nonce, "styles.css", []byte("body { color: blue; }"))

	res = callCentralTool(t, srv, "list_app_files", map[string]interface{}{"nonce": nonce})
	if res.IsError {
		t.Fatalf("list_app_files failed: %s", toolResultText(t, res))
	}
	var listRes struct {
		Files []appFile `json:"files"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &listRes); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(listRes.Files) != 2 {
		t.Errorf("files = %v, want 2", listRes.Files)
	}

	// Search by content.
	res = callCentralTool(t, srv, "list_app_files", map[string]interface{}{"nonce": nonce, "search": "blue"})
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &listRes); err != nil {
		t.Fatalf("parse search: %v", err)
	}
	if len(listRes.Files) != 1 || listRes.Files[0].Path != "styles.css" {
		t.Errorf("search result = %v, want [styles.css]", listRes.Files)
	}
}

func TestMCPReadAppFile(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "read-app", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "index.html", []byte("<h1>hello world</h1>"))

	res := callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce": nonce,
		"path":  "index.html",
	})
	if res.IsError {
		t.Fatalf("read_app_file failed: %s", toolResultText(t, res))
	}
	var readRes struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &readRes); err != nil {
		t.Fatalf("parse read: %v", err)
	}
	if readRes.Content != "<h1>hello world</h1>" {
		t.Errorf("content = %q, want <h1>hello world</h1>", readRes.Content)
	}

	// Chunk read.
	res = callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce":  nonce,
		"path":   "index.html",
		"offset": 4,
		"limit":  5,
	})
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &readRes); err != nil {
		t.Fatalf("parse chunk: %v", err)
	}
	if readRes.Content != "hello" {
		t.Errorf("chunk = %q, want hello", readRes.Content)
	}
}

func TestMCPReadAppFileBinary(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "read-bin", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "data.bin", []byte{0xff, 0xfe, 0xfd})

	res := callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce": nonce,
		"path":  "data.bin",
	})
	if res.IsError {
		t.Fatalf("read_app_file failed: %s", toolResultText(t, res))
	}
	var readRes struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &readRes); err != nil {
		t.Fatalf("parse read: %v", err)
	}
	if readRes.Encoding != "base64" {
		t.Errorf("encoding = %q, want base64", readRes.Encoding)
	}
	want := base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe, 0xfd})
	if readRes.Content != want {
		t.Errorf("content = %q, want %q", readRes.Content, want)
	}
}

func TestMCPWriteAppFileWholeAndPatch(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "write-app", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Write whole file.
	res := callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce":   nonce,
		"path":    "index.html",
		"content": "<h1>hello</h1>",
	})
	if res.IsError {
		t.Fatalf("write_app_file failed: %s", toolResultText(t, res))
	}

	// Patch via old_text.
	res = callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce":    nonce,
		"path":     "index.html",
		"content":  "goodbye",
		"old_text": "hello",
	})
	if res.IsError {
		t.Fatalf("write_app_file patch failed: %s", toolResultText(t, res))
	}

	data, err := srv.coreReadAppFile(&db.User{ID: 1, Role: "Superuser"}, nonce, "index.html", 0, 0)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "<h1>goodbye</h1>" {
		t.Errorf("content = %q, want <h1>goodbye</h1>", data)
	}
}

func TestMCPWriteAppFileOldTextNotFound(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "write-missing", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "index.html", []byte("abc"))

	res := callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce":    nonce,
		"path":     "index.html",
		"content":  "x",
		"old_text": "notfound",
	})
	if !res.IsError {
		t.Fatal("expected error for missing old_text")
	}
	if !strings.Contains(toolResultText(t, res), "not found") {
		t.Errorf("error = %q, want not found", toolResultText(t, res))
	}
}

func TestMCPWriteAppFileOldTextNotUnique(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "write-dup", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "index.html", []byte("abc abc"))

	res := callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce":    nonce,
		"path":     "index.html",
		"content":  "x",
		"old_text": "abc",
	})
	if !res.IsError {
		t.Fatal("expected error for non-unique old_text")
	}
	if !strings.Contains(toolResultText(t, res), "not unique") {
		t.Errorf("error = %q, want not unique", toolResultText(t, res))
	}
}

func TestMCPDeleteAppFile(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "delete-app", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "index.html", []byte("bye"))

	res := callCentralTool(t, srv, "delete_app_file", map[string]interface{}{
		"nonce": nonce,
		"path":  "index.html",
	})
	if res.IsError {
		t.Fatalf("delete_app_file failed: %s", toolResultText(t, res))
	}

	_, err = srv.coreReadAppFile(&db.User{ID: 1, Role: "Superuser"}, nonce, "index.html", 0, 0)
	if err == nil {
		t.Fatal("expected file to be deleted")
	}
}

func TestMCPListServiceFiles(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	// Empty initially.
	res := callCentralTool(t, srv, "list_service_files", map[string]interface{}{"name": svc.Name})
	if res.IsError {
		t.Fatalf("list_service_files failed: %s", toolResultText(t, res))
	}
	if !strings.Contains(toolResultText(t, res), `"files":[]`) {
		t.Errorf("expected empty files, got %s", toolResultText(t, res))
	}

	content := []byte("[build]\nmake all\n")
	if err := srv.coreWriteServiceFile(admin, svc.ID, content, ""); err != nil {
		t.Fatalf("write service file: %v", err)
	}

	res = callCentralTool(t, srv, "list_service_files", map[string]interface{}{"name": svc.Name})
	var listRes struct {
		Files []appFile `json:"files"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &listRes); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(listRes.Files) != 1 || listRes.Files[0].Path != "deploy.txt" {
		t.Errorf("files = %v, want [deploy.txt]", listRes.Files)
	}

	// Search by content.
	res = callCentralTool(t, srv, "list_service_files", map[string]interface{}{"name": svc.Name, "search": "make all"})
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &listRes); err != nil {
		t.Fatalf("parse search: %v", err)
	}
	if len(listRes.Files) != 1 {
		t.Errorf("search result = %v, want 1 file", listRes.Files)
	}

	res = callCentralTool(t, srv, "list_service_files", map[string]interface{}{"name": svc.Name, "search": "missing"})
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &listRes); err != nil {
		t.Fatalf("parse search missing: %v", err)
	}
	if len(listRes.Files) != 0 {
		t.Errorf("search result = %v, want empty", listRes.Files)
	}
}

func TestMCPReadServiceFile(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	content := []byte("[build]\nmake all\n")
	if err := srv.coreWriteServiceFile(admin, svc.ID, content, ""); err != nil {
		t.Fatalf("write service file: %v", err)
	}

	res := callCentralTool(t, srv, "read_service_file", map[string]interface{}{"name": svc.Name})
	if res.IsError {
		t.Fatalf("read_service_file failed: %s", toolResultText(t, res))
	}
	var readRes struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &readRes); err != nil {
		t.Fatalf("parse read: %v", err)
	}
	if readRes.Content != string(content) {
		t.Errorf("content = %q, want %q", readRes.Content, content)
	}

	// Chunk read.
	res = callCentralTool(t, srv, "read_service_file", map[string]interface{}{
		"name":   svc.Name,
		"offset": 1,
		"limit":  5,
	})
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &readRes); err != nil {
		t.Fatalf("parse chunk: %v", err)
	}
	if readRes.Content != "build" {
		t.Errorf("chunk = %q, want build", readRes.Content)
	}
}

func TestMCPWriteServiceFile(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	res := callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name":    svc.Name,
		"content": "[build]\nmake all\n",
	})
	if res.IsError {
		t.Fatalf("write_service_file failed: %s", toolResultText(t, res))
	}

	res = callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name":     svc.Name,
		"content":  "make install",
		"old_text": "make all",
	})
	if res.IsError {
		t.Fatalf("write_service_file patch failed: %s", toolResultText(t, res))
	}

	data, _, err := srv.coreReadServiceFile(admin, svc.ID, 0, 0)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "[build]\nmake install\n" {
		t.Errorf("content = %q, want [build]\\nmake install\\n", data)
	}
}

func TestMCPDeleteServiceFile(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := srv.coreWriteServiceFile(admin, svc.ID, []byte("x"), ""); err != nil {
		t.Fatalf("write service file: %v", err)
	}

	res := callCentralTool(t, srv, "delete_service_file", map[string]interface{}{"name": svc.Name})
	if res.IsError {
		t.Fatalf("delete_service_file failed: %s", toolResultText(t, res))
	}

	_, _, err = srv.coreReadServiceFile(admin, svc.ID, 0, 0)
	if err == nil {
		t.Fatal("expected file to be deleted")
	}
}

func TestMCPServiceFileUnsupportedType(t *testing.T) {
	srv := newTestServer(t)
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "api-svc", "http://example.com", db.ServiceDescriptor{Type: "api"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	res := callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name":    svc.Name,
		"content": "x",
	})
	if !res.IsError {
		t.Fatal("expected error for unsupported service type")
	}
	if !strings.Contains(toolResultText(t, res), "does not support file publishing") {
		t.Errorf("error = %q, want unsupported type", toolResultText(t, res))
	}
}

// ── transport:"http" + read threshold escape ──
//
// The four transfer tools gain a `transport` option ("mcp" default / "http"
// escape). transport:"http" mints an act-token URL without transferring bytes.
// Reads additionally auto-escape to a URL when the whole-file result exceeds
// mcpInlineMaxBytes (chunked reads always inline). Writes never auto-escape —
// if MCP received the bytes, write them; a client that wants to skip the
// inline bloat uses transport:"http" up front.
//
// callCentralTool runs as the synthetic Superuser, which has no email — so an
// act token it mints can't be re-resolved by handleAct (a test artifact; in
// production mcpUser returns a real emailable user). The tests below verify
// the tool's minted token (verifyActToken) pins the right path+method, then
// re-mint that exact path for a real Admin (Ada) and dispatch via httptest to
// prove the chosen route yields the right bytes.

func toolResultJSON(t *testing.T, res *mcp.CallToolResult) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &m); err != nil {
		t.Fatalf("parse tool result %q: %v", toolResultText(t, res), err)
	}
	return m
}

// actTokenPayloadFromURL verifies the act token embedded in a tool-returned
// /api/act/<token> URL and returns its payload — proving the tool minted a
// valid token (HMAC + expiry + scope) for the right operation.
func actTokenPayloadFromURL(t *testing.T, srv *Server, fullURL string) *actTokenPayload {
	t.Helper()
	tok := strings.TrimPrefix(fullURL, srv.config.PublicBaseURL+"/api/act/")
	p, err := srv.verifyActToken(tok)
	if err != nil {
		t.Fatalf("verify tool-minted token: %v (url=%s)", err, fullURL)
	}
	return p
}

// ensureActAdmin returns Ada (get-or-create), so dispatchActPathAsAdmin is
// safe to call more than once within a test.
func ensureActAdmin(t *testing.T, srv *Server) *db.User {
	t.Helper()
	if u, err := srv.store.GetUserByEmail("ada@example.com"); err == nil {
		return u
	}
	return createActUser(t, srv)
}

// dispatchActPathAsAdmin re-mints an act token for the exact path the tool
// chose, but for a real Admin (Ada), then dispatches through httptest. The
// synthetic superuser's tokens can't dispatch (no email to re-resolve); this
// proves the tool's chosen route actually serves/accepts bytes.
func dispatchActPathAsAdmin(t *testing.T, srv *Server, method, pathQuery string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	ada := ensureActAdmin(t, srv)
	tok, err := srv.mintActToken(ada, method, pathQuery, 5*time.Minute)
	if err != nil {
		t.Fatalf("mint for ada: %v", err)
	}
	headers := map[string]string{}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	return testRequest(t, srv, method, "/api/act/"+tok, body, headers)
}

func makeAppWithFile(t *testing.T, srv *Server, name, file string, content []byte) string {
	t.Helper()
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, name, "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, file, content)
	return nonce
}

// ── read_app_file ──

func TestMCPReadAppFileTransportHTTP(t *testing.T) {
	srv := newTestServer(t)
	nonce := makeAppWithFile(t, srv, "xfer-app", "big.txt", bytes.Repeat([]byte("a"), 500))

	res := callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce": nonce, "path": "big.txt", "transport": "http",
	})
	m := toolResultJSON(t, res)
	url, _ := m["url"].(string)
	if url == "" || !strings.HasPrefix(url, srv.config.PublicBaseURL+"/api/act/") {
		t.Fatalf("url = %q, want a /api/act/ URL under PublicBaseURL", url)
	}
	if m["method"] != "GET" {
		t.Fatalf("method = %v, want GET", m["method"])
	}
	if m["size"] != float64(500) {
		t.Fatalf("size = %v, want 500", m["size"])
	}
	if ct, _ := m["content_type"].(string); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content_type = %q, want text/plain (sniffed)", ct)
	}
	p := actTokenPayloadFromURL(t, srv, url)
	if p.Method != "GET" || p.Path != "/api/apps/"+nonce+"/web?file=big.txt" {
		t.Fatalf("payload = %+v, want GET /api/apps/%s/web?file=big.txt", p, nonce)
	}
	rr := dispatchActPathAsAdmin(t, srv, "GET", p.Path, nil, "")
	if rr.Code != http.StatusOK || rr.Body.String() != strings.Repeat("a", 500) {
		t.Fatalf("dispatch = %d %q, want 500 a's", rr.Code, rr.Body.String())
	}
}

func TestMCPReadAppFileTransportHTTPRejectsChunked(t *testing.T) {
	srv := newTestServer(t)
	nonce := makeAppWithFile(t, srv, "xfer-chunk", "f.txt", []byte("data"))
	res := callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce": nonce, "path": "f.txt", "offset": 0, "limit": 10, "transport": "http",
	})
	if !res.IsError {
		t.Fatalf("expected error for transport:http + offset/limit, got: %s", toolResultText(t, res))
	}
}

func TestMCPReadAppFileThresholdEscape(t *testing.T) {
	srv := newTestServer(t)
	body := bytes.Repeat([]byte("Z"), 12000)
	nonce := makeAppWithFile(t, srv, "thresh-app", "big.bin", body)

	res := callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce": nonce, "path": "big.bin", // default transport
	})
	m := toolResultJSON(t, res)
	if _, ok := m["error"]; !ok {
		t.Fatalf("expected error field for threshold escape, got: %v", m)
	}
	url, _ := m["url"].(string)
	if url == "" {
		t.Fatalf("expected url in threshold escape, got: %v", m)
	}
	if m["method"] != "GET" {
		t.Fatalf("method = %v, want GET", m["method"])
	}
	if m["size"] != float64(12000) {
		t.Fatalf("size = %v, want 12000", m["size"])
	}
	if m["max_inline_bytes"] != float64(mcpInlineMaxBytes) {
		t.Fatalf("max_inline_bytes = %v, want %d", m["max_inline_bytes"], mcpInlineMaxBytes)
	}
	p := actTokenPayloadFromURL(t, srv, url)
	rr := dispatchActPathAsAdmin(t, srv, "GET", p.Path, nil, "")
	if rr.Code != http.StatusOK || !bytes.Equal(rr.Body.Bytes(), body) {
		t.Fatalf("dispatch = %d (len %d), want 200 len %d", rr.Code, rr.Body.Len(), len(body))
	}
}

func TestMCPReadAppFileChunkedStaysInline(t *testing.T) {
	srv := newTestServer(t)
	nonce := makeAppWithFile(t, srv, "chunk-app", "big.txt", bytes.Repeat([]byte("Q"), 12000))

	res := callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce": nonce, "path": "big.txt", "offset": 0, "limit": 100,
	})
	m := toolResultJSON(t, res)
	if _, ok := m["url"]; ok {
		t.Fatalf("chunked read should stay inline, got url: %v", m)
	}
	if content, _ := m["content"].(string); content != strings.Repeat("Q", 100) {
		t.Fatalf("content len = %d, want 100 inline bytes", len(content))
	}
}

func TestMCPReadAppFileSmallStaysInline(t *testing.T) {
	srv := newTestServer(t)
	nonce := makeAppWithFile(t, srv, "small-app", "s.txt", []byte("small"))
	res := callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce": nonce, "path": "s.txt",
	})
	m := toolResultJSON(t, res)
	if _, ok := m["url"]; ok {
		t.Fatalf("small file should stay inline, got url: %v", m)
	}
	if m["content"] != "small" {
		t.Fatalf("content = %v, want small", m["content"])
	}
}

// ── write_app_file ──

func TestMCPWriteAppFileTransportHTTP(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "wapp", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	// content is ignored on the http path — the client PUTs the real body later.
	res := callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce": nonce, "path": "via-http.txt", "content": "ignored", "transport": "http",
	})
	m := toolResultJSON(t, res)
	url, _ := m["url"].(string)
	if url == "" || !strings.HasPrefix(url, srv.config.PublicBaseURL+"/api/act/") {
		t.Fatalf("url = %q", url)
	}
	if m["method"] != "PUT" {
		t.Fatalf("method = %v, want PUT", m["method"])
	}
	p := actTokenPayloadFromURL(t, srv, url)
	if p.Method != "PUT" || !strings.HasPrefix(p.Path, "/api/apps/"+nonce+"/web?file=") {
		t.Fatalf("payload = %+v, want PUT /api/apps/%s/web?file=...", p, nonce)
	}
	rr := dispatchActPathAsAdmin(t, srv, "PUT", p.Path, bytes.NewReader([]byte("written over http\n")), "text/plain")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("dispatch = %d %q, want 204", rr.Code, rr.Body.String())
	}
	data, err := srv.coreReadAppFile(&db.User{ID: 1, Role: "Superuser"}, nonce, "via-http.txt", 0, 0)
	if err != nil || string(data) != "written over http\n" {
		t.Fatalf("readback = %q err=%v", string(data), err)
	}
}

func TestMCPWriteAppFileTransportHTTPRejectsPatch(t *testing.T) {
	srv := newTestServer(t)
	nonce, _ := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "wpapp", "", "", nil, nil)
	res := callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce": nonce, "path": "x.txt", "content": "new", "old_text": "old", "transport": "http",
	})
	if !res.IsError {
		t.Fatalf("expected error for transport:http + old_text, got: %s", toolResultText(t, res))
	}
}

func TestMCPWriteAppFileLargeNoThreshold(t *testing.T) {
	srv := newTestServer(t)
	nonce, _ := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "bigw", "", "", nil, nil)
	big := bytes.Repeat([]byte("M"), 12000)
	res := callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce": nonce, "path": "big.txt", "content": string(big), // default transport
	})
	m := toolResultJSON(t, res)
	if _, ok := m["url"]; ok {
		t.Fatalf("writes never auto-escape, got url: %v", m)
	}
	if m["status"] != "written" {
		t.Fatalf("status = %v, want written (no threshold on writes)", m["status"])
	}
	data, err := srv.coreReadAppFile(&db.User{ID: 1, Role: "Superuser"}, nonce, "big.txt", 0, 0)
	if err != nil || !bytes.Equal(data, big) {
		t.Fatalf("readback len = %d err=%v, want %d", len(data), err, len(big))
	}
}

func TestMCPWriteAppFilePatchStaysInline(t *testing.T) {
	srv := newTestServer(t)
	nonce, _ := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "patchapp", "", "", nil, nil)
	createAppFile(t, srv, nonce, "p.txt", []byte("hello old world"))
	res := callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce": nonce, "path": "p.txt", "content": "new", "old_text": "old",
	})
	m := toolResultJSON(t, res)
	if m["status"] != "written" {
		t.Fatalf("status = %v, want written", m["status"])
	}
	data, _ := srv.coreReadAppFile(&db.User{ID: 1, Role: "Superuser"}, nonce, "p.txt", 0, 0)
	if string(data) != "hello new world" {
		t.Fatalf("patched = %q, want hello new world", string(data))
	}
}

// ── read_service_file / write_service_file (symmetric) ──

func TestMCPReadServiceFileTransportHTTP(t *testing.T) {
	srv := newTestServer(t)
	idStr := registerService(t, srv, "rsvc", "http://rsvc", db.ServiceDescriptor{Type: "tasks"})
	id := parseServiceID(t, idStr)
	content := []byte("task one\ntask two\n")
	if err := srv.coreWriteServiceFile(&db.User{ID: 1, Role: "Superuser"}, id, content, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res := callCentralTool(t, srv, "read_service_file", map[string]interface{}{
		"name": "rsvc", "transport": "http",
	})
	m := toolResultJSON(t, res)
	url, _ := m["url"].(string)
	if url == "" || !strings.HasPrefix(url, srv.config.PublicBaseURL+"/api/act/") {
		t.Fatalf("url = %q", url)
	}
	if m["method"] != "GET" {
		t.Fatalf("method = %v, want GET", m["method"])
	}
	if m["size"] != float64(len(content)) {
		t.Fatalf("size = %v, want %d", m["size"], len(content))
	}
	p := actTokenPayloadFromURL(t, srv, url)
	wantPath := "/api/services/" + idStr + "/files"
	if p.Method != "GET" || p.Path != wantPath {
		t.Fatalf("payload = %+v, want GET %s", p, wantPath)
	}
	rr := dispatchActPathAsAdmin(t, srv, "GET", p.Path, nil, "")
	if rr.Code != http.StatusOK || rr.Body.String() != string(content) {
		t.Fatalf("dispatch = %d %q, want the definition bytes", rr.Code, rr.Body.String())
	}
}

func TestMCPReadServiceFileThresholdEscape(t *testing.T) {
	srv := newTestServer(t)
	idStr := registerService(t, srv, "tsvc", "http://tsvc", db.ServiceDescriptor{Type: "tasks"})
	id := parseServiceID(t, idStr)
	body := bytes.Repeat([]byte("T"), 12000)
	if err := srv.coreWriteServiceFile(&db.User{ID: 1, Role: "Superuser"}, id, body, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res := callCentralTool(t, srv, "read_service_file", map[string]interface{}{
		"name": "tsvc", // default transport
	})
	m := toolResultJSON(t, res)
	if _, ok := m["error"]; !ok {
		t.Fatalf("expected threshold escape, got: %v", m)
	}
	url, _ := m["url"].(string)
	if url == "" || m["method"] != "GET" {
		t.Fatalf("expected {error,url,method:GET}, got: %v", m)
	}
	if m["size"] != float64(12000) {
		t.Fatalf("size = %v, want 12000", m["size"])
	}
	p := actTokenPayloadFromURL(t, srv, url)
	rr := dispatchActPathAsAdmin(t, srv, "GET", p.Path, nil, "")
	if rr.Code != http.StatusOK || !bytes.Equal(rr.Body.Bytes(), body) {
		t.Fatalf("dispatch = %d len %d, want 200 len %d", rr.Code, rr.Body.Len(), len(body))
	}
}

func TestMCPReadServiceFileChunkedStaysInline(t *testing.T) {
	srv := newTestServer(t)
	idStr := registerService(t, srv, "csvc", "http://csvc", db.ServiceDescriptor{Type: "tasks"})
	id := parseServiceID(t, idStr)
	if err := srv.coreWriteServiceFile(&db.User{ID: 1, Role: "Superuser"}, id, bytes.Repeat([]byte("T"), 12000), ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res := callCentralTool(t, srv, "read_service_file", map[string]interface{}{
		"name": "csvc", "offset": 0, "limit": 100,
	})
	m := toolResultJSON(t, res)
	if _, ok := m["url"]; ok {
		t.Fatalf("chunked read should stay inline, got url: %v", m)
	}
	if content, _ := m["content"].(string); len(content) != 100 {
		t.Fatalf("content len = %d, want 100", len(content))
	}
}

func TestMCPWriteServiceFileTransportHTTP(t *testing.T) {
	srv := newTestServer(t)
	idStr := registerService(t, srv, "wsvc", "http://wsvc", db.ServiceDescriptor{Type: "tasks"})
	res := callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name": "wsvc", "content": "ignored", "transport": "http",
	})
	m := toolResultJSON(t, res)
	url, _ := m["url"].(string)
	if url == "" {
		t.Fatalf("url = %q", url)
	}
	if m["method"] != "PUT" {
		t.Fatalf("method = %v, want PUT", m["method"])
	}
	p := actTokenPayloadFromURL(t, srv, url)
	wantPath := "/api/services/" + idStr + "/files"
	if p.Method != "PUT" || p.Path != wantPath {
		t.Fatalf("payload = %+v, want PUT %s", p, wantPath)
	}
	rr := dispatchActPathAsAdmin(t, srv, "PUT", p.Path, bytes.NewReader([]byte("svc via http\n")), "text/plain")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("dispatch = %d %q, want 204", rr.Code, rr.Body.String())
	}
	id := parseServiceID(t, idStr)
	data, _, err := srv.coreReadServiceFile(&db.User{ID: 1, Role: "Superuser"}, id, 0, 0)
	if err != nil || string(data) != "svc via http\n" {
		t.Fatalf("readback = %q err=%v", string(data), err)
	}
}

func TestMCPWriteServiceFileTransportHTTPRejectsPatch(t *testing.T) {
	srv := newTestServer(t)
	registerService(t, srv, "wpsvc", "http://wpsvc", db.ServiceDescriptor{Type: "tasks"})
	res := callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name": "wpsvc", "content": "new", "old_text": "old", "transport": "http",
	})
	if !res.IsError {
		t.Fatalf("expected error for transport:http + old_text, got: %s", toolResultText(t, res))
	}
}

func TestMCPWriteServiceFileLargeNoThreshold(t *testing.T) {
	srv := newTestServer(t)
	idStr := registerService(t, srv, "bwsvc", "http://bwsvc", db.ServiceDescriptor{Type: "tasks"})
	id := parseServiceID(t, idStr)
	big := bytes.Repeat([]byte("S"), 12000)
	res := callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name": "bwsvc", "content": string(big), // default transport, no threshold
	})
	m := toolResultJSON(t, res)
	if _, ok := m["url"]; ok {
		t.Fatalf("service writes never auto-escape, got url: %v", m)
	}
	if m["status"] != "written" {
		t.Fatalf("status = %v, want written", m["status"])
	}
	data, _, err := srv.coreReadServiceFile(&db.User{ID: 1, Role: "Superuser"}, id, 0, 0)
	if err != nil || !bytes.Equal(data, big) {
		t.Fatalf("readback len = %d err=%v, want %d", len(data), err, len(big))
	}
}
