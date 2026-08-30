package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	_ "github.com/mattn/go-sqlite3"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	store := db.NewStore(sqlDB)
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	localKey, err := store.GetOrCreateLocalSigningKey()
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	baseDir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("path issue: %v", err)
	}
	agentMgr := sshkit.NewAgentManager()
	srv := &Server{
		config: Config{
			Dir:           baseDir,
			DataDir:       baseDir,
			PublicBaseURL: "http://localhost:9009",
		},
		store:          store,
		pending:        make(map[string]*pendingAuth),
		httpClient:     &http.Client{},
		oidcProviders:  make(map[int64]*oidc.Provider),
		hostedRoutes:   make(map[string]hostedApp),
		lastSeenAt:     make(map[int64]time.Time),
		localKey:       localKey,
		agentMgr:       agentMgr,
		gitGw:          sshkit.NewGitGateway(agentMgr, store),
		virtualMCPs:    newVirtualMCPRegistry(),
		mcpAuthPending: &sync.Map{},
	}
	srv.oauthSrv = newOAuthServer(srv)
	srv.SetupRoutes()
	return srv
}

// builtinAuth fetches one of the two seeded records (db.AuthSSHKey or
// db.AuthAnonymous).
func builtinAuth(t *testing.T, srv *Server, kind string) *db.AuthRecord {
	t.Helper()
	id, err := srv.store.BuiltinAuthID(kind)
	if err != nil {
		t.Fatalf("builtin %s record: %v", kind, err)
	}
	rec, err := srv.store.GetAuthRecord(id)
	if err != nil {
		t.Fatalf("get builtin %s record: %v", kind, err)
	}
	return rec
}

// newAuthRecord creates an auth record, failing the test on error.
func newAuthRecord(t *testing.T, srv *Server, name, kind string, d db.AuthDescriptor) *db.AuthRecord {
	t.Helper()
	rec, err := srv.store.CreateAuthRecord(name, kind, d)
	if err != nil {
		t.Fatalf("create auth record %q: %v", name, err)
	}
	return rec
}

func testRequest(t *testing.T, srv *Server, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// ── Static routes ──

func TestHandleEnvJS(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/env.js", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "__HOMESLICE_CONFIG") {
		t.Errorf("expected __HOMESLICE_CONFIG in body, got: %s", body)
	}
}

func TestHandleIndexRedirect(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/", nil, nil)
	if rr.Code != 302 {
		t.Fatalf("status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/control" {
		t.Errorf("Location = %q, want /control", loc)
	}
}

func TestHandleSetupJS(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/frbr.js", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
}

// ── Admin API: Apps ──

func TestCreateAndListApps(t *testing.T) {
	srv := newTestServer(t)

	body := `{"name": "my-app"}`
	rr := testRequest(t, srv, "POST", "/api/apps", strings.NewReader(body), nil)
	if rr.Code != 200 {
		t.Fatalf("create app: status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var created map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if created["nonce"] == "" {
		t.Fatal("expected nonce in create response")
	}

	rr = testRequest(t, srv, "GET", "/api/apps", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("list apps: status = %d", rr.Code)
	}

	var list map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	apps := list["apps"].([]interface{})
	if len(apps) != 1 {
		t.Errorf("len(apps) = %d, want 1", len(apps))
	}
}

func TestCreateAppMissingName(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "POST", "/api/apps", strings.NewReader(`{}`), nil)
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestAppDetail(t *testing.T) {
	srv := newTestServer(t)

	nonce := createApp(t, srv, "detail-test")
	rr := testRequest(t, srv, "GET", "/api/apps/"+nonce, nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var detail map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if detail["nonce"] != nonce {
		t.Errorf("nonce = %q, want %q", detail["nonce"], nonce)
	}
}

func TestAppDetailNotFound(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/api/apps/bogus", nil, nil)
	if rr.Code != 404 {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// ── Admin API: Services ──

func TestCreateAndListServices(t *testing.T) {
	srv := newTestServer(t)

	body := `{"name": "slack", "url": "https://slack.example/mcp", "descriptor": {"type": "mcp"}}`
	rr := testRequest(t, srv, "POST", "/api/services", strings.NewReader(body), nil)
	if rr.Code != 200 {
		t.Fatalf("create service: status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var created map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &created)
	if created["id"] == nil {
		t.Fatal("expected id in create response")
	}

	rr = testRequest(t, srv, "GET", "/api/services", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("list services: status = %d", rr.Code)
	}

	var list map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &list)
	services := list["services"].([]interface{})
	if len(services) != 1 {
		t.Errorf("len(services) = %d, want 1", len(services))
	}
}

func TestCreateServiceWithDescriptor(t *testing.T) {
	srv := newTestServer(t)

	// The descriptor keeps only Type/Proxied/DatabaseTarget/DatabaseName —
	// auth fields moved to auth records and no longer round-trip here.
	body := `{"name": "github", "url": "https://api.github.com", "descriptor": {"type": "api", "proxied": true, "database_target": "global", "database_name": "shared"}}`
	rr := testRequest(t, srv, "POST", "/api/services", strings.NewReader(body), nil)
	if rr.Code != 200 {
		t.Fatalf("create service: status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var created map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &created)
	desc := created["descriptor"].(map[string]interface{})
	if desc["type"] != "api" {
		t.Errorf("descriptor.type = %v, want api", desc["type"])
	}
	if desc["proxied"] != true {
		t.Errorf("descriptor.proxied = %v, want true", desc["proxied"])
	}
	if desc["database_target"] != "global" {
		t.Errorf("descriptor.database_target = %v, want global", desc["database_target"])
	}
	if desc["database_name"] != "shared" {
		t.Errorf("descriptor.database_name = %v, want shared", desc["database_name"])
	}
}

func TestCreateServiceMissingURL(t *testing.T) {
	srv := newTestServer(t)
	body := `{"name": "slack", "descriptor": {"type": "mcp"}}`
	rr := testRequest(t, srv, "POST", "/api/services", strings.NewReader(body), nil)
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestServiceDetail(t *testing.T) {
	srv := newTestServer(t)
	id := registerService(t, srv, "slack", "https://slack.example/mcp", db.ServiceDescriptor{Type: "mcp"})

	rr := testRequest(t, srv, "GET", "/api/services/"+id, nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var detail map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &detail)
	if detail["name"] != "slack" {
		t.Errorf("name = %q, want slack", detail["name"])
	}
}

func TestServiceDetailNotFound(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/api/services/9999", nil, nil)
	if rr.Code != 404 {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// ── Admin API: Service tools ──

func TestServiceToolsTasks(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	id := registerService(t, srv, "mytasks", "", db.ServiceDescriptor{Type: "tasks"})

	// Missing file returns an empty list for newly-created services.
	rr := testRequest(t, srv, "GET", "/api/services/"+id+"/tools", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var empty map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &empty)
	if len(empty["tools"].([]interface{})) != 0 {
		t.Errorf("expected empty tools, got %v", empty["tools"])
	}

	// Write a tasks file and verify the endpoint parses it.
	path := filepath.Join(srv.config.DataDir, "tasks", "mytasks.txt")
	if err := os.MkdirAll(filepath.Join(srv.config.DataDir, "tasks"), 0755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}
	if err := os.WriteFile(path, []byte("[greet] Say hello\necho hi\n---\n[build] Compile\nmake\n"), 0644); err != nil {
		t.Fatalf("write tasks file: %v", err)
	}
	defer os.Remove(path)

	rr = testRequest(t, srv, "GET", "/api/services/"+id+"/tools", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var res map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &res)
	tools := res["tools"].([]interface{})
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
	first := tools[0].(map[string]interface{})
	if first["name"] != "greet" || first["description"] != "Say hello" {
		t.Errorf("first tool = %v, want {name:greet description:Say hello}", first)
	}
	second := tools[1].(map[string]interface{})
	if second["name"] != "build" || second["description"] != "Compile" {
		t.Errorf("second tool = %v, want {name:build description:Compile}", second)
	}
}

func TestServiceToolsVirtual(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	id := registerService(t, srv, "myvirtual", "", db.ServiceDescriptor{Type: "virtual"})

	path := filepath.Join(srv.config.DataDir, "virtual", "myvirtual.txt")
	if err := os.MkdirAll(filepath.Join(srv.config.DataDir, "virtual"), 0755); err != nil {
		t.Fatalf("mkdir virtual: %v", err)
	}
	content := "[get-user] Fetch a user\nGET https://api.example.com/users/$id\n---\n[list-users] List users\nGET https://api.example.com/users\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write virtual file: %v", err)
	}
	defer os.Remove(path)

	rr := testRequest(t, srv, "GET", "/api/services/"+id+"/tools", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var res map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &res)
	tools := res["tools"].([]interface{})
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
	names := []string{}
	for _, t := range tools {
		names = append(names, t.(map[string]interface{})["name"].(string))
	}
	if names[0] != "get-user" || names[1] != "list-users" {
		t.Errorf("tool names = %v, want [get-user list-users]", names)
	}
}

func TestServiceToolsUnsupportedType(t *testing.T) {
	srv := newTestServer(t)
	id := registerService(t, srv, "slack", "https://slack.example/mcp", db.ServiceDescriptor{Type: "mcp"})

	rr := testRequest(t, srv, "GET", "/api/services/"+id+"/tools", nil, nil)
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// uploadServiceFile builds a multipart request body for a service file upload.
func uploadServiceFile(t *testing.T, srv *Server, serviceID int64, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	headers := map[string]string{
		"Content-Type": mw.FormDataContentType(),
	}
	return testRequest(t, srv, "POST", "/api/services/"+strconv.FormatInt(serviceID, 10)+"/files", &body, headers)
}

func TestServiceFilesHTTP(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()

	idStr := registerService(t, srv, "deploy", "", db.ServiceDescriptor{Type: "tasks"})
	id, _ := strconv.ParseInt(idStr, 10, 64)
	content := []byte("[build]\nmake all\n")

	// Download before upload is 404.
	rr := testRequest(t, srv, "GET", "/api/services/"+idStr+"/files", nil, nil)
	if rr.Code != 404 {
		t.Fatalf("pre-upload download status = %d, want 404", rr.Code)
	}

	// Upload.
	rr = uploadServiceFile(t, srv, id, "deploy.txt", content)
	if rr.Code != 200 {
		t.Fatalf("upload status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var upRes map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &upRes); err != nil {
		t.Fatalf("unmarshal upload response: %v", err)
	}
	if upRes["route"] != "tasks://deploy" {
		t.Errorf("route = %q, want tasks://deploy", upRes["route"])
	}

	// Legacy file exists.
	path := filepath.Join(srv.config.DataDir, "tasks", "deploy.txt")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read legacy file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("legacy file = %q, want %q", got, content)
	}

	// Download returns raw text.
	rr = testRequest(t, srv, "GET", "/api/services/"+idStr+"/files", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("download status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "deploy.txt") {
		t.Errorf("Content-Disposition = %q, want deploy.txt", cd)
	}
	if !bytes.Equal(rr.Body.Bytes(), content) {
		t.Errorf("download body = %q, want %q", rr.Body.Bytes(), content)
	}

	// Delete.
	rr = testRequest(t, srv, "DELETE", "/api/services/"+idStr+"/files", nil, nil)
	if rr.Code != 204 {
		t.Fatalf("delete status = %d, want 204", rr.Code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected legacy file to be removed")
	}
}

func TestServiceFilesHTTPUnsupportedType(t *testing.T) {
	srv := newTestServer(t)
	idStr := registerService(t, srv, "api-svc", "http://example.com", db.ServiceDescriptor{Type: "api"})
	id, _ := strconv.ParseInt(idStr, 10, 64)

	rr := uploadServiceFile(t, srv, id, "x.txt", []byte("x"))
	if rr.Code != 400 {
		t.Errorf("upload status = %d, want 400", rr.Code)
	}
}

func TestServiceFilesHTTPNoZip(t *testing.T) {
	srv := newTestServer(t)
	idStr := registerService(t, srv, "deploy", "", db.ServiceDescriptor{Type: "tasks"})
	id, _ := strconv.ParseInt(idStr, 10, 64)

	rr := uploadServiceFile(t, srv, id, "site.zip", []byte("PK"))
	if rr.Code != 400 {
		t.Errorf("upload status = %d, want 400", rr.Code)
	}
}

// ── Proxy ──

func TestProxyForwardsRequest(t *testing.T) {
	mockService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", auth)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"method":"tools/list"}` {
			t.Errorf("body = %q, want {\"method\":\"tools/list\"}", string(body))
		}
		w.Header().Set("X-Mock-Header", "yes")
		w.WriteHeader(200)
		w.Write([]byte(`{"tools":[]}`))
	}))
	defer mockService.Close()

	srv := newTestServer(t)
	nonce := createApp(t, srv, "proxy")
	id := registerService(t, srv, "mock", mockService.URL, db.ServiceDescriptor{Type: "mcp"})
	linkServiceToApp(t, srv, nonce, id)

	rr := testRequest(t, srv, "POST", "/service/"+id+"/tools/list",
		strings.NewReader(`{"method":"tools/list"}`),
		map[string]string{
			"X-App-Nonce":   nonce,
			"Accept":        "application/json",
			"Authorization": "Bearer test-token",
		})

	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != `{"tools":[]}` {
		t.Errorf("response body = %q, want {\"tools\":[]}", rr.Body.String())
	}
	if rr.Header().Get("X-Mock-Header") != "yes" {
		t.Error("expected X-Mock-Header to be forwarded")
	}
}

func TestLoginWithMetadataFallback(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "fallback-test")
	srv.httpClient = mockHTTPClient(func(req *http.Request) *http.Response {
		switch {
		// Metadata 404 — should fall back to default endpoints
		case strings.HasSuffix(req.URL.Path, "/.well-known/oauth-authorization-server"),
			strings.HasSuffix(req.URL.Path, "/.well-known/openid-configuration"):
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}
		case req.URL.Path == "/register":
			return jsonResp(201, map[string]string{"client_id": "fallback-client"})
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}
		}
	})

	// No client_id: the record falls back to metadata discovery + DCR, and
	// when even the metadata is missing, to default endpoint paths.
	rec := newAuthRecord(t, srv, "GH Upstream", db.AuthOAuth2,
		db.AuthDescriptor{AuthorizeURL: "https://gh.example/authorize"})
	setAppGate(t, srv, nonce, rec.ID)
	id := registerService(t, srv, "github", "https://gh.example/mcp", db.ServiceDescriptor{Type: "mcp"})
	linkServiceToApp(t, srv, nonce, id)

	rr := testRequest(t, srv, "GET", "/service/login?url=https://gh.example/mcp&state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body["type"] != "redirect" {
		t.Fatalf("expected redirect type, got %v", body["type"])
	}
	loc := body["url"].(string)
	if !strings.Contains(loc, "client_id=fallback-client") {
		t.Errorf("redirect missing client_id: %s", loc)
	}
}

func TestProxyMissingNonce(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "POST", "/service/1/tools/list", strings.NewReader(`{}`), nil)
	if rr.Code != 401 {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ── Login ──

func TestLoginByServiceURL(t *testing.T) {
	srv := newTestServer(t)
	srv.httpClient = mockHTTPClient(func(req *http.Request) *http.Response {
		switch {
		case strings.HasSuffix(req.URL.Path, "/.well-known/oauth-authorization-server"):
			return jsonResp(200, map[string]interface{}{
				"issuer":                           "https://slack.example",
				"authorization_endpoint":           "https://slack.example/authorize",
				"token_endpoint":                   "https://slack.example/token",
				"registration_endpoint":            "https://slack.example/register",
				"code_challenge_methods_supported": []string{"S256"},
			})
		case req.URL.Path == "/register":
			return jsonResp(201, map[string]string{"client_id": "mock-client"})
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}
		}
	})

	nonce := createApp(t, srv, "login-test")
	// No client_id: the gate record discovers metadata and registers a
	// client dynamically — the old upstream-MCP DCR path, now record-shaped.
	rec := newAuthRecord(t, srv, "Slack Upstream", db.AuthOAuth2,
		db.AuthDescriptor{AuthorizeURL: "https://slack.example/authorize"})
	setAppGate(t, srv, nonce, rec.ID)
	id := registerService(t, srv, "slack", "https://slack.example/mcp", db.ServiceDescriptor{Type: "mcp"})
	linkServiceToApp(t, srv, nonce, id)

	rr := testRequest(t, srv, "GET", "/service/login?url=https://slack.example/mcp&state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body["type"] != "redirect" {
		t.Fatalf("expected redirect type, got %v", body["type"])
	}
	loc := body["url"].(string)
	if !strings.Contains(loc, "client_id=mock-client") {
		t.Errorf("redirect missing client_id: %s", loc)
	}
}

func TestLoginWithoutURLIsAppGateLogin(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "login-test")
	// No url names the app's own gate; an open gate resolves instantly.
	rr := testRequest(t, srv, "GET", "/service/login?state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body["type"] != "anonymous" {
		t.Errorf("type = %v, want anonymous (open gate, nothing to clear)", body["type"])
	}
	// Missing state is still an error.
	if rr := testRequest(t, srv, "GET", "/service/login", nil,
		map[string]string{"X-App-Nonce": nonce}); rr.Code != 400 {
		t.Errorf("missing state: status = %d, want 400", rr.Code)
	}
}

func TestLoginUnknownServiceURL(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "login-test")
	rr := testRequest(t, srv, "GET", "/service/login?url=https://unknown.example/mcp&state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 403 {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestLoginViaQueryParam(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "login-test")
	rec := newAuthRecord(t, srv, "Slack Upstream", db.AuthOAuth2,
		db.AuthDescriptor{AuthorizeURL: "https://slack.example/authorize"})
	setAppGate(t, srv, nonce, rec.ID)
	srv.httpClient = mockHTTPClient(func(req *http.Request) *http.Response {
		switch {
		case strings.HasSuffix(req.URL.Path, "/.well-known/oauth-authorization-server"):
			return jsonResp(200, map[string]interface{}{
				"issuer":                           "https://slack.example",
				"authorization_endpoint":           "https://slack.example/authorize",
				"token_endpoint":                   "https://slack.example/token",
				"registration_endpoint":            "https://slack.example/register",
				"code_challenge_methods_supported": []string{"S256"},
			})
		case req.URL.Path == "/register":
			return jsonResp(201, map[string]string{"client_id": "mock-client"})
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}
		}
	})

	id := registerService(t, srv, "slack", "https://slack.example/mcp", db.ServiceDescriptor{Type: "mcp"})
	linkServiceToApp(t, srv, nonce, id)

	// No X-App-Nonce header — nonce comes from query param
	rr := testRequest(t, srv, "GET", "/service/login?url=https://slack.example/mcp&state=x&app_nonce="+nonce, nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body["type"] != "redirect" {
		t.Fatalf("expected redirect type, got %v", body["type"])
	}
	loc := body["url"].(string)
	if !strings.Contains(loc, "client_id=mock-client") {
		t.Errorf("redirect missing client_id: %s", loc)
	}
}

// ── Login with pre-registered client_id (API service) ──

func TestLoginWithPreRegisteredClient(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "api-test")
	srv.httpClient = mockHTTPClient(func(req *http.Request) *http.Response {
		switch {
		case strings.HasSuffix(req.URL.Path, "/.well-known/oauth-authorization-server"):
			return jsonResp(200, map[string]interface{}{
				"issuer":                           "https://github.com",
				"authorization_endpoint":           "https://github.com/login/oauth/authorize",
				"token_endpoint":                   "https://github.com/login/oauth/access_token",
				"code_challenge_methods_supported": []string{"S256"},
			})
		// Should NOT hit /register — client_id is pre-registered
		case req.URL.Path == "/register":
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("should not be called"))}
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}
		}
	})

	rec := newAuthRecord(t, srv, "GitHub", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		ClientID:     "gh-pre-registered-client",
		Provider:     "github",
	})
	setAppGate(t, srv, nonce, rec.ID)
	id := registerService(t, srv, "github", "https://api.github.com", db.ServiceDescriptor{Type: "api"})
	linkServiceToApp(t, srv, nonce, id)

	rr := testRequest(t, srv, "GET", "/service/login?url=https://api.github.com&state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body["type"] != "redirect" {
		t.Fatalf("expected redirect type, got %v", body["type"])
	}
	loc := body["url"].(string)
	if !strings.Contains(loc, "client_id=gh-pre-registered-client") {
		t.Errorf("redirect missing client_id: %s", loc)
	}
	// Should NOT have hit the registration endpoint
	if strings.Contains(loc, "/register") {
		t.Error("should not have attempted DCR for pre-registered client")
	}
}

// ── Login with API key auth ──

func TestLoginWithAPIKeyGate(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "key-test")
	rec := newAuthRecord(t, srv, "Weather Key", db.AuthAPIKey, db.AuthDescriptor{Key: "s3cret"})
	setAppGate(t, srv, nonce, rec.ID)
	id := registerService(t, srv, "weather", "https://api.weather.com", db.ServiceDescriptor{Type: "api"})
	linkServiceToApp(t, srv, nonce, id)

	rr := testRequest(t, srv, "GET", "/service/login?url=https://api.weather.com&state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	// An api_key gate always redirects to the key entry form — the stored
	// key is what the typed key is checked against, never handed out.
	if resp["type"] != "redirect" {
		t.Errorf("type = %v, want redirect", resp["type"])
	}
	url, _ := resp["url"].(string)
	if !strings.Contains(url, "/service/apikey-auth") {
		t.Errorf("url = %q, want apikey-auth URL", url)
	}
}

// The full api_key gate flow: login redirects to the form, the wrong key is
// refused, the right key finishes with a store entry carrying what was typed.
func TestAPIKeyAuthFlow(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "key-flow-test")
	rec := newAuthRecord(t, srv, "Weather Key", db.AuthAPIKey, db.AuthDescriptor{Key: "s3cret", Header: "X-Weather-Key"})
	setAppGate(t, srv, nonce, rec.ID)

	rr := testRequest(t, srv, "GET", "/service/login?state=corr-1", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("login: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	url, _ := resp["url"].(string)
	i := strings.Index(url, "state=")
	if i < 0 {
		t.Fatalf("no state in redirect URL %q", url)
	}
	state := url[i+len("state="):]

	// Wrong key → 401, and the state survives for a retry.
	rr = testRequest(t, srv, "POST", "/service/apikey-auth",
		strings.NewReader(`{"state":"`+state+`","api_key":"wrong"}`), nil)
	if rr.Code != 401 {
		t.Fatalf("wrong key: status = %d, want 401", rr.Code)
	}

	// Right key → the final page hands the typed key back as a store entry.
	rr = testRequest(t, srv, "POST", "/service/apikey-auth",
		strings.NewReader(`{"state":"`+state+`","api_key":"s3cret"}`), nil)
	if rr.Code != 200 {
		t.Fatalf("right key: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"key":"s3cret"`) || !strings.Contains(body, `"kind":"api_key"`) {
		t.Errorf("final page missing store entry fields: %s", body)
	}
}

func TestLoginAppNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "no-link-test")
	registerService(t, srv, "weather", "https://api.weather.com", db.ServiceDescriptor{Type: "api"})

	rr := testRequest(t, srv, "GET", "/service/login?url=https://api.weather.com&state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 403 {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestLoginAdminNonceAllowed(t *testing.T) {
	srv := newTestServer(t)
	srv.adminNonce = db.GenNonce()

	// Fully-specified oauth2 record: no discovery, no DCR, no network.
	rec := newAuthRecord(t, srv, "Admin IdP", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: "https://admin.example/authorize",
		TokenURL:     "https://admin.example/token",
		ClientID:     "admin-client",
	})
	srv.store.SetSetting("admin_auth_service", strconv.FormatInt(rec.ID, 10))

	// The control panel's ephemeral nonce logs in to its own gate (no url).
	rr := testRequest(t, srv, "GET", "/service/login?state=x", nil,
		map[string]string{"X-App-Nonce": srv.adminNonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body["type"] != "redirect" {
		t.Fatalf("expected redirect type, got %v", body["type"])
	}
	if loc, _ := body["url"].(string); !strings.Contains(loc, "client_id=admin-client") {
		t.Errorf("redirect missing client_id: %v", body["url"])
	}
}

func TestProxyInjectsAdminAPIKey(t *testing.T) {
	var receivedAuth string
	mockService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer mockService.Close()

	srv := newTestServer(t)
	nonce := createApp(t, srv, "proxy-key-test")
	rec := newAuthRecord(t, srv, "Weather Key", db.AuthAPIKey, db.AuthDescriptor{Key: "admin-secret-key"})
	id := registerService(t, srv, "weather", mockService.URL, db.ServiceDescriptor{Type: "api"})
	setServiceActsAs(t, srv, id, rec.ID)
	linkServiceToApp(t, srv, nonce, id)

	// Request WITHOUT Authorization header — proxy should inject the key
	rr := testRequest(t, srv, "GET", "/service/"+id+"/forecast", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if receivedAuth != "Bearer admin-secret-key" {
		t.Errorf("Authorization = %q, want Bearer admin-secret-key", receivedAuth)
	}
}

func TestProxyXAPIKeyOverridesStoredKey(t *testing.T) {
	var receivedAuth string
	mockService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer mockService.Close()

	srv := newTestServer(t)
	nonce := createApp(t, srv, "proxy-user-key-test")
	rec := newAuthRecord(t, srv, "Weather Key", db.AuthAPIKey, db.AuthDescriptor{Key: "admin-secret-key"})
	id := registerService(t, srv, "weather", mockService.URL, db.ServiceDescriptor{Type: "api"})
	setServiceActsAs(t, srv, id, rec.ID)
	linkServiceToApp(t, srv, nonce, id)

	// X-API-Key overrides the stored key when acts_as is an api_key record.
	rr := testRequest(t, srv, "GET", "/service/"+id+"/forecast", nil,
		map[string]string{
			"X-App-Nonce": nonce,
			"X-API-Key":   "user-own-key",
		})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if receivedAuth != "Bearer user-own-key" {
		t.Errorf("Authorization = %q, want Bearer user-own-key (X-API-Key should win)", receivedAuth)
	}
}

func TestProxyXAPIKeyRejectedWithoutKeyRecord(t *testing.T) {
	mockService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer mockService.Close()

	srv := newTestServer(t)
	nonce := createApp(t, srv, "proxy-stray-key-test")
	id := registerService(t, srv, "weather", mockService.URL, db.ServiceDescriptor{Type: "api"})
	linkServiceToApp(t, srv, nonce, id)

	// No api_key acts_as record: a stray X-API-Key is a caller error, not
	// silently swallowed like the old door did.
	rr := testRequest(t, srv, "GET", "/service/"+id+"/forecast", nil,
		map[string]string{
			"X-App-Nonce": nonce,
			"X-API-Key":   "user-own-key",
		})
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

// ── CORS ──

func TestCORSWithOrigin(t *testing.T) {
	srv := newTestServer(t)
	// Create an app whose URL matches the cross-origin request.
	rr := testRequest(t, srv, "POST", "/api/apps", strings.NewReader(`{"name": "ext", "url": "https://example.com/app"}`), nil)
	if rr.Code != 200 {
		t.Fatalf("create app: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]string
	json.Unmarshal(rr.Body.Bytes(), &res)
	rr = testRequest(t, srv, "GET", "/env.js?"+res["nonce"], nil, map[string]string{"Origin": "https://example.com"})
	if rr.Code != 200 {
		t.Fatalf("status = %d", rr.Code)
	}
	acao := rr.Header().Get("Access-Control-Allow-Origin")
	if acao != "https://example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want https://example.com", acao)
	}
	if rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("expected Access-Control-Allow-Credentials: true")
	}
}

func TestCORSMissingOrigin(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/env.js", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("expected no CORS headers when Origin is absent")
	}
}

func TestCORSPreflight(t *testing.T) {
	srv := newTestServer(t)
	// Create an app whose URL matches the cross-origin request.
	rr := testRequest(t, srv, "POST", "/api/apps", strings.NewReader(`{"name": "ext", "url": "https://example.com/app"}`), nil)
	if rr.Code != 200 {
		t.Fatalf("create app: %d %s", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, "OPTIONS", "/api/apps", nil, map[string]string{
		"Origin":                         "https://example.com",
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "X-App-Nonce, Content-Type",
	})
	if rr.Code != 204 {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Error("expected allowed origin on preflight")
	}
	if !strings.Contains(rr.Header().Get("Access-Control-Allow-Headers"), "X-App-Nonce") {
		t.Errorf("Allow-Headers = %q, expected X-App-Nonce", rr.Header().Get("Access-Control-Allow-Headers"))
	}
	if rr.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("expected Allow-Methods on preflight")
	}
}

// ── helpers ──

func createApp(t *testing.T, srv *Server, name string) string {
	t.Helper()
	rr := testRequest(t, srv, "POST", "/api/apps", strings.NewReader(`{"name": "`+name+`"}`), nil)
	if rr.Code != 200 {
		t.Fatalf("create app failed: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]string
	json.Unmarshal(rr.Body.Bytes(), &res)
	return res["nonce"]
}

func registerService(t *testing.T, srv *Server, name, serviceURL string, descriptor db.ServiceDescriptor) string {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"name":       name,
		"url":        serviceURL,
		"descriptor": descriptor,
	})
	rr := testRequest(t, srv, "POST", "/api/services", bytes.NewReader(body), nil)
	if rr.Code != 200 {
		t.Fatalf("create service failed: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &res)
	idf := res["id"].(float64)
	return formatInt(int64(idf))
}

// setAppGate points an app's protected_by at an auth record.
func setAppGate(t *testing.T, srv *Server, nonce string, recID int64) {
	t.Helper()
	app, err := srv.store.GetApp(nonce)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if err := srv.store.UpdateApp(nonce, app.Name, app.Environment, app.URL, app.OwnerID, &recID); err != nil {
		t.Fatalf("set app gate: %v", err)
	}
}

// setServiceActsAs points a service's acts_as at an auth record.
func setServiceActsAs(t *testing.T, srv *Server, serviceID string, recID int64) {
	t.Helper()
	id, err := strconv.ParseInt(serviceID, 10, 64)
	if err != nil {
		t.Fatalf("parse service ID: %v", err)
	}
	svc, err := srv.store.GetService(id)
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	if err := srv.store.UpdateService(id, svc.Name, svc.URL, svc.Descriptor, svc.ProtectedBy, &recID); err != nil {
		t.Fatalf("set service acts_as: %v", err)
	}
}

func linkServiceToApp(t *testing.T, srv *Server, appNonce string, serviceID string) {
	t.Helper()
	id, err := strconv.ParseInt(serviceID, 10, 64)
	if err != nil {
		t.Fatalf("parse service ID: %v", err)
	}
	if err := srv.store.SetAppServiceAllowed(appNonce, id, true); err != nil {
		t.Fatalf("link service to app: %v", err)
	}
}

func mockHTTPClient(fn func(req *http.Request) *http.Response) *http.Client {
	return &http.Client{Transport: mockRoundTripper{fn}}
}

type mockRoundTripper struct {
	fn func(req *http.Request) *http.Response
}

func (m mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.fn(req), nil
}

func jsonResp(code int, body interface{}) *http.Response {
	b, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}

func formatInt(n int64) string {
	var buf [20]byte
	i := 0
	if n == 0 {
		return "0"
	}
	for n > 0 {
		buf[i] = byte('0' + n%10)
		n /= 10
		i++
	}
	for j := 0; j < i/2; j++ {
		buf[j], buf[i-1-j] = buf[i-1-j], buf[j]
	}
	return string(buf[:i])
}

// ── Session Management API Tests ──

// authTokenForUser mints a real Freshbreath identity token for the given
// user by creating a synthetic auth record, wiring it as admin_auth_service,
// and signing a JWT. The caller must have already created the user in s.store.
func authTokenForUser(t *testing.T, srv *Server, email, name, role string) string {
	t.Helper()
	rec := newAuthRecord(t, srv, "test-auth", db.AuthOIDC,
		db.AuthDescriptor{Issuer: "https://test-auth.example", Provider: "test-auth"})
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(rec.ID, 10)); err != nil {
		t.Fatalf("set admin_auth_service: %v", err)
	}
	user, err := srv.store.GetUserByEmail(email)
	if err != nil {
		t.Fatalf("user %s must exist before minting: %v", email, err)
	}
	token, err := srv.mintFreshbreathToken(subjectForUser(user), email, role, name, rec.ID, nil, nil)
	if err != nil {
		t.Fatalf("mint identity token: %v", err)
	}
	return token
}

func TestGetMySessions(t *testing.T) {
	srv := newTestServer(t)
	user, err := srv.store.CreateUser("Session User", "session@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := authTokenForUser(t, srv, "session@example.com", "Session User", "Member")
	fam := &db.RefreshFamily{
		ID:          "test-session-id",
		Subject:     subjectForUser(user),
		AuthID:      1,
		DeviceLabel: "Test Device",
		CurrentJTI:  "test-jti",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}
	if err := srv.store.CreateRefreshFamily(fam); err != nil {
		t.Fatalf("create refresh family: %v", err)
	}

	rr := testRequest(t, srv, "GET", "/api/me/sessions", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	sessions := resp["sessions"].([]interface{})
	if len(sessions) < 1 {
		t.Errorf("expected at least 1 session, got %d", len(sessions))
	}
}

func TestRevokeAllSessions(t *testing.T) {
	srv := newTestServer(t)
	user, err := srv.store.CreateUser("Revoke User", "revoke@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := authTokenForUser(t, srv, "revoke@example.com", "Revoke User", "Member")
	fam := &db.RefreshFamily{
		ID:          "revoke-session-id",
		Subject:     subjectForUser(user),
		AuthID:      1,
		DeviceLabel: "Test Device",
		CurrentJTI:  "test-jti",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}
	if err := srv.store.CreateRefreshFamily(fam); err != nil {
		t.Fatalf("create refresh family: %v", err)
	}

	rr := testRequest(t, srv, "DELETE", "/api/me/sessions", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if rr.Code != 204 {
		t.Fatalf("status = %d, want 204, body = %s", rr.Code, rr.Body.String())
	}

	got, _, _ := srv.store.GetRefreshFamily("revoke-session-id")
	if !got.Revoked {
		t.Error("expected session to be revoked")
	}
}

func TestRevokeSpecificSession(t *testing.T) {
	srv := newTestServer(t)
	user, err := srv.store.CreateUser("Revoke Single User", "single@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := authTokenForUser(t, srv, "single@example.com", "Revoke Single User", "Member")
	fam := &db.RefreshFamily{
		ID:          "single-session-id",
		Subject:     subjectForUser(user),
		AuthID:      1,
		DeviceLabel: "Test Device",
		CurrentJTI:  "test-jti",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}
	if err := srv.store.CreateRefreshFamily(fam); err != nil {
		t.Fatalf("create refresh family: %v", err)
	}

	rr := testRequest(t, srv, "DELETE", "/api/me/sessions/single-session-id", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if rr.Code != 204 {
		t.Fatalf("status = %d, want 204, body = %s", rr.Code, rr.Body.String())
	}

	got, _, _ := srv.store.GetRefreshFamily("single-session-id")
	if !got.Revoked {
		t.Error("expected session to be revoked")
	}
}

func TestRevokeSessionNotFound(t *testing.T) {
	srv := newTestServer(t)
	_, err := srv.store.CreateUser("Not Found User", "notfound@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := authTokenForUser(t, srv, "notfound@example.com", "Not Found User", "Member")

	rr := testRequest(t, srv, "DELETE", "/api/me/sessions/nonexistent-id", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if rr.Code != 404 {
		t.Fatalf("status = %d, want 404, body = %s", rr.Code, rr.Body.String())
	}
}

// ── Login resolution (resolve=1) ──
//
// The browser cannot know which stored credential to present until it knows
// the door's gate, so resolve=1 answers that as a pure query: no pending
// state, no flow, just the bill.

func TestLoginResolveListsLegs(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "resolve-legs")
	gate := newAuthRecord(t, srv, "Staff", db.AuthOAuth2,
		db.AuthDescriptor{AuthorizeURL: "https://gh.example/authorize", TokenURL: "https://gh.example/token", ClientID: "c", Provider: "github"})
	setAppGate(t, srv, nonce, gate.ID)
	out := newAuthRecord(t, srv, "Jira", db.AuthOAuth2,
		db.AuthDescriptor{AuthorizeURL: "https://jira.example/authorize", TokenURL: "https://jira.example/token", ClientID: "j", Provider: "jira"})
	id := registerService(t, srv, "tickets", "https://jira.example/mcp",
		db.ServiceDescriptor{Type: "mcp", Proxied: true})
	setServiceActsAs(t, srv, id, out.ID)
	linkServiceToApp(t, srv, nonce, id)

	rr := testRequest(t, srv, "GET", "/service/login?url=https://jira.example/mcp&resolve=1", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Type string `json:"type"`
		Legs []struct {
			AuthID   int64  `json:"auth_id"`
			Kind     string `json:"kind"`
			Provider string `json:"provider"`
			Name     string `json:"name"`
		} `json:"legs"`
		Service struct {
			ID      int64  `json:"id"`
			URL     string `json:"url"`
			Proxied bool   `json:"proxied"`
		} `json:"service"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Type != "legs" {
		t.Fatalf("type = %q, want legs", resp.Type)
	}
	if len(resp.Legs) != 2 {
		t.Fatalf("legs = %+v, want gate then acts_as", resp.Legs)
	}
	if resp.Legs[0].AuthID != gate.ID || resp.Legs[1].AuthID != out.ID {
		t.Errorf("leg order = %d,%d, want %d,%d (gate first)",
			resp.Legs[0].AuthID, resp.Legs[1].AuthID, gate.ID, out.ID)
	}
	if resp.Legs[1].Provider != "jira" {
		t.Errorf("outbound provider = %q, want jira", resp.Legs[1].Provider)
	}
	if formatInt(resp.Service.ID) != id || !resp.Service.Proxied {
		t.Errorf("service = %+v, want id %s and proxied", resp.Service, id)
	}
}

// resolve=1 needs no state because it starts nothing — and starting nothing
// is the point: a resolve must not leave a pending login behind.
func TestLoginResolveStartsNoFlow(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "resolve-clean")
	gate := newAuthRecord(t, srv, "Keys", db.AuthAPIKey, db.AuthDescriptor{Key: "s3cret"})
	setAppGate(t, srv, nonce, gate.ID)

	rr := testRequest(t, srv, "GET", "/service/login?resolve=1", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["type"] != "legs" {
		t.Fatalf("type = %v, want legs", resp["type"])
	}
	if resp["url"] != nil {
		t.Errorf("resolve returned a flow url %v — it must not begin a leg", resp["url"])
	}
	srv.pendingMu.Lock()
	n := len(srv.pending)
	srv.pendingMu.Unlock()
	if n != 0 {
		t.Errorf("resolve left %d pending logins behind, want 0", n)
	}
}

func TestLoginAnonymousCarriesServiceInfo(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "anon-info")
	anonID, err := srv.store.BuiltinAuthID(db.AuthAnonymous)
	if err != nil {
		t.Fatalf("builtin anonymous: %v", err)
	}
	setAppGate(t, srv, nonce, anonID)
	id := registerService(t, srv, "open", "https://open.example/api",
		db.ServiceDescriptor{Type: "api", Proxied: true})
	linkServiceToApp(t, srv, nonce, id)

	rr := testRequest(t, srv, "GET", "/service/login?url=https://open.example/api&state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Type    string `json:"type"`
		Service struct {
			ID      int64 `json:"id"`
			Proxied bool  `json:"proxied"`
		} `json:"service"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Type != "anonymous" {
		t.Fatalf("type = %q, want anonymous", resp.Type)
	}
	// Without this the browser has no service id and cannot build a proxied
	// URL — an anonymous service would resolve to a proxy that can't call.
	if formatInt(resp.Service.ID) != id || !resp.Service.Proxied {
		t.Errorf("service = %+v, want id %s and proxied", resp.Service, id)
	}
}
