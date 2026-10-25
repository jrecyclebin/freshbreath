package server

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// Cross-origin is only for apps that run elsewhere, named by nonce — the
// nonce is their credential — and only from the one origin each may speak
// from: the origin of its registered URL, or "null" (a page from disk)
// when it has none. Hosted apps never cross an origin (their pages are on
// this one), and "null" without a nonce is refused outright, so no unnamed
// frame can read anything, env.js's ?loc= resolution included.
//
// An app runs elsewhere when its URL names a location (https://…) or when
// it has no routable slot — a blank URL that isn't hosted is a page opened
// from disk, which no one has to declare.
func TestCrossOriginOnlyForAppsThatRunElsewhere(t *testing.T) {
	srv := newTestServer(t)

	// A hosted app: a deployment slot exists, so it's in the route map.
	hosted, _ := srv.store.CreateApp("Hosted", "Development", "", nil, nil)
	cleanupSlotApp(t, srv, hosted)
	createSlotFile(t, srv, hosted, "web", "index.html", []byte("<h1>dev</h1>"))
	srv.rebuildHostedRoutes()

	// Blank URL, no slot: running from disk, like a file:// page.
	local, _ := srv.store.CreateApp("Local", "Development", "", nil, nil)
	// A URL that names a web origin: running off the server by declaration.
	remote, _ := srv.store.CreateApp("Remote", "Development", "https://app.example/start", nil, nil)

	// Origin: null, with and without an app named. Only the app with no
	// URL runs from disk.
	for nonce, want := range map[string]int{local: 200, remote: 403, hosted: 403, "": 403} {
		rr := testRequest(t, srv, "GET", "/env.js", nil, map[string]string{"Origin": "null", "X-App-Nonce": nonce})
		if rr.Code != want {
			t.Errorf("nonce %q: status %d, want %d", nonce, rr.Code, want)
		}
		if want == 200 && rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Errorf("nonce %q: allow credentials missing", nonce)
		}
	}

	// A web origin passes only for the app registered at it (scheme and
	// host compared case-insensitively, default port dropped).
	for nonce, want := range map[string]int{remote: 403, local: 403, hosted: 403, "": 403} {
		rr := testRequest(t, srv, "GET", "/env.js", nil, map[string]string{"Origin": "https://evil.example", "X-App-Nonce": nonce})
		if rr.Code != want {
			t.Errorf("foreign origin, nonce %q: status %d, want %d", nonce, rr.Code, want)
		}
	}
	shouty, _ := srv.store.CreateApp("Shouty", "Development", "HTTPS://App.Example:443/", nil, nil)
	for nonce, want := range map[string]int{remote: 200, shouty: 200, local: 403, hosted: 403} {
		rr := testRequest(t, srv, "GET", "/env.js", nil, map[string]string{"Origin": "https://app.example", "X-App-Nonce": nonce})
		if rr.Code != want {
			t.Errorf("app origin, nonce %q: status %d, want %d", nonce, rr.Code, want)
		}
	}

	// Same-origin always carries cookies, no nonce needed.
	rr := testRequest(t, srv, "GET", "/env.js", nil, map[string]string{"Origin": "http://example.com"})
	if rr.Code != 200 || rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("same-origin: status %d credentials %q", rr.Code, rr.Header().Get("Access-Control-Allow-Credentials"))
	}

	// ?loc= used to be readable from any "null" frame, handing out a hosted
	// app's nonce. Now even that needs the app's own nonce to pass the gate.
	rr = testRequest(t, srv, "GET", "/env.js?loc=/hosted/", nil, map[string]string{"Origin": "null"})
	if rr.Code != 403 {
		t.Errorf("null frame reading ?loc= for a hosted app: status %d, want 403", rr.Code)
	}
}

// frbr.js's config blob is served only to pages that already name the app
// by nonce — this server's own (Referer-resolved) or an off-server app's
// (?NONCE in the script URL, the only way a script tag can name one).
func TestFrbrJSNeedsNonceToLeaveHome(t *testing.T) {
	srv := newTestServer(t)
	remote, _ := srv.store.CreateApp("Remote", "Development", "https://app.example/start", nil, nil)

	// No nonce, no frbr.js: a "null" frame gets nothing at all.
	rr := testRequest(t, srv, "GET", "/frbr.js", nil, map[string]string{"Origin": "null"})
	if rr.Code != 403 {
		t.Errorf("frbr.js without a nonce from null: status %d, want 403", rr.Code)
	}

	// The nonce in the URL is what names the app (script tags carry no headers).
	rr = testRequest(t, srv, "GET", "/frbr.js?"+remote, nil, map[string]string{"Origin": "https://app.example"})
	if rr.Code != 200 {
		t.Fatalf("frbr.js?NONCE from the app's origin: status %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"appNonce":"`+remote+`"`) {
		t.Error("frbr.js config does not echo the nonce it was asked by")
	}
	if rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("frbr.js?NONCE: allow credentials missing")
	}
}

// A finished browser login is posted only to the origin that may hold it.
func TestLoginPostedToAppOrigin(t *testing.T) {
	srv := newTestServer(t)

	// A hosted app: routed from its own slot, so logins stay on this origin.
	hosted, _ := srv.store.CreateApp("Hosted", "Development", "", nil, nil)
	cleanupSlotApp(t, srv, hosted)
	createSlotFile(t, srv, hosted, "web", "index.html", []byte("<h1>dev</h1>"))
	srv.rebuildHostedRoutes()

	// Blank URL with no slot (a page from disk) and a declared web origin
	// both run off the server. A page from disk has no origin to name, so
	// it gets "*"; a declared web origin gets exactly that origin.
	local, _ := srv.store.CreateApp("Local", "Development", "", nil, nil)
	remote, _ := srv.store.CreateApp("Remote", "Development", "https://app.example/start", nil, nil)

	r := httptest.NewRequest("GET", "/service/login", nil) // Host: example.com
	for nonce, want := range map[string]string{
		hosted: "http://example.com", local: "*", remote: "https://app.example", "": "http://example.com",
	} {
		a, _ := srv.store.GetApp(nonce)
		if got := srv.loginOpenerOrigin(r, a); got != want {
			t.Errorf("app nonce %q: opener origin = %q, want %q", nonce, got, want)
		}
	}

	var page strings.Builder
	writeCallbackPage(&page, &pendingAuth{appState: "s", opener: "https://app.example"}, map[string]interface{}{"v": 1})
	if !strings.Contains(page.String(), `}, "https://app.example");`) {
		t.Errorf("callback page does not target the app origin:\n%s", page.String())
	}
}

// X-Forwarded-Proto is client-settable, so it only counts from a trusted
// proxy peer: loopback by default, FRBR_TRUSTED_PROXIES on top of it.
func TestSchemeOfTrustsForwardedProtoOnlyFromProxies(t *testing.T) {
	srv := newTestServer(t) // default: loopback only

	mk := func(remote, proto string) *http.Request {
		r := httptest.NewRequest("GET", "/env.js", nil)
		r.RemoteAddr = remote
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		return r
	}
	if got := srv.schemeOf(mk("203.0.113.9:4444", "https")); got != "http" {
		t.Errorf("foreign peer claiming https: scheme = %q, want http", got)
	}
	if got := srv.schemeOf(mk("127.0.0.1:4444", "https")); got != "https" {
		t.Errorf("loopback proxy: scheme = %q, want https", got)
	}
	if got := srv.schemeOf(mk("[::1]:4444", "https")); got != "https" {
		t.Errorf("IPv6 loopback proxy: scheme = %q, want https", got)
	}
	if got := srv.schemeOf(mk("127.0.0.1:4444", "")); got != "http" {
		t.Errorf("loopback without a header: scheme = %q, want http", got)
	}

	// A configured proxy joins; a same-CIDR neighbor still does not.
	srv.trustedProxies = trustedProxySet(Config{TrustedProxies: []string{"10.1.2.3", "fd00::/8"}})
	if got := srv.schemeOf(mk("10.1.2.3:9", "https")); got != "https" {
		t.Errorf("configured proxy: scheme = %q, want https", got)
	}
	if got := srv.schemeOf(mk("10.9.9.9:9", "https")); got != "http" {
		t.Errorf("unconfigured neighbor: scheme = %q, want http", got)
	}
	if got := srv.schemeOf(mk("[fd00::5]:9", "https")); got != "https" {
		t.Errorf("configured v6 prefix: scheme = %q, want https", got)
	}

	// TLS on the connection itself needs no proxy.
	r := httptest.NewRequest("GET", "/env.js", nil)
	r.TLS = &tls.ConnectionState{}
	if got := srv.schemeOf(r); got != "https" {
		t.Errorf("direct TLS: scheme = %q, want https", got)
	}
}

// A declared FRBR_BASE_URL makes its scheme authoritative for its own
// host: TLS may terminate at a front-end whose addresses the server
// cannot name (a cloud load balancer), and there is no trusted-proxy
// list of those. The declared scheme only applies when the Host is the
// declared one — a caller reaching a different host gains nothing.
func TestSchemeOfTrustsDeclaredPublicBaseURL(t *testing.T) {
	srv := newTestServer(t)
	srv.config.PublicBaseURL = "https://fresh.example.com"
	srv.publicScheme = "https"
	srv.publicHost = "fresh.example.com"

	mk := func(host, proto string) *http.Request {
		r := httptest.NewRequest("GET", "/env.js", nil)
		r.Host = host
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		return r
	}
	if got := srv.schemeOf(mk("fresh.example.com", "")); got != "https" {
		t.Errorf("declared public host: scheme = %q, want https", got)
	}
	if got := srv.schemeOf(mk("FRESH.example.com.", "")); got != "https" {
		t.Errorf("case and trailing dot normalized: scheme = %q, want https", got)
	}
	if got := srv.schemeOf(mk("fresh.example.com:443", "")); got != "https" {
		t.Errorf("default port stripped: scheme = %q, want https", got)
	}
	if got := srv.schemeOf(mk("fresh.example.com", "http")); got != "https" {
		t.Errorf("an untrusted caller's proto cannot downgrade a declared https host: %q, want https", got)
	}
	if got := srv.schemeOf(mk("app.example.com", "")); got != "http" {
		t.Errorf("undeclared host: scheme = %q, want http", got)
	}
	if got := srv.schemeOf(mk("evil.example.com:9009", "")); got != "http" {
		t.Errorf("a port the declaration does not name: scheme = %q, want http", got)
	}
}

// F4 (audit 202610): a page on another origin that holds an off-server
// app's nonce used to start that app's login and receive the token. Now
// the login can only start from the app's own origin, and the sign-in
// form says which app is asking.
func TestLoginStartsOnlyFromAppOrigin(t *testing.T) {
	srv := newTestServer(t)
	gate := builtinAuth(t, srv, db.AuthSSHKey)
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(gate.ID, 10)); err != nil {
		t.Fatal(err)
	}
	remote, _ := srv.store.CreateApp("Remote Notes", "Development", "https://app.example/start", nil, nil)
	local, _ := srv.store.CreateApp("Disk <Notes>", "Development", "", nil, nil)

	start := func(nonce, origin string) *httptest.ResponseRecorder {
		return testRequest(t, srv, "GET", "/service/login?state=s1", nil, map[string]string{"Origin": origin, "X-App-Nonce": nonce})
	}
	for _, c := range []struct{ nonce, origin string }{
		{remote, "https://evil.example"}, {remote, "null"}, {local, "https://evil.example"},
	} {
		if rr := start(c.nonce, c.origin); rr.Code != http.StatusForbidden {
			t.Errorf("login for %s from %s: status %d, want 403", c.nonce, c.origin, rr.Code)
		}
	}

	form := func(rr *httptest.ResponseRecorder) string {
		t.Helper()
		var res struct {
			URL string `json:"url"`
		}
		if rr.Code != 200 || json.NewDecoder(rr.Body).Decode(&res) != nil {
			t.Fatalf("login start: %d %s", rr.Code, rr.Body.String())
		}
		u, _ := url.Parse(res.URL)
		page := testRequest(t, srv, "GET", u.RequestURI(), nil, nil)
		return page.Body.String()
	}
	if page := form(start(remote, "https://app.example")); !strings.Contains(page, "<b>Remote Notes</b> at https://app.example") {
		t.Errorf("sign-in form doesn't name the app and its origin:\n%s", page)
	}
	if page := form(start(local, "null")); !strings.Contains(page, "<b>Disk &lt;Notes&gt;</b>, a page opened from disk") {
		t.Errorf("sign-in form doesn't name the disk app:\n%s", page)
	}
}

// F2 (audit 202610): the refresh cookie is Strict, so an app that runs
// elsewhere can't spend it. Its login hands the refresh token over in the
// store entry instead, and sets no cookie (one holder per refresh family).
// The panel and hosted apps keep the HttpOnly cookie and never see the
// token itself.
func TestLoginHandsOffServerAppsTheirRefreshToken(t *testing.T) {
	srv := newTestServer(t)
	gate := builtinAuth(t, srv, db.AuthSSHKey)
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(gate.ID, 10)); err != nil {
		t.Fatal(err)
	}
	u, _ := srv.store.CreateUser("Ada", "ada@example.com", "Member", "Active")
	if _, err := srv.coreGenerateSSHKey(u, u, "correct horse battery"); err != nil {
		t.Fatalf("ssh key: %v", err)
	}
	remote, _ := srv.store.CreateApp("Remote", "Development", "https://app.example/", nil, nil)
	srv.adminNonce = adminNonceFrom(srv.store)

	login := func(headers map[string]string) (*httptest.ResponseRecorder, string) {
		t.Helper()
		rr := testRequest(t, srv, "GET", "/service/login?state=s1", nil, headers)
		var res struct {
			URL string `json:"url"`
		}
		if rr.Code != 200 || json.NewDecoder(rr.Body).Decode(&res) != nil {
			t.Fatalf("login start: %d %s", rr.Code, rr.Body.String())
		}
		u, _ := url.Parse(res.URL)
		body, _ := json.Marshal(map[string]string{"state": u.Query().Get("state"), "email": "ada@example.com", "passphrase": "correct horse battery"})
		rr = testRequest(t, srv, "POST", "/service/ssh-auth", strings.NewReader(string(body)), nil)
		if rr.Code != 200 {
			t.Fatalf("ssh-auth: %d %s", rr.Code, rr.Body.String())
		}
		return rr, rr.Body.String()
	}

	rr, page := login(map[string]string{"Origin": "https://app.example", "X-App-Nonce": remote})
	if !strings.Contains(page, `"refresh_token":"`) {
		t.Error("off-server login entry carries no refresh_token")
	}
	if findRefreshCookie(rr) != nil {
		t.Error("off-server login also set the refresh cookie")
	}

	rr, page = login(map[string]string{"X-App-Nonce": srv.adminNonce}) // the panel, same-origin
	if strings.Contains(page, `"refresh_token"`) {
		t.Error("panel login leaked the refresh token into the page")
	}
	if findRefreshCookie(rr) == nil {
		t.Error("panel login set no refresh cookie")
	}
}
