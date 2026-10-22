package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Origin: null is also what any sandboxed frame sends, so it may read a
// cookie-bearing response only for an app registered with a file: URL.
func TestNullOriginCookiesOnlyForFileApps(t *testing.T) {
	srv := newTestServer(t)
	hosted, _ := srv.store.CreateApp("Hosted", "Development", "", nil, nil)
	local, _ := srv.store.CreateApp("Local", "Development", "file:///home/me/app/index.html", nil, nil)

	for nonce, wantCookies := range map[string]bool{hosted: false, local: true, "": false} {
		rr := testRequest(t, srv, "GET", "/env.js", nil, map[string]string{"Origin": "null", "X-App-Nonce": nonce})
		if rr.Code != 200 {
			t.Fatalf("nonce %q: status %d", nonce, rr.Code)
		}
		if got := rr.Header().Get("Access-Control-Allow-Credentials") == "true"; got != wantCookies {
			t.Errorf("nonce %q: allow credentials = %v, want %v", nonce, got, wantCookies)
		}
	}

	// The app_nonce query parameter names the app as well as the header does.
	rr := testRequest(t, srv, "GET", "/env.js?app_nonce="+local, nil, map[string]string{"Origin": "null"})
	if rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("app_nonce query did not identify the file: app")
	}
}

// A finished browser login is posted only to the origin that may hold it.
func TestLoginPostedToAppOrigin(t *testing.T) {
	srv := newTestServer(t)
	hosted, _ := srv.store.CreateApp("Hosted", "Development", "", nil, nil)
	remote, _ := srv.store.CreateApp("Remote", "Development", "https://app.example/start", nil, nil)
	local, _ := srv.store.CreateApp("Local", "Development", "file:///home/me/app/index.html", nil, nil)

	r := httptest.NewRequest("GET", "/service/login", nil) // Host: example.com
	for nonce, want := range map[string]string{hosted: "http://example.com", remote: "https://app.example", local: "*"} {
		app, _ := srv.store.GetApp(nonce)
		if got := loginOpenerOrigin(r, app); got != want {
			t.Errorf("app %s: opener origin = %q, want %q", app.Name, got, want)
		}
	}

	var page strings.Builder
	writeCallbackPage(&page, &pendingAuth{appState: "s", opener: "https://app.example"}, map[string]interface{}{"v": 1})
	if !strings.Contains(page.String(), `}, "https://app.example");`) {
		t.Errorf("callback page does not target the app origin:\n%s", page.String())
	}
}

// A request naming a host the operator never configured is refused before
// anything runs — a page can point its own DNS name at a local install.
func TestUnknownHostRefused(t *testing.T) {
	srv := newTestServer(t)
	for host, want := range map[string]int{
		"rebind.attacker.test": 421,
		"localhost:9009":       200,
		"127.0.0.1:9009":       200,
		"[::1]:9009":           200,
		"192.168.1.20":         200,
		"example.com":          200, // FRBR_ALLOWED_HOSTS in the test server
	} {
		req := httptest.NewRequest("GET", "/env.js", nil)
		req.Host = host
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Errorf("Host %s: status %d, want %d", host, rr.Code, want)
		}
	}
}

// Fresh Breath's own pages refuse framing; hosted apps are left to decide.
func TestServerPagesRefuseFraming(t *testing.T) {
	srv := newTestServer(t)
	for path, framed := range map[string]bool{
		"/control":                  false,
		"/service/ssh-auth?state=x": false,
		"/api/me":                   false,
		"/some-hosted-app/":         true,
	} {
		rr := testRequest(t, srv, "GET", path, nil, nil)
		if got := rr.Header().Get("X-Frame-Options") == ""; got != framed {
			t.Errorf("%s: frameable = %v, want %v", path, got, framed)
		}
		if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", path)
		}
	}
}
