package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// envJSFor sends GET /env.js (or /frbr.js) with an explicit Host and headers,
// so nonceForLocation's same-origin / subdomain checks can be driven
// precisely. httptest.NewRequest defaults Host to example.com (a subdomain
// shape), which would trip the fail-closed rules — so every case sets Host.
func envJSFor(t *testing.T, srv *Server, path, host string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if host != "" {
		req.Host = host
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// parseEnvJS pulls the JSON object out of a `window.__HOMESLICE_CONFIG = {...};`
// response body. renderEnvJS emits the object via mustJSON, so the payload
// between `=` and `;` is plain JSON.
func parseEnvJS(t *testing.T, body string) map[string]any {
	t.Helper()
	body = strings.TrimSpace(body)
	left := strings.Index(body, "=")
	right := strings.LastIndex(body, ";")
	if left < 0 || right < 0 || right <= left {
		t.Fatalf("can't find = ... ; in env.js body: %q", body)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body[left+1:right]), &m); err != nil {
		t.Fatalf("parse env.js JSON: %v\nbody: %s", err, body)
	}
	return m
}

// makeHostedApp creates an app, writes a web slot file so appHasSlotDir is
// true, rebuilds the route map, and returns the app's nonce and slug.
// makeHostedApp creates an app, writes a web slot file so appHasSlotDir is
// true, rebuilds the route map, and returns the app's nonce and slug. It also
// mints a real admin nonce — newTestServer leaves it empty (the real New()
// sets one), and several assertions compare against it.
func makeHostedApp(t *testing.T, srv *Server, name string) (nonce, slug string) {
	t.Helper()
	srv.adminNonce = db.GenNonce()
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, name, "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "index.html", []byte("<h1>x</h1>"))
	srv.rebuildHostedRoutes()
	app, err := srv.store.GetApp(nonce)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	return nonce, appSlug(app)
}

// ── Referer-based resolution ──

func TestResolveNonceRefererHostedApp(t *testing.T) {
	srv := newTestServer(t)
	nonce, slug := makeHostedApp(t, srv, "myapp")
	rr := envJSFor(t, srv, "/env.js", "localhost:9009", map[string]string{
		"Referer": "http://localhost:9009/" + slug + "/",
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	cfg := parseEnvJS(t, rr.Body.String())
	if cfg["appNonce"] != nonce {
		t.Fatalf("appNonce = %v, want %q (resolved from Referer /%s/)", cfg["appNonce"], nonce, slug)
	}
	if cfg["authRequired"] != false {
		t.Fatalf("authRequired = %v, want false (ungated app)", cfg["authRequired"])
	}
}

func TestResolveNonceRefererControlIsAdmin(t *testing.T) {
	srv := newTestServer(t)
	srv.adminNonce = db.GenNonce()
	rr := envJSFor(t, srv, "/env.js", "localhost:9009", map[string]string{
		"Referer": "http://localhost:9009/control",
	})
	cfg := parseEnvJS(t, rr.Body.String())
	if cfg["appNonce"] != srv.adminNonce {
		t.Fatalf("appNonce = %v, want admin nonce %q (Referer /control)", cfg["appNonce"], srv.adminNonce)
	}
}

// ── ?loc= programmatic fetch ──

func TestResolveNonceLocHostedAppJSON(t *testing.T) {
	srv := newTestServer(t)
	nonce, slug := makeHostedApp(t, srv, "locapp")
	rr := envJSFor(t, srv, "/env.js?loc=/"+slug+"/", "localhost:9009", nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json for ?loc= fetch", ct)
	}
	var cfg map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("parse JSON: %v\nbody: %s", err, rr.Body.String())
	}
	if cfg["appNonce"] != nonce {
		t.Fatalf("appNonce = %v, want %q (?loc= resolved)", cfg["appNonce"], nonce)
	}
}

// ?loc= is client-supplied and untrusted — it must NOT claim the admin door.
func TestResolveNonceLocCannotClaimAdmin(t *testing.T) {
	srv := newTestServer(t)
	srv.adminNonce = db.GenNonce()
	rr := envJSFor(t, srv, "/env.js?loc=/control/", "localhost:9009", nil)
	var cfg map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if cfg["appNonce"] == srv.adminNonce {
		t.Fatalf("appNonce = admin nonce; ?loc=/control/ must NOT resolve to admin (untrusted)")
	}
	if cfg["appNonce"] != "" {
		t.Fatalf("appNonce = %v, want empty for ?loc=/control/", cfg["appNonce"])
	}
}

// ── Fail-closed cases ──

func TestResolveNonceUnknownRefererFailsClosed(t *testing.T) {
	srv := newTestServer(t)
	makeHostedApp(t, srv, "realapp")
	rr := envJSFor(t, srv, "/env.js", "localhost:9009", map[string]string{
		"Referer": "http://localhost:9009/no-such-app/",
	})
	cfg := parseEnvJS(t, rr.Body.String())
	if cfg["appNonce"] != "" {
		t.Fatalf("appNonce = %v, want empty for unknown location (fail closed)", cfg["appNonce"])
	}
	if cfg["authRequired"] != false {
		t.Fatalf("authRequired = %v, want false when no gate resolved", cfg["authRequired"])
	}
}

// Subdomain-shaped host: FRBR-15 territory, fail closed until then.
func TestResolveNonceSubdomainFailsClosed(t *testing.T) {
	srv := newTestServer(t)
	nonce, slug := makeHostedApp(t, srv, "subapp")
	_ = nonce
	rr := envJSFor(t, srv, "/env.js", "blog.example.com", map[string]string{
		"Referer": "https://blog.example.com/" + slug + "/",
	})
	cfg := parseEnvJS(t, rr.Body.String())
	if cfg["appNonce"] != "" {
		t.Fatalf("appNonce = %v, want empty for subdomain host (FRBR-15 territory)", cfg["appNonce"])
	}
}

// Cross-origin Referer: not ours to resolve.
func TestResolveNonceCrossOriginRefererFailsClosed(t *testing.T) {
	srv := newTestServer(t)
	makeHostedApp(t, srv, "xoapp")
	rr := envJSFor(t, srv, "/env.js", "localhost:9009", map[string]string{
		"Referer": "https://evil.example.com/myapp/",
	})
	cfg := parseEnvJS(t, rr.Body.String())
	if cfg["appNonce"] != "" {
		t.Fatalf("appNonce = %v, want empty for cross-origin Referer", cfg["appNonce"])
	}
}

// ── Back-compat / override ──

func TestResolveNonceHeaderOverride(t *testing.T) {
	srv := newTestServer(t)
	nonce, _ := makeHostedApp(t, srv, "hdrapp")
	rr := envJSFor(t, srv, "/env.js", "localhost:9009", map[string]string{
		"X-App-Nonce": nonce,
	})
	cfg := parseEnvJS(t, rr.Body.String())
	if cfg["appNonce"] != nonce {
		t.Fatalf("appNonce = %v, want %q (header override)", cfg["appNonce"], nonce)
	}
}

func TestResolveNonceBareQueryBackcompat(t *testing.T) {
	srv := newTestServer(t)
	nonce, _ := makeHostedApp(t, srv, "bareapp")
	rr := envJSFor(t, srv, "/env.js?"+nonce, "localhost:9009", nil)
	cfg := parseEnvJS(t, rr.Body.String())
	if cfg["appNonce"] != nonce {
		t.Fatalf("appNonce = %v, want %q (bare query back-compat)", cfg["appNonce"], nonce)
	}
}

// No nonce anywhere and no Referer: fail closed, never admin.
func TestResolveNonceNoSignalFailsClosed(t *testing.T) {
	srv := newTestServer(t)
	srv.adminNonce = db.GenNonce()
	rr := envJSFor(t, srv, "/env.js", "localhost:9009", nil)
	cfg := parseEnvJS(t, rr.Body.String())
	if cfg["appNonce"] != "" {
		t.Fatalf("appNonce = %v, want empty (no signal)", cfg["appNonce"])
	}
	if cfg["appNonce"] == srv.adminNonce {
		t.Fatal("admin nonce leaked as the default — the old fallback is back?")
	}
}
