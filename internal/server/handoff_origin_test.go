package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Cross-origin is only for apps that run elsewhere, named by nonce — the
// nonce is their credential. Hosted apps never cross an origin (their pages
// are on this one), and "null" — which every sandboxed frame on the web
// also sends — is refused outright without a nonce, so no unnamed frame
// can read anything, env.js's ?loc= resolution included.
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

	// Origin: null, with and without an app named.
	for nonce, want := range map[string]int{local: 200, remote: 200, hosted: 403, "": 403} {
		rr := testRequest(t, srv, "GET", "/env.js", nil, map[string]string{"Origin": "null", "X-App-Nonce": nonce})
		if rr.Code != want {
			t.Errorf("nonce %q: status %d, want %d", nonce, rr.Code, want)
		}
		if want == 200 && rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Errorf("nonce %q: allow credentials missing", nonce)
		}
	}

	// A foreign origin naming an app that runs elsewhere is allowed; one
	// naming a hosted app (or nothing) is not.
	for nonce, want := range map[string]int{remote: 200, local: 200, hosted: 403, "": 403} {
		rr := testRequest(t, srv, "GET", "/env.js", nil, map[string]string{"Origin": "https://evil.example", "X-App-Nonce": nonce})
		if rr.Code != want {
			t.Errorf("foreign origin, nonce %q: status %d, want %d", nonce, rr.Code, want)
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
	rr = testRequest(t, srv, "GET", "/frbr.js?"+remote, nil, map[string]string{"Origin": "null"})
	if rr.Code != 200 {
		t.Fatalf("frbr.js?NONCE from null: status %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"appNonce":"`+remote+`"`) {
		t.Error("frbr.js config does not echo the nonce it was asked by")
	}
	if rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("frbr.js?NONCE from null: allow credentials missing")
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
	// both run off the server.
	local, _ := srv.store.CreateApp("Local", "Development", "", nil, nil)
	remote, _ := srv.store.CreateApp("Remote", "Development", "https://app.example/start", nil, nil)

	r := httptest.NewRequest("GET", "/service/login", nil) // Host: example.com
	for nonce, want := range map[string]string{
		hosted: "http://example.com", local: "*", remote: "*", "": "http://example.com",
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
