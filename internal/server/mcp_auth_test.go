package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// ── MCP services: auth discovered at login ──────────────────────────

// fakeMCPUpstream is an MCP server guarded by OAuth, with its own
// authorization server on the same origin — the Notion shape. It admits
// "Bearer up-token" and records what the token endpoint was sent.
type fakeMCPUpstream struct {
	*httptest.Server
	tokenForms []url.Values
	lastAuth   string
}

func newFakeMCPUpstream(t *testing.T) *fakeMCPUpstream {
	t.Helper()
	f := &fakeMCPUpstream{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := f.URL
		writeJSON := func(v interface{}) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(v)
		}
		switch r.URL.Path {
		case "/mcp":
			f.lastAuth = r.Header.Get("Authorization")
			if f.lastAuth != "Bearer up-token" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeJSON(map[string]string{"ok": "yes"})
		case "/.well-known/oauth-protected-resource/mcp":
			writeJSON(map[string]interface{}{"resource": base + "/mcp", "authorization_servers": []string{base}})
		case "/.well-known/oauth-authorization-server":
			writeJSON(map[string]interface{}{
				"issuer":                           base,
				"authorization_endpoint":           base + "/authorize",
				"token_endpoint":                   base + "/token",
				"registration_endpoint":            base + "/register",
				"scopes_supported":                 []string{"offline_access"},
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/register":
			w.WriteHeader(http.StatusCreated)
			writeJSON(map[string]interface{}{"client_id": "dcr-client", "redirect_uris": []string{"http://localhost:9009/service/callback"}})
		case "/token":
			r.ParseForm()
			f.tokenForms = append(f.tokenForms, r.PostForm)
			writeJSON(map[string]interface{}{"access_token": "up-token", "refresh_token": "up-refresh", "token_type": "Bearer", "expires_in": 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// openApp creates an app behind the explicit Anonymous gate.
func openApp(t *testing.T, srv *Server, name string) string {
	t.Helper()
	nonce := createApp(t, srv, name)
	setAppGate(t, srv, nonce, builtinAuth(t, srv, db.AuthAnonymous).ID)
	return nonce
}

// callbackEntry pulls the store entry out of the postMessage page.
func callbackEntry(t *testing.T, page string) map[string]interface{} {
	t.Helper()
	_, rest, ok := strings.Cut(page, "var entry = ")
	if !ok {
		t.Fatalf("no entry in callback page: %s", page)
	}
	raw, _, _ := strings.Cut(rest, ";\n")
	var entry map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("decode entry %q: %v", raw, err)
	}
	return entry
}

func TestMCPServiceOpenServerNeedsNoLogin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	srv := newTestServer(t)
	nonce := openApp(t, srv, "calc")
	id := registerService(t, srv, "open-mcp", upstream.URL+"/mcp", db.ServiceDescriptor{Type: "mcp"})
	linkServiceToApp(t, srv, nonce, id)

	rr := testRequest(t, srv, "GET", "/service/login?resolve=1&url="+url.QueryEscape(upstream.URL+"/mcp"), nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"type":"anonymous"`) {
		t.Fatalf("status = %d, body = %s; want anonymous", rr.Code, rr.Body.String())
	}
}

// An open app with an optional OAuth MCP connection: the login is the MCP
// leg alone, the proxy swaps the Fresh Breath token for the upstream one,
// and each login gets its own subject.
func TestMCPServiceOAuthBehindOpenGate(t *testing.T) {
	up := newFakeMCPUpstream(t)
	srv := newTestServer(t)
	nonce := openApp(t, srv, "calc")
	id := registerService(t, srv, "notion", up.URL+"/mcp", db.ServiceDescriptor{Type: "mcp", Proxied: true})
	linkServiceToApp(t, srv, nonce, id)
	svcID, _ := strconv.ParseInt(id, 10, 64)

	rr := testRequest(t, srv, "GET", "/service/login?state=app-state&url="+url.QueryEscape(up.URL+"/mcp"), nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("login status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var login struct {
		Type string `json:"type"`
		URL  string `json:"url"`
		Legs []struct {
			AuthID int64  `json:"auth_id"`
			Kind   string `json:"kind"`
		} `json:"legs"`
	}
	json.Unmarshal(rr.Body.Bytes(), &login)
	if login.Type != "redirect" || len(login.Legs) != 1 || login.Legs[0].AuthID != mcpAuthID(svcID) || login.Legs[0].Kind != db.AuthMCP {
		t.Fatalf("login = %+v, want one mcp leg", login)
	}
	authURL, _ := url.Parse(login.URL)
	q := authURL.Query()
	if authURL.Path != "/authorize" || q.Get("client_id") != "dcr-client" || q.Get("resource") != up.URL+"/mcp" {
		t.Fatalf("authorize url = %s", login.URL)
	}
	if q.Get("scope") != "offline_access" {
		t.Errorf("scope = %q, want offline_access", q.Get("scope"))
	}

	rr = testRequest(t, srv, "GET", "/service/callback?code=c1&state="+q.Get("state"), nil, nil)
	if rr.Code != 200 {
		t.Fatalf("callback status = %d, body = %s", rr.Code, rr.Body.String())
	}
	entry := callbackEntry(t, rr.Body.String())
	if int64(entry["auth_id"].(float64)) != mcpAuthID(svcID) {
		t.Errorf("entry auth_id = %v, want %d", entry["auth_id"], mcpAuthID(svcID))
	}
	sub, _ := entry["subject"].(string)
	prefix := "ext:" + mcpProvider(svcID) + ":"
	if !strings.HasPrefix(sub, prefix) || sub == prefix+"key" {
		t.Errorf("subject = %q, want a per-login %s…", sub, prefix)
	}
	if len(up.tokenForms) != 1 || up.tokenForms[0].Get("resource") != up.URL+"/mcp" || up.tokenForms[0].Get("client_id") != "dcr-client" {
		t.Fatalf("token requests = %v", up.tokenForms)
	}
	token := entry["access_token"].(string)

	// With the token, the proxy sends the upstream one.
	rr = testRequest(t, srv, "POST", "/service/"+id+"/", strings.NewReader(`{}`),
		map[string]string{"X-App-Nonce": nonce, "Authorization": "Bearer " + token})
	if rr.Code != 200 || up.lastAuth != "Bearer up-token" {
		t.Fatalf("proxied status = %d, upstream saw %q, body = %s", rr.Code, up.lastAuth, rr.Body.String())
	}

	// Without one, nothing goes upstream and the server answers for itself.
	rr = testRequest(t, srv, "POST", "/service/"+id+"/", strings.NewReader(`{}`),
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 401 || up.lastAuth != "" {
		t.Fatalf("anonymous status = %d, upstream saw %q", rr.Code, up.lastAuth)
	}

	// A Fresh Breath token from some other login is refused, not forwarded.
	other, _ := srv.mintFreshbreathToken("ext:x:1", "", "", "", 12345, nil, nil)
	rr = testRequest(t, srv, "POST", "/service/"+id+"/", strings.NewReader(`{}`),
		map[string]string{"X-App-Nonce": nonce, "Authorization": "Bearer " + other})
	if rr.Code != 401 || up.lastAuth != "" {
		t.Fatalf("foreign token status = %d, upstream saw %q", rr.Code, up.lastAuth)
	}

	// The app may refresh the MCP session by cookie, and the refresh
	// rotates the upstream token against the discovered client.
	if !srv.appMayRefreshRecord(nonce, mcpAuthID(svcID)) {
		t.Error("app should be allowed to refresh its MCP service's session")
	}
	_, rd, err := srv.oauthSrv.refreshLegs(context.Background(), &freshbreathRefreshData{
		Subject: sub, AuthID: mcpAuthID(svcID),
		Upstreams: map[string]upstreamRefreshLeg{mcpProvider(svcID): {
			AuthID: mcpAuthID(svcID), RefreshToken: "up-refresh", TokenURL: up.URL + "/token",
		}},
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	last := up.tokenForms[len(up.tokenForms)-1]
	if last.Get("grant_type") != "refresh_token" || last.Get("client_id") != "dcr-client" || last.Get("resource") != up.URL+"/mcp" {
		t.Errorf("refresh request = %v", last)
	}
	if rd.Upstreams[mcpProvider(svcID)].RefreshToken != "up-refresh" {
		t.Errorf("refresh data = %+v", rd)
	}
}

func TestMCPServiceOAuthRequiresProxied(t *testing.T) {
	up := newFakeMCPUpstream(t)
	srv := newTestServer(t)
	nonce := openApp(t, srv, "calc")
	id := registerService(t, srv, "notion", up.URL+"/mcp", db.ServiceDescriptor{Type: "mcp"})
	linkServiceToApp(t, srv, nonce, id)

	rr := testRequest(t, srv, "GET", "/service/login?resolve=1&url="+url.QueryEscape(up.URL+"/mcp"), nil,
		map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 500 || !strings.Contains(rr.Body.String(), "proxied") {
		t.Fatalf("status = %d, body = %s; want a proxied-only refusal", rr.Code, rr.Body.String())
	}
}

// A gated app adds the MCP leg after its gate, and the MCP service's
// acts_as is dropped on save.
func TestMCPServiceLegFollowsGate(t *testing.T) {
	up := newFakeMCPUpstream(t)
	srv := newTestServer(t)
	nonce := createApp(t, srv, "gated")
	gate := builtinAuth(t, srv, db.AuthSSHKey)
	setAppGate(t, srv, nonce, gate.ID)
	id := registerService(t, srv, "notion", up.URL+"/mcp", db.ServiceDescriptor{Type: "mcp", Proxied: true})
	linkServiceToApp(t, srv, nonce, id)
	svcID, _ := strconv.ParseInt(id, 10, 64)

	rr := testRequest(t, srv, "GET", "/service/login?resolve=1&url="+url.QueryEscape(up.URL+"/mcp"), nil,
		map[string]string{"X-App-Nonce": nonce})
	var resp struct {
		Legs []struct {
			AuthID int64 `json:"auth_id"`
		} `json:"legs"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Legs) != 2 || resp.Legs[0].AuthID != gate.ID || resp.Legs[1].AuthID != mcpAuthID(svcID) {
		t.Fatalf("legs = %s, want gate then mcp", rr.Body.String())
	}

	other := newAuthRecord(t, srv, "Key", db.AuthAPIKey, db.AuthDescriptor{Key: "k"})
	body, _ := json.Marshal(map[string]interface{}{
		"name": "notion-2", "url": up.URL + "/mcp2",
		"descriptor": db.ServiceDescriptor{Type: "mcp", Proxied: true}, "acts_as": other.ID,
	})
	rr = testRequest(t, srv, "POST", "/api/services", strings.NewReader(string(body)), nil)
	if rr.Code != 200 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	svc, err := srv.store.GetServiceByURL(up.URL + "/mcp2")
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	if svc.ActsAs != nil {
		t.Errorf("acts_as = %d, want nil for an mcp service", *svc.ActsAs)
	}
}

// ── OIDC discovery cache ────────────────────────────────────────────

// An edited issuer is discovered afresh: the cache is keyed by issuer, so
// the record's old provider can't answer for its new one.
func TestOIDCProviderFollowsIssuerEdit(t *testing.T) {
	newIssuer := func() *httptest.Server {
		var s *httptest.Server
		s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"issuer": s.URL, "authorization_endpoint": s.URL + "/authorize",
				"token_endpoint": s.URL + "/token", "jwks_uri": s.URL + "/jwks",
			})
		}))
		t.Cleanup(s.Close)
		return s
	}
	first, second := newIssuer(), newIssuer()

	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "IdP", db.AuthOIDC, db.AuthDescriptor{Issuer: first.URL, ClientID: "c"})
	authURL, _, _, _, _, err := srv.oidcBeginAuth(context.Background(), rec, "http://localhost:9009/service/callback")
	if err != nil || !strings.HasPrefix(authURL, first.URL+"/authorize") {
		t.Fatalf("first auth url = %q, err = %v", authURL, err)
	}

	if err := srv.store.UpdateAuthRecord(rec.ID, rec.Name, rec.Kind, db.AuthDescriptor{Issuer: second.URL, ClientID: "c"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	rec, _ = srv.store.GetAuthRecord(rec.ID)
	authURL, _, _, _, _, err = srv.oidcBeginAuth(context.Background(), rec, "http://localhost:9009/service/callback")
	if err != nil || !strings.HasPrefix(authURL, second.URL+"/authorize") {
		t.Fatalf("after edit auth url = %q, err = %v; want the new issuer", authURL, err)
	}
}
