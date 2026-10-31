package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// ── Where apps are hosted ──
//
// An app's URL is a slug on this server ("blog" → /blog/) or a remote URL.
// A remote-URL app that has files is served here at that URL's host (its
// DNS pointed at this server), and only there.

func TestNormalizeAppURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                           "",
		"  ":                         "",
		"blog":                       "blog",
		"/blog":                      "blog",
		"/blog/":                     "blog",
		"Blog":                       "blog",
		"my-app2":                    "my-app2",
		"https://Blog.Example.org/":  "https://blog.example.org",
		"HTTPS://blog.example.org":   "https://blog.example.org",
		"http://localhost:5173/app/": "http://localhost:5173/app/",
	} {
		got, err := normalizeAppURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeAppURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for in, wantErr := range map[string]string{
		"/blog/admin":   "create a 'blog' app and publish to its 'admin' folder",
		"blog/admin/x":  "create a 'blog' app and publish to its 'admin/x' folder",
		"/api":          "reserved",
		"control":       "reserved",
		"/mcp/":         "reserved",
		"service":       "reserved",
		"oauth":         "reserved",
		"my app":        "slug",
		"blog@staging":  "slug",
		"ftp://x.org":   "http or https",
		"https://":      "not a valid URL",
		"my_app":        "slug",
		"/":             "slug",
		"-blog":         "slug",
		"blog.html":     "slug",
		"../etc/passwd": "subdirectories",
	} {
		_, err := normalizeAppURL(in)
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("normalizeAppURL(%q) error = %v, want one mentioning %q", in, err, wantErr)
		}
	}
}

func TestCreateAppStoresCleanURL(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "POST", "/api/apps", strings.NewReader(`{"name":"Blog","url":"/Blog/"}`), nil)
	if rr.Code != 200 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]string
	json.Unmarshal(rr.Body.Bytes(), &res)
	app, _ := srv.store.GetApp(res["nonce"])
	if app.URL != "blog" {
		t.Errorf("stored url = %q, want blog", app.URL)
	}

	rr = testRequest(t, srv, "POST", "/api/apps", strings.NewReader(`{"name":"Admin","url":"/blog/admin"}`), nil)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "Apps cannot be hosted at subdirectories") {
		t.Errorf("subdirectory: %d %s, want 400 with the subdirectory warning", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, "PUT", "/api/apps/"+res["nonce"], strings.NewReader(`{"name":"Blog","url":"control"}`), nil)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "reserved") {
		t.Errorf("reserved on update: %d %s, want 400", rr.Code, rr.Body.String())
	}
}

// An app named after a reserved path isn't mounted there by its name.
func TestReservedNameIsNotMounted(t *testing.T) {
	srv := newTestServer(t)
	nonce, _ := srv.store.CreateApp("API", "Development", "", nil, nil)
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("<h1>shadow</h1>"))
	srv.rebuildHostedRoutes()
	if _, ok := srv.hostedRoutes["api"]; ok {
		t.Error("an app named API was mounted at /api")
	}
}

// listHost puts name on the Hosts list, straight into the store.
func listHost(t *testing.T, srv *Server, name string) *db.Host {
	t.Helper()
	h := &db.Host{ID: "host-" + name, Name: name}
	if err := srv.store.CreateHost(h); err != nil {
		t.Fatal(err)
	}
	srv.rebuildHostedRoutes()
	return h
}

// hostedRemoteApp creates an app at https://blog.example.org with a page.
func hostedRemoteApp(t *testing.T, srv *Server, protectedBy *int64) string {
	t.Helper()
	listHost(t, srv, "blog.example.org")
	nonce, err := srv.store.CreateApp("Blog", "Development", "https://blog.example.org", nil, protectedBy)
	if err != nil {
		t.Fatal(err)
	}
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("<h1>blog home</h1>"))
	createSlotFile(t, srv, nonce, "web", "admin/index.html", []byte("<h1>blog admin</h1>"))
	srv.rebuildHostedRoutes()
	return nonce
}

func hostRequest(srv *Server, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func TestRemoteAppServedAtItsHost(t *testing.T) {
	srv := newTestServer(t)
	nonce := hostedRemoteApp(t, srv, nil)

	for path, want := range map[string]string{"/": "blog home", "/admin/": "blog admin", "/@dev/": "blog home"} {
		rr := hostRequest(srv, "GET", "https://blog.example.org"+path, nil)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), want) {
			t.Errorf("%s: %d %q, want 200 with %q", path, rr.Code, rr.Body.String(), want)
		}
	}
	// Not at a slug on the main host.
	if rr := hostRequest(srv, "GET", "/blog/", nil); rr.Code != 404 {
		t.Errorf("/blog/ on the main host: %d, want 404", rr.Code)
	}
	// An unrelated host still isn't answered.
	if rr := hostRequest(srv, "GET", "https://other.example.org/", nil); rr.Code != http.StatusMisdirectedRequest {
		t.Errorf("unknown host: %d, want 421", rr.Code)
	}

	// The panel, the admin API and the central MCP don't answer there.
	for _, path := range []string{"/control", "/control/", "/control/app.js", "/api/apps", "/api/me", "/mcp",
		"/oauth/authorize", "/oauth/register", "/.well-known/oauth-protected-resource/mcp"} {
		if rr := hostRequest(srv, "GET", "https://blog.example.org"+path, nil); rr.Code != 404 {
			t.Errorf("%s on the app host: %d, want 404", path, rr.Code)
		}
	}

	// What the app itself needs does.
	rr := hostRequest(srv, "GET", "https://blog.example.org/frbr.js", nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"appNonce":"`+nonce+`"`) {
		t.Errorf("frbr.js on the app host: %d, want the app's own nonce in its config", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"apiBase":"https://blog.example.org"`) {
		t.Error("frbr.js on the app host should talk to that host")
	}

	// The host speaks for its own app only — not another's, not the panel's.
	srv.adminNonce = "panelnonce"
	other, _ := srv.store.CreateApp("Other", "Development", "", nil, nil)
	for _, n := range []string{other, srv.adminNonce} {
		if rr := hostRequest(srv, "GET", "https://blog.example.org/env.js", map[string]string{"X-App-Nonce": n}); rr.Code != 403 {
			t.Errorf("nonce %q on the app host: %d, want 403", n, rr.Code)
		}
		if rr := hostRequest(srv, "POST", "https://blog.example.org/oauth/token/1?"+n, nil); rr.Code != 403 {
			t.Errorf("bare nonce %q on the app host: %d, want 403", n, rr.Code)
		}
	}
}

// A remote URL at a name the server already answers to (or at localhost)
// names the server, not the app: it isn't routed by host.
func TestRemoteURLAtServerOwnNameIsNotRouted(t *testing.T) {
	srv := newTestServer(t)
	for _, u := range []string{"http://localhost:5173", "http://127.0.0.1:3000"} {
		nonce, _ := srv.store.CreateApp("Dev "+u, "Development", u, nil, nil)
		cleanupSlotApp(t, srv, nonce)
		createSlotFile(t, srv, nonce, "web", "index.html", []byte("<h1>dev</h1>"))
	}
	srv.rebuildHostedRoutes()
	if len(srv.hostedHosts) != 0 {
		t.Errorf("hostedHosts = %v, want none", srv.hostedHosts)
	}
	if rr := hostRequest(srv, "GET", "http://localhost:9009/", nil); rr.Code != http.StatusFound {
		t.Errorf("panel root: %d, want the usual redirect", rr.Code)
	}
}

// A gate on an app's own host signs in at the server's main address, then
// hands the result back to the app's host: the gate pass and the session
// land there, never on the panel's host.
func TestRemoteAppGateHandsOffToItsHost(t *testing.T) {
	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "blog key", db.AuthAPIKey, db.AuthDescriptor{Key: "sekret"})
	hostedRemoteApp(t, srv, &rec.ID)

	rr := pageRequest(srv, "https://blog.example.org/admin/?x=1", "navigate", nil, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("gate: %d, want 302 into the login", rr.Code)
	}
	loginURL := rr.Header().Get("Location")
	if !strings.HasPrefix(loginURL, srv.config.PublicBaseURL+"/service/apikey-auth?state=") {
		t.Fatalf("login URL = %q, want the form at the main address", loginURL)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Error("the gate set cookies on the app host before any login")
	}

	u, _ := url.Parse(loginURL)
	headers := openForm(t, srv, loginURL)
	body := `{"state":"` + u.Query().Get("state") + `","api_key":"sekret"}`
	rr = testRequest(t, srv, "POST", "/service/apikey-auth", strings.NewReader(body), headers)
	if rr.Code != 200 {
		t.Fatalf("key form: %d %s", rr.Code, rr.Body.String())
	}
	if gateCookie(rr) != nil {
		t.Error("the main address got the app's gate pass")
	}
	var res map[string]string
	json.Unmarshal(rr.Body.Bytes(), &res)
	handoff := res["redirect"]
	if !strings.HasPrefix(handoff, "https://blog.example.org/service/handoff?ticket=") {
		t.Fatalf("redirect = %q, want the app host's handoff", handoff)
	}

	// Only the app's own host may collect the ticket.
	hu, _ := url.Parse(handoff)
	if rr := hostRequest(srv, "GET", "/service/handoff?"+hu.RawQuery, nil); rr.Code != 400 {
		t.Errorf("ticket on the main host: %d, want 400", rr.Code)
	}
	// The wrong-host attempt spent it, so sign in again for the real run.
	rr = pageRequest(srv, "https://blog.example.org/admin/?x=1", "navigate", nil, nil)
	loginURL = rr.Header().Get("Location")
	u, _ = url.Parse(loginURL)
	headers = openForm(t, srv, loginURL)
	body = `{"state":"` + u.Query().Get("state") + `","api_key":"sekret"}`
	rr = testRequest(t, srv, "POST", "/service/apikey-auth", strings.NewReader(body), headers)
	json.Unmarshal(rr.Body.Bytes(), &res)
	handoff = res["redirect"]

	rr = hostRequest(srv, "GET", handoff, nil)
	pass := gateCookie(rr)
	if rr.Code != 200 || pass == nil {
		t.Fatalf("handoff: %d, pass %v", rr.Code, pass)
	}
	if page := rr.Body.String(); !strings.Contains(page, "localStorage.setItem") || !strings.Contains(page, "/admin/?x=1&amp;frbr_signin=") && !strings.Contains(page, `/admin/?x=1&frbr_signin=`) {
		t.Errorf("handoff page should store the session and return to the page asked:\n%s", page)
	}
	if rr := hostRequest(srv, "GET", handoff, nil); rr.Code != 400 {
		t.Errorf("ticket reuse: %d, want 400", rr.Code)
	}

	rr = pageRequest(srv, "https://blog.example.org/admin/", "navigate", []*http.Cookie{pass}, nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "blog admin") {
		t.Errorf("with the pass: %d %q, want the page", rr.Code, rr.Body.String())
	}
}

func TestUploadReportsRemoteAppLocation(t *testing.T) {
	srv := newTestServer(t)
	nonce := hostedRemoteApp(t, srv, nil)
	rr := deploySlot(t, srv, nonce, `{"target":"staging"}`)
	var res map[string]string
	json.Unmarshal(rr.Body.Bytes(), &res)
	if res["route"] != "https://blog.example.org/@staging" {
		t.Errorf("route = %q (%d %s), want https://blog.example.org/@staging", res["route"], rr.Code, rr.Body.String())
	}
	if rr := hostRequest(srv, "GET", "https://blog.example.org/@staging/", nil); rr.Code != 200 {
		t.Errorf("staging slot at the app host: %d, want 200", rr.Code)
	}
}
