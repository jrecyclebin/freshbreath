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

// setAdminAuth points admin_auth_service at an OIDC auth record and
// returns it.
func setAdminAuth(t *testing.T, srv *Server) *db.AuthRecord {
	t.Helper()
	rec := newAuthRecord(t, srv, "Admin IdP", db.AuthOIDC,
		db.AuthDescriptor{Issuer: "https://admin.example", Provider: "admin-idp"})
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(rec.ID, 10)); err != nil {
		t.Fatalf("set admin_auth_service: %v", err)
	}
	return rec
}

// ── Central MCP: bearer enforcement at the HTTP layer ───────────────

func TestCentralMCPRequiresBearer(t *testing.T) {
	srv := newTestServer(t)
	// admin auth record configured so the verifier reaches token validation,
	// not the "not configured" short-circuit.
	setAdminAuth(t, srv)

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
	setAdminAuth(t, srv)

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
	rec := setAdminAuth(t, srv)
	// The DB user is a Member.
	grace, err := srv.store.CreateUser("Grace Hopper", "grace@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// ...but the token *claims* Superuser. The verifier must ignore the
	// token's role and re-resolve from the DB, or a holder of a stale/forged
	// token could escalate. Minting with a role that disagrees with the DB is
	// the whole point — a test that minted "Member" too would pass even if the
	// verifier wrongly trusted the token.
	tok, err := srv.mintFreshbreathToken(subjectForUser(grace), "grace@example.com", "Superuser", "Grace Hopper", rec.ID, nil, nil)
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

// ── Virtual MCP mounts: the door owns the gate ──────────────────────

// mountVirtual writes a minimal tool file and registers svc in the
// virtual MCP registry.
func mountVirtual(t *testing.T, srv *Server, svc *db.Service) {
	t.Helper()
	dataDir := t.TempDir()
	srv.config.DataDir = dataDir
	if err := os.MkdirAll(filepath.Join(dataDir, "virtual"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	toolFile := "[hello] Say hello\nGET https://up.example/greet/$name\nAuthorization: Bearer $token\n"
	if err := os.WriteFile(filepath.Join(dataDir, "virtual", svc.Name+".txt"), []byte(toolFile), 0o644); err != nil {
		t.Fatalf("write tool file: %v", err)
	}
	srv.virtualMCPs.add(srv, svc)
}

// A mount's gate is its protected_by record: no bearer 401s with a PRM
// challenge, a token bound to a different record 401s, a bound token
// clears the door.
func TestVirtualMCPGateBinding(t *testing.T) {
	srv := newTestServer(t)
	gate := newAuthRecord(t, srv, "Upstream IdP", db.AuthOAuth2,
		db.AuthDescriptor{AuthorizeURL: "https://up.example/authorize", TokenURL: "https://up.example/token", Provider: "up"})
	other := newAuthRecord(t, srv, "Other IdP", db.AuthOIDC,
		db.AuthDescriptor{Issuer: "https://other.example", Provider: "other"})

	svc := &db.Service{ID: 7, Name: "Upstream", URL: "/mcp/upstream",
		Descriptor: db.ServiceDescriptor{Type: "virtual"}, ProtectedBy: &gate.ID}
	mountVirtual(t, srv, svc)

	hdr := map[string]string{"Accept": "application/json, text/event-stream", "Content-Type": "application/json"}
	rr := testRequest(t, srv, "POST", "/mcp/upstream", nil, hdr)
	if rr.Code != 401 {
		t.Fatalf("no bearer: status = %d, want 401", rr.Code)
	}
	if wa := rr.Header().Get("WWW-Authenticate"); wa == "" {
		t.Error("expected WWW-Authenticate challenge with PRM URL")
	}

	// Bound to the wrong record → still 401.
	tokOther, err := srv.mintFreshbreathToken(extSubject("other", "sub-1"), "", "", "", other.ID, nil, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	hdrOther := map[string]string{"Accept": hdr["Accept"], "Content-Type": hdr["Content-Type"], "Authorization": "Bearer " + tokOther}
	if rr := testRequest(t, srv, "POST", "/mcp/upstream", nil, hdrOther); rr.Code != 401 {
		t.Fatalf("foreign-record token: status = %d, want 401", rr.Code)
	}

	// Bound to the gate record → past the door (the MCP layer may still
	// reject the empty body, but not with a 401).
	tokGate, err := srv.mintFreshbreathToken(extSubject("up", "sub-2"), "", "", "", gate.ID, nil, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	hdrGate := map[string]string{"Accept": hdr["Accept"], "Content-Type": hdr["Content-Type"], "Authorization": "Bearer " + tokGate}
	if rr := testRequest(t, srv, "POST", "/mcp/upstream", nil, hdrGate); rr.Code == 401 {
		t.Fatalf("bound token: status = 401, want admission; body = %s", rr.Body.String())
	}
}

// An empty protected_by slot inherits the admin gate — the old open-mount
// hole must stay closed.
func TestVirtualMCPEmptySlotInheritsAdmin(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)

	svc := &db.Service{ID: 9, Name: "Inherit", URL: "/mcp/inherit",
		Descriptor: db.ServiceDescriptor{Type: "virtual"}}
	mountVirtual(t, srv, svc)

	rr := testRequest(t, srv, "POST", "/mcp/inherit", nil,
		map[string]string{"Accept": "application/json, text/event-stream", "Content-Type": "application/json"})
	if rr.Code != 401 {
		t.Fatalf("empty slot with admin auth set: status = %d, want 401 (must NOT mount open)", rr.Code)
	}

	// And the PRM advertises the authorization server.
	if rr := testRequest(t, srv, "GET", "/.well-known/oauth-protected-resource/mcp/inherit", nil, nil); rr.Code != 200 {
		t.Errorf("PRM status = %d, want 200", rr.Code)
	}
}

// ── Virtual MCP HTTP routing ────────────────────────────────────────

func TestVirtualMCPPRM(t *testing.T) {
	srv := newTestServer(t)
	gate := newAuthRecord(t, srv, "Upstream IdP", db.AuthOAuth2,
		db.AuthDescriptor{AuthorizeURL: "https://up.example/authorize", TokenURL: "https://up.example/token", Provider: "up"})
	svc := &db.Service{ID: 7, Name: "Upstream", URL: "/mcp/upstream",
		Descriptor: db.ServiceDescriptor{Type: "virtual"}, ProtectedBy: &gate.ID}
	mountVirtual(t, srv, svc)

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

// ── Explicit Anonymous mount → open, and no PRM ─────────────────────

func TestVirtualMCPAnonymousHasNoPRM(t *testing.T) {
	srv := newTestServer(t)
	anon := builtinAuth(t, srv, db.AuthAnonymous)

	svc := &db.Service{ID: 8, Name: "Open", URL: "/mcp/open",
		Descriptor: db.ServiceDescriptor{Type: "virtual"}, ProtectedBy: &anon.ID}
	mountVirtual(t, srv, svc)

	rr := testRequest(t, srv, "GET", "/.well-known/oauth-protected-resource/mcp/open", nil, nil)
	if rr.Code != 404 {
		t.Errorf("status = %d, want 404 (Anonymous gate advertises nothing)", rr.Code)
	}

	// And the door admits a bare request.
	rr = testRequest(t, srv, "POST", "/mcp/open", nil,
		map[string]string{"Accept": "application/json, text/event-stream", "Content-Type": "application/json"})
	if rr.Code == 401 {
		t.Errorf("Anonymous mount answered 401; body = %s", rr.Body.String())
	}
}

// ── Handler-level token helpers ─────────────────────────────────────

func TestIsFreshbreathToken(t *testing.T) {
	srv := newTestServer(t)
	tok, err := srv.mintFreshbreathToken("frbr:1", "u@example.com", "Admin", "U", 1, nil, nil)
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
