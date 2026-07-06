package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// ── Central MCP: protected resource metadata ────────────────────────

func TestCentralMCPPRM(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/.well-known/oauth-protected-resource/mcp", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var prm map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &prm)
	base := "http://localhost:9009"
	if prm["resource"] != base+"/mcp" {
		t.Errorf("resource = %v, want %s/mcp", prm["resource"], base)
	}
	servers, _ := prm["authorization_servers"].([]interface{})
	if len(servers) != 1 || servers[0] != base {
		t.Errorf("authorization_servers = %v, want [%s]", prm["authorization_servers"], base)
	}
}

// ── Central MCP: bearer enforcement at the HTTP layer ───────────────

func TestCentralMCPRequiresBearer(t *testing.T) {
	srv := newTestServer(t)
	// admin auth service configured so the verifier reaches token validation,
	// not the "not configured" short-circuit.
	svcID, _ := srv.store.RegisterService("admin-idp", "https://admin.example", db.ServiceDescriptor{Type: "oidc"})
	srv.store.SetSetting("admin_auth_service", strconv.FormatInt(svcID, 10))

	rr := testRequest(t, srv, "POST", "/mcp", nil, map[string]string{"Accept": "application/json"})
	if rr.Code != 401 {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if wa := rr.Header().Get("WWW-Authenticate"); wa == "" {
		t.Error("expected WWW-Authenticate challenge header")
	}
}

func TestCentralMCPRejectsBadToken(t *testing.T) {
	srv := newTestServer(t)
	svcID, _ := srv.store.RegisterService("admin-idp", "https://admin.example", db.ServiceDescriptor{Type: "oidc"})
	srv.store.SetSetting("admin_auth_service", strconv.FormatInt(svcID, 10))

	rr := testRequest(t, srv, "POST", "/mcp", nil, map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer garbage.token.here",
	})
	if rr.Code != 401 {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// ── Central MCP token verifier (direct) ─────────────────────────────

func TestCentralMCPTokenVerifierNoAdminService(t *testing.T) {
	srv := newTestServer(t)
	verify := srv.centralMCPTokenVerifier()
	req := httptest.NewRequest("POST", "/mcp", nil)
	if _, err := verify(context.Background(), "anything", req); err == nil {
		t.Fatal("expected error when admin auth service is not configured")
	}
}

func TestCentralMCPTokenVerifierValid(t *testing.T) {
	srv := newTestServer(t)
	svcID, err := srv.store.RegisterService("admin-idp", "https://admin.example", db.ServiceDescriptor{Type: "oidc"})
	if err != nil {
		t.Fatalf("register service: %v", err)
	}
	srv.store.SetSetting("admin_auth_service", strconv.FormatInt(svcID, 10))
	// The DB user is a Member.
	if _, err := srv.store.CreateUser("Grace Hopper", "grace@example.com", "Member", "Active"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// ...but the token *claims* Superuser. The verifier must ignore the
	// token's role and re-resolve from the DB, or a holder of a stale/forged
	// token could escalate. Minting with a role that disagrees with the DB is
	// the whole point — a test that minted "Member" too would pass even if the
	// verifier wrongly trusted the token.
	tok, err := srv.mintFreshbreathToken("identity", "grace@example.com", "Superuser", "Grace Hopper", svcID, nil)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	verify := srv.centralMCPTokenVerifier()
	req := httptest.NewRequest("POST", "/mcp", nil)
	info, err := verify(context.Background(), tok, req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if info.UserID != "grace@example.com" {
		t.Errorf("UserID = %q, want grace@example.com", info.UserID)
	}
	// DB says Member — that must win over the token's "Superuser" claim.
	if info.Extra["role"] != "Member" {
		t.Errorf("role = %v, want Member (DB role must override the token's claim)", info.Extra["role"])
	}
}

// ── Virtual MCP token verifier (direct) ─────────────────────────────

func TestVirtualTokenVerifierWrapped(t *testing.T) {
	srv := newTestServer(t)
	svc := &db.Service{ID: 7, Name: "up", URL: "/mcp/up", Descriptor: db.ServiceDescriptor{Type: "mcp", OAuthURL: "https://up.example"}}

	tok, err := srv.mintFreshbreathToken("wrapped", "user@example.com", "", "", svc.ID,
		&sealedUpstreamData{UpstreamToken: "upstream-xyz", UpstreamScopes: "openid email"})
	if err != nil {
		t.Fatalf("mint wrapped: %v", err)
	}

	verify := srv.virtualTokenVerifier(svc)
	req := httptest.NewRequest("POST", "/mcp/up", nil)

	info, err := verify(context.Background(), tok, req)
	if err != nil {
		t.Fatalf("verify wrapped: %v", err)
	}
	if info.UserID != "user@example.com" {
		t.Errorf("UserID = %q, want user@example.com", info.UserID)
	}

	// A token wrapped for a different service must be rejected.
	otherSvc := &db.Service{ID: 99, Name: "other", URL: "/mcp/other", Descriptor: db.ServiceDescriptor{Type: "mcp", OAuthURL: "https://up.example"}}
	if _, err := srv.virtualTokenVerifier(otherSvc)(context.Background(), tok, req); err == nil {
		t.Error("expected wrapped token bound to svc 7 to be rejected by svc 99")
	}
}

// ── Virtual MCP HTTP routing ────────────────────────────────────────

func TestVirtualMCPPRM(t *testing.T) {
	srv := newTestServer(t)

	// Hermetic virtual tool file under a temp data dir.
	dataDir := t.TempDir()
	srv.config.DataDir = dataDir
	if err := os.MkdirAll(filepath.Join(dataDir, "virtual"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	toolFile := "[hello] Say hello\nGET https://up.example/greet/$name\nAuthorization: Bearer $token\n"
	if err := os.WriteFile(filepath.Join(dataDir, "virtual", "Upstream.txt"), []byte(toolFile), 0o644); err != nil {
		t.Fatalf("write tool file: %v", err)
	}

	svc := &db.Service{ID: 7, Name: "Upstream", URL: "/mcp/upstream", Descriptor: db.ServiceDescriptor{Type: "mcp", OAuthURL: "https://up.example"}}
	srv.virtualMCPs.add(srv, svc)

	rr := testRequest(t, srv, "GET", "/.well-known/oauth-protected-resource/mcp/upstream", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var prm map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &prm)
	if prm["resource"] != "http://localhost:9009/mcp/upstream" {
		t.Errorf("resource = %v", prm["resource"])
	}
	if prm["resource_name"] != "Upstream" {
		t.Errorf("resource_name = %v, want Upstream", prm["resource_name"])
	}
}

func TestVirtualMCPNotFound(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/.well-known/oauth-protected-resource/mcp/ghost", nil, nil)
	if rr.Code != 404 {
		t.Errorf("PRM status = %d, want 404", rr.Code)
	}
	rr = testRequest(t, srv, "POST", "/mcp/ghost", nil, nil)
	if rr.Code != 404 {
		t.Errorf("dispatch status = %d, want 404", rr.Code)
	}
}

// ── Virtual MCP without auth → no PRM ────────────────────────────────

func TestVirtualMCPNoAuthHasNoPRM(t *testing.T) {
	srv := newTestServer(t)
	dataDir := t.TempDir()
	srv.config.DataDir = dataDir
	os.MkdirAll(filepath.Join(dataDir, "virtual"), 0o755)
	os.WriteFile(filepath.Join(dataDir, "virtual", "Open.txt"),
		[]byte("[ping] Ping\nGET https://open.example/ping\n"), 0o644)

	// No OAuthURL / ClientID / key auth → unauthenticated virtual service.
	svc := &db.Service{ID: 8, Name: "Open", URL: "/mcp/open", Descriptor: db.ServiceDescriptor{Type: "mcp"}}
	srv.virtualMCPs.add(srv, svc)

	rr := testRequest(t, srv, "GET", "/.well-known/oauth-protected-resource/mcp/open", nil, nil)
	if rr.Code != 404 {
		t.Errorf("status = %d, want 404 (no auth configured → no PRM)", rr.Code)
	}
}

// ── Handler-level token helpers ─────────────────────────────────────

func TestIsFreshbreathToken(t *testing.T) {
	srv := newTestServer(t)
	tok, err := srv.mintFreshbreathToken("identity", "u@example.com", "Admin", "U", 1, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !isFreshbreathToken(tok) {
		t.Error("minted Freshbreath token should be recognized")
	}
	// Wrong shape.
	if isFreshbreathToken("not-a-jwt") {
		t.Error("non-JWT should not be recognized")
	}
	// Valid 3-part shape but foreign issuer.
	if isFreshbreathToken("aaa.bbb.ccc") {
		t.Error("garbage payload should not be recognized")
	}
}

func TestVerifyTaskTokenMissingBearer(t *testing.T) {
	srv := newTestServer(t)
	svcID, _ := srv.store.RegisterService("idp", "https://idp.example", db.ServiceDescriptor{Type: "oidc"})
	req := httptest.NewRequest("GET", "/whatever", nil)
	if _, err := srv.verifyTaskToken(req, svcID); err == nil {
		t.Fatal("expected error when Authorization header is absent")
	}
}

func TestVerifyTaskTokenIdentity(t *testing.T) {
	srv := newTestServer(t)
	svcID, _ := srv.store.RegisterService("idp", "https://idp.example", db.ServiceDescriptor{Type: "oidc"})
	if _, err := srv.store.CreateUser("Kay", "kay@example.com", "Member", "Active"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	tok, _ := srv.mintFreshbreathToken("identity", "kay@example.com", "Member", "Kay", svcID, nil)

	req := httptest.NewRequest("GET", "/whatever", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	user, err := srv.verifyTaskToken(req, svcID)
	if err != nil {
		t.Fatalf("verifyTaskToken: %v", err)
	}
	if user.Email != "kay@example.com" {
		t.Errorf("email = %q, want kay@example.com", user.Email)
	}
}
