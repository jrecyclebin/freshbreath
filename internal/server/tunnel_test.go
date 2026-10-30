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
	access, _ := tunnelSession(t, srv, `{"email":"`+knownEmail+`","passphrase":"correct horse"}`)
	return access
}

// tunnelSession posts body to the tunnel's door and returns the pair.
func tunnelSession(t *testing.T, srv *Server, body string) (access, refresh string) {
	t.Helper()
	rr := tunnelRequest(srv, "POST", "/service/tunnel", body, nil)
	if rr.Code != 200 {
		t.Fatalf("tunnel sign-in: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &res)
	access, _ = res["access_token"].(string)
	refresh, _ = res["refresh_token"].(string)
	if access == "" || refresh == "" || res["expires_in"] != float64(900) {
		t.Fatalf("want a 15-minute pair, got %s", rr.Body.String())
	}
	return access, refresh
}

func tunnelRefreshBody(refresh string) string {
	return `{"refresh_token":"` + refresh + `"}`
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

// A refresh token rotates: each spend gives a new pair, and spending one
// that has been rotated past ends the whole session.
func TestTunnelRefreshRotates(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")
	tunnelUp(srv, "nodekey:one")
	_, r1 := tunnelSession(t, srv, `{"email":"`+knownEmail+`","passphrase":"correct horse"}`)

	a2, r2 := tunnelSession(t, srv, tunnelRefreshBody(r1))
	if rr := tunnelRequest(srv, "GET", "/api/me", "", map[string]string{"Authorization": "Bearer " + a2}); rr.Code != 200 {
		t.Fatalf("refreshed access token: %d %s", rr.Code, rr.Body.String())
	}
	a3, _ := tunnelSession(t, srv, tunnelRefreshBody(r2))

	// r1 is two rotations back: theft, so the session is over.
	if rr := tunnelRequest(srv, "POST", "/service/tunnel", tunnelRefreshBody(r1), nil); rr.Code != 401 {
		t.Errorf("stale refresh token: %d, want 401", rr.Code)
	}
	if rr := tunnelRequest(srv, "GET", "/api/me", "", map[string]string{"Authorization": "Bearer " + a3}); rr.Code != 401 {
		t.Errorf("access token after reuse: %d, want 401", rr.Code)
	}
}

// Access and refresh tokens don't stand in for each other, and neither is
// any use at the main listener's token endpoint.
func TestTunnelTokenKindsStayApart(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")
	tunnelUp(srv, "nodekey:one")
	access, refresh := tunnelSession(t, srv, `{"email":"`+knownEmail+`","passphrase":"correct horse"}`)

	if rr := tunnelRequest(srv, "GET", "/api/me", "", map[string]string{"Authorization": "Bearer " + refresh}); rr.Code != 401 {
		t.Errorf("refresh token as a bearer: %d, want 401", rr.Code)
	}
	if rr := tunnelRequest(srv, "POST", "/service/tunnel", tunnelRefreshBody(access), nil); rr.Code != 401 {
		t.Errorf("access token as a refresh token: %d, want 401", rr.Code)
	}
	form := strings.NewReader("grant_type=refresh_token&refresh_token=" + refresh)
	rr := testRequest(t, srv, "POST", "/oauth/token", form, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if rr.Code == 200 {
		t.Errorf("tunnel refresh token spent at /oauth/token: %s", rr.Body.String())
	}
}

// A tunnel session is a session like any other: it's listed, and signing
// out (or a passphrase reset, which revokes the same way) ends it at once.
func TestTunnelSessionIsRevocable(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")
	tunnelUp(srv, "nodekey:one")
	access, refresh := tunnelSession(t, srv, `{"email":"`+knownEmail+`","passphrase":"correct horse"}`)
	auth := map[string]string{"Authorization": "Bearer " + access}

	rr := tunnelRequest(srv, "GET", "/api/me/sessions", "", auth)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"device_label":"tunnel"`) {
		t.Fatalf("sessions: %d %s", rr.Code, rr.Body.String())
	}
	if rr := tunnelRequest(srv, "DELETE", "/api/me/sessions", "", auth); rr.Code != 204 {
		t.Fatalf("sign out everywhere: %d %s", rr.Code, rr.Body.String())
	}
	if rr := tunnelRequest(srv, "GET", "/api/me", "", auth); rr.Code != 401 {
		t.Errorf("access token after sign-out: %d, want 401", rr.Code)
	}
	if rr := tunnelRequest(srv, "POST", "/service/tunnel", tunnelRefreshBody(refresh), nil); rr.Code != 401 {
		t.Errorf("refresh after sign-out: %d, want 401", rr.Code)
	}
}
