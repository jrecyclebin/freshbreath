package main

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
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := &Store{db: db}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	localKey, err := store.GetOrCreateLocalSigningKey()
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	srv := &Server{
		config: Config{
			Dir:           ".",
			DataDir:       ".",
			PublicBaseURL: "http://localhost:9009",
		},
		store:          store,
		pending:        make(map[string]*pendingAuth),
		httpClient:     &http.Client{},
		oidcProviders:  make(map[int64]*oidc.Provider),
		hostedRoutes:   make(map[string]string),
		xfers:          make(map[string]*transferEntry),
		lastSeenAt:     make(map[int64]time.Time),
		localKey:       localKey,
		virtualMCPs:    newVirtualMCPRegistry(),
		mcpAuthPending: &sync.Map{},
	}
	srv.oauthSrv = newOAuthServer(srv)
	srv.SetupRoutes()
	return srv
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

	body := `{"name": "github", "url": "https://api.github.com", "descriptor": {"type": "api", "proxied": true, "client_id": "gh-client", "oauth_url": "https://github.com/login/oauth"}}`
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
	if desc["client_id"] != "gh-client" {
		t.Errorf("descriptor.client_id = %v, want gh-client", desc["client_id"])
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
	id := registerService(t, srv, "slack", "https://slack.example/mcp", ServiceDescriptor{Type: "mcp"})

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
	id := registerService(t, srv, "mytasks", "", ServiceDescriptor{Type: "tasks"})

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
	path := filepath.Join("tasks", "mytasks.txt")
	if err := os.MkdirAll("tasks", 0755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}
	if err := os.WriteFile(path, []byte("[greet] Say hello\necho hi\n[build] Compile\nmake\n"), 0644); err != nil {
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
	id := registerService(t, srv, "myvirtual", "", ServiceDescriptor{Type: "virtual"})

	path := filepath.Join("virtual", "myvirtual.txt")
	if err := os.MkdirAll("virtual", 0755); err != nil {
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
	id := registerService(t, srv, "slack", "https://slack.example/mcp", ServiceDescriptor{Type: "mcp"})

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

	idStr := registerService(t, srv, "deploy", "", ServiceDescriptor{Type: "tasks"})
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
	idStr := registerService(t, srv, "api-svc", "http://example.com", ServiceDescriptor{Type: "api"})
	id, _ := strconv.ParseInt(idStr, 10, 64)

	rr := uploadServiceFile(t, srv, id, "x.txt", []byte("x"))
	if rr.Code != 400 {
		t.Errorf("upload status = %d, want 400", rr.Code)
	}
}

func TestServiceFilesHTTPNoZip(t *testing.T) {
	srv := newTestServer(t)
	idStr := registerService(t, srv, "deploy", "", ServiceDescriptor{Type: "tasks"})
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
	id := registerService(t, srv, "mock", mockService.URL, ServiceDescriptor{Type: "mcp"})
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

	registerService(t, srv, "github", "https://gh.example/mcp", ServiceDescriptor{Type: "mcp"})

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
	registerService(t, srv, "slack", "https://slack.example/mcp", ServiceDescriptor{Type: "mcp"})

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

func TestLoginMissingServiceURL(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "login-test")
	rr := testRequest(t, srv, "GET", "/service/login?state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
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

	registerService(t, srv, "slack", "https://slack.example/mcp", ServiceDescriptor{Type: "mcp"})

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

	registerService(t, srv, "github", "https://api.github.com", ServiceDescriptor{
		Type:     "api",
		ClientID: "gh-pre-registered-client",
		OAuthURL: "https://github.com",
	})

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

func TestLoginWithAPIKey(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "key-test")
	registerService(t, srv, "weather", "https://api.weather.com", ServiceDescriptor{
		Type: "api",
		Auth: "key",
	})

	rr := testRequest(t, srv, "GET", "/service/login?url=https://api.weather.com&state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	// No admin key set → redirect to key entry form
	if resp["type"] != "redirect" {
		t.Errorf("type = %v, want redirect", resp["type"])
	}
	url, _ := resp["url"].(string)
	if !strings.Contains(url, "/service/apikey-auth") {
		t.Errorf("url = %q, want apikey-auth URL", url)
	}
}

func TestLoginWithAPIKeyAdminSet(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "key-admin-test")
	registerService(t, srv, "weather", "https://api.weather.com", ServiceDescriptor{
		Type:   "api",
		Auth:   "key",
		APIKey: "admin-secret-key",
	})

	rr := testRequest(t, srv, "GET", "/service/login?url=https://api.weather.com&state=x", nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["type"] != "key-auth-complete" {
		t.Errorf("type = %v, want key-auth-complete", resp["type"])
	}
	// Admin key is available in the response
	if resp["apiKey"] != "admin-secret-key" {
		t.Errorf("apiKey = %v, want admin-secret-key", resp["apiKey"])
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
	id := registerService(t, srv, "weather", mockService.URL, ServiceDescriptor{
		Type:   "api",
		Auth:   "key",
		APIKey: "admin-secret-key",
	})
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

func TestProxyDoesNotOverrideUserAPIKey(t *testing.T) {
	var receivedAuth string
	mockService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer mockService.Close()

	srv := newTestServer(t)
	nonce := createApp(t, srv, "proxy-user-key-test")
	id := registerService(t, srv, "weather", mockService.URL, ServiceDescriptor{
		Type:   "api",
		Auth:   "key",
		APIKey: "admin-secret-key",
	})
	linkServiceToApp(t, srv, nonce, id)

	// Request WITH Authorization header — user's key wins
	rr := testRequest(t, srv, "GET", "/service/"+id+"/forecast", nil,
		map[string]string{
			"X-App-Nonce":   nonce,
			"Authorization": "Bearer user-own-key",
		})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if receivedAuth != "Bearer user-own-key" {
		t.Errorf("Authorization = %q, want Bearer user-own-key (user key should win)", receivedAuth)
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

func registerService(t *testing.T, srv *Server, name, serviceURL string, descriptor ServiceDescriptor) string {
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
// user by creating a synthetic auth service, wiring it as admin_auth_service,
// and signing a JWT. The caller must have already created the user in s.store.
func authTokenForUser(t *testing.T, srv *Server, email, name, role string) string {
	t.Helper()
	svcID := registerService(t, srv, "test-auth", "http://localhost/mcp", ServiceDescriptor{Type: "mcp"})
	sid, _ := strconv.ParseInt(svcID, 10, 64)
	if err := srv.store.SetSetting("admin_auth_service", svcID); err != nil {
		t.Fatalf("set admin_auth_service: %v", err)
	}
	token, err := srv.mintFreshbreathToken("identity", email, role, name, sid, nil)
	if err != nil {
		t.Fatalf("mint identity token: %v", err)
	}
	return token
}

func TestGetMySessions(t *testing.T) {
	srv := newTestServer(t)
	_, err := srv.store.CreateUser("Session User", "session@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := authTokenForUser(t, srv, "session@example.com", "Session User", "Member")
	fam := &RefreshFamily{
		ID:          "test-session-id",
		UserEmail:   "session@example.com",
		ServiceID:   1,
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
	_, err := srv.store.CreateUser("Revoke User", "revoke@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := authTokenForUser(t, srv, "revoke@example.com", "Revoke User", "Member")
	fam := &RefreshFamily{
		ID:          "revoke-session-id",
		UserEmail:   "revoke@example.com",
		ServiceID:   1,
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
	_, err := srv.store.CreateUser("Revoke Single User", "single@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := authTokenForUser(t, srv, "single@example.com", "Revoke Single User", "Member")
	fam := &RefreshFamily{
		ID:          "single-session-id",
		UserEmail:   "single@example.com",
		ServiceID:   1,
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
