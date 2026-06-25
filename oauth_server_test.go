package main

import (
  "bytes"
  "crypto/sha256"
  "encoding/base64"
  "encoding/json"
  "io"
  "net/http"
  "net/http/httptest"
  "net/url"
  "strconv"
  "strings"
  "testing"
  "time"

  jose "github.com/go-jose/go-jose/v4"
  josejwt "github.com/go-jose/go-jose/v4/jwt"
)

// ── helpers ─────────────────────────────────────────────────────────

// createRefreshFamily creates a RefreshFamily in the store and returns
// its id. Useful when setting up refresh-token tests that now require a
// backing family record.
func createRefreshFamily(t *testing.T, store *Store, email string, svcID int64, currentJTI string) string {
  t.Helper()
  famID := genNonce()
  fam := &RefreshFamily{
    ID:         famID,
    UserEmail:  email,
    ServiceID:  svcID,
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
  rr := testRequest(t, srv, "POST", "/oauth/authorize", nil, nil)
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

// A full authorize against a generic OAuth service should redirect the
// browser upstream and stash pending state keyed by the upstream state.
func TestOAuthAuthorizeRedirectsUpstream(t *testing.T) {
  srv := newTestServer(t)
  srv.httpClient = mockHTTPClient(func(req *http.Request) *http.Response {
    switch {
    case strings.HasSuffix(req.URL.Path, "/.well-known/oauth-authorization-server"):
      return jsonResp(200, map[string]interface{}{
        "issuer":                           "https://up.example",
        "authorization_endpoint":           "https://up.example/authorize",
        "token_endpoint":                   "https://up.example/token",
        "registration_endpoint":            "https://up.example/register",
        "code_challenge_methods_supported": []string{"S256"},
      })
    case req.URL.Path == "/register":
      return jsonResp(201, map[string]string{"client_id": "up-client"})
    default:
      return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}
    }
  })

  // The MCP resource resolves to a service by its URL (the slug path);
  // OAuthURL points discovery at the upstream auth server.
  registerService(t, srv, "up", "/mcp/up", ServiceDescriptor{Type: "mcp", OAuthURL: "https://up.example"})

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
  if rr.Code != 302 {
    t.Fatalf("status = %d, want 302; body = %s", rr.Code, rr.Body.String())
  }
  loc := rr.Header().Get("Location")
  if !strings.HasPrefix(loc, "https://up.example/authorize") {
    t.Errorf("Location = %q, want upstream authorize URL", loc)
  }
  if !strings.Contains(loc, "client_id=up-client") {
    t.Errorf("Location missing upstream client_id: %s", loc)
  }

  // The point of handleAuthorize isn't the redirect URL — it's that the
  // MCP client's original request got stashed so the upstream callback can
  // resume it. Pull the upstream `state` out of the redirect and confirm
  // pending state exists under it, carrying the MCP client's own state.
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
  // appNonce links back to the stored mcpPendingAuth (keyed separately),
  // which is what carries the MCP client's redirect_uri/state/PKCE forward.
  if pa.appNonce == "" {
    t.Error("pending auth missing appNonce link to the MCP request")
  }
  if _, mcpOK := srv.mcpAuthPending.Load(pa.appNonce); !mcpOK {
    t.Error("MCP pending request not stored for the callback to resume")
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

// Full authorization_code exchange for an external (wrapped) token. This
// drives PKCE verification, token minting, sealing of upstream data, and
// the refresh-cookie response.
func TestOAuthAuthCodeGrantWrappedHappyPath(t *testing.T) {
  srv := newTestServer(t)
  clientID, secret := registerOAuthClient(t, srv)
  verifier, challenge := pkcePair()
  svcID := registerService(t, srv, "up", "/mcp/up", ServiceDescriptor{Type: "mcp"})
  sid, _ := strconv.ParseInt(svcID, 10, 64)

  code := "fb-auth-code-xyz"
  srv.oauthSrv.codesMu.Lock()
  srv.oauthSrv.codes[code] = &mcpAuthCode{
    issued: time.Now(),
    pending: &mcpPendingAuth{
      clientID:            clientID,
      codeChallenge:       challenge,
      codeChallengeMethod: "S256",
      serviceID:           sid,
      userEmail:           "user@example.com",
      upstreamToken:       "real-upstream-token",
      upstreamRefresh:     "real-upstream-refresh",
      upstreamTokenURL:    "https://up.example/token",
      upstreamScopes:      "openid email",
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

  var resp map[string]interface{}
  json.Unmarshal(rr.Body.Bytes(), &resp)
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

  // The minted access token must be a wrapped Freshbreath token that
  // unwraps to the original upstream token for this service.
  claims, err := srv.verifyAndUnwrapToken(at, sid)
  if err != nil {
    t.Fatalf("verifyAndUnwrapToken: %v", err)
  }
  if claims == nil {
    t.Fatal("expected wrapped claims, got nil")
  }
  if claims.UpstreamToken != "real-upstream-token" {
    t.Errorf("UpstreamToken = %q, want real-upstream-token", claims.UpstreamToken)
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
      userEmail:           "user@example.com",
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

// An identity refresh token re-mints an identity access token. The user is
// re-resolved from the DB, so this also covers refreshIdentity's lookup.
func TestOAuthRefreshIdentityHappyPath(t *testing.T) {
  srv := newTestServer(t)
  svcID, err := srv.store.RegisterService("admin-idp", "https://admin.example", ServiceDescriptor{Type: "oidc"})
  if err != nil {
    t.Fatalf("register service: %v", err)
  }
  if _, err := srv.store.CreateUser("Ada Lovelace", "ada@example.com", "Admin", "Active"); err != nil {
    t.Fatalf("create user: %v", err)
  }

  jti := genNonce()
  famID := createRefreshFamily(t, srv.store, "ada@example.com", svcID, jti)
  rt, err := srv.mintRefreshToken(freshbreathRefreshData{
    Kind:      "identity",
    ServiceID: svcID,
    UserEmail: "ada@example.com",
    UserRole:  "Admin",
    UserName:  "Ada Lovelace",
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
  var resp map[string]interface{}
  json.Unmarshal(rr.Body.Bytes(), &resp)
  at, _ := resp["access_token"].(string)
  if at == "" {
    t.Fatal("expected access_token")
  }
  claims, err := srv.verifyFreshbreathToken(at)
  if err != nil || claims == nil {
    t.Fatalf("verify minted token: claims=%v err=%v", claims, err)
  }
  if claims.Kind != "identity" {
    t.Errorf("Kind = %q, want identity", claims.Kind)
  }
  if claims.UserEmail != "ada@example.com" {
    t.Errorf("UserEmail = %q, want ada@example.com", claims.UserEmail)
  }
  if claims.ServiceID != svcID {
    t.Errorf("ServiceID = %d, want %d (binding must survive refresh)", claims.ServiceID, svcID)
  }
}

// A deleted user must not be able to refresh — refreshIdentity re-resolves
// the user from the DB and fails closed.
func TestOAuthRefreshIdentityDeletedUser(t *testing.T) {
  srv := newTestServer(t)
  svcID, _ := srv.store.RegisterService("admin-idp", "https://admin.example", ServiceDescriptor{Type: "oidc"})
  jti := genNonce()
  famID := createRefreshFamily(t, srv.store, "ghost@example.com", svcID, jti)
  rt, err := srv.mintRefreshToken(freshbreathRefreshData{
    Kind:      "identity",
    ServiceID: svcID,
    UserEmail: "ghost@example.com",
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

// A wrapped refresh token refreshes the upstream OAuth token, then re-wraps
// it into a fresh Freshbreath access token. This drives refreshWrapped +
// resolveTokenEndpoint + the upstream PostForm.
func TestOAuthRefreshWrappedHappyPath(t *testing.T) {
  srv := newTestServer(t)
  srv.httpClient = mockHTTPClient(func(req *http.Request) *http.Response {
    switch {
    case strings.HasSuffix(req.URL.Path, "/.well-known/oauth-authorization-server"):
      return jsonResp(200, map[string]interface{}{
        "issuer":         "https://up.example",
        "token_endpoint": "https://up.example/token",
      })
    case req.URL.Path == "/token" && req.Method == "POST":
      return jsonResp(200, map[string]interface{}{
        "access_token":  "new-upstream-token",
        "refresh_token": "new-upstream-refresh",
        "scope":         "openid email",
        "expires_in":    3600,
      })
    default:
      return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}
    }
  })

  svcID, err := srv.store.RegisterService("up", "/mcp/up", ServiceDescriptor{
    Type:     "mcp",
    OAuthURL: "https://up.example",
    ClientID: "up-client",
  })
  if err != nil {
    t.Fatalf("register service: %v", err)
  }

  jti := genNonce()
  famID := createRefreshFamily(t, srv.store, "user@example.com", svcID, jti)
  rt, err := srv.mintRefreshToken(freshbreathRefreshData{
    Kind:             "wrapped",
    ServiceID:        svcID,
    UserEmail:        "user@example.com",
    UpstreamRefresh:  "old-upstream-refresh",
    UpstreamTokenURL: "https://up.example/token",
    UpstreamScopes:   "openid email",
    FamilyID:         famID,
    JTI:              jti,
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
  var resp map[string]interface{}
  json.Unmarshal(rr.Body.Bytes(), &resp)
  at, _ := resp["access_token"].(string)
  if at == "" {
    t.Fatal("expected access_token")
  }
  claims, err := srv.verifyAndUnwrapToken(at, svcID)
  if err != nil || claims == nil {
    t.Fatalf("unwrap re-minted token: claims=%v err=%v", claims, err)
  }
  if claims.UpstreamToken != "new-upstream-token" {
    t.Errorf("UpstreamToken = %q, want new-upstream-token", claims.UpstreamToken)
  }
}

// A browser refreshes with the HttpOnly cookie (credentials: include). The
// cookie is the system of record, so the response body must NOT echo the
// refresh token — otherwise any script reading the response defeats HttpOnly.
func TestRefreshGrantCookieOmitsBodyToken(t *testing.T) {
  srv := newTestServer(t)
  svcID, _ := srv.store.RegisterService("idp", "https://idp.example", ServiceDescriptor{Type: "oidc"})
  if _, err := srv.store.CreateUser("Web User", "web@example.com", "Member", "Active"); err != nil {
    t.Fatalf("create user: %v", err)
  }
  jti := genNonce()
  famID := createRefreshFamily(t, srv.store, "web@example.com", svcID, jti)
  rt, err := srv.mintRefreshToken(freshbreathRefreshData{
    Kind: "identity", ServiceID: svcID, UserEmail: "web@example.com",
    FamilyID: famID, JTI: jti,
  })
  if err != nil {
    t.Fatalf("mint refresh token: %v", err)
  }

  req := httptest.NewRequest("POST", "/oauth/token",
    strings.NewReader(url.Values{"grant_type": {"refresh_token"}}.Encode()))
  req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
  req.AddCookie(&http.Cookie{Name: "refresh_token", Value: rt})
  rr := httptest.NewRecorder()
  srv.ServeHTTP(rr, req)

  if rr.Code != 200 {
    t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
  }
  var resp map[string]interface{}
  json.Unmarshal(rr.Body.Bytes(), &resp)
  if resp["access_token"] == nil || resp["access_token"] == "" {
    t.Error("expected access_token in body")
  }
  if _, present := resp["refresh_token"]; present {
    t.Error("cookie-based (browser) refresh must NOT echo refresh_token in the body")
  }
  // A rotated refresh token must still be issued — as a fresh cookie.
  if !hasRefreshCookie(rr) {
    t.Error("expected a rotated refresh_token Set-Cookie")
  }
}

// A CLI/MCP client has no cookie jar — it sends the refresh token in the form
// body and reads the new one from the response body. That path must keep
// returning refresh_token, per the OAuth token-response contract.
func TestRefreshGrantFormKeepsBodyToken(t *testing.T) {
  srv := newTestServer(t)
  svcID, _ := srv.store.RegisterService("idp", "https://idp.example", ServiceDescriptor{Type: "oidc"})
  if _, err := srv.store.CreateUser("CLI User", "cli@example.com", "Member", "Active"); err != nil {
    t.Fatalf("create user: %v", err)
  }
  jti := genNonce()
  famID := createRefreshFamily(t, srv.store, "cli@example.com", svcID, jti)
  rt, _ := srv.mintRefreshToken(freshbreathRefreshData{
    Kind: "identity", ServiceID: svcID, UserEmail: "cli@example.com",
    FamilyID: famID, JTI: jti,
  })

  rr := postForm(t, srv, "/oauth/token", url.Values{
    "grant_type":    {"refresh_token"},
    "refresh_token": {rt},
  })
  if rr.Code != 200 {
    t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
  }
  var resp map[string]interface{}
  json.Unmarshal(rr.Body.Bytes(), &resp)
  if resp["refresh_token"] == nil || resp["refresh_token"] == "" {
    t.Error("form-based (CLI) refresh must include refresh_token in the body")
  }
}

// A newly-minted refresh token must carry family_id and jti in its JWT
// claims so the refresh grant can rotate them without decrypting the sealed
// payload. Those claims must survive verifyRefreshToken.
func TestRefreshTokenClaimsRoundtrip(t *testing.T) {
  srv := newTestServer(t)
  data := freshbreathRefreshData{
    Kind:      "identity",
    ServiceID: 99,
    UserEmail: "claims@example.com",
    UserRole:  "Member",
    UserName:  "Claims Tester",
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

  // The family_id and jti are also readable directly from the JWT (so the
  // grant handler can look up the family without asking for the sealed
  // payload). Parse just the claims layer to confirm.
  tok, _ := josejwt.ParseSigned(rt, []jose.SignatureAlgorithm{jose.HS256})
  var outer struct {
    josejwt.Claims
    Sealed  string `json:"sealed"`
    FamilyID string `json:"family_id"`
    JTI     string `json:"jti"`
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

// ── Refresh token rotation (token families) ─────────────────────────

// A normal refresh with the current jti rotates the family to a new jti
// and issues a fresh token pair.
func TestOAuthRefreshRotationHappyPath(t *testing.T) {
  srv := newTestServer(t)
  svcID, _ := srv.store.RegisterService("idp", "https://idp.example", ServiceDescriptor{Type: "oidc"})
  if _, err := srv.store.CreateUser("Rot User", "rot@example.com", "Member", "Active"); err != nil {
    t.Fatalf("create user: %v", err)
  }

  famID := genNonce()
  jti1 := genNonce()
  fam := &RefreshFamily{
    ID:         famID,
    UserEmail:  "rot@example.com",
    ServiceID:  svcID,
    CurrentJTI: jti1,
    ExpiresAt:  time.Now().Add(24 * time.Hour),
  }
  if err := srv.store.CreateRefreshFamily(fam); err != nil {
    t.Fatalf("create family: %v", err)
  }

  rt, err := srv.mintRefreshToken(freshbreathRefreshData{
    Kind: "identity", ServiceID: svcID, UserEmail: "rot@example.com",
    FamilyID: famID, JTI: jti1,
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
  svcID, _ := srv.store.RegisterService("idp", "https://idp.example", ServiceDescriptor{Type: "oidc"})
  if _, err := srv.store.CreateUser("Grace User", "grace@example.com", "Member", "Active"); err != nil {
    t.Fatalf("create user: %v", err)
  }

  famID := genNonce()
  jti1 := genNonce()
  srv.store.CreateRefreshFamily(&RefreshFamily{
    ID:         famID,
    UserEmail:  "grace@example.com",
    ServiceID:  svcID,
    CurrentJTI: jti1,
    ExpiresAt:  time.Now().Add(24 * time.Hour),
  })

  rt1, _ := srv.mintRefreshToken(freshbreathRefreshData{
    Kind: "identity", ServiceID: svcID, UserEmail: "grace@example.com",
    FamilyID: famID, JTI: jti1,
  })

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

  // Second refresh with the original token (jti1 is now prev_jti).
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
  svcID, _ := srv.store.RegisterService("idp", "https://idp.example", ServiceDescriptor{Type: "oidc"})
  if _, err := srv.store.CreateUser("Reuse User", "reuse@example.com", "Member", "Active"); err != nil {
    t.Fatalf("create user: %v", err)
  }

  famID := genNonce()
  jti1 := genNonce()
  srv.store.CreateRefreshFamily(&RefreshFamily{
    ID:         famID,
    UserEmail:  "reuse@example.com",
    ServiceID:  svcID,
    CurrentJTI: jti1,
    ExpiresAt:  time.Now().Add(24 * time.Hour),
  })

  rt1, _ := srv.mintRefreshToken(freshbreathRefreshData{
    Kind: "identity", ServiceID: svcID, UserEmail: "reuse@example.com",
    FamilyID: famID, JTI: jti1,
  })

  // First refresh rotates jti-1 → jti-2.
  rr1 := postForm(t, srv, "/oauth/token", url.Values{
    "grant_type":    {"refresh_token"},
    "refresh_token": {rt1},
  })
  if rr1.Code != 200 {
    t.Fatalf("first refresh: %d", rr1.Code)
  }

  // Advance rotated_at past the grace window by forcing an update.
  srv.store.db.Exec(
    "UPDATE refresh_families SET rotated_at = datetime('now', '-60 seconds') WHERE id = ?", famID,
  )

  // Replay with the old token (jti-1 is now prev_jti, but outside grace).
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
    Kind: "identity", ServiceID: svcID, UserEmail: "reuse@example.com",
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
  svcID, _ := srv.store.RegisterService("idp", "https://idp.example", ServiceDescriptor{Type: "oidc"})
  if _, err := srv.store.CreateUser("Revoked User", "rev@example.com", "Member", "Active"); err != nil {
    t.Fatalf("create user: %v", err)
  }

  famID := genNonce()
  jti1 := genNonce()
  srv.store.CreateRefreshFamily(&RefreshFamily{
    ID:         famID,
    UserEmail:  "rev@example.com",
    ServiceID:  svcID,
    CurrentJTI: jti1,
    ExpiresAt:  time.Now().Add(24 * time.Hour),
  })
  srv.store.RevokeRefreshFamily(famID)

  rt, _ := srv.mintRefreshToken(freshbreathRefreshData{
    Kind: "identity", ServiceID: svcID, UserEmail: "rev@example.com",
    FamilyID: famID, JTI: jti1,
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

// A token missing family_id (legacy stateless) is treated as having no
// family and fails gracefully — the client must re-login.
func TestOAuthRefreshLegacyNoFamily(t *testing.T) {
  srv := newTestServer(t)
  svcID, _ := srv.store.RegisterService("idp", "https://idp.example", ServiceDescriptor{Type: "oidc"})
  if _, err := srv.store.CreateUser("Legacy User", "leg@example.com", "Member", "Active"); err != nil {
    t.Fatalf("create user: %v", err)
  }

  // Mint a refresh token with empty family_id (as legacy tokens have).
  rt, _ := srv.mintRefreshToken(freshbreathRefreshData{
    Kind: "identity", ServiceID: svcID, UserEmail: "leg@example.com",
    // FamilyID and JTI intentionally empty — this is a legacy token.
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

  // A wrapped token must still mint → verify → unseal correctly.
  tok, err := srv.mintFreshbreathToken("wrapped", "u@example.com", "", "", 5,
    &sealedUpstreamData{UpstreamToken: "up"})
  if err != nil {
    t.Fatalf("mint: %v", err)
  }
  claims, err := srv.verifyFreshbreathToken(tok)
  if err != nil || claims == nil {
    t.Fatalf("roundtrip broke after subkey split: claims=%v err=%v", claims, err)
  }
  if claims.UpstreamToken != "up" {
    t.Errorf("UpstreamToken = %q, want up", claims.UpstreamToken)
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
  const sid int64 = 42

  wrapped, err := srv.mintFreshbreathToken("wrapped", "u@example.com", "", "", sid,
    &sealedUpstreamData{UpstreamToken: "secret-upstream"})
  if err != nil {
    t.Fatalf("mint wrapped: %v", err)
  }

  // Correct service → unwraps.
  claims, err := srv.verifyAndUnwrapToken(wrapped, sid)
  if err != nil {
    t.Fatalf("unwrap: %v", err)
  }
  if claims == nil || claims.UpstreamToken != "secret-upstream" {
    t.Fatalf("expected unwrapped upstream token, got %+v", claims)
  }

  // Wrong service ID → rejected (token binding).
  if _, err := srv.verifyAndUnwrapToken(wrapped, sid+1); err == nil {
    t.Error("expected service_id mismatch to fail")
  }

  // Identity token (not wrapped) → rejected by verifyAndUnwrapToken.
  identity, _ := srv.mintFreshbreathToken("identity", "u@example.com", "Admin", "U", sid, nil)
  if _, err := srv.verifyAndUnwrapToken(identity, sid); err == nil {
    t.Error("expected non-wrapped token to be rejected")
  }

  // A non-Freshbreath token → (nil, nil): caller uses the raw token as-is.
  claims, err = srv.verifyAndUnwrapToken("not.a.freshbreathtoken", sid)
  if err != nil || claims != nil {
    t.Errorf("non-fb token: got claims=%v err=%v, want nil,nil", claims, err)
  }
}

// ── helper ──────────────────────────────────────────────────────────

func postForm(t *testing.T, srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
  t.Helper()
  req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
  req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
  rr := httptest.NewRecorder()
  srv.ServeHTTP(rr, req)
  return rr
}
