package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// ── Where apps are hosted ──
//
// An app's URL is a slug on this server ("blog" → /blog/) or a URL one
// level down on a listed host (https://blog.example.org/notes). A host app
// that has files is served there (the host's DNS pointed at this server),
// and only there.

func TestNormalizeAppURL(t *testing.T) {
	srv := newTestServer(t)
	listHost(t, srv, "blog.example.org")
	for in, want := range map[string]string{
		"":                                     "",
		"  ":                                   "",
		"blog":                                 "blog",
		"/blog":                                "blog",
		"/blog/":                               "blog",
		"Blog":                                 "blog",
		"my-app2":                              "my-app2",
		"https://Blog.Example.org/Notes/":      "https://blog.example.org/notes",
		"HTTPS://blog.example.org/notes?x=1#y": "https://blog.example.org/notes",
		"https://blog.example.org:8443/notes":  "https://blog.example.org:8443/notes",
		"http://localhost:5173/app/":           "http://localhost:5173/app/",
		"http://127.0.0.1:3000/":               "http://127.0.0.1:3000",
		"https://MyApp.netlify.app/":           "https://myapp.netlify.app",
		"https://elsewhere.example/a/b":        "https://elsewhere.example/a/b",
	} {
		got, err := srv.normalizeAppURL(in, "")
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

		"https://blog.example.org":          "not at its root",
		"https://blog.example.org/":         "not at its root",
		"https://blog.example.org/a/b":      "subdirectories",
		"https://blog.example.org/api":      "reserved",
		"https://blog.example.org/my_notes": "slug",
	} {
		_, err := srv.normalizeAppURL(in, "")
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

// hostedRemoteApp lists blog.example.org and puts an app with pages at
// https://blog.example.org/notes.
func hostedRemoteApp(t *testing.T, srv *Server, protectedBy *int64) string {
	t.Helper()
	listHost(t, srv, "blog.example.org")
	return hostApp(t, srv, "Notes", "https://blog.example.org/notes", protectedBy)
}

// hostApp creates an app at url with a home and an admin page.
func hostApp(t *testing.T, srv *Server, name, url string, protectedBy *int64) string {
	t.Helper()
	nonce, err := srv.store.CreateApp(name, "Development", url, nil, protectedBy)
	if err != nil {
		t.Fatal(err)
	}
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("<h1>"+name+" home</h1>"))
	createSlotFile(t, srv, nonce, "web", "admin/index.html", []byte("<h1>"+name+" admin</h1>"))
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

	for path, want := range map[string]string{"/notes/": "Notes home", "/notes/admin/": "Notes admin", "/notes@dev/": "Notes home"} {
		rr := hostRequest(srv, "GET", "https://blog.example.org"+path, nil)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), want) {
			t.Errorf("%s: %d %q, want 200 with %q", path, rr.Code, rr.Body.String(), want)
		}
	}
	// Nothing at the root without a default app, and the old /@slot form
	// is gone.
	for _, path := range []string{"/", "/@dev/", "/admin/"} {
		if rr := hostRequest(srv, "GET", "https://blog.example.org"+path, nil); rr.Code != 404 {
			t.Errorf("%s on the host: %d, want 404", path, rr.Code)
		}
	}
	// Not at a slug on the main host.
	if rr := hostRequest(srv, "GET", "/notes/", nil); rr.Code != 404 {
		t.Errorf("/notes/ on the main host: %d, want 404", rr.Code)
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

	// What the app itself needs does, finding the app by its page.
	rr := hostRequest(srv, "GET", "https://blog.example.org/frbr.js", map[string]string{"Referer": "https://blog.example.org/notes/admin/"})
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"appNonce":"`+nonce+`"`) {
		t.Errorf("frbr.js on the app host: %d, want the app's own nonce in its config", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"apiBase":"https://blog.example.org"`) {
		t.Error("frbr.js on the app host should talk to that host")
	}

	// The host speaks for its own apps only — not another's, not the panel's.
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

	rr := pageRequest(srv, "https://blog.example.org/notes/admin/?x=1", "navigate", nil, nil)
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
	rr = pageRequest(srv, "https://blog.example.org/notes/admin/?x=1", "navigate", nil, nil)
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
	if page := rr.Body.String(); !strings.Contains(page, "localStorage.setItem") || !strings.Contains(page, "/notes/admin/?x=1&amp;frbr_signin=") && !strings.Contains(page, `/notes/admin/?x=1&frbr_signin=`) {
		t.Errorf("handoff page should store the session and return to the page asked:\n%s", page)
	}
	if rr := hostRequest(srv, "GET", handoff, nil); rr.Code != 400 {
		t.Errorf("ticket reuse: %d, want 400", rr.Code)
	}

	rr = pageRequest(srv, "https://blog.example.org/notes/admin/", "navigate", []*http.Cookie{pass}, nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Notes admin") {
		t.Errorf("with the pass: %d %q, want the page", rr.Code, rr.Body.String())
	}
}

func TestUploadReportsRemoteAppLocation(t *testing.T) {
	srv := newTestServer(t)
	nonce := hostedRemoteApp(t, srv, nil)
	rr := deploySlot(t, srv, nonce, `{"target":"staging"}`)
	var res map[string]string
	json.Unmarshal(rr.Body.Bytes(), &res)
	if res["route"] != "https://blog.example.org/notes@staging" {
		t.Errorf("route = %q (%d %s), want https://blog.example.org/notes@staging", res["route"], rr.Code, rr.Body.String())
	}
	if rr := hostRequest(srv, "GET", "https://blog.example.org/notes@staging/", nil); rr.Code != 200 {
		t.Errorf("staging slot at the app host: %d, want 200", rr.Code)
	}
}

// Apps on one host are neighbours: each served at its slug, each named
// freely there (they share an origin on purpose), each found by its page.
// The root goes to the default app; an app at the root itself isn't
// routed at all.
func TestHostServesSiblingApps(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	h := listHost(t, srv, "blog.example.org")
	notes := hostApp(t, srv, "Notes", "https://blog.example.org/notes", nil)
	shop := hostApp(t, srv, "Shop", "https://blog.example.org/shop", nil)
	hostApp(t, srv, "Rooted", "https://blog.example.org", nil)

	for path, want := range map[string]string{"/notes/": "Notes home", "/shop/": "Shop home", "/shop/admin/": "Shop admin"} {
		if rr := hostRequest(srv, "GET", "https://blog.example.org"+path, nil); rr.Code != 200 || !strings.Contains(rr.Body.String(), want) {
			t.Errorf("%s: %d, want %q", path, rr.Code, want)
		}
	}
	if rr := hostRequest(srv, "GET", "https://blog.example.org/", nil); rr.Code != 404 || strings.Contains(rr.Body.String(), "Rooted") {
		t.Errorf("root with an app saved there: %d %q, want a plain 404", rr.Code, rr.Body.String())
	}

	for page, want := range map[string]string{"https://blog.example.org/notes/": notes, "https://blog.example.org/shop@dev/": shop} {
		rr := hostRequest(srv, "GET", "https://blog.example.org/frbr.js", map[string]string{"Referer": page})
		if !strings.Contains(rr.Body.String(), `"appNonce":"`+want+`"`) {
			t.Errorf("frbr.js from %s doesn't name its app", page)
		}
	}
	if rr := hostRequest(srv, "GET", "https://blog.example.org/env.js", map[string]string{"X-App-Nonce": shop}); rr.Code != 200 {
		t.Errorf("a sibling's nonce on the host: %d, want 200", rr.Code)
	}

	rr := requestAs(t, srv, admin, "PUT", "/api/hosts/"+h.ID, fmt.Sprintf(`{"name":"blog.example.org","owner_id":%d,"default_app":"%s"}`, admin.ID, shop))
	if rr.Code != 200 {
		t.Fatalf("set default app: %d %s", rr.Code, rr.Body.String())
	}
	rr = hostRequest(srv, "GET", "https://blog.example.org/?ref=x", nil)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/shop/?ref=x" {
		t.Errorf("root with a default app: %d to %q, want 302 to /shop/?ref=x", rr.Code, rr.Header().Get("Location"))
	}
}

// Only an app mounted on a host can be its default.
func TestHostDefaultAppMustBeOnIt(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	h := listHost(t, srv, "blog.example.org")
	rooted := hostApp(t, srv, "Rooted", "https://blog.example.org", nil)
	here := hostApp(t, srv, "Here", "here", nil)
	for _, nonce := range []string{rooted, here} {
		rr := requestAs(t, srv, admin, "PUT", "/api/hosts/"+h.ID, fmt.Sprintf(`{"name":"blog.example.org","owner_id":%d,"default_app":"%s"}`, admin.ID, nonce))
		if rr.Code != 400 {
			t.Errorf("default app %s: %d, want 400", nonce, rr.Code)
		}
	}
}

// Two apps can't take the same place on a host; moving to a free one, or
// saving an app where it already is, is fine.
func TestHostSlugIsOneAppsAlone(t *testing.T) {
	srv := newTestServer(t)
	listHost(t, srv, "blog.example.org")
	notes := hostApp(t, srv, "Notes", "https://blog.example.org/notes", nil)
	rr := testRequest(t, srv, "POST", "/api/apps", strings.NewReader(`{"name":"Copy","url":"https://BLOG.example.org/notes/"}`), nil)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "Notes") {
		t.Errorf("same place: %d %s, want 409 naming Notes", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, "PUT", "/api/apps/"+notes, strings.NewReader(`{"name":"Notes","url":"https://blog.example.org/notes"}`), nil)
	if rr.Code/100 != 2 {
		t.Errorf("re-save in place: %d %s", rr.Code, rr.Body.String())
	}
}

// An app on a host that inherits its gate gets the host's; its own gate
// still wins; with no host gate it falls through to admin auth.
func TestHostGateIsInherited(t *testing.T) {
	srv := newTestServer(t)
	hostKey := newAuthRecord(t, srv, "host key", db.AuthAPIKey, db.AuthDescriptor{Key: "h"})
	ownKey := newAuthRecord(t, srv, "own key", db.AuthAPIKey, db.AuthDescriptor{Key: "o"})
	h := listHost(t, srv, "blog.example.org")
	inherits := hostApp(t, srv, "Notes", "https://blog.example.org/notes", nil)
	own := hostApp(t, srv, "Shop", "https://blog.example.org/shop", &ownKey.ID)

	gateOf := func(nonce string) string {
		app, _ := srv.store.GetApp(nonce)
		rec, err := srv.resolveAppGate(app)
		if err != nil || rec == nil {
			return ""
		}
		return rec.Name
	}
	admin, _ := srv.adminAuthRecord()
	adminName := ""
	if admin != nil {
		adminName = admin.Name
	}
	if got := gateOf(inherits); got != adminName {
		t.Errorf("no host gate: %q, want admin auth %q", got, adminName)
	}

	h.ProtectedBy = &hostKey.ID
	if err := srv.store.UpdateHost(h); err != nil {
		t.Fatal(err)
	}
	srv.rebuildHostedRoutes()
	if got := gateOf(inherits); got != "host key" {
		t.Errorf("inheriting app: %q, want host key", got)
	}
	if got := gateOf(own); got != "own key" {
		t.Errorf("app with its own gate: %q, want own key", got)
	}
	// The host gate guards the page itself.
	if rr := pageRequest(srv, "https://blog.example.org/notes/", "navigate", nil, nil); rr.Code != http.StatusFound {
		t.Errorf("page behind the host gate: %d, want 302 into the login", rr.Code)
	}
}
