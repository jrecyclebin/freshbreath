package server

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tailscale/tailcat"
	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
	"tailscale.com/tailcfg"
)

// ── The tunnel ──
//
// The tailcat server itself isn't started here (it needs a relay); these
// drive the tunnel's HTTP front as though a request had come through it.

func tunnelRequest(srv *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://localhost:18080"+path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	srv.tunnelHandler().ServeHTTP(rr, req)
	return rr
}

func bearer(tok string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + tok}
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

// setAdminSSH gates the panel with the built-in SSH record.
func setAdminSSH(t *testing.T, srv *Server) {
	t.Helper()
	id, err := srv.store.BuiltinAuthID(db.AuthSSHKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(id, 10)); err != nil {
		t.Fatal(err)
	}
}

type tunnelPair struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Expires int    `json:"expires_in"`
	AuthID  int64  `json:"auth_id"`
}

func tunnelLogin(t *testing.T, srv *Server) tunnelPair {
	t.Helper()
	rr := tunnelRequest(srv, "POST", "/service/direct", `{"email":"`+knownEmail+`","passphrase":"correct horse"}`, nil)
	if rr.Code != 200 {
		t.Fatalf("tunnel sign-in: %d %s", rr.Code, rr.Body.String())
	}
	var p tunnelPair
	json.Unmarshal(rr.Body.Bytes(), &p)
	sshID, _ := srv.store.BuiltinAuthID(db.AuthSSHKey)
	if p.Access == "" || p.Refresh == "" || p.Expires != 900 || p.AuthID != sshID {
		t.Fatalf("want a 15-minute pair on the SSH record, got %s", rr.Body.String())
	}
	return p
}

// tunnelRefresh spends a refresh token at /oauth/token/{auth_id} through
// the tunnel, as frbr.js or an MCP client would.
func tunnelRefresh(srv *Server, p tunnelPair) *httptest.ResponseRecorder {
	return tunnelRequest(srv, "POST", "/oauth/token/"+strconv.FormatInt(p.AuthID, 10),
		"grant_type=refresh_token&refresh_token="+p.Refresh,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

func TestTunnelNeedsASignIn(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")

	// Nothing answers without a token — not the API, not the MCP, not
	// even the open bits.
	for _, path := range []string{"/api/me", "/mcp", "/frbr.js", "/control"} {
		if rr := tunnelRequest(srv, "GET", path, "", nil); rr.Code != 401 {
			t.Errorf("%s without a token: %d, want 401", path, rr.Code)
		}
	}
	if rr := tunnelRequest(srv, "POST", "/service/direct", `{"email":"`+knownEmail+`","passphrase":"nope"}`, nil); rr.Code != 401 {
		t.Errorf("wrong passphrase: %d, want 401", rr.Code)
	}

	p := tunnelLogin(t, srv)
	if rr := tunnelRequest(srv, "GET", "/api/me", "", bearer(p.Access)); rr.Code != 200 || !strings.Contains(rr.Body.String(), knownEmail) {
		t.Errorf("/api/me with a tunnel token: %d %s", rr.Code, rr.Body.String())
	}
	// A refresh token is no bearer.
	if rr := tunnelRequest(srv, "GET", "/api/me", "", bearer(p.Refresh)); rr.Code != 401 {
		t.Errorf("refresh token as a bearer: %d, want 401", rr.Code)
	}
}

// A sign-in on the panel's own record works through the tunnel too.
func TestTunnelTakesPanelTokens(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	u := passphraseUser(t, srv, "Superuser")
	rec, _ := srv.adminAuthRecord()
	panelTok, _ := srv.mintFreshbreathToken(subjectForUser(u), u.Email, u.Role, u.Name, rec.ID, nil, nil)
	if rr := tunnelRequest(srv, "GET", "/api/me", "", bearer(panelTok)); rr.Code != 200 {
		t.Errorf("panel token through the tunnel: %d %s", rr.Code, rr.Body.String())
	}
}

// When the panel is gated by something other than SSH, a tunnel sign-in
// opens it only through the tunnel.
func TestTunnelTokenStaysInTheTunnelWhenThePanelIsNotSSH(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")
	p := tunnelLogin(t, srv)

	if rr := testRequest(t, srv, "GET", "/api/me", nil, bearer(p.Access)); rr.Code != 401 {
		t.Errorf("tunnel token at an OIDC-gated panel: %d, want 401", rr.Code)
	}
	if rr := testRequest(t, srv, "POST", "/service/direct", strings.NewReader(`{"email":"`+knownEmail+`","passphrase":"correct horse"}`), nil); rr.Code == 200 {
		t.Error("tunnel sign-in answered on the main listener")
	}
}

// When the panel is gated by the SSH record, a tunnel sign-in is a panel
// sign-in: the same token works on the main listener.
func TestTunnelTokenIsAPanelTokenWhenThePanelIsSSH(t *testing.T) {
	srv := newTestServer(t)
	setAdminSSH(t, srv)
	passphraseUser(t, srv, "Superuser")
	p := tunnelLogin(t, srv)

	if rr := testRequest(t, srv, "GET", "/api/me", nil, bearer(p.Access)); rr.Code != 200 {
		t.Errorf("tunnel token at an SSH-gated panel: %d %s", rr.Code, rr.Body.String())
	}
	if rr := tunnelRequest(srv, "GET", "/api/me", "", bearer(p.Access)); rr.Code != 200 {
		t.Errorf("tunnel token through the tunnel: %d %s", rr.Code, rr.Body.String())
	}
}

// Refreshing goes through /oauth/token like any session's, and rotates:
// spending a token that has been rotated past ends the session.
func TestTunnelRefreshRotates(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")
	p1 := tunnelLogin(t, srv)

	next := func(p tunnelPair) tunnelPair {
		t.Helper()
		rr := tunnelRefresh(srv, p)
		if rr.Code != 200 {
			t.Fatalf("refresh: %d %s", rr.Code, rr.Body.String())
		}
		var n tunnelPair
		json.Unmarshal(rr.Body.Bytes(), &n)
		n.AuthID = p.AuthID
		return n
	}
	p2 := next(p1)
	if rr := tunnelRequest(srv, "GET", "/api/me", "", bearer(p2.Access)); rr.Code != 200 {
		t.Fatalf("refreshed access token: %d %s", rr.Code, rr.Body.String())
	}
	p3 := next(p2)

	// p1 is two rotations back: theft, so the session is over.
	if rr := tunnelRefresh(srv, p1); rr.Code == 200 {
		t.Error("stale refresh token was spent")
	}
	if rr := tunnelRefresh(srv, p3); rr.Code == 200 {
		t.Error("session survived refresh token reuse")
	}
}

// A tunnel session is listed with the rest, and signing out ends it.
func TestTunnelSessionIsRevocable(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")
	p := tunnelLogin(t, srv)

	rr := tunnelRequest(srv, "GET", "/api/me/sessions", "", bearer(p.Access))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"device_label":"direct"`) {
		t.Fatalf("sessions: %d %s", rr.Code, rr.Body.String())
	}
	if rr := tunnelRequest(srv, "DELETE", "/api/me/sessions", "", bearer(p.Access)); rr.Code != 204 {
		t.Fatalf("sign out everywhere: %d %s", rr.Code, rr.Body.String())
	}
	if rr := tunnelRefresh(srv, p); rr.Code == 200 {
		t.Error("refresh after sign-out succeeded")
	}
}

// The tunnel lets the user in as themselves: their role, not more — and
// not at all once deactivated.
func TestTunnelKeepsTheUsersStanding(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	u := passphraseUser(t, srv, "Member")
	p := tunnelLogin(t, srv)
	if rr := tunnelRequest(srv, "GET", "/api/users", "", bearer(p.Access)); rr.Code != 403 {
		t.Errorf("member at /api/users: %d, want 403", rr.Code)
	}
	if rr := tunnelRequest(srv, "GET", "/api/tunnel", "", bearer(p.Access)); rr.Code != 403 {
		t.Errorf("member at /api/tunnel: %d, want 403", rr.Code)
	}
	srv.store.UpdateUser(u.ID, u.Name, u.Email, u.Role, "Inactive", u.Metadata)
	if rr := tunnelRequest(srv, "GET", "/api/me", "", bearer(p.Access)); rr.Code != 401 {
		t.Errorf("deactivated account: %d, want 401", rr.Code)
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

// The pinned relay is the whole region, not its DERP map ID: an address
// drops the ID, so parsing one back numbers regions from 1, an ID the
// live map doesn't have. Restarting on the pin must give the same address.
func TestTunnelPinsTheWholeRelay(t *testing.T) {
	srv := newTestServer(t)
	id, err := srv.loadTunnelIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ci := tailcat.ConnInfo{
		ServerPublic:      tailcat.NodePublic{NodePublic: id.Key.Public()},
		ServerDiscoPublic: tailcat.DiscoPublicForNode(id.Key),
		PresharedKey:      id.PSK,
		Region: []*tailcfg.DERPRegion{{
			RegionID: 301, RegionCode: "nyc", RegionName: "New York City",
			Nodes: []*tailcfg.DERPNode{{Name: "301a", RegionID: 301, HostName: "tc301a.ipn.dev", CanPort80: true}},
		}},
	}
	addr := ci.Addr()
	if !id.pinRelay(addr) {
		t.Fatal("relay not pinned")
	}
	if err := srv.storeTunnelIdentity(id); err != nil {
		t.Fatal(err)
	}
	again, err := srv.loadTunnelIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if again.Region == nil || len(again.Region.Nodes) == 0 || again.Region.Nodes[0].HostName != "tc301a.ipn.dev" {
		t.Fatalf("pinned region lost: %+v", again.Region)
	}
	ci.Region = []*tailcfg.DERPRegion{again.Region}
	if ci.Addr() != addr {
		t.Error("restarting on the pinned relay changes the address")
	}
}

// An identity pinned by DERP map ID (always a meaningless 1) still loads,
// keeping its key; the relay is picked afresh.
func TestTunnelIgnoresARegionID(t *testing.T) {
	srv := newTestServer(t)
	id, err := srv.loadTunnelIdentity()
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(map[string]any{"key": id.Key, "psk": id.PSK, "region_id": 1})
	sealed, err := seal(srv.deriveSubkey(db.SealSubkeyLabel), plain)
	if err != nil {
		t.Fatal(err)
	}
	srv.store.SetSetting("tunnel_identity", sealed)
	again, err := srv.loadTunnelIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if !again.Key.Equal(id.Key) || again.Region != nil {
		t.Errorf("old identity loaded wrong: region %+v", again.Region)
	}
}

// Through the tunnel the Host names this server, whatever the client
// sends: an app's own host doesn't steer into the app, and a name the
// server doesn't answer to isn't refused. A local Host keeps its port.
func TestTunnelPinsTheHost(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	passphraseUser(t, srv, "Superuser")
	hostedRemoteApp(t, srv, nil)
	p := tunnelLogin(t, srv)

	for _, host := range []string{"blog.example.org", "nowhere.example.com", "localhost:18080", "127.0.0.1:18080"} {
		req := httptest.NewRequest("GET", "/api/me", nil)
		req.Host = host
		req.Header.Set("Authorization", "Bearer "+p.Access)
		rr := httptest.NewRecorder()
		srv.tunnelHandler().ServeHTTP(rr, req)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), knownEmail) {
			t.Errorf("Host %s: %d %s, want the panel's /api/me", host, rr.Code, rr.Body.String())
		}
	}

	req := httptest.NewRequest("GET", "/frbr.js", nil)
	req.Host = "localhost:18080"
	req.Header.Set("Authorization", "Bearer "+p.Access)
	rr := httptest.NewRecorder()
	srv.tunnelHandler().ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), `"apiBase":"http://localhost:18080"`) {
		t.Errorf("frbr.js through a forward should keep its port: %d", rr.Code)
	}
}
