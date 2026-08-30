package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"poggers.institute/freshbreath/internal/db"
)

// ── helpers ─────────────────────────────────────────────────────────

// oidcRecord creates a plain OIDC auth record — the workhorse gate for
// refresh tests that never touch the network.
func oidcRecord(t *testing.T, srv *Server, name string) *db.AuthRecord {
	t.Helper()
	return newAuthRecord(t, srv, name, db.AuthOIDC,
		db.AuthDescriptor{Issuer: "https://" + slugify(name) + ".example", Provider: slugify(name)})
}

// createRefreshFamily creates a RefreshFamily in the store and returns
// its id. Refresh-token tests need a backing family record.
func createRefreshFamily(t *testing.T, store *db.Store, subject string, authID int64, currentJTI string) string {
	t.Helper()
	famID := db.GenNonce()
	fam := &db.RefreshFamily{
		ID:         famID,
		Subject:    subject,
		AuthID:     authID,
		CurrentJTI: currentJTI,
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}
	if err := store.CreateRefreshFamily(fam); err != nil {
		t.Fatalf("create refresh family: %v", err)
	}
	return famID
}

// pkcePair returns a (verifier, S256 challenge) pair suitable for the
// OAuth authorization_code grant.
func pkcePair() (verifier, challenge string) {
	verifier = "test-verifier-0123456789-abcdefghijklmnop-zyxwvut"
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// registerOAuthClient registers a DCR client directly against the store
// and returns (clientID, clientSecret).
func registerOAuthClient(t *testing.T, srv *Server, redirectURIs ...string) (string, string) {
	t.Helper()
	if len(redirectURIs) == 0 {
		redirectURIs = []string{"https://client.example/callback"}
	}
	id, secret, err := srv.oauthSrv.clients.register(redirectURIs)
	if err != nil {
		t.Fatalf("register oauth client: %v", err)
	}
	return id, secret
}

// ── Metadata / JWKS ─────────────────────────────────────────────────

func TestOAuthMetadata(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/.well-known/oauth-authorization-server", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	base := "http://localhost:9009"
	if meta["issuer"] != base {
		t.Errorf("issuer = %v, want %v", meta["issuer"], base)
	}
	if meta["authorization_endpoint"] != base+"/oauth/authorize" {
		t.Errorf("authorization_endpoint = %v", meta["authorization_endpoint"])
	}
	if meta["token_endpoint"] != base+"/oauth/token" {
		t.Errorf("token_endpoint = %v", meta["token_endpoint"])
	}
	if meta["registration_endpoint"] != base+"/oauth/register" {
		t.Errorf("registration_endpoint = %v", meta["registration_endpoint"])
	}
}

func TestOAuthJWKS(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/oauth/jwks", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d", rr.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &body)
	keys, ok := body["keys"].([]interface{})
	if !ok {
		t.Fatalf("keys missing or wrong type: %v", body["keys"])
	}
	// HMAC-signed — the key set is intentionally empty.
	if len(keys) != 0 {
		t.Errorf("len(keys) = %d, want 0", len(keys))
	}
}

// ── Dynamic Client Registration ─────────────────────────────────────

func TestOAuthRegister(t *testing.T) {
	srv := newTestServer(t)
	body := `{"redirect_uris": ["https://client.example/cb"], "client_name": "Test Client"}`
	rr := testRequest(t, srv, "POST", "/oauth/register", strings.NewReader(body), nil)
	if rr.Code != 201 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["client_id"] == nil || resp["client_id"] == "" {
		t.Error("expected client_id in response")
	}
	if resp["client_secret"] == nil || resp["client_secret"] == "" {
		t.Error("expected client_secret in response")
	}

	// The registered client must be retrievable from the store.
	secret, uris, ok, err := srv.oauthSrv.clients.get(resp["client_id"].(string))
	if err != nil || !ok {
		t.Fatalf("get registered client: ok=%v err=%v", ok, err)
	}
	if secret != resp["client_secret"] {
		t.Error("stored secret does not match returned secret")
	}
	if len(uris) != 1 || uris[0] != "https://client.example/cb" {
		t.Errorf("redirect URIs = %v", uris)
	}
}

func TestOAuthRegisterMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/oauth/register", nil, nil)
	if rr.Code != 405 {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestOAuthRegisterBadJSON(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "POST", "/oauth/register", strings.NewReader(`{not json`), nil)
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestOAuthRegisterMissingRedirectURIs(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "POST", "/oauth/register", strings.NewReader(`{"client_name": "x"}`), nil)
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["error"] != "invalid_request" {
		t.Errorf("error = %q, want invalid_request", resp["error"])
	}
}

// ── Authorize ───────────────────────────────────────────────────────

func TestOAuthAuthorizeMissingParams(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/oauth/authorize?client_id=x", nil, nil)
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestOAuthAuthorizeMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	// POST is the interstitial's continue path now; PUT is nobody's.
	rr := testRequest(t, srv, "PUT", "/oauth/authorize", nil, nil)
	if rr.Code != 405 {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestOAuthAuthorizeUnknownClient(t *testing.T) {
	srv := newTestServer(t)
	_, challenge := pkcePair()
	q := url.Values{
		"client_id":             {"nonexistent"},
		"redirect_uri":          {"https://client.example/callback"},
		"state":                 {"st"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	rr := testRequest(t, srv, "GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["error"] != "invalid_client" {
		t.Errorf("error = %q, want invalid_client", resp["error"])
	}
}

func TestOAuthAuthorizeInvalidRedirect(t *testing.T) {
	srv := newTestServer(t)
	clientID, _ := registerOAuthClient(t, srv, "https://client.example/callback")
	_, challenge := pkcePair()
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"https://evil.example/steal"},
		"state":                 {"st"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	rr := testRequest(t, srv, "GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if !strings.Contains(resp["error_description"], "redirect_uri") {
		t.Errorf("error_description = %q, want mention of redirect_uri", resp["error_description"])
	}
}

// The authorization check handleAuthorize never had: a flow against the
// central /mcp resource demands a configured admin gate — with none, the
// flow is refused instead of proceeding unauthenticated.
func TestOAuthAuthorizeCentralRequiresAdminAuth(t *testing.T) {
	srv := newTestServer(t)
	clientID, _ := registerOAuthClient(t, srv, "https://client.example/callback")
	_, challenge := pkcePair()
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"https://client.example/callback"},
		"state":                 {"st"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {"/mcp"},
	}
	rr := testRequest(t, srv, "GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	if rr.Code != 403 {
		t.Fatalf("status = %d, want 403 (no admin auth configured)", rr.Code)
	}
}

// mountAuthorizeTarget registers a gated virtual service at /mcp/up and
// returns its gate record. The gate is a fully-specified oauth2 record, so
// beginning its leg needs no discovery and no network.
func mountAuthorizeTarget(t *testing.T, srv *Server) *db.AuthRecord {
	t.Helper()
	gate := newAuthRecord(t, srv, "Upstream IdP", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: "https://up.example/authorize",
		TokenURL:     "https://up.example/token",
		ClientID:     "up-client",
		Provider:     "up",
	})
	if _, err := srv.store.RegisterService("up", "/mcp/up",
		db.ServiceDescriptor{Type: "virtual"}, &gate.ID, nil); err != nil {
		t.Fatalf("register service: %v", err)
	}
	return gate
}

// interstitialState pulls the continue-state out of the leg-skip page.
func interstitialState(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`var state = "([^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no state in interstitial page: %s", page)
	}
	return m[1]
}

// A full authorize against a gated mount: GET answers with the leg-skip
// interstitial (a page, not a 302 — a redirect can't read localStorage),
// and the POST continue with no stored tokens begins the first leg and
// hands back the upstream authorize URL.
func TestOAuthAuthorizeInterstitialFlow(t *testing.T) {
	srv := newTestServer(t)
	gate := mountAuthorizeTarget(t, srv)

	clientID, _ := registerOAuthClient(t, srv, "https://client.example/callback")
	_, challenge := pkcePair()
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"https://client.example/callback"},
		"state":                 {"mcp-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {"https://localhost:9009/mcp/up"},
	}
	rr := testRequest(t, srv, "GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 interstitial; body = %s", rr.Code, rr.Body.String())
	}
	page := rr.Body.String()
	if !strings.Contains(page, "frbr:auth:") {
		t.Error("interstitial must read the frbr:auth:* store")
	}
	if !strings.Contains(page, strconv.FormatInt(gate.ID, 10)) {
		t.Error("interstitial must name the gate record id")
	}
	contState := interstitialState(t, page)

	// Continue with no stored tokens: the flow begins the gate leg and
	// sends the browser upstream.
	rr2 := testRequest(t, srv, "POST", "/oauth/authorize",
		strings.NewReader(`{"state":"`+contState+`","tokens":[]}`), nil)
	if rr2.Code != 200 {
		t.Fatalf("continue status = %d, body = %s", rr2.Code, rr2.Body.String())
	}
	var cont map[string]string
	json.Unmarshal(rr2.Body.Bytes(), &cont)
	loc := cont["redirect"]
	if !strings.HasPrefix(loc, "https://up.example/authorize") {
		t.Fatalf("redirect = %q, want upstream authorize URL", loc)
	}
	if !strings.Contains(loc, "client_id=up-client") {
		t.Errorf("redirect missing upstream client_id: %s", loc)
	}

	// The MCP request must be stashed for the callback to resume: pending
	// state keyed by the upstream OAuth state, linked to the MCP pending
	// entry through mcpKey.
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	upstreamState := u.Query().Get("state")
	if upstreamState == "" {
		t.Fatal("redirect has no upstream state to key pending auth")
	}
	srv.pendingMu.Lock()
	pa, ok := srv.pending[upstreamState]
	srv.pendingMu.Unlock()
	if !ok {
		t.Fatal("no pending auth stored under the upstream state")
	}
	if pa.mcpKey == "" {
		t.Fatal("pending auth missing mcpKey link to the MCP request")
	}
	if _, mcpOK := srv.mcpAuthPending.Load(pa.mcpKey); !mcpOK {
		t.Error("MCP pending request not stored for the callback to resume")
	}
}

// The leg-skip path: a browser holding a live token bound to the gate
// record posts it back, the server re-verifies it (a browser asserting
// "logged in" is a claim, not a fact), and the flow finishes single-leg —
// straight back to the MCP client with a code.
func TestOAuthAuthorizeLegSkip(t *testing.T) {
	srv := newTestServer(t)
	gate := mountAuthorizeTarget(t, srv)

	clientID, secret := registerOAuthClient(t, srv, "https://client.example/callback")
	verifier, challenge := pkcePair()
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"https://client.example/callback"},
		"state":                 {"mcp-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {"/mcp/up"},
	}
	rr := testRequest(t, srv, "GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	contState := interstitialState(t, rr.Body.String())

	// A live token bound to the gate, carrying its provider credential.
	tok, err := srv.mintFreshbreathToken(extSubject("up", "u-1"), "user@example.com", "", "", gate.ID, nil,
		sealedCreds{"up": {UpstreamToken: "upstream-xyz"}})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{"state": contState, "tokens": []string{tok}})
	rr2 := testRequest(t, srv, "POST", "/oauth/authorize", bytes.NewReader(body), nil)
	if rr2.Code != 200 {
		t.Fatalf("continue status = %d, body = %s", rr2.Code, rr2.Body.String())
	}
	var cont map[string]string
	json.Unmarshal(rr2.Body.Bytes(), &cont)
	loc := cont["redirect"]
	if !strings.HasPrefix(loc, "https://client.example/callback") {
		t.Fatalf("redirect = %q, want the MCP client's redirect_uri (leg skipped)", loc)
	}
	u, _ := url.Parse(loc)
	code := u.Query().Get("code")
	if code == "" || u.Query().Get("state") != "mcp-state" {
		t.Fatalf("redirect missing code/state: %s", loc)
	}

	// The code exchanges for a token that still carries the original
	// upstream credential.
	rr3 := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"client_secret": {secret},
	})
	if rr3.Code != 200 {
		t.Fatalf("token status = %d, body = %s", rr3.Code, rr3.Body.String())
	}
	at, _ := decodeMap(rr3)["access_token"].(string)
	claims, err := srv.verifyAndUnwrapToken(at, gate.ID)
	if err != nil || claims == nil {
		t.Fatalf("unwrap: claims=%v err=%v", claims, err)
	}
	if claims.Creds["up"].UpstreamToken != "upstream-xyz" {
		t.Errorf("upstream cred = %q, want upstream-xyz", claims.Creds["up"].UpstreamToken)
	}

	// A garbage token in the same POST must not be able to skip a leg.
	rrG := testRequest(t, srv, "GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	gState := interstitialState(t, rrG.Body.String())
	rr4 := testRequest(t, srv, "POST", "/oauth/authorize",
		strings.NewReader(`{"state":"`+gState+`","tokens":["garbage.token.here"]}`), nil)
	if rr4.Code != 200 {
		t.Fatalf("continue status = %d", rr4.Code)
	}
	var cont2 map[string]string
	json.Unmarshal(rr4.Body.Bytes(), &cont2)
	if !strings.HasPrefix(cont2["redirect"], "https://up.example/authorize") {
		t.Errorf("garbage token skipped a leg: redirect = %q", cont2["redirect"])
	}
}

// ── Token endpoint: dispatch + validation ───────────────────────────

func TestOAuthTokenMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, "GET", "/oauth/token", nil, nil)
	if rr.Code != 405 {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestOAuthTokenUnsupportedGrant(t *testing.T) {
	srv := newTestServer(t)
	rr := postForm(t, srv, "/oauth/token", url.Values{"grant_type": {"password"}})
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["error"] != "unsupported_grant_type" {
		t.Errorf("error = %q, want unsupported_grant_type", resp["error"])
	}
}

func TestOAuthAuthCodeGrantMissingParams(t *testing.T) {
	srv := newTestServer(t)
	rr := postForm(t, srv, "/oauth/token", url.Values{"grant_type": {"authorization_code"}})
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestOAuthAuthCodeGrantInvalidClient(t *testing.T) {
	srv := newTestServer(t)
	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"some-code"},
		"code_verifier": {"some-verifier"},
		"client_id":     {"unknown-client"},
	})
	if rr.Code != 401 {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["error"] != "invalid_client" {
		t.Errorf("error = %q, want invalid_client", resp["error"])
	}
}

func TestOAuthAuthCodeGrantUnknownCode(t *testing.T) {
	srv := newTestServer(t)
	clientID, secret := registerOAuthClient(t, srv)
	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"does-not-exist"},
		"code_verifier": {"some-verifier"},
		"client_id":     {clientID},
		"client_secret": {secret},
	})
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["error"] != "invalid_grant" {
		t.Errorf("error = %q, want invalid_grant", resp["error"])
	}
}

// Full authorization_code exchange. The login legs already minted the
// token; the grant verifies PKCE, delivers it, and opens a refresh family.
func TestOAuthAuthCodeGrantHappyPath(t *testing.T) {
	srv := newTestServer(t)
	clientID, secret := registerOAuthClient(t, srv)
	verifier, challenge := pkcePair()
	gate := oidcRecord(t, srv, "Up IdP")

	subject := extSubject("up-idp", "u-77")
	fbToken, err := srv.mintFreshbreathToken(subject, "user@example.com", "", "", gate.ID, nil,
		sealedCreds{"up-idp": {UpstreamToken: "real-upstream-token", UpstreamRefresh: "real-upstream-refresh"}})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	code := "fb-auth-code-xyz"
	srv.oauthSrv.codesMu.Lock()
	srv.oauthSrv.codes[code] = &mcpAuthCode{
		issued: time.Now(),
		pending: &mcpPendingAuth{
			clientID:            clientID,
			codeChallenge:       challenge,
			codeChallengeMethod: "S256",
			fbToken:             fbToken,
			refreshData: freshbreathRefreshData{
				Subject:   subject,
				UserEmail: "user@example.com",
				AuthID:    gate.ID,
				Upstreams: map[string]upstreamRefreshLeg{
					"up-idp": {AuthID: gate.ID, RefreshToken: "real-upstream-refresh", TokenURL: "https://up.example/token"},
				},
			},
		},
	}
	srv.oauthSrv.codesMu.Unlock()

	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"client_secret": {secret},
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	resp := decodeMap(rr)
	at, _ := resp["access_token"].(string)
	if at == "" {
		t.Fatal("expected access_token")
	}
	if resp["token_type"] != "Bearer" {
		t.Errorf("token_type = %v, want Bearer", resp["token_type"])
	}
	if resp["refresh_token"] == nil || resp["refresh_token"] == "" {
		t.Error("expected refresh_token")
	}

	// The delivered token is bound to the gate record and unseals to the
	// original upstream credential.
	claims, err := srv.verifyAndUnwrapToken(at, gate.ID)
	if err != nil || claims == nil {
		t.Fatalf("verifyAndUnwrapToken: claims=%v err=%v", claims, err)
	}
	if claims.Creds["up-idp"].UpstreamToken != "real-upstream-token" {
		t.Errorf("upstream cred = %q, want real-upstream-token", claims.Creds["up-idp"].UpstreamToken)
	}
	if claims.UserEmail != "user@example.com" {
		t.Errorf("UserEmail = %q, want user@example.com", claims.UserEmail)
	}

	// The code is single-use — a replay must fail.
	rr2 := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"client_secret": {secret},
	})
	if rr2.Code != 400 {
		t.Errorf("replay status = %d, want 400", rr2.Code)
	}
}

func TestOAuthAuthCodeGrantCreatesFamily(t *testing.T) {
	// Authorization code grant must create a refresh family automatically,
	// and the first refresh must rotate within that family.
	srv := newTestServer(t)
	clientID, secret := registerOAuthClient(t, srv)
	verifier, challenge := pkcePair()
	gate := oidcRecord(t, srv, "Family IdP")

	// A real user, so the refresh path can re-resolve them from the subject.
	user, err := srv.store.CreateUser("Family User", "family@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	subject := subjectForUser(user)

	fbToken, err := srv.mintFreshbreathToken(subject, user.Email, user.Role, user.Name, gate.ID, nil, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	code := "fb-auth-code-family-test"
	srv.oauthSrv.codesMu.Lock()
	srv.oauthSrv.codes[code] = &mcpAuthCode{
		issued: time.Now(),
		pending: &mcpPendingAuth{
			clientID:            clientID,
			codeChallenge:       challenge,
			codeChallengeMethod: "S256",
			fbToken:             fbToken,
			refreshData:         freshbreathRefreshData{Subject: subject, UserEmail: user.Email, AuthID: gate.ID},
		},
	}
	srv.oauthSrv.codesMu.Unlock()

	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"client_secret": {secret},
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	rt, _ := decodeMap(rr)["refresh_token"].(string)
	if rt == "" {
		t.Fatal("expected refresh_token in body")
	}

	// Decode the refresh token to extract family_id and jti.
	data, err := srv.verifyRefreshToken(rt)
	if err != nil {
		t.Fatalf("verify refresh token: %v", err)
	}
	if data.FamilyID == "" {
		t.Fatal("expected refresh token to have family_id")
	}
	if data.JTI == "" {
		t.Fatal("expected refresh token to have jti")
	}

	// A family record must exist in the store, bound to subject + record.
	fam, ok, err := srv.store.GetRefreshFamily(data.FamilyID)
	if err != nil {
		t.Fatalf("get family: %v", err)
	}
	if !ok {
		t.Fatal("family not found in store")
	}
	if fam.CurrentJTI != data.JTI {
		t.Errorf("family current_jti = %q, want %q", fam.CurrentJTI, data.JTI)
	}
	if fam.Subject != subject {
		t.Errorf("family subject = %q, want %q", fam.Subject, subject)
	}
	if fam.AuthID != gate.ID {
		t.Errorf("family auth_id = %d, want %d", fam.AuthID, gate.ID)
	}
	if fam.Revoked {
		t.Error("family should not be revoked")
	}

	// First refresh must rotate within the same family.
	rr2 := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	})
	if rr2.Code != 200 {
		t.Fatalf("refresh status = %d, body = %s", rr2.Code, rr2.Body.String())
	}

	rt2, _ := decodeMap(rr2)["refresh_token"].(string)
	data2, err := srv.verifyRefreshToken(rt2)
	if err != nil {
		t.Fatalf("verify rotated refresh: %v", err)
	}
	if data2.FamilyID != data.FamilyID {
		t.Errorf("rotated family_id = %q, want same family %q", data2.FamilyID, data.FamilyID)
	}
	if data2.JTI == data.JTI {
		t.Error("jti should have rotated")
	}

	// Family should have rotated in the store.
	fam2, _, _ := srv.store.GetRefreshFamily(data.FamilyID)
	if fam2.CurrentJTI != data2.JTI {
		t.Errorf("family current_jti after rotation = %q, want %q", fam2.CurrentJTI, data2.JTI)
	}
}

func TestOAuthAuthCodeGrantPKCEMismatch(t *testing.T) {
	srv := newTestServer(t)
	clientID, secret := registerOAuthClient(t, srv)
	_, challenge := pkcePair()

	code := "fb-code-pkce"
	srv.oauthSrv.codesMu.Lock()
	srv.oauthSrv.codes[code] = &mcpAuthCode{
		issued: time.Now(),
		pending: &mcpPendingAuth{
			clientID:            clientID,
			codeChallenge:       challenge,
			codeChallengeMethod: "S256",
		},
	}
	srv.oauthSrv.codesMu.Unlock()

	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {"wrong-verifier"},
		"client_id":     {clientID},
		"client_secret": {secret},
	})
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if !strings.Contains(resp["error_description"], "PKCE") {
		t.Errorf("error_description = %q, want PKCE failure", resp["error_description"])
	}
}

// ── Refresh token grant ─────────────────────────────────────────────

func TestOAuthRefreshGrantMissingToken(t *testing.T) {
	srv := newTestServer(t)
	rr := postForm(t, srv, "/oauth/token", url.Values{"grant_type": {"refresh_token"}})
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestOAuthRefreshGrantInvalidToken(t *testing.T) {
	srv := newTestServer(t)
	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"not-a-real-token"},
	})
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["error"] != "invalid_grant" {
		t.Errorf("error = %q, want invalid_grant", resp["error"])
	}
}

// A refresh with no upstream legs re-mints an access token from the
// subject. The user is re-resolved from the DB, so this also covers
// refreshLegs' identity lookup.
func TestOAuthRefreshIdentityHappyPath(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "Admin IdP")
	ada, err := srv.store.CreateUser("Ada Lovelace", "ada@example.com", "Admin", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	subject := subjectForUser(ada)

	jti := db.GenNonce()
	famID := createRefreshFamily(t, srv.store, subject, rec.ID, jti)
	rt, err := srv.mintRefreshToken(freshbreathRefreshData{
		Subject:   subject,
		UserEmail: "ada@example.com",
		AuthID:    rec.ID,
		FamilyID:  famID,
		JTI:       jti,
	})
	if err != nil {
		t.Fatalf("mint refresh token: %v", err)
	}

	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	at, _ := decodeMap(rr)["access_token"].(string)
	if at == "" {
		t.Fatal("expected access_token")
	}
	claims, err := srv.verifyFreshbreathToken(at)
	if err != nil || claims == nil {
		t.Fatalf("verify minted token: claims=%v err=%v", claims, err)
	}
	if claims.Subject != subject {
		t.Errorf("Subject = %q, want %q", claims.Subject, subject)
	}
	if claims.UserEmail != "ada@example.com" {
		t.Errorf("UserEmail = %q, want ada@example.com", claims.UserEmail)
	}
	if claims.AuthID != rec.ID {
		t.Errorf("AuthID = %d, want %d (record binding must survive refresh)", claims.AuthID, rec.ID)
	}
}

// A deleted user must not be able to refresh — the frbr: subject is
// re-resolved from the DB and fails closed.
func TestOAuthRefreshDeletedUser(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "Admin IdP")
	jti := db.GenNonce()
	famID := createRefreshFamily(t, srv.store, "frbr:9999", rec.ID, jti)
	rt, err := srv.mintRefreshToken(freshbreathRefreshData{
		Subject:   "frbr:9999",
		UserEmail: "ghost@example.com",
		AuthID:    rec.ID,
		FamilyID:  famID,
		JTI:       jti,
	})
	if err != nil {
		t.Fatalf("mint refresh token: %v", err)
	}
	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	})
	if rr.Code != 500 {
		t.Fatalf("status = %d, want 500 (user not found)", rr.Code)
	}
}

// A refresh with an upstream leg rotates the upstream token against its
// record's endpoint, then re-seals it into a fresh access token. This
// drives refreshLegs' upstream PostForm and the credential map rebuild.
func TestOAuthRefreshUpstreamLegHappyPath(t *testing.T) {
	srv := newTestServer(t)
	srv.httpClient = mockHTTPClient(func(req *http.Request) *http.Response {
		if req.URL.Path == "/token" && req.Method == "POST" {
			return jsonResp(200, map[string]interface{}{
				"access_token":  "new-upstream-token",
				"refresh_token": "new-upstream-refresh",
				"scope":         "openid email",
				"expires_in":    3600,
			})
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}
	})

	rec := newAuthRecord(t, srv, "Up IdP", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: "https://up.example/authorize",
		TokenURL:     "https://up.example/token",
		ClientID:     "up-client",
		Provider:     "up",
	})

	subject := extSubject("up", "u-42")
	jti := db.GenNonce()
	famID := createRefreshFamily(t, srv.store, subject, rec.ID, jti)
	rt, err := srv.mintRefreshToken(freshbreathRefreshData{
		Subject:   subject,
		UserEmail: "user@example.com",
		AuthID:    rec.ID,
		Upstreams: map[string]upstreamRefreshLeg{
			"up": {AuthID: rec.ID, RefreshToken: "old-upstream-refresh", TokenURL: "https://up.example/token", Scopes: "openid email"},
		},
		FamilyID: famID,
		JTI:      jti,
	})
	if err != nil {
		t.Fatalf("mint refresh token: %v", err)
	}

	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	at, _ := decodeMap(rr)["access_token"].(string)
	if at == "" {
		t.Fatal("expected access_token")
	}
	claims, err := srv.verifyAndUnwrapToken(at, rec.ID)
	if err != nil || claims == nil {
		t.Fatalf("unwrap re-minted token: claims=%v err=%v", claims, err)
	}
	if claims.Creds["up"].UpstreamToken != "new-upstream-token" {
		t.Errorf("upstream cred = %q, want new-upstream-token", claims.Creds["up"].UpstreamToken)
	}
	if claims.Creds["up"].UpstreamRefresh != "new-upstream-refresh" {
		t.Errorf("upstream refresh = %q, want new-upstream-refresh", claims.Creds["up"].UpstreamRefresh)
	}
}

// A two-leg refresh rotates EVERY upstream leg — the gate's credential and
// the acts_as credential both come back fresh in one token.
func TestOAuthRefreshRotatesEveryLeg(t *testing.T) {
	srv := newTestServer(t)
	var refreshed []string
	srv.httpClient = mockHTTPClient(func(req *http.Request) *http.Response {
		if strings.HasSuffix(req.URL.Path, "/token") && req.Method == "POST" {
			req.ParseForm()
			refreshed = append(refreshed, req.URL.Host)
			return jsonResp(200, map[string]interface{}{
				"access_token":  "fresh-from-" + req.URL.Host,
				"refresh_token": "next-" + req.PostForm.Get("refresh_token"),
			})
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}
	})

	gate := newAuthRecord(t, srv, "Company IdP", db.AuthOIDC,
		db.AuthDescriptor{Issuer: "https://company.example", Provider: "company", ClientID: "c1"})
	acts := newAuthRecord(t, srv, "GitHub", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: "https://github.example/authorize",
		TokenURL:     "https://github.example/token",
		ClientID:     "gh1", Provider: "github",
	})

	subject := extSubject("company", "u-2")
	jti := db.GenNonce()
	famID := createRefreshFamily(t, srv.store, subject, acts.ID, jti)
	rt, err := srv.mintRefreshToken(freshbreathRefreshData{
		Subject:   subject,
		UserEmail: "user@example.com",
		AuthID:    acts.ID,
		Legs:      []int64{gate.ID},
		Upstreams: map[string]upstreamRefreshLeg{
			"company": {AuthID: gate.ID, RefreshToken: "company-r1", TokenURL: "https://company.example/token"},
			"github":  {AuthID: acts.ID, RefreshToken: "github-r1", TokenURL: "https://github.example/token"},
		},
		FamilyID: famID,
		JTI:      jti,
	})
	if err != nil {
		t.Fatalf("mint refresh token: %v", err)
	}

	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(refreshed) != 2 {
		t.Fatalf("upstream refreshes = %v, want both providers", refreshed)
	}

	at, _ := decodeMap(rr)["access_token"].(string)
	claims, err := srv.verifyAndUnwrapToken(at, acts.ID)
	if err != nil || claims == nil {
		t.Fatalf("unwrap: claims=%v err=%v", claims, err)
	}
	if claims.Creds["company"].UpstreamToken != "fresh-from-company.example" {
		t.Errorf("company cred = %q", claims.Creds["company"].UpstreamToken)
	}
	if claims.Creds["github"].UpstreamToken != "fresh-from-github.example" {
		t.Errorf("github cred = %q", claims.Creds["github"].UpstreamToken)
	}
	// The legs binding survives the refresh.
	if !claims.boundTo(gate.ID) {
		t.Error("refreshed token lost its gate-leg binding")
	}
}

// A browser refreshes with the HttpOnly cookie (credentials: include). The
// cookie is the system of record, so the response body must NOT echo the
// refresh token — otherwise any script reading the response defeats HttpOnly.
//
// This models the control panel's own identity session: the cookie path
// requires an app identity, and the console is the sole consumer of the
// ephemeral adminNonce, restricted to the configured admin auth record.
func TestRefreshGrantCookieOmitsBodyToken(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "IdP")
	web, err := srv.store.CreateUser("Web User", "web@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	srv.adminNonce = db.GenNonce() // production sets this in NewServer; tests build the literal directly
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(rec.ID, 10)); err != nil {
		t.Fatalf("set admin_auth_service: %v", err)
	}
	rt := mintIdentityRefresh(t, srv, web, rec.ID)

	rr := cookieRefresh(srv, rec.ID, rt, srv.adminNonce)

	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	resp := decodeMap(rr)
	if resp["access_token"] == nil || resp["access_token"] == "" {
		t.Error("expected access_token in body")
	}
	if _, present := resp["refresh_token"]; present {
		t.Error("cookie-based (browser) refresh must NOT echo refresh_token in the body")
	}
	// A rotated refresh token must still be issued — as a fresh cookie,
	// scoped to this record's token path.
	if !hasRefreshCookie(rr) {
		t.Error("expected a rotated refresh_token Set-Cookie")
	}
	if got, want := refreshCookiePath(rr), "/oauth/token/"+strconv.FormatInt(rec.ID, 10); got != want {
		t.Errorf("rotated cookie Path = %q, want %q", got, want)
	}
}

// A CLI/MCP client has no cookie jar — it sends the refresh token in the form
// body and reads the new one from the response body. That path must keep
// returning refresh_token, per the OAuth token-response contract.
func TestRefreshGrantFormKeepsBodyToken(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "IdP")
	cli, err := srv.store.CreateUser("CLI User", "cli@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	rt := mintIdentityRefresh(t, srv, cli, rec.ID)

	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	resp := decodeMap(rr)
	if resp["refresh_token"] == nil || resp["refresh_token"] == "" {
		t.Error("form-based (CLI) refresh must include refresh_token in the body")
	}
}

// ── Cookie-path app/record authorization ────────────────────────────
//
// The cookie is SameSite=None (file:// and foreign-origin apps need it), so
// it attaches cross-site. The CORS origin allowlist gates which origins may
// call at all, but a registered app is still allowed — so on its own the
// allowlist can't stop one app's page from refreshing another app's cookie.
// On the cookie path we therefore bind the request's app (X-App-Nonce) to
// the token's auth record: the app's own gate, or a slot of a service it is
// allowed to use. The form path (CLI/MCP) is authorized by possession and
// is exempt.

// cookieRefresh simulates a browser POSTing to the path-scoped token
// endpoint with the HttpOnly refresh_token cookie attached and the given
// app identity in X-App-Nonce. The cookie is added directly (path-matching
// is the browser's job); the request URL carries the record id the path is
// scoped to.
func cookieRefresh(srv *Server, authID int64, rt, appNonce string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/oauth/token/"+strconv.FormatInt(authID, 10),
		strings.NewReader(url.Values{"grant_type": {"refresh_token"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if appNonce != "" {
		req.Header.Set("X-App-Nonce", appNonce)
	}
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: rt})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// mintIdentityRefresh mints a refresh token for a user bound to an auth
// record, backed by a real refresh family — the minimal setup the cookie
// path needs to reach rotation.
func mintIdentityRefresh(t *testing.T, srv *Server, user *db.User, authID int64) string {
	t.Helper()
	subject := subjectForUser(user)
	jti := db.GenNonce()
	famID := createRefreshFamily(t, srv.store, subject, authID, jti)
	rt, err := srv.mintRefreshToken(freshbreathRefreshData{
		Subject: subject, UserEmail: user.Email, AuthID: authID,
		FamilyID: famID, JTI: jti,
	})
	if err != nil {
		t.Fatalf("mint refresh token: %v", err)
	}
	return rt
}

// A cookie-path refresh with no X-App-Nonce at all is rejected. We must not
// fall back to the admin nonce on a headerless request the way the env/CORS
// helpers do — there the origin allowlist still backs the fallback, but here
// the header is the only app signal, so absence must mean deny.
func TestRefreshGrantCookieRequiresAppNonce(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "IdP")
	nn, _ := srv.store.CreateUser("No-Nonce User", "nn@example.com", "Member", "Active")
	rt := mintIdentityRefresh(t, srv, nn, rec.ID)

	rr := cookieRefresh(srv, rec.ID, rt, "")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rr.Code, rr.Body.String())
	}
}

// A registered app with no relation to the token's record cannot refresh
// its cookie — the cross-app / cross-record harvest is refused.
func TestRefreshGrantCookieAppNotPermitted(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "IdP")
	un, _ := srv.store.CreateUser("Unauth User", "unauth@example.com", "Member", "Active")
	rt := mintIdentityRefresh(t, srv, un, rec.ID)
	appNonce := createApp(t, srv, "outsider-app") // no gate, no service carrying the record

	rr := cookieRefresh(srv, rec.ID, rt, appNonce)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rr.Code, rr.Body.String())
	}
}

// An app whose allowed service carries the token's record in a slot
// refreshes its cookie happily — the hosted-app happy path.
func TestRefreshGrantCookieAppPermitted(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "IdP")
	hosted, _ := srv.store.CreateUser("Hosted User", "hosted@example.com", "Member", "Active")
	rt := mintIdentityRefresh(t, srv, hosted, rec.ID)
	appNonce := createApp(t, srv, "hosted-app")
	svcID := registerService(t, srv, "gated", "https://gated.example", db.ServiceDescriptor{Type: "api"})
	setServiceActsAs(t, srv, svcID, rec.ID)
	linkServiceToApp(t, srv, appNonce, svcID)

	rr := cookieRefresh(srv, rec.ID, rt, appNonce)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	if _, present := decodeMap(rr)["refresh_token"]; present {
		t.Error("cookie-based refresh must NOT echo refresh_token in the body")
	}
	if got, want := refreshCookiePath(rr), "/oauth/token/"+strconv.FormatInt(rec.ID, 10); got != want {
		t.Errorf("rotated cookie Path = %q, want %q", got, want)
	}
}

// So is an app whose own gate is the token's record — two apps behind one
// gate share a session, and either may keep it fresh.
func TestRefreshGrantCookieAppGatePermitted(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "IdP")
	gated, _ := srv.store.CreateUser("Gated User", "gated@example.com", "Member", "Active")
	rt := mintIdentityRefresh(t, srv, gated, rec.ID)
	appNonce := createApp(t, srv, "gated-app")
	setAppGate(t, srv, appNonce, rec.ID)

	rr := cookieRefresh(srv, rec.ID, rt, appNonce)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
}

// The admin console's ephemeral nonce may only refresh the configured admin
// auth record — not any other record's cookie. A leaked adminNonce must not
// become a universal refresh credential.
func TestRefreshGrantAdminNonceWrongRecord(t *testing.T) {
	srv := newTestServer(t)
	authRec := oidcRecord(t, srv, "Auth IdP")
	otherRec := oidcRecord(t, srv, "Other IdP")
	admin, _ := srv.store.CreateUser("Admin User", "admin@example.com", "Admin", "Active")
	srv.adminNonce = db.GenNonce()
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(authRec.ID, 10)); err != nil {
		t.Fatalf("set admin_auth_service: %v", err)
	}
	// A refresh token bound to the *other* record, presented with the admin nonce.
	rt := mintIdentityRefresh(t, srv, admin, otherRec.ID)

	rr := cookieRefresh(srv, otherRec.ID, rt, srv.adminNonce)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rr.Code, rr.Body.String())
	}
}

// The clobber fix: two records' refresh cookies get distinct, record-scoped
// Paths so the browser's jar keeps both slots instead of one overwriting the
// other. (The jar's path-matching itself is the browser's job; this pins the
// server's side of the contract — distinct records → distinct cookie Paths.)
func TestRefreshCookiePathScopedByRecord(t *testing.T) {
	srv := newTestServer(t)
	recA := oidcRecord(t, srv, "IdP A")
	recB := oidcRecord(t, srv, "IdP B")
	scope, _ := srv.store.CreateUser("Scope User", "scope@example.com", "Admin", "Active")
	srv.adminNonce = db.GenNonce()
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(recA.ID, 10)); err != nil {
		t.Fatalf("set admin_auth_service: %v", err)
	}

	rtA := mintIdentityRefresh(t, srv, scope, recA.ID)
	rrA := cookieRefresh(srv, recA.ID, rtA, srv.adminNonce)
	if rrA.Code != 200 {
		t.Fatalf("recA refresh = %d, body = %s", rrA.Code, rrA.Body.String())
	}

	// Second record: re-point admin_auth_service at recB so the admin nonce
	// is valid for it too, then refresh.
	srv.store.SetSetting("admin_auth_service", strconv.FormatInt(recB.ID, 10))
	rtB := mintIdentityRefresh(t, srv, scope, recB.ID)
	rrB := cookieRefresh(srv, recB.ID, rtB, srv.adminNonce)
	if rrB.Code != 200 {
		t.Fatalf("recB refresh = %d, body = %s", rrB.Code, rrB.Body.String())
	}

	pathA := refreshCookiePath(rrA)
	pathB := refreshCookiePath(rrB)
	if pathA != "/oauth/token/"+strconv.FormatInt(recA.ID, 10) {
		t.Errorf("recA cookie Path = %q", pathA)
	}
	if pathB != "/oauth/token/"+strconv.FormatInt(recB.ID, 10) {
		t.Errorf("recB cookie Path = %q", pathB)
	}
	if pathA == pathB {
		t.Errorf("expected distinct cookie Paths for distinct records, both %q", pathA)
	}
}

// The path-scoped route rejects a cookie whose token binds a different
// record — a cookie that leaks onto another record's path must not mint.
func TestRefreshGrantPathRecordMismatch(t *testing.T) {
	srv := newTestServer(t)
	recA := oidcRecord(t, srv, "IdP A")
	recB := oidcRecord(t, srv, "IdP B")
	leak, _ := srv.store.CreateUser("Leak User", "leak@example.com", "Member", "Active")
	appNonce := createApp(t, srv, "leak-app")
	setAppGate(t, srv, appNonce, recA.ID)
	rt := mintIdentityRefresh(t, srv, leak, recA.ID)

	// recA's token presented on recB's path.
	rr := cookieRefresh(srv, recB.ID, rt, appNonce)
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400 (path/record mismatch); body = %s", rr.Code, rr.Body.String())
	}
}

// The form path (CLI/MCP) carries the token in the body and is authorized by
// possession — no X-App-Nonce, no app/service grant, no admin_auth_service.
// The app/record check must be skipped there entirely.
func TestRefreshGrantFormPathSkipsAppAuth(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "IdP")
	form, _ := srv.store.CreateUser("CLI User", "form@example.com", "Member", "Active")
	rt := mintIdentityRefresh(t, srv, form, rec.ID)

	// No X-App-Nonce, no admin_auth_service, no app — form body only.
	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
}

// decodeMap is a small helper for asserting on a token-response JSON body.
func decodeMap(rr *httptest.ResponseRecorder) map[string]interface{} {
	var m map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &m)
	return m
}

// A newly-minted refresh token must carry family_id and jti in its JWT
// claims so the refresh grant can rotate them without decrypting the sealed
// payload. Those claims must survive verifyRefreshToken.
func TestRefreshTokenClaimsRoundtrip(t *testing.T) {
	srv := newTestServer(t)
	data := freshbreathRefreshData{
		Subject:   "frbr:99",
		UserEmail: "claims@example.com",
		AuthID:    7,
		FamilyID:  "fam-abc-123",
		JTI:       "jti-first-xyz",
	}
	rt, err := srv.mintRefreshToken(data)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	got, err := srv.verifyRefreshToken(rt)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.FamilyID != data.FamilyID {
		t.Errorf("FamilyID = %q, want %q", got.FamilyID, data.FamilyID)
	}
	if got.JTI != data.JTI {
		t.Errorf("JTI = %q, want %q", got.JTI, data.JTI)
	}
	if got.Subject != data.Subject || got.AuthID != data.AuthID {
		t.Errorf("payload roundtrip: got subject=%q auth=%d", got.Subject, got.AuthID)
	}

	// The family_id and jti are also readable directly from the JWT (so the
	// grant handler can look up the family without asking for the sealed
	// payload). Parse just the claims layer to confirm.
	tok, _ := josejwt.ParseSigned(rt, []jose.SignatureAlgorithm{jose.HS256})
	var outer struct {
		josejwt.Claims
		Sealed   string `json:"sealed"`
		FamilyID string `json:"family_id"`
		JTI      string `json:"jti"`
	}
	if err := tok.Claims(srv.deriveSubkey(jwtSignLabel), &outer); err != nil {
		t.Fatalf("parse outer claims: %v", err)
	}
	if outer.FamilyID != data.FamilyID {
		t.Errorf("outer FamilyID = %q, want %q", outer.FamilyID, data.FamilyID)
	}
	if outer.JTI != data.JTI {
		t.Errorf("outer JTI = %q, want %q", outer.JTI, data.JTI)
	}
}

func hasRefreshCookie(rr *httptest.ResponseRecorder) bool {
	for _, c := range rr.Result().Cookies() {
		if c.Name == "refresh_token" && c.Value != "" {
			return true
		}
	}
	return false
}

// refreshCookiePath returns the Path of the rotated refresh_token Set-Cookie,
// or "" if none was set. The path is scoped by auth record id so that refresh
// tokens for several records occupy distinct cookie slots.
func refreshCookiePath(rr *httptest.ResponseRecorder) string {
	for _, c := range rr.Result().Cookies() {
		if c.Name == "refresh_token" {
			return c.Path
		}
	}
	return ""
}

// ── Refresh token rotation (token families) ─────────────────────────

// refreshFixture is a user + record + family + refresh token, the standard
// setup for the rotation tests.
func refreshFixture(t *testing.T, srv *Server, email string) (rec *db.AuthRecord, famID string, rt string) {
	t.Helper()
	rec = oidcRecord(t, srv, "IdP")
	user, err := srv.store.CreateUser("Rotation User", email, "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	subject := subjectForUser(user)
	jti := db.GenNonce()
	famID = createRefreshFamily(t, srv.store, subject, rec.ID, jti)
	rt, err = srv.mintRefreshToken(freshbreathRefreshData{
		Subject: subject, UserEmail: email, AuthID: rec.ID,
		FamilyID: famID, JTI: jti,
	})
	if err != nil {
		t.Fatalf("mint refresh token: %v", err)
	}
	return rec, famID, rt
}

// A normal refresh with the current jti rotates the family to a new jti
// and issues a fresh token pair.
func TestOAuthRefreshRotationHappyPath(t *testing.T) {
	srv := newTestServer(t)
	_, famID, rt := refreshFixture(t, srv, "rot@example.com")
	fam0, _, _ := srv.store.GetRefreshFamily(famID)
	jti1 := fam0.CurrentJTI

	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	// Family must have rotated to a new jti.
	got, _, _ := srv.store.GetRefreshFamily(famID)
	if got.CurrentJTI == jti1 {
		t.Error("expected jti to rotate")
	}
	if got.PrevJTI != jti1 {
		t.Errorf("prev_jti = %q, want %q", got.PrevJTI, jti1)
	}
}

// A retry with the previous jti within the grace window succeeds
// idempotently — no second rotate, no revoke.
func TestOAuthRefreshRotationGraceWindow(t *testing.T) {
	srv := newTestServer(t)
	_, famID, rt1 := refreshFixture(t, srv, "grace@example.com")

	// First refresh: normal rotation.
	rr1 := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt1},
	})
	if rr1.Code != 200 {
		t.Fatalf("first refresh status = %d, body = %s", rr1.Code, rr1.Body.String())
	}

	fam, _, _ := srv.store.GetRefreshFamily(famID)
	jti2 := fam.CurrentJTI

	// Second refresh with the original token (its jti is now prev_jti).
	rr2 := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt1},
	})
	if rr2.Code != 200 {
		t.Fatalf("grace-window retry status = %d, body = %s", rr2.Code, rr2.Body.String())
	}

	fam, _, _ = srv.store.GetRefreshFamily(famID)
	if fam.CurrentJTI != jti2 {
		t.Error("grace-window retry must NOT rotate again")
	}
	if fam.Revoked {
		t.Error("grace-window retry must NOT revoke the family")
	}
}

// A replay with the previous jti outside the grace window is a detected
// reuse → the entire family is revoked and subsequent attempts fail.
func TestOAuthRefreshRotationReuseOutsideGrace(t *testing.T) {
	srv := newTestServer(t)
	rec, famID, rt1 := refreshFixture(t, srv, "reuse@example.com")

	// First refresh rotates jti-1 → jti-2.
	rr1 := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt1},
	})
	if rr1.Code != 200 {
		t.Fatalf("first refresh: %d", rr1.Code)
	}

	// Advance rotated_at past the grace window by forcing an update.
	srv.store.DB().Exec(
		"UPDATE refresh_families SET rotated_at = datetime('now', '-60 seconds') WHERE id = ?", famID,
	)

	// Replay with the old token (its jti is now prev_jti, but outside grace).
	rr2 := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt1},
	})
	if rr2.Code != 400 {
		t.Fatalf("reuse status = %d, want 400; body=%s", rr2.Code, rr2.Body.String())
	}

	fam, _, _ := srv.store.GetRefreshFamily(famID)
	if !fam.Revoked {
		t.Error("expected family revoked on reuse detection")
	}

	// Even a correct current-jti refresh must now fail.
	fam2, _, _ := srv.store.GetRefreshFamily(famID)
	rtCurrent, _ := srv.mintRefreshToken(freshbreathRefreshData{
		Subject: fam2.Subject, UserEmail: "reuse@example.com", AuthID: rec.ID,
		FamilyID: famID, JTI: fam2.CurrentJTI,
	})
	rr3 := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rtCurrent},
	})
	if rr3.Code != 400 {
		t.Errorf("refresh after revoke status = %d, want 400", rr3.Code)
	}
}

// A revoked family rejects all refresh attempts immediately.
func TestOAuthRefreshRotationRevokedFamily(t *testing.T) {
	srv := newTestServer(t)
	_, famID, rt := refreshFixture(t, srv, "rev@example.com")
	srv.store.RevokeRefreshFamily(famID)

	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	})
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["error"] != "invalid_grant" {
		t.Errorf("error = %q, want invalid_grant", resp["error"])
	}
}

// A token missing family_id is treated as having no family and fails
// gracefully — the client must re-login.
func TestOAuthRefreshNoFamily(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "IdP")
	user, err := srv.store.CreateUser("Legacy User", "leg@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Mint a refresh token with an empty family_id.
	rt, _ := srv.mintRefreshToken(freshbreathRefreshData{
		Subject: subjectForUser(user), UserEmail: user.Email, AuthID: rec.ID,
		// FamilyID and JTI intentionally empty.
	})
	rr := postForm(t, srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
	})
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["error"] != "invalid_grant" {
		t.Errorf("error = %q, want invalid_grant", resp["error"])
	}
}

// ── Unit: PKCE + verifyAndUnwrapToken ───────────────────────────────

// Key separation: the JWT-signing (HMAC) key and the AES-GCM sealing key must
// be cryptographically independent derivations of the master localKey, not the
// raw key reused for both primitives. Tokens must still roundtrip after the
// derivation.
func TestTokenSubkeySeparation(t *testing.T) {
	srv := newTestServer(t)

	sign := srv.deriveSubkey(jwtSignLabel)
	sealK := srv.deriveSubkey(sealLabel)

	if len(sign) != 32 || len(sealK) != 32 {
		t.Fatalf("subkey lengths = %d/%d, want 32/32", len(sign), len(sealK))
	}
	if bytes.Equal(sign, sealK) {
		t.Error("sign and seal subkeys must differ")
	}
	if bytes.Equal(sign, srv.localKey) {
		t.Error("sign subkey must not equal the master key")
	}
	if bytes.Equal(sealK, srv.localKey) {
		t.Error("seal subkey must not equal the master key")
	}
	// Derivation is deterministic for a given master + label.
	if !bytes.Equal(sign, srv.deriveSubkey(jwtSignLabel)) {
		t.Error("subkey derivation must be deterministic")
	}

	// A credential-carrying token must still mint → verify → unseal correctly.
	tok, err := srv.mintFreshbreathToken("ext:up:1", "u@example.com", "", "", 5, nil,
		sealedCreds{"up": {UpstreamToken: "up"}})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claims, err := srv.verifyFreshbreathToken(tok)
	if err != nil || claims == nil {
		t.Fatalf("roundtrip broke after subkey split: claims=%v err=%v", claims, err)
	}
	if claims.Creds["up"].UpstreamToken != "up" {
		t.Errorf("unsealed cred = %q, want up", claims.Creds["up"].UpstreamToken)
	}
}

func TestVerifyPKCE(t *testing.T) {
	verifier, challenge := pkcePair()
	if !verifyPKCE(verifier, challenge) {
		t.Error("valid verifier/challenge pair should pass")
	}
	if verifyPKCE("wrong", challenge) {
		t.Error("mismatched verifier should fail")
	}
	if verifyPKCE(verifier, "wrong-challenge") {
		t.Error("mismatched challenge should fail")
	}
}

func TestVerifyAndUnwrapToken(t *testing.T) {
	srv := newTestServer(t)
	const recID int64 = 42

	tok, err := srv.mintFreshbreathToken("ext:up:9", "u@example.com", "", "", recID, nil,
		sealedCreds{"up": {UpstreamToken: "secret-upstream"}})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	// Correct record → unseals.
	claims, err := srv.verifyAndUnwrapToken(tok, recID)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if claims == nil || claims.Creds["up"].UpstreamToken != "secret-upstream" {
		t.Fatalf("expected unsealed upstream token, got %+v", claims)
	}

	// Wrong record → rejected (token binding).
	if _, err := srv.verifyAndUnwrapToken(tok, recID+1); err == nil {
		t.Error("expected record mismatch to fail")
	}

	// A record carried in Legs also satisfies the binding.
	legged, _ := srv.mintFreshbreathToken("ext:up:9", "u@example.com", "", "", recID, []int64{7}, nil)
	if _, err := srv.verifyAndUnwrapToken(legged, 7); err != nil {
		t.Errorf("legs binding rejected: %v", err)
	}

	// A non-Freshbreath token → (nil, nil): caller uses the raw token as-is.
	claims, err = srv.verifyAndUnwrapToken("not.a.freshbreathtoken", recID)
	if err != nil || claims != nil {
		t.Errorf("non-fb token: got claims=%v err=%v, want nil,nil", claims, err)
	}
}

// ── helper ──────────────────────────────────────────────────────────

func TestDeviceLabelFromUA(t *testing.T) {
	if got := deviceLabelFromUA(""); got != "" {
		t.Errorf("empty UA = %q, want empty", got)
	}

	// A recognized UA renders as "Browser on OS".
	chrome := "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
	if got := deviceLabelFromUA(chrome); got != "Chrome on Linux" {
		t.Errorf("chrome UA = %q, want %q", got, "Chrome on Linux")
	}

	// Browser unknown but OS recognized — fall back to the OS alone.
	if got := deviceLabelFromUA("Mozilla/5.0 (Macintosh)"); got != "macOS" {
		t.Errorf("macintosh UA = %q, want %q", got, "macOS")
	}

	// A non-browser client with no OS keeps its clean name.
	if got := deviceLabelFromUA("curl/8.4.0"); got != "curl" {
		t.Errorf("curl UA = %q, want %q", got, "curl")
	}

	// Wholly unrecognized UA falls back to a truncated copy of the raw string.
	long := strings.Repeat("x", 100)
	if got := deviceLabelFromUA(long); len(got) != 80 {
		t.Errorf("long UA len = %d, want 80", len(got))
	}
}

func postForm(t *testing.T, srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}
