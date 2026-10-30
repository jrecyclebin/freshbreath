package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
)

// ── The tunnel ──
//
// The tailcat server itself isn't started here (it needs a relay); these
// drive the tunnel's HTTP front as though a request had come through it.

// tunnelUp fakes a running tunnel at the given node key.
func tunnelUp(srv *Server, pub string) {
	srv.tunnel.mu.Lock()
	srv.tunnel.pub = pub
	srv.tunnel.mu.Unlock()
}

func tunnelRequest(srv *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, "http://localhost:18080"+path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, "http://localhost:18080"+path, nil)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	srv.tunnelHandler().ServeHTTP(rr, req)
	return rr
}

// passphraseUser gives the known user an SSH key behind "correct horse".
func passphraseUser(t *testing.T, srv *Server, role string) *db.User {
	t.Helper()
	u := knownUser(t, srv)
	key, err := sshkit.GenerateSSHKey("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.UpdateUser(u.ID, u.Name, u.Email, role, u.Status, &db.UserMetadata{SSHKey: key}); err != nil {
		t.Fatal(err)
	}
	u.Role = role
	return u
}

func tunnelLogin(t *testing.T, srv *Server) string {
	t.Helper()
	rr := tunnelRequest(srv, "POST", "/service/tunnel", `{"email":"`+knownEmail+`","passphrase":"correct horse"}`, nil)
	if rr.Code != 200 {
		t.Fatalf("tunnel login: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &res)
	tok, _ := res["access_token"].(string)
	if tok == "" {
		t.Fatalf("no token in %s", rr.Body.String())
	}
	return tok
}

func TestTunnelNeedsItsOwnSignIn(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")
	tunnelUp(srv, "nodekey:one")

	// Nothing answers without a tunnel token — not the API, not the MCP,
	// not even the open bits.
	for _, path := range []string{"/api/me", "/mcp", "/frbr.js", "/control"} {
		if rr := tunnelRequest(srv, "GET", path, "", nil); rr.Code != 401 {
			t.Errorf("%s without a token: %d, want 401", path, rr.Code)
		}
	}

	// A wrong passphrase gets nothing.
	rr := tunnelRequest(srv, "POST", "/service/tunnel", `{"email":"`+knownEmail+`","passphrase":"nope"}`, nil)
	if rr.Code != 401 {
		t.Errorf("wrong passphrase: %d, want 401", rr.Code)
	}

	tok := tunnelLogin(t, srv)
	rr = tunnelRequest(srv, "GET", "/api/me", "", map[string]string{"Authorization": "Bearer " + tok})
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), knownEmail) {
		t.Errorf("/api/me with a tunnel token: %d %s", rr.Code, rr.Body.String())
	}

	// A token the panel minted doesn't open the tunnel.
	u, _ := srv.store.GetUserByEmail(knownEmail)
	rec, _ := srv.adminAuthRecord()
	panelTok, _ := srv.mintFreshbreathToken(subjectForUser(u), u.Email, u.Role, u.Name, rec.ID, nil, nil)
	if rr := tunnelRequest(srv, "GET", "/api/me", "", map[string]string{"Authorization": "Bearer " + panelTok}); rr.Code != 401 {
		t.Errorf("panel token through the tunnel: %d, want 401", rr.Code)
	}
}

// A tunnel token is no use outside the tunnel.
func TestTunnelTokenOnlyWorksThroughTheTunnel(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")
	tunnelUp(srv, "nodekey:one")
	tok := tunnelLogin(t, srv)

	rr := testRequest(t, srv, "GET", "/api/me", nil, map[string]string{"Authorization": "Bearer " + tok})
	if rr.Code != 401 {
		t.Errorf("tunnel token on the main listener: %d, want 401", rr.Code)
	}
	// And the sign-in itself isn't on the main listener.
	rr = testRequest(t, srv, "POST", "/service/tunnel", strings.NewReader(`{"email":"`+knownEmail+`","passphrase":"correct horse"}`), nil)
	if rr.Code == 200 {
		t.Error("tunnel sign-in answered on the main listener")
	}
}

// Resetting the address signs everyone out; so does deactivation.
func TestTunnelTokenDiesWithTheAddressAndTheAccount(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	u := passphraseUser(t, srv, "Superuser")
	tunnelUp(srv, "nodekey:one")
	tok := tunnelLogin(t, srv)
	auth := map[string]string{"Authorization": "Bearer " + tok}

	tunnelUp(srv, "nodekey:two")
	if rr := tunnelRequest(srv, "GET", "/api/me", "", auth); rr.Code != 401 {
		t.Errorf("token from the old address: %d, want 401", rr.Code)
	}

	tunnelUp(srv, "nodekey:one")
	srv.store.UpdateUser(u.ID, u.Name, u.Email, u.Role, "Inactive", nil)
	if rr := tunnelRequest(srv, "GET", "/api/me", "", auth); rr.Code != 401 {
		t.Errorf("deactivated account: %d, want 401", rr.Code)
	}
}

// The tunnel lets the user in as themselves: their role, not more.
func TestTunnelKeepsTheUsersRole(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Member")
	tunnelUp(srv, "nodekey:one")
	tok := tunnelLogin(t, srv)
	if rr := tunnelRequest(srv, "GET", "/api/users", "", map[string]string{"Authorization": "Bearer " + tok}); rr.Code != 403 {
		t.Errorf("member at /api/users: %d, want 403", rr.Code)
	}
	if rr := tunnelRequest(srv, "GET", "/api/tunnel", "", map[string]string{"Authorization": "Bearer " + tok}); rr.Code != 403 {
		t.Errorf("member at /api/tunnel: %d, want 403", rr.Code)
	}
}

// The tunnel can't be minted for while it's down.
func TestTunnelLoginNeedsARunningTunnel(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")
	rr := tunnelRequest(srv, "POST", "/service/tunnel", `{"email":"`+knownEmail+`","passphrase":"correct horse"}`, nil)
	if rr.Code == 200 {
		t.Error("tunnel token minted with no tunnel running")
	}
}

// The identity round-trips sealed, so the address survives a restart.
func TestTunnelIdentityPersists(t *testing.T) {
	srv := newTestServer(t)
	first, err := srv.loadTunnelIdentity()
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := srv.store.GetSetting("tunnel_identity")
	if sealed == "" || strings.Contains(sealed, "privkey") {
		t.Fatalf("identity not stored sealed: %q", sealed)
	}
	again, err := srv.loadTunnelIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if !again.Key.Equal(first.Key) || !again.PSK.Equal(first.PSK) {
		t.Error("identity changed between loads")
	}
}
