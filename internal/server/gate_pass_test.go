package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// ── The gate pass ──
//
// A gated hosted app is walked through the login before any of it is
// served, and let in afterwards by the frbr_gate cookie. The api_key gate
// keeps the login to one form POST, with no provider in the way.

// gatedApp creates an app behind an api_key record and gives it a page.
func gatedApp(t *testing.T, srv *Server) (slug string, rec *db.AuthRecord) {
	t.Helper()
	rec = newAuthRecord(t, srv, "Gate key", db.AuthAPIKey, db.AuthDescriptor{Key: "sekret"})
	body := `{"name":"gated","protected_by":` + strconv.FormatInt(rec.ID, 10) + `}`
	rr := testRequest(t, srv, "POST", "/api/apps", strings.NewReader(body), nil)
	if rr.Code != 200 {
		t.Fatalf("create app: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]string
	json.Unmarshal(rr.Body.Bytes(), &res)
	cleanupSlotApp(t, srv, res["nonce"])
	createSlotFile(t, srv, res["nonce"], "web", "index.html", []byte("<h1>secret page</h1>"))
	createSlotFile(t, srv, res["nonce"], "web", "app.js", []byte("// secret code"))
	srv.rebuildHostedRoutes()
	return "gated", rec
}

// pageRequest makes a request the way a browser would, carrying cookies.
func pageRequest(srv *Server, path, mode string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Sec-Fetch-Mode", mode)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func gateCookie(rr *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == gatePassCookie {
			return c
		}
	}
	return nil
}

// signInByKey follows a gate redirect through the key form and returns the
// gate pass the login set.
func signInByKey(t *testing.T, srv *Server, loginURL string) *http.Cookie {
	t.Helper()
	u, err := url.Parse(loginURL)
	if err != nil || u.Path != "/service/apikey-auth" {
		t.Fatalf("login URL = %q, want the api key form", loginURL)
	}
	body := `{"state":"` + u.Query().Get("state") + `","api_key":"sekret"}`
	rr := testRequest(t, srv, "POST", "/service/apikey-auth", strings.NewReader(body), nil)
	if rr.Code != 200 {
		t.Fatalf("key form: %d %s", rr.Code, rr.Body.String())
	}
	c := gateCookie(rr)
	if c == nil || c.Value == "" {
		t.Fatal("login set no gate pass")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("gate pass = %+v, want HttpOnly, SameSite=Lax, Path=/", c)
	}
	return c
}

func TestGatePassWalksNavigationIntoLogin(t *testing.T) {
	srv := newTestServer(t)
	slug, _ := gatedApp(t, srv)

	rr := pageRequest(srv, "/"+slug+"/", "navigate", nil, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 into the login", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "/service/apikey-auth?state=") {
		t.Errorf("Location = %q, want the api key form", loc)
	}
	if strings.Contains(rr.Body.String(), "secret") {
		t.Error("gated page content leaked")
	}

	// A subresource is refused, not redirected.
	rr = pageRequest(srv, "/"+slug+"/app.js", "no-cors", nil, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("asset status = %d, want 401", rr.Code)
	}
}

func TestGatePassLetsSignedInBrowserThrough(t *testing.T) {
	srv := newTestServer(t)
	slug, _ := gatedApp(t, srv)

	rr := pageRequest(srv, "/"+slug+"/?x=1", "navigate", nil, nil)
	pass := signInByKey(t, srv, rr.Header().Get("Location"))

	for _, path := range []string{"/" + slug + "/", "/" + slug + "/app.js"} {
		rr = pageRequest(srv, path, "navigate", []*http.Cookie{pass}, nil)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), "secret") {
			t.Errorf("%s: status = %d, want 200 with content", path, rr.Code)
		}
	}
}

func TestGatePassReturnsToThePageAsked(t *testing.T) {
	srv := newTestServer(t)
	slug, _ := gatedApp(t, srv)

	rr := pageRequest(srv, "/"+slug+"/deep?x=1", "navigate", nil, nil)
	u, _ := url.Parse(rr.Header().Get("Location"))
	body := `{"state":"` + u.Query().Get("state") + `","api_key":"sekret"}`
	rr = testRequest(t, srv, "POST", "/service/apikey-auth", strings.NewReader(body), nil)
	if !strings.Contains(rr.Body.String(), `window.location.replace("/gated/deep?x=1")`) {
		t.Errorf("callback page does not return to the page asked:\n%s", rr.Body.String())
	}
}

func TestGatePassRejectsTampering(t *testing.T) {
	srv := newTestServer(t)
	slug, _ := gatedApp(t, srv)
	rr := pageRequest(srv, "/"+slug+"/", "navigate", nil, nil)
	pass := signInByKey(t, srv, rr.Header().Get("Location"))

	payload, sig, _ := strings.Cut(pass.Value, ".")
	forged := &http.Cookie{Name: gatePassCookie, Value: payload + "." + sig[:len(sig)-2] + "AA"}
	rr = pageRequest(srv, "/"+slug+"/", "navigate", []*http.Cookie{forged}, nil)
	if rr.Code != http.StatusFound {
		t.Errorf("forged pass: status = %d, want 302", rr.Code)
	}
}

func TestGatePassDoesNotCoverOtherGates(t *testing.T) {
	srv := newTestServer(t)
	slug, rec := gatedApp(t, srv)
	rr := pageRequest(srv, "/"+slug+"/", "navigate", nil, nil)
	pass := signInByKey(t, srv, rr.Header().Get("Location"))

	other := newAuthRecord(t, srv, "Other key", db.AuthAPIKey, db.AuthDescriptor{Key: "other"})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(pass)
	if _, ok := srv.gatePassUser(req, rec); !ok {
		t.Error("pass did not cover the record it cleared")
	}
	if _, ok := srv.gatePassUser(req, other); ok {
		t.Error("pass covered a record it never cleared")
	}
}

func TestGatePassDiesWithSignOut(t *testing.T) {
	srv := newTestServer(t)
	slug, rec := gatedApp(t, srv)
	rr := pageRequest(srv, "/"+slug+"/", "navigate", nil, nil)
	pass := signInByKey(t, srv, rr.Header().Get("Location"))

	req := httptest.NewRequest("POST", "/service/logout?auth_id="+strconv.FormatInt(rec.ID, 10), nil)
	req.AddCookie(pass)
	out := httptest.NewRecorder()
	srv.ServeHTTP(out, req)
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want 204", out.Code)
	}
	if c := gateCookie(out); c == nil || c.MaxAge >= 0 {
		t.Errorf("logout left the pass in place: %+v", c)
	}

	// The old cookie is dead too: its family was revoked.
	rr = pageRequest(srv, "/"+slug+"/", "navigate", []*http.Cookie{pass}, nil)
	if rr.Code != http.StatusFound {
		t.Errorf("after logout: status = %d, want 302", rr.Code)
	}
}

func TestGatePassBreaksLoginLoop(t *testing.T) {
	srv := newTestServer(t)
	slug, _ := gatedApp(t, srv)

	// Back from a login with no pass: the browser didn't keep the cookie.
	rr := pageRequest(srv, "/"+slug+"/", "navigate", nil, map[string]string{
		"Referer": "http://example.com/service/callback?code=x",
	})
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "Sign-in didn&#39;t stick") {
		t.Errorf("status = %d, want the cookie error page", rr.Code)
	}
}

func TestGatePassControlWantsARealUser(t *testing.T) {
	srv := newTestServer(t)
	slug, rec := gatedApp(t, srv)
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(rec.ID, 10)); err != nil {
		t.Fatal(err)
	}

	rr := pageRequest(srv, "/control", "navigate", nil, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("control: status = %d, want 302 into the login", rr.Code)
	}

	// A key proves no identity, so the pass it earns can't open the
	// control panel — the admin API would refuse it anyway.
	rr = pageRequest(srv, "/"+slug+"/", "navigate", nil, nil)
	pass := signInByKey(t, srv, rr.Header().Get("Location"))
	rr = pageRequest(srv, "/control", "navigate", []*http.Cookie{pass}, nil)
	if rr.Code != http.StatusForbidden {
		t.Errorf("control with an identity-free pass: status = %d, want 403", rr.Code)
	}
}

func TestGatePassOpenGateServes(t *testing.T) {
	srv := newTestServer(t)
	nonce := createApp(t, srv, "opengate")
	cleanupSlotApp(t, srv, nonce)
	createSlotFile(t, srv, nonce, "web", "index.html", []byte("<h1>hi</h1>"))
	srv.rebuildHostedRoutes()

	rr := pageRequest(srv, "/opengate/", "navigate", nil, nil)
	if rr.Code != 200 {
		t.Errorf("open gate: status = %d, want 200", rr.Code)
	}
}
