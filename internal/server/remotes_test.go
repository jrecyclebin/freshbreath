package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"

	"poggers.institute/freshbreath/internal/db"
)

// ── Remotes ──

// testTailcatAddr makes a fresh, valid tailcat address.
func testTailcatAddr(t *testing.T) string {
	t.Helper()
	nk := key.NewNode()
	ci := tailcat.ConnInfo{
		ServerPublic:      tailcat.NodePublic{NodePublic: nk.Public()},
		ServerDiscoPublic: tailcat.DiscoPublicForNode(nk),
		PresharedKey:      tailcat.NewPresharedKey(),
		RegionID:          1,
	}
	return string(ci.Addr())
}

func addRemote(t *testing.T, srv *Server, as *db.User, name, addr string) *db.Remote {
	t.Helper()
	rr := requestAs(t, srv, as, "POST", "/api/remotes", `{"name":"`+name+`","address":"`+addr+`"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add remote: %d %s", rr.Code, rr.Body.String())
	}
	var r db.Remote
	json.Unmarshal(rr.Body.Bytes(), &r)
	return &r
}

// Admins manage remotes; Members don't see them at all.
func TestRemotesAreForAdmins(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	member := mustUser(t, srv, "Mo", "Member", "Active")
	r := addRemote(t, srv, admin, "Client X", testTailcatAddr(t))

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/remotes", ""},
		{"POST", "/api/remotes", `{"name":"Y","address":"` + testTailcatAddr(t) + `"}`},
		{"PUT", "/api/remotes/" + r.ID, `{"name":"Z"}`},
		{"DELETE", "/api/remotes/" + r.ID, ""},
	} {
		if rr := requestAs(t, srv, member, c.method, c.path, c.body); rr.Code != http.StatusForbidden {
			t.Errorf("Member %s %s: %d, want 403", c.method, c.path, rr.Code)
		}
	}
}

// The address goes in and never comes back out; a fingerprint stands in
// for it.
func TestRemoteAddressIsNeverShown(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	addr := testTailcatAddr(t)
	r := addRemote(t, srv, admin, "Client X", addr)
	if r.ID == "" || r.Fingerprint == "" || !strings.HasPrefix(r.Fingerprint, "r1-") {
		t.Errorf("created remote: %+v", r)
	}

	rr := requestAs(t, srv, admin, "GET", "/api/remotes", "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Client X") {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), addr) || strings.Contains(rr.Body.String(), `"address"`) {
		t.Errorf("list shows the address: %s", rr.Body.String())
	}
	if got, _ := srv.store.GetRemote(r.ID); got == nil || got.Address != addr {
		t.Errorf("stored address didn't round-trip")
	}
}

func TestRemoteNeedsATailcatAddress(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	for _, body := range []string{
		`{"name":"X","address":"https://example.com"}`,
		`{"name":"X","address":""}`,
		`{"name":"","address":"` + testTailcatAddr(t) + `"}`,
	} {
		if rr := requestAs(t, srv, admin, "POST", "/api/remotes", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rr.Code)
		}
	}
}

// A remote can be renamed and deleted, but its address can't change: a
// new address is a new remote.
func TestRemoteAddressIsFixed(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	addr := testTailcatAddr(t)
	r := addRemote(t, srv, admin, "Client X", addr)

	if rr := requestAs(t, srv, admin, "PUT", "/api/remotes/"+r.ID, `{"name":"Client X","address":"`+testTailcatAddr(t)+`"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("changing the address: %d, want 400", rr.Code)
	}
	if rr := requestAs(t, srv, admin, "PUT", "/api/remotes/"+r.ID, `{"name":"Client Y"}`); rr.Code != http.StatusNoContent {
		t.Fatalf("rename: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := srv.store.GetRemote(r.ID)
	if got.Name != "Client Y" || got.Address != addr {
		t.Errorf("after rename: %q %v", got.Name, got.Address == addr)
	}

	if rr := requestAs(t, srv, admin, "DELETE", "/api/remotes/"+r.ID, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rr.Code)
	}
	if rr := requestAs(t, srv, admin, "DELETE", "/api/remotes/"+r.ID, ""); rr.Code != http.StatusNotFound {
		t.Errorf("delete again: %d, want 404", rr.Code)
	}
}

// Names pick a remote in login("remote://<name>"), so no two share one,
// whatever the case. A remote can still change the case of its own.
func TestRemoteNamesAreUnique(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	prod := addRemote(t, srv, admin, "Prod", testTailcatAddr(t))
	other := addRemote(t, srv, admin, "Staging", testTailcatAddr(t))

	if rr := requestAs(t, srv, admin, "POST", "/api/remotes", `{"name":" prod ","address":"`+testTailcatAddr(t)+`"}`); rr.Code != http.StatusConflict {
		t.Errorf("adding a second prod: %d, want 409", rr.Code)
	}
	if rr := requestAs(t, srv, admin, "PUT", "/api/remotes/"+other.ID, `{"name":"PROD"}`); rr.Code != http.StatusConflict {
		t.Errorf("renaming onto prod: %d, want 409", rr.Code)
	}
	if rr := requestAs(t, srv, admin, "PUT", "/api/remotes/"+prod.ID, `{"name":"PROD"}`); rr.Code != http.StatusNoContent {
		t.Errorf("recasing its own name: %d %s", rr.Code, rr.Body.String())
	}
}

// ── The proxy ──

// dialTo makes remote clients reach handler over real HTTP, standing in
// for the tunnel. closed counts how many clients were closed.
func dialTo(t *testing.T, handler http.Handler, closed *int) func(string) *remoteConn {
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return func(string) *remoteConn {
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", ts.Listener.Addr().String())
		}}
		return &remoteConn{client: &http.Client{Transport: tr}, close: func() {
			tr.CloseIdleConnections()
			if closed != nil {
				*closed++
			}
		}}
	}
}

// remoteRequest is requestAs with headers of our own.
func remoteRequest(srv *Server, as *db.User, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), userKey, as))
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// remoteSetup is server A with an Admin and a remote that reaches server
// B's tunnel, where the known user (of role bRole) has a passphrase.
func remoteSetup(t *testing.T, bRole string) (a *Server, admin *db.User, remote *db.Remote) {
	t.Helper()
	a, b := newTestServer(t), newTestServer(t)
	setAdminSSH(t, b)
	passphraseUser(t, b, bRole)
	a.remoteConns.open = dialTo(t, b.tunnelHandler(), nil)
	admin = mustUser(t, a, "Ada", "Admin", "Active")
	return a, admin, addRemote(t, a, admin, "Server B", testTailcatAddr(t))
}

func pairRemote(t *testing.T, a *Server, as *db.User, remote *db.Remote) tunnelPair {
	t.Helper()
	rr := remoteRequest(a, as, "POST", "/api/remotes/"+remote.ID+"/service/direct",
		`{"email":"`+knownEmail+`","passphrase":"correct horse"}`, nil)
	if rr.Code != 200 {
		t.Fatalf("pair: %d %s", rr.Code, rr.Body.String())
	}
	var p tunnelPair
	json.Unmarshal(rr.Body.Bytes(), &p)
	if p.Access == "" || p.Refresh == "" || p.AuthID == 0 {
		t.Fatalf("pair gave %s", rr.Body.String())
	}
	return p
}

// Pair, call and refresh, all through A's proxy.
func TestRemoteProxyRoundTrip(t *testing.T) {
	a, admin, remote := remoteSetup(t, "Superuser")
	base := "/api/remotes/" + remote.ID
	p := pairRemote(t, a, admin, remote)

	rr := remoteRequest(a, admin, "GET", base+"/api/me", "", map[string]string{"X-Tunnel-Token": p.Access})
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), knownEmail) {
		t.Fatalf("remote /api/me: %d %s", rr.Code, rr.Body.String())
	}

	rr = remoteRequest(a, admin, "POST", base+"/oauth/token/"+strconv.FormatInt(p.AuthID, 10),
		"grant_type=refresh_token&refresh_token="+p.Refresh,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if rr.Code != 200 {
		t.Fatalf("remote refresh: %d %s", rr.Code, rr.Body.String())
	}
	var next tunnelPair
	json.Unmarshal(rr.Body.Bytes(), &next)
	if next.Refresh == "" || next.Refresh == p.Refresh {
		t.Errorf("refresh didn't rotate: %s", rr.Body.String())
	}
	if rr := remoteRequest(a, admin, "GET", base+"/api/me", "", map[string]string{"X-Tunnel-Token": next.Access}); rr.Code != 200 {
		t.Errorf("refreshed token at the remote: %d %s", rr.Code, rr.Body.String())
	}
}

// The remote decides what its user may do; the proxy only carries it.
func TestRemoteProxyLeavesAuthorizationToTheRemote(t *testing.T) {
	a, admin, remote := remoteSetup(t, "Member")
	base := "/api/remotes/" + remote.ID
	p := pairRemote(t, a, admin, remote)

	if rr := remoteRequest(a, admin, "GET", base+"/api/users", "", map[string]string{"X-Tunnel-Token": p.Access}); rr.Code != http.StatusForbidden {
		t.Errorf("B Member at B's /api/users: %d, want B's 403", rr.Code)
	}
	if rr := remoteRequest(a, admin, "GET", base+"/api/me", "", nil); rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "/service/direct") {
		t.Errorf("no remote token: %d %s, want B's 401", rr.Code, rr.Body.String())
	}
}

func TestRemoteProxyIsForAdmins(t *testing.T) {
	a, _, remote := remoteSetup(t, "Superuser")
	member := mustUser(t, a, "Mo", "Member", "Active")
	if rr := remoteRequest(a, member, "POST", "/api/remotes/"+remote.ID+"/service/direct",
		`{"email":"`+knownEmail+`","passphrase":"correct horse"}`, nil); rr.Code != http.StatusForbidden {
		t.Errorf("Member through the proxy: %d, want 403", rr.Code)
	}
}

// What belongs to A stays at A: its cookies, its bearer, the page's origin
// and app. The remote's token goes on as the remote's Authorization, and
// the remote's cookies don't come back.
func TestRemoteProxyHeaders(t *testing.T) {
	a := newTestServer(t)
	var seen *http.Request
	a.remoteConns.open = dialTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.Header().Set("Set-Cookie", "remote=1")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("X-Remote", "yes")
		w.WriteHeader(201)
		w.Write([]byte("made"))
	}), nil)
	admin := mustUser(t, a, "Ada", "Admin", "Active")
	remote := addRemote(t, a, admin, "Spy", testTailcatAddr(t))

	rr := remoteRequest(a, admin, "POST", "/api/remotes/"+remote.ID+"/api/things?x=1", `{"a":1}`, map[string]string{
		"Authorization":  "Bearer a-panel-token",
		"Cookie":         "frbr_gate=abc",
		"Origin":         "http://example.com",
		"X-App-Nonce":    "panelnonce",
		"X-Tunnel-Token": "Bearer b-token",
		"X-Custom":       "kept",
	})
	if rr.Code != 201 || rr.Body.String() != "made" || rr.Header().Get("X-Remote") != "yes" {
		t.Fatalf("response: %d %q %v", rr.Code, rr.Body.String(), rr.Header())
	}
	if rr.Header().Get("Set-Cookie") != "" {
		t.Error("the remote's cookie came back")
	}
	if rr.Header().Get("X-Frbr-Remote") != "1" {
		t.Error("the remote's answer isn't marked as the remote's")
	}
	if seen == nil {
		t.Fatal("the remote saw nothing")
	}
	if seen.URL.Path != "/api/things" || seen.URL.RawQuery != "x=1" || seen.Method != "POST" {
		t.Errorf("remote got %s %s?%s", seen.Method, seen.URL.Path, seen.URL.RawQuery)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer b-token" {
		t.Errorf("remote Authorization: %q", got)
	}
	for _, h := range []string{"Cookie", "Origin", "X-App-Nonce", "X-Tunnel-Token"} {
		if seen.Header.Get(h) != "" {
			t.Errorf("%s reached the remote: %q", h, seen.Header.Get(h))
		}
	}
	if seen.Header.Get("X-Custom") != "kept" {
		t.Error("an ordinary header was dropped")
	}
}

// An event stream reaches the browser as it's written, not when it ends.
func TestRemoteProxyStreams(t *testing.T) {
	a := newTestServer(t)
	release := make(chan struct{})
	a.remoteConns.open = dialTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: one\n\n"))
		w.(http.Flusher).Flush()
		// Wait for the test to see "one" (but never hang a failing test).
		select {
		case <-release:
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			return
		}
		w.Write([]byte("data: two\n\n"))
	}), nil)
	admin := mustUser(t, a, "Ada", "Admin", "Active")
	remote := addRemote(t, a, admin, "Streamer", testTailcatAddr(t))

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, admin)))
	}))
	defer front.Close()
	resp, err := http.Get(front.URL + "/api/remotes/" + remote.ID + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	first := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		first <- string(buf[:n])
	}()
	select {
	case got := <-first:
		if !strings.Contains(got, "one") {
			t.Errorf("first chunk: %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first event was held back until the stream ended")
	}
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "two") {
		t.Errorf("rest of the stream: %q", rest)
	}
}

// An unknown remote is a 404, an unreachable one a 502, and deleting a
// remote closes its client.
func TestRemoteProxyFailuresAndCleanup(t *testing.T) {
	a := newTestServer(t)
	admin := mustUser(t, a, "Ada", "Admin", "Active")
	if rr := remoteRequest(a, admin, "GET", "/api/remotes/nope/api/me", "", nil); rr.Code != http.StatusNotFound || rr.Header().Get("X-Frbr-Remote") != "" {
		t.Errorf("unknown remote: %d (marked %q), want this server's own 404", rr.Code, rr.Header().Get("X-Frbr-Remote"))
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	deadAddr := dead.Listener.Addr().String()
	dead.Close()
	closed := 0
	a.remoteConns.open = func(string) *remoteConn {
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", deadAddr)
		}}
		return &remoteConn{client: &http.Client{Transport: tr}, close: func() { closed++ }}
	}
	remote := addRemote(t, a, admin, "Gone", testTailcatAddr(t))
	if rr := remoteRequest(a, admin, "GET", "/api/remotes/"+remote.ID+"/api/me", "", nil); rr.Code != http.StatusBadGateway {
		t.Errorf("unreachable remote: %d, want 502", rr.Code)
	}
	if rr := requestAs(t, a, admin, "DELETE", "/api/remotes/"+remote.ID, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rr.Code)
	}
	if closed != 1 {
		t.Errorf("client closed %d times on delete, want 1", closed)
	}
}

// ── Transfer ──

// A transfer packs apps and services here and imports them there, as the
// remote user the browser signed in as. The remote's progress comes back in
// the same stream, and a second transfer is an update, not a duplicate.
func TestTransferToARemote(t *testing.T) {
	a, b := newTestServer(t), newTestServer(t)
	a.config.DataDir, b.config.DataDir = t.TempDir(), t.TempDir()
	setAdminSSH(t, b)
	passphraseUser(t, b, "Admin")
	a.remoteConns.open = dialTo(t, b.tunnelHandler(), nil)
	admin := mustUser(t, a, "Ada", "Admin", "Active")
	remote := addRemote(t, a, admin, "Server B", testTailcatAddr(t))
	p := pairRemote(t, a, admin, remote)

	nonce, err := a.store.CreateApp("Blog", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	createSlotFile(t, a, nonce, "web", "index.html", []byte("v1"))
	registerService(t, a, "greeter", "/mcp/greeter", db.ServiceDescriptor{Type: "virtual"})
	svc, _ := a.store.GetServiceByName("greeter")
	os.MkdirAll(filepath.Dir(serviceDefinitionPath(a.config.DataDir, svc)), 0755)
	os.WriteFile(serviceDefinitionPath(a.config.DataDir, svc), []byte("greeter def"), 0644)

	transfer := func() []sseEvent {
		t.Helper()
		rr := remoteRequest(a, admin, "POST", "/api/transfer",
			`{"remote":"`+remote.ID+`","apps":["`+nonce+`"],"services":["greeter"]}`,
			map[string]string{"X-Tunnel-Token": p.Access})
		if rr.Code != 200 {
			t.Fatalf("transfer: %d %s", rr.Code, rr.Body.String())
		}
		return parseSSE(rr.Body.String())
	}
	events := transfer()
	var seq []string
	for _, e := range events {
		if e.Event != "op" {
			seq = append(seq, e.Event)
		}
	}
	if want := "[collect app service manifest send extract validate done]"; fmt.Sprint(seq) != want {
		t.Errorf("events = %v, want %s", seq, want)
	}

	if app, err := b.store.GetApp(nonce); err != nil || app.Name != "Blog" {
		t.Fatalf("app on B: %+v %v", app, err)
	}
	if got, _ := os.ReadFile(filepath.Join(b.config.DataDir, "apps", nonce, "staging", "index.html")); string(got) != "v1" {
		t.Errorf("B staging = %q", got)
	}
	bsvc, err := b.store.GetServiceByURL("/mcp/greeter")
	if err != nil {
		t.Fatalf("service on B: %v", err)
	}
	if got, _ := os.ReadFile(serviceDefinitionPath(b.config.DataDir, bsvc)); string(got) != "greeter def" {
		t.Errorf("B service file = %q", got)
	}

	// Again, with new files: updated in place.
	createSlotFile(t, a, nonce, "web", "index.html", []byte("v2"))
	for _, e := range transfer() {
		if e.Data["status"] == "created" {
			t.Errorf("second transfer created %v again", e.Data["target"])
		}
	}
	if got, _ := os.ReadFile(filepath.Join(b.config.DataDir, "apps", nonce, "staging", "index.html")); string(got) != "v2" {
		t.Errorf("B staging after the update = %q", got)
	}
	apps, _ := b.store.ListApps()
	if len(apps) != 1 {
		t.Errorf("B has %d apps, want 1", len(apps))
	}
}

// A refused token ends the stream with a plain error, and without one the
// transfer doesn't start.
func TestTransferNeedsARemoteSignIn(t *testing.T) {
	a, admin, remote := remoteSetup(t, "Admin")
	a.config.DataDir = t.TempDir()
	nonce, _ := a.store.CreateApp("Blog", "", "", nil, nil)
	createSlotFile(t, a, nonce, "web", "index.html", []byte("v1"))
	body := `{"remote":"` + remote.ID + `","apps":["` + nonce + `"]}`

	if rr := remoteRequest(a, admin, "POST", "/api/transfer", body, nil); rr.Code != http.StatusBadRequest {
		t.Errorf("no token: %d, want 400", rr.Code)
	}
	rr := remoteRequest(a, admin, "POST", "/api/transfer", body, map[string]string{"X-Tunnel-Token": "nonsense"})
	events := parseSSE(rr.Body.String())
	if last := events[len(events)-1]; last.Event != "feed_error" || !strings.Contains(fmt.Sprint(last.Data["message"]), "sign in to it again") {
		t.Errorf("bad token: %+v", last)
	}
}
