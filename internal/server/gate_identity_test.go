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
	"poggers.institute/freshbreath/internal/sshkit"
)

// ── Gate identity through real doors ────────────────────────────────
//
// A login's identity comes from its door's gate: frbr:<id> only when that
// gate is the admin auth record (or an SSH passphrase), ext: otherwise —
// even when the provider reports a Fresh Breath user's email. These drive
// the app door, the MCP service door and the passphrase form end to end
// against a GitHub-shaped provider whose user shares a Fresh Breath email.

const knownEmail = "known@example.com"

// fakeGitHubIdP is a GitHub-shaped OAuth2 provider: no id_token, a /user
// with a numeric id and the known email.
func fakeGitHubIdP(t *testing.T, srv *Server) *db.AuthRecord {
	t.Helper()
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			w.Write([]byte(`{"access_token":"gh-token","token_type":"bearer"}`))
		case "/user":
			w.Write([]byte(`{"id":4242,"login":"octo","email":"` + knownEmail + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.Close)
	return newAuthRecord(t, srv, "GitHub", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: idp.URL + "/authorize",
		TokenURL:     idp.URL + "/token",
		UserInfoURL:  idp.URL + "/user",
		ClientID:     "gh-client",
		ClientSecret: "gh-secret",
		Provider:     "github",
	})
}

func makeAdminAuth(t *testing.T, srv *Server, rec *db.AuthRecord) {
	t.Helper()
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(rec.ID, 10)); err != nil {
		t.Fatalf("set admin_auth_service: %v", err)
	}
}

func knownUser(t *testing.T, srv *Server) *db.User {
	t.Helper()
	u, err := srv.store.CreateUser("Known", knownEmail, "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// appBehind creates an app behind protectedBy (nil inherits the admin gate).
func appBehind(t *testing.T, srv *Server, name string, protectedBy *int64) string {
	t.Helper()
	nonce, err := srv.store.CreateApp(name, "Development", "", nil, protectedBy)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	return nonce
}

// appLoginStart begins a login at an app's door and returns the URL of its
// first leg.
func appLoginStart(t *testing.T, srv *Server, nonce string) string {
	t.Helper()
	rr := testRequest(t, srv, "GET", "/service/login?state=app-state", nil, map[string]string{"X-App-Nonce": nonce})
	if rr.Code != 200 {
		t.Fatalf("login status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var login struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	json.Unmarshal(rr.Body.Bytes(), &login)
	if login.Type != "redirect" {
		t.Fatalf("login = %s, want a redirect", rr.Body.String())
	}
	return login.URL
}

// beginInBrowser plays the popup's first stop: it opens a login's
// /service/begin URL and returns the provider URL it forwards to, plus the
// Cookie header that binds the flow to this "browser".
func beginInBrowser(t *testing.T, srv *Server, beginURL string) (*url.URL, map[string]string) {
	t.Helper()
	u, _ := url.Parse(beginURL)
	if u.Path != "/service/begin" {
		t.Fatalf("login url = %s, want /service/begin", beginURL)
	}
	rr := testRequest(t, srv, "GET", u.RequestURI(), nil, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("begin status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var cookies []string
	for _, c := range rr.Result().Cookies() {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	leg, _ := url.Parse(rr.Header().Get("Location"))
	return leg, map[string]string{"Cookie": strings.Join(cookies, "; ")}
}

// providerURL follows a login's /service/begin hop to the provider URL
// the popup ends up at.
func providerURL(t *testing.T, srv *Server, beginURL string) string {
	t.Helper()
	leg, _ := beginInBrowser(t, srv, beginURL)
	return leg.String()
}

// appOAuthLogin runs an app door login through its OAuth gate and returns
// the minted subject.
func appOAuthLogin(t *testing.T, srv *Server, nonce string) string {
	t.Helper()
	leg, browser := beginInBrowser(t, srv, appLoginStart(t, srv, nonce))
	rr := testRequest(t, srv, "GET", "/service/callback?code=c1&state="+leg.Query().Get("state"), nil, browser)
	if rr.Code != 200 {
		t.Fatalf("callback status = %d, body = %s", rr.Code, rr.Body.String())
	}
	sub, _ := callbackEntry(t, rr.Body.String())["subject"].(string)
	return sub
}

// mcpOAuthLogin runs an MCP client's authorization against a service door
// through its OAuth gate and returns the minted token's subject.
func mcpOAuthLogin(t *testing.T, srv *Server, resource string) string {
	t.Helper()
	clientID, secret := registerOAuthClient(t, srv, "https://client.example/callback")
	verifier, challenge := pkcePair()
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"https://client.example/callback"},
		"state":                 {"mcp-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}
	rr := testRequest(t, srv, "GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	if rr.Code != 200 {
		t.Fatalf("authorize status = %d, body = %s", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, "POST", "/oauth/authorize",
		strings.NewReader(`{"state":"`+interstitialState(t, rr.Body.String())+`","tokens":[]}`), nil)
	var cont map[string]string
	json.Unmarshal(rr.Body.Bytes(), &cont)
	leg, _ := url.Parse(cont["redirect"])

	rr = testRequest(t, srv, "GET", "/service/callback?code=c1&state="+leg.Query().Get("state"), nil, nil)
	back, _ := url.Parse(rr.Header().Get("Location"))
	code := back.Query().Get("code")
	if rr.Code != http.StatusFound || code == "" {
		t.Fatalf("callback status = %d, location = %q, body = %s", rr.Code, rr.Header().Get("Location"), rr.Body.String())
	}

	rr = postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"client_secret": {secret},
	})
	at, _ := decodeMap(rr)["access_token"].(string)
	claims, err := srv.verifyFreshbreathToken(at)
	if err != nil || claims == nil {
		t.Fatalf("token status = %d, verify: %v, body = %s", rr.Code, err, rr.Body.String())
	}
	return claims.Subject
}

func TestAppDoorNonAdminGateMatchingEmailYieldsExt(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	gh := fakeGitHubIdP(t, srv)
	knownUser(t, srv)
	nonce := appBehind(t, srv, "gh-app", &gh.ID)

	if sub := appOAuthLogin(t, srv, nonce); sub != "ext:github:4242" {
		t.Errorf("subject = %q, want ext:github:4242 (a non-admin gate never elevates)", sub)
	}
}

func TestAppDoorAdminGateMatchingEmailYieldsFrbr(t *testing.T) {
	srv := newTestServer(t)
	gh := fakeGitHubIdP(t, srv)
	makeAdminAuth(t, srv, gh)
	u := knownUser(t, srv)
	nonce := appBehind(t, srv, "gh-app", &gh.ID)

	if sub := appOAuthLogin(t, srv, nonce); sub != subjectForUser(u) {
		t.Errorf("subject = %q, want %s", sub, subjectForUser(u))
	}
}

func TestAppDoorEmptyGateInheritsAdminYieldsFrbr(t *testing.T) {
	srv := newTestServer(t)
	gh := fakeGitHubIdP(t, srv)
	makeAdminAuth(t, srv, gh)
	u := knownUser(t, srv)
	nonce := appBehind(t, srv, "gh-app", nil)

	if sub := appOAuthLogin(t, srv, nonce); sub != subjectForUser(u) {
		t.Errorf("subject = %q, want %s (empty protected_by inherits the admin gate)", sub, subjectForUser(u))
	}
}

func TestServiceDoorNonAdminGateMatchingEmailYieldsExt(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	gh := fakeGitHubIdP(t, srv)
	knownUser(t, srv)
	if _, err := srv.store.RegisterService("gh", "/mcp/gh", db.ServiceDescriptor{Type: "virtual"}, &gh.ID, nil); err != nil {
		t.Fatalf("register service: %v", err)
	}

	if sub := mcpOAuthLogin(t, srv, "/mcp/gh"); sub != "ext:github:4242" {
		t.Errorf("subject = %q, want ext:github:4242 (a non-admin gate never elevates)", sub)
	}
}

func TestServiceDoorEmptyGateInheritsAdminYieldsFrbr(t *testing.T) {
	srv := newTestServer(t)
	gh := fakeGitHubIdP(t, srv)
	makeAdminAuth(t, srv, gh)
	u := knownUser(t, srv)
	if _, err := srv.store.RegisterService("gh", "/mcp/gh", db.ServiceDescriptor{Type: "virtual"}, nil, nil); err != nil {
		t.Fatalf("register service: %v", err)
	}

	if sub := mcpOAuthLogin(t, srv, "/mcp/gh"); sub != subjectForUser(u) {
		t.Errorf("subject = %q, want %s (empty protected_by inherits the admin gate)", sub, subjectForUser(u))
	}
}

// A passphrase proves the user directly, so it mints frbr even on a record
// that isn't the admin gate.
func TestAppDoorSSHGateOffAdminYieldsFrbr(t *testing.T) {
	srv := newTestServer(t)
	setAdminAuth(t, srv)
	u := knownUser(t, srv)
	key, err := sshkit.GenerateSSHKey("correct horse")
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := srv.store.UpdateUser(u.ID, u.Name, u.Email, u.Role, u.Status, &db.UserMetadata{SSHKey: key}); err != nil {
		t.Fatalf("store key: %v", err)
	}
	sshRec := builtinAuth(t, srv, db.AuthSSHKey)
	nonce := appBehind(t, srv, "ssh-app", &sshRec.ID)

	leg, _ := url.Parse(appLoginStart(t, srv, nonce))
	body := `{"state":"` + leg.Query().Get("state") + `","email":"` + knownEmail + `","passphrase":"correct horse"}`
	rr := testRequest(t, srv, "POST", "/service/ssh-auth", strings.NewReader(body), nil)
	if rr.Code != 200 {
		t.Fatalf("ssh-auth status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if sub, _ := callbackEntry(t, rr.Body.String())["subject"].(string); sub != subjectForUser(u) {
		t.Errorf("subject = %q, want %s", sub, subjectForUser(u))
	}
}

// The control panel's frbr: grant survives signing in at an app whose gate
// proves a different (ext:) subject — the two gates hold the pass together.
func TestControlPassSurvivesOtherGateSignIn(t *testing.T) {
	srv := newTestServer(t)
	slug, _ := gatedAppNamed(t, srv, "keyed", "keyed") // before the admin gate ends setup mode
	sshRec := builtinAuth(t, srv, db.AuthSSHKey)
	makeAdminAuth(t, srv, sshRec)
	u := knownUser(t, srv)
	key, err := sshkit.GenerateSSHKey("correct horse")
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := srv.store.UpdateUser(u.ID, u.Name, u.Email, u.Role, u.Status, &db.UserMetadata{SSHKey: key}); err != nil {
		t.Fatalf("store key: %v", err)
	}

	// Into the control panel by passphrase.
	rr := pageRequest(srv, "/control", "navigate", nil, nil)
	leg, _ := url.Parse(rr.Header().Get("Location"))
	if leg.Path != "/service/ssh-auth" {
		t.Fatalf("control: redirect = %q, want the passphrase form", rr.Header().Get("Location"))
	}
	body := `{"state":"` + leg.Query().Get("state") + `","email":"` + knownEmail + `","passphrase":"correct horse"}`
	rr = testRequest(t, srv, "POST", "/service/ssh-auth", strings.NewReader(body), nil)
	pass := gateCookie(rr)
	if pass == nil {
		t.Fatalf("passphrase login set no pass: %d %s", rr.Code, rr.Body.String())
	}

	// Then into the key-gated app, holding that pass.
	rr = pageRequest(srv, "/"+slug+"/", "navigate", []*http.Cookie{pass}, nil)
	pass = signInByKeyHolding(t, srv, rr.Header().Get("Location"), pass)

	if rr := pageRequest(srv, "/control", "navigate", []*http.Cookie{pass}, nil); rr.Code != http.StatusOK {
		t.Errorf("control after the app sign-in: status = %d, want 200", rr.Code)
	}
	if rr := pageRequest(srv, "/"+slug+"/", "navigate", []*http.Cookie{pass}, nil); rr.Code != http.StatusOK {
		t.Errorf("app: status = %d, want 200", rr.Code)
	}
}

// F14 (audit 202610): an upstream login's state wasn't tied to the browser
// that started it, so a callback URL from someone else's login could sign a
// victim's browser in as them. The popup now stops at /service/begin, which
// sets a cookie for this flow, and the callback refuses a browser without it.
func TestCallbackNeedsTheBrowserThatStartedTheLogin(t *testing.T) {
	srv := newTestServer(t)
	rec := fakeGitHubIdP(t, srv)
	nonce := appBehind(t, srv, "Notes", &rec.ID)

	leg, browser := beginInBrowser(t, srv, appLoginStart(t, srv, nonce))
	if !strings.HasSuffix(leg.Path, "/authorize") {
		t.Fatalf("begin forwarded to %s, want the provider", leg)
	}
	callback := "/service/callback?code=c1&state=" + leg.Query().Get("state")

	for name, headers := range map[string]map[string]string{
		"no cookie":    nil,
		"wrong cookie": {"Cookie": strings.Replace(browser["Cookie"], "=", "=x", 1)},
	} {
		if rr := testRequest(t, srv, "GET", callback, nil, headers); rr.Code != http.StatusForbidden {
			t.Errorf("%s: callback status %d, want 403", name, rr.Code)
		}
	}
	if rr := testRequest(t, srv, "GET", callback, nil, browser); rr.Code != 200 {
		t.Errorf("starting browser: callback status %d, body %s", rr.Code, rr.Body.String())
	}
}
