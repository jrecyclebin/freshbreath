package main

import (
  "crypto/rand"
  "crypto/sha256"
  "encoding/base64"
  "encoding/json"
  "fmt"
  "net/http"
  "strings"
  "sync"
  "time"

  jose "github.com/go-jose/go-jose/v4"
  josejwt "github.com/go-jose/go-jose/v4/jwt"
  "github.com/modelcontextprotocol/go-sdk/oauthex"
)

// ── OAuth Authorization Server ──────────────────────────────────────
//
// Freshbreath acts as an OAuth authorization server for MCP clients.
// The flow:
//
//  1. MCP client registers at /oauth/register (DCR)
//  2. MCP client sends user to /oauth/authorize
//  3. Freshbreath stores the MCP request, kicks off upstream OAuth
//  4. User authenticates upstream, callback arrives at /service/callback
//  5. Freshbreath exchanges upstream code → upstream token
//  6. Freshbreath generates its own auth code, stores it with the upstream token
//  7. Freshbreath redirects MCP client to their redirect_uri
//  8. MCP client exchanges code at /oauth/token
//  9. Freshbreath verifies PKCE, mints a JWT wrapping the upstream token
//  10. MCP client uses that JWT as a Bearer token; Freshbreath unwraps it
//      to get the real upstream token for virtual scripts.

// ── OAuth JSON Error Responses ──────────────────────────────────────
//
// RFC 6749 §5.2 requires error responses to be application/json with
// "error" and "error_description" fields. We use this across all OAuth
// endpoints so MCP clients can parse errors instead of choking on
// plain text.

func oauthWriteError(w http.ResponseWriter, status int, code string, desc string) {
  w.Header().Set("Content-Type", "application/json")
  w.WriteHeader(status)
  json.NewEncoder(w).Encode(map[string]string{
    "error":             code,
    "error_description": desc,
  })
}

// ── DCR Client Store ────────────────────────────────────────────────

// oauthClientStore persists DCR clients to the database so they survive restarts.
// MCP clients (like Claude Code) cache their client_id across sessions.
type oauthClientStore struct {
  store *Store
}

func newOAuthClientStore(s *Store) *oauthClientStore {
  return &oauthClientStore{store: s}
}

func (cs *oauthClientStore) register(redirectURIs []string) (string, string, error) {
  id := rand.Text()
  secret := rand.Text()
  if err := cs.store.RegisterOAuthClient(id, secret, redirectURIs); err != nil {
    return "", "", err
  }
  return id, secret, nil
}

func (cs *oauthClientStore) get(id string) (string, []string, bool, error) {
  return cs.store.GetOAuthClient(id)
}

// ── MCP Auth Pending State ──────────────────────────────────────────
//
// When an MCP client hits /oauth/authorize, we store their request
// so we can resume after the upstream OAuth flow completes.

type mcpPendingAuth struct {
  // MCP client's original params
  clientID     string
  redirectURI  string
  state        string
  codeChallenge string
  codeChallengeMethod string
  resource     string // /mcp/{slug}

  // The virtual service being accessed
  serviceID  int64
  serviceURL string

  // Populated after upstream callback
  upstreamToken    string
  upstreamRefresh  string
  upstreamTokenURL string
  upstreamExpiry   time.Time
  userEmail        string
  upstreamScopes   string
}

// ── MCP Auth Code Store ─────────────────────────────────────────────
//
// After upstream callback, we issue a short-lived auth code that
// maps to the MCP pending state. The MCP client exchanges this at /oauth/token.

type mcpAuthCode struct {
  pending *mcpPendingAuth
  issued  time.Time
}

// ── OAuth Server ────────────────────────────────────────────────────

type oauthServer struct {
  clients    *oauthClientStore
  codes      map[string]*mcpAuthCode // code → pending auth
  codesMu    sync.Mutex
  server     *Server // back-reference for upstream flow
}

func newOAuthServer(s *Server) *oauthServer {
  return &oauthServer{
    clients: newOAuthClientStore(s.store),
    codes:   make(map[string]*mcpAuthCode),
    server:  s,
  }
}

// ── Auth Server Metadata ────────────────────────────────────────────

func (os *oauthServer) handleMetadata(w http.ResponseWriter, r *http.Request) {
  base := os.server.config.PublicBaseURL
  meta := &oauthex.AuthServerMeta{
    Issuer:                            base,
    AuthorizationEndpoint:             base + "/oauth/authorize",
    TokenEndpoint:                     base + "/oauth/token",
    RegistrationEndpoint:              base + "/oauth/register",
    JWKSURI:                           base + "/oauth/jwks",
    ResponseTypesSupported:            []string{"code"},
    GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
    CodeChallengeMethodsSupported:     []string{"S256"},
    TokenEndpointAuthMethodsSupported: []string{"client_secret_post", "client_secret_basic", "none"},
    ScopesSupported:                   []string{"openid", "email", "profile"},
  }
  w.Header().Set("Content-Type", "application/json")
  json.NewEncoder(w).Encode(meta)
}

// ── DCR: Dynamic Client Registration ────────────────────────────────

func (os *oauthServer) handleRegister(w http.ResponseWriter, r *http.Request) {
  if r.Method != http.MethodPost {
    oauthWriteError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
    return
  }

  var meta oauthex.ClientRegistrationMetadata
  if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
    oauthWriteError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
    return
  }
  if len(meta.RedirectURIs) == 0 {
    oauthWriteError(w, http.StatusBadRequest, "invalid_request", "redirect_uris is required")
    return
  }

  clientID, clientSecret, err := os.clients.register(meta.RedirectURIs)
  if err != nil {
    oauthWriteError(w, http.StatusInternalServerError, "server_error", "client registration failed")
    return
  }

  w.Header().Set("Content-Type", "application/json")
  w.WriteHeader(http.StatusCreated)
  json.NewEncoder(w).Encode(&oauthex.ClientRegistrationResponse{
    ClientID:     clientID,
    ClientSecret: clientSecret,
    ClientRegistrationMetadata: oauthex.ClientRegistrationMetadata{
      RedirectURIs:       meta.RedirectURIs,
      ClientName:         meta.ClientName,
      TokenEndpointAuthMethod: firstNonEmpty(meta.TokenEndpointAuthMethod, "client_secret_post"),
      GrantTypes:         []string{"authorization_code", "refresh_token"},
      ResponseTypes:      []string{"code"},
      Scope:              meta.Scope,
    },
  })
}

// ── Authorization Endpoint ──────────────────────────────────────────
//
// The MCP client sends the user here. We store their request,
// begin the upstream OAuth flow, and redirect the user's browser
// directly to the upstream provider's login page.

func (os *oauthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
  if r.Method != http.MethodGet {
    oauthWriteError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
    return
  }

  q := r.URL.Query()
  clientID := q.Get("client_id")
  redirectURI := q.Get("redirect_uri")
  state := q.Get("state")
  codeChallenge := q.Get("code_challenge")
  codeChallengeMethod := q.Get("code_challenge_method")
  resource := q.Get("resource")

  if clientID == "" || redirectURI == "" || state == "" || codeChallenge == "" {
    oauthWriteError(w, http.StatusBadRequest, "invalid_request", "missing required OAuth parameters")
    return
  }

  // Validate client
  clientSecret, clientRedirectURIs, clientOK, err := os.clients.get(clientID)
  if err != nil {
    oauthWriteError(w, http.StatusInternalServerError, "server_error", "client lookup error")
    return
  }
  if !clientOK {
    oauthWriteError(w, http.StatusBadRequest, "invalid_client", "unknown client_id")
    return
  }
  validRedirect := false
  for _, u := range clientRedirectURIs {
    if u == redirectURI {
      validRedirect = true
      break
    }
  }
  if !validRedirect {
    oauthWriteError(w, http.StatusBadRequest, "invalid_request", "invalid redirect_uri")
    return
  }
  _ = clientSecret

  // Resolve the virtual service from the resource parameter.
  // resource is like "/mcp/sharepoint" or a full URL like "https://host/mcp/sharepoint".
  slug := resource
  if strings.HasPrefix(slug, "http") {
    parts := strings.SplitN(slug, "/mcp/", 2)
    if len(parts) == 2 {
      slug = "/mcp/" + parts[1]
    }
  }
  svc, err := os.server.store.GetServiceByURL(slug)
  if err != nil {
    oauthWriteError(w, http.StatusNotFound, "invalid_scope", "virtual service not found for resource")
    return
  }

  // Store the MCP client's pending auth request.
  mcpPendingKey := rand.Text()
  mcpPending := &mcpPendingAuth{
    clientID:            clientID,
    redirectURI:        redirectURI,
    state:              state,
    codeChallenge:      codeChallenge,
    codeChallengeMethod: codeChallengeMethod,
    resource:           resource,
    serviceID:          svc.ID,
    serviceURL:        svc.URL,
  }
  os.server.mcpAuthPending.Store(mcpPendingKey, mcpPending)

  // For virtual services with API-key auth, skip the upstream OAuth flow.
  // Redirect the user to a key-entry form instead.
  if svc.Descriptor.Auth == "key" {
    stateKey := rand.Text()
    os.server.pendingMu.Lock()
    os.server.pending[stateKey] = &pendingAuth{
      serviceID:   svc.ID,
      serviceURL:  svc.URL,
      appNonce:    mcpPendingKey,
      appState:    mcpPendingKey,
      serviceType: "apikey",
    }
    os.server.pendingMu.Unlock()

    keyAuthURL := fmt.Sprintf("%s/service/apikey-auth?state=%s&service_id=%d&mcp=1",
      os.server.config.PublicBaseURL, stateKey, svc.ID)
    http.Redirect(w, r, keyAuthURL, http.StatusFound)
    return
  }

  // Begin the upstream OAuth flow.
  // We create a regular pendingAuth for the callback, using the MCP
  // pending key as the app_nonce.
  callbackRedirectURI := os.server.config.PublicBaseURL + "/service/callback"

  pa := &pendingAuth{
    serviceID:   svc.ID,
    serviceURL:  svc.URL,
    appNonce:    mcpPendingKey,
    appState:    mcpPendingKey,
    scopes:      svc.Descriptor.Scopes,
    proxied:     svc.Descriptor.Proxied,
    serviceType: "mcp",
  }

  var authURL, oauthState string

  if svc.Descriptor.Type == "oidc" {
    au, st, vf, nc, tu, err := os.server.oidcBeginAuth(r.Context(), svc, callbackRedirectURI)
    if err != nil {
      oauthWriteError(w, http.StatusInternalServerError, "server_error", fmt.Sprintf("upstream OIDC auth failed: %v", err))
      return
    }
    authURL, oauthState = au, st
    pa.verifier = vf
    pa.clientID = svc.Descriptor.ClientID
    pa.clientSecret = svc.Descriptor.ClientSecret
    pa.tokenEndpoint = tu
    pa.oidcNonce = nc
    pa.oidcIssuer = svc.URL
  } else {
    au, ci, cs, tu, st, vf, err := os.server.serviceBeginAuth(r.Context(), svc, callbackRedirectURI)
    if err != nil {
      oauthWriteError(w, http.StatusInternalServerError, "server_error", fmt.Sprintf("upstream auth failed: %v", err))
      return
    }
    authURL, oauthState = au, st
    pa.verifier = vf
    pa.clientID = ci
    pa.clientSecret = cs
    pa.tokenEndpoint = tu
  }

  os.server.pendingMu.Lock()
  os.server.pending[oauthState] = pa
  os.server.pendingMu.Unlock()

  // Redirect the user's browser directly to the upstream provider.
  http.Redirect(w, r, authURL, http.StatusFound)
}

// ── Token Endpoint ──────────────────────────────────────────────────
//
// The MCP client exchanges their auth code for a Freshbreath JWT
// that wraps the upstream access token.

func (os *oauthServer) handleToken(w http.ResponseWriter, r *http.Request) {
  if r.Method != http.MethodPost {
    oauthWriteError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
    return
  }

  if err := r.ParseForm(); err != nil {
    oauthWriteError(w, http.StatusBadRequest, "invalid_request", "invalid form data")
    return
  }

  grantType := r.Form.Get("grant_type")
  if grantType != "authorization_code" {
    oauthWriteError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
    return
  }

  code := r.Form.Get("code")
  verifier := r.Form.Get("code_verifier")
  if code == "" || verifier == "" {
    oauthWriteError(w, http.StatusBadRequest, "invalid_request", "missing code or code_verifier")
    return
  }

  // Authenticate the client
  clientID, clientSecret, hasBasic := r.BasicAuth()
  if !hasBasic {
    clientID = r.Form.Get("client_id")
    clientSecret = r.Form.Get("client_secret")
  }
  storedSecret, _, clientOK, err := os.clients.get(clientID)
  if err != nil {
    oauthWriteError(w, http.StatusInternalServerError, "server_error", "client lookup error")
    return
  }
  if !clientOK || (clientSecret != "" && storedSecret != clientSecret) {
    oauthWriteError(w, http.StatusUnauthorized, "invalid_client", "invalid client credentials")
    return
  }

  // Look up the auth code
  os.codesMu.Lock()
  mcpCode, ok := os.codes[code]
  if ok {
    delete(os.codes, code)
  }
  os.codesMu.Unlock()

  if !ok {
    oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired authorization code")
    return
  }
  if time.Since(mcpCode.issued) > 10*time.Minute {
    oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "authorization code expired")
    return
  }

  pending := mcpCode.pending

  // Verify PKCE
  if pending.codeChallengeMethod != "S256" {
    oauthWriteError(w, http.StatusBadRequest, "invalid_request", "unsupported code_challenge_method")
    return
  }
  if !verifyPKCE(verifier, pending.codeChallenge) {
    oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
    return
  }

  // Verify client matches
  if pending.clientID != clientID {
    oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")
    return
  }

  // Mint a Freshbreath JWT wrapping the upstream token
  jwt, err := os.mintWrappedToken(pending)
  if err != nil {
    oauthWriteError(w, http.StatusInternalServerError, "server_error", "token issuance failed")
    return
  }

  expiresIn := int(time.Until(pending.upstreamExpiry).Seconds())
  if expiresIn < 0 {
    expiresIn = 0
  }

  w.Header().Set("Content-Type", "application/json")
  json.NewEncoder(w).Encode(map[string]interface{}{
    "access_token": jwt,
    "token_type":   "Bearer",
    "expires_in":   expiresIn,
    "scope":        pending.upstreamScopes,
  })
}

// ── JWKS Endpoint ───────────────────────────────────────────────────
//
// Serves the public key for verifying Freshbreath JWTs.
// Since we use HMAC-SHA256, there's no public key — but the MCP spec
// requires jwks_uri. We serve an empty key set. Token verification
// is done by Freshbreath itself (not by the MCP client).

func (os *oauthServer) handleJWKS(w http.ResponseWriter, r *http.Request) {
  w.Header().Set("Content-Type", "application/json")
  json.NewEncoder(w).Encode(map[string]interface{}{
    "keys": []interface{}{},
  })
}

// ── JWT Minting ─────────────────────────────────────────────────────
//
// Mint a Freshbreath JWT that wraps the upstream access token as claims.
// The MCP client holds this as an opaque Bearer token. When it comes
// back to /mcp/{name}, we crack it open to get the real upstream token.

func (os *oauthServer) mintWrappedToken(pending *mcpPendingAuth) (string, error) {
  sig, err := jose.NewSigner(
    jose.SigningKey{Algorithm: jose.HS256, Key: os.server.localKey},
    (&jose.SignerOptions{}).WithType("JWT"),
  )
  if err != nil {
    return "", err
  }

  claims := wrappedTokenClaims{
    Claims: josejwt.Claims{
      Issuer:   "freshbreath",
      Subject:  pending.userEmail,
      Audience: josejwt.Audience{"freshbreath"},
      IssuedAt: josejwt.NewNumericDate(time.Now()),
      Expiry:   josejwt.NewNumericDate(pending.upstreamExpiry),
    },
    ServiceID:        pending.serviceID,
    UpstreamToken:    pending.upstreamToken,
    UpstreamRefresh:  pending.upstreamRefresh,
    UpstreamTokenURL: pending.upstreamTokenURL,
    UpstreamScopes:   pending.upstreamScopes,
  }

  return josejwt.Signed(sig).Claims(claims).Serialize()
}

type wrappedTokenClaims struct {
  josejwt.Claims
  ServiceID        int64  `json:"service_id"`
  UpstreamToken    string `json:"upstream_token"`
  UpstreamRefresh  string `json:"upstream_refresh,omitempty"`
  UpstreamTokenURL string `json:"upstream_token_url,omitempty"`
  UpstreamScopes   string `json:"upstream_scopes,omitempty"`
}

// verifyAndUnwrapToken checks if a Bearer token is a Freshbreath-wrapped JWT
// and returns the full claims if so. Returns (nil, nil) if the token is not
// a Freshbreath JWT — the caller should use the raw token as-is.
func (s *Server) verifyAndUnwrapToken(raw string, expectedServiceID int64) (*wrappedTokenClaims, error) {
  if !isFreshbreathToken(raw) {
    return nil, nil
  }

  tok, err := josejwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.HS256})
  if err != nil {
    return nil, fmt.Errorf("parse wrapped token: %w", err)
  }

  var claims wrappedTokenClaims
  if err := tok.Claims(s.localKey, &claims); err != nil {
    return nil, fmt.Errorf("verify wrapped token: %w", err)
  }

  if err := claims.Claims.Validate(josejwt.Expected{
    Issuer:      "freshbreath",
    AnyAudience: josejwt.Audience{"freshbreath"},
    Time:        time.Now(),
  }); err != nil {
    return nil, fmt.Errorf("wrapped token invalid: %w", err)
  }

  if claims.ServiceID != expectedServiceID {
    return nil, fmt.Errorf("wrapped token service_id mismatch")
  }

  return &claims, nil
}

// ── PKCE Verification ───────────────────────────────────────────────

func verifyPKCE(verifier, challenge string) bool {
  // S256: BASE64URL(SHA256(verifier))
  h := sha256.Sum256([]byte(verifier))
  computed := base64.RawURLEncoding.EncodeToString(h[:])
  return computed == challenge
}
