package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mileusna/useragent"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"poggers.institute/freshbreath/internal/db"
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
	store *db.Store
}

func newOAuthClientStore(s *db.Store) *oauthClientStore {
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
	clientID            string
	redirectURI         string
	state               string
	codeChallenge       string
	codeChallengeMethod string
	resource            string // /mcp/{slug} or /mcp

	serviceID int64 // the mounted virtual service; 0 for the central MCP

	// Populated when the login legs finish.
	fbToken     string
	refreshData freshbreathRefreshData
	expiresAt   time.Time // pendingAuthTTL from creation; link usable until then
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
	clients *oauthClientStore
	codes   map[string]*mcpAuthCode // code → pending auth
	codesMu sync.Mutex
	server  *Server // back-reference for upstream flow
}

func newOAuthServer(s *Server) *oauthServer {
	return &oauthServer{
		clients: newOAuthClientStore(s.store),
		codes:   make(map[string]*mcpAuthCode),
		server:  s,
	}
}

// sweepsExpiredCodes drops exchanged-or-stale auth codes; called lazily on
// store/lookup, same janitor style as sweepPendingLocked.
func (os *oauthServer) sweepExpiredCodes(now time.Time) {
	os.codesMu.Lock()
	defer os.codesMu.Unlock()
	for k, c := range os.codes {
		if now.Sub(c.issued) > 10*time.Minute {
			delete(os.codes, k)
		}
	}
}

// issueCode mints a one-shot authorization code for a finished MCP login.
func (os *oauthServer) issueCode(m *mcpPendingAuth) string {
	code := rand.Text()
	os.sweepExpiredCodes(time.Now())
	os.codesMu.Lock()
	os.codes[code] = &mcpAuthCode{pending: m, issued: time.Now()}
	os.codesMu.Unlock()
	return code
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
			RedirectURIs:            meta.RedirectURIs,
			ClientName:              meta.ClientName,
			TokenEndpointAuthMethod: firstNonEmpty(meta.TokenEndpointAuthMethod, "client_secret_post"),
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			Scope:                   meta.Scope,
		},
	})
}

// ── Authorization Endpoint ──────────────────────────────────────────
//
// The MCP client sends the user here. We store their request,
// begin the upstream auth flow, and redirect the user's browser
// to the appropriate login page (upstream provider, SSH form, or API key form).

func (os *oauthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		os.handleAuthorizeStart(w, r)
	case http.MethodPost:
		os.handleAuthorizeContinue(w, r)
	default:
		oauthWriteError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
	}
}

// handleAuthorizeStart validates the MCP client's request, resolves the
// resource's inbound gate (and outbound leg, if any), and answers with the
// leg-skip interstitial: a same-origin page that offers any frbr:auth:*
// tokens this browser already holds before falling back to a fresh login.
// A 302 can't read localStorage; a page can.
func (os *oauthServer) handleAuthorizeStart(w http.ResponseWriter, r *http.Request) {
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
	_, clientRedirectURIs, clientOK, err := os.clients.get(clientID)
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

	// Resolve slug from resource parameter.
	// resource is like "/mcp/sharepoint" or a full URL like "https://host/mcp/sharepoint".
	// The special resource "/mcp" (no slug) routes to the central MCP server.
	slug := resource
	if strings.HasPrefix(slug, "http") {
		parts := strings.SplitN(slug, "/mcp", 2)
		if len(parts) == 2 {
			rest := parts[1]
			if rest == "" || rest == "/" {
				slug = "/mcp"
			} else {
				slug = "/mcp" + rest
			}
		}
	}

	// Resolve the gate: the admin record for the central MCP, the mounted
	// service's inbound gate otherwise. This is the same authorization
	// /service/login enforces — a flow can only target a resource that is
	// actually exposed, against that resource's own gate.
	var gate *db.AuthRecord
	var svc *db.Service
	var serviceID int64

	if slug == "/mcp" {
		gate, err = os.server.adminAuthRecord()
		if err != nil {
			oauthWriteError(w, http.StatusInternalServerError, "server_error", fmt.Sprintf("admin auth: %v", err))
			return
		}
		if gate == nil {
			oauthWriteError(w, http.StatusForbidden, "invalid_scope", "admin auth not configured — log in to the control panel first")
			return
		}
	} else {
		svc, err = os.server.store.GetServiceByURL(slug)
		if err != nil || svc.Descriptor.Type != "virtual" {
			oauthWriteError(w, http.StatusNotFound, "invalid_scope", "virtual service not found for resource")
			return
		}
		serviceID = svc.ID
		gate, err = os.server.resolveServiceGate(svc)
		if err != nil {
			oauthWriteError(w, http.StatusInternalServerError, "server_error", fmt.Sprintf("gate resolution: %v", err))
			return
		}
	}

	legs, err := os.server.legsForLogin(gate, svc)
	if err != nil {
		oauthWriteError(w, http.StatusInternalServerError, "server_error", fmt.Sprintf("legs resolution: %v", err))
		return
	}
	if len(legs) == 0 {
		// An anonymous mount serves no PRM and needs no flow — a client
		// that starts one anyway is confused.
		oauthWriteError(w, http.StatusBadRequest, "invalid_request", "resource requires no authorization")
		return
	}

	mcpKey := rand.Text()
	os.server.putMCPPending(mcpKey, &mcpPendingAuth{
		clientID:            clientID,
		redirectURI:         redirectURI,
		state:               state,
		codeChallenge:       codeChallenge,
		codeChallengeMethod: codeChallengeMethod,
		resource:            resource,
		serviceID:           serviceID,
	})

	contState := db.GenNonce()
	os.server.putPending(contState, &pendingAuth{
		mcpKey:    mcpKey,
		legs:      legs,
		primaryID: legs[len(legs)-1].ID,
	})

	var recIDs []string
	for _, rec := range legs {
		recIDs = append(recIDs, strconv.FormatInt(rec.ID, 10))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := strings.Replace(authorizeInterstitialHTML, "{{STATE}}", contState, 1)
	page = strings.Replace(page, "{{RECORD_IDS}}", strings.Join(recIDs, ","), 1)
	w.Write([]byte(page))
}

// handleAuthorizeContinue receives the interstitial's POST: the state plus
// any candidate tokens read from this browser's store. Tokens are claims,
// not facts — each is re-verified and checked against the leg's record
// binding before it skips anything. Whatever remains uncovered runs as a
// fresh login flow.
func (os *oauthServer) handleAuthorizeContinue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State  string   `json:"state"`
		Tokens []string `json:"tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		oauthWriteError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
		return
	}
	p, ok, expired := os.server.getPending(req.State)
	ok = ok && p.mcpKey != "" && p.at == 0 && len(p.done) == 0
	if !ok {
		msg := "unknown auth state"
		if expired {
			msg = "auth state expired — restart the login flow"
		}
		oauthWriteError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}

	// Verify candidate tokens (cap: one per leg is all that's useful).
	var verified []*freshbreathClaims
	for i, raw := range req.Tokens {
		if i >= 5 {
			break
		}
		if claims, err := os.server.verifyFreshbreathToken(raw); err == nil && claims != nil {
			verified = append(verified, claims)
		}
	}

	var done []*completedLeg
	var todo []*db.AuthRecord
	for _, rec := range p.legs {
		covered := false
		for _, claims := range verified {
			if legCovered(claims, rec) {
				done = append(done, os.server.legFromClaims(rec, claims))
				covered = true
				break
			}
		}
		if !covered {
			todo = append(todo, rec)
		}
	}
	p.legs, p.at, p.done = todo, 0, done

	w.Header().Set("Content-Type", "application/json")
	if len(todo) == 0 {
		redirect, err := os.server.finishLogin(w, r, p)
		if err != nil || redirect == "" {
			oauthWriteError(w, http.StatusInternalServerError, "server_error", fmt.Sprintf("login completion: %v", err))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"redirect": redirect})
		return
	}

	next, err := os.server.beginLeg(r.Context(), p)
	if err != nil {
		oauthWriteError(w, http.StatusInternalServerError, "server_error", fmt.Sprintf("begin leg: %v", err))
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"redirect": next})
}

// authorizeInterstitialHTML is the leg-skip page: same-origin, so it can
// read the frbr:auth:* store that apps on this host share. It offers any
// live tokens for the records this flow needs and follows the server's
// verdict.
const authorizeInterstitialHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Authorizing — Fresh Breath</title>
<style>
  body{font-family:system-ui,-apple-system,sans-serif;background:#0f0f11;color:#e4e4e7;display:grid;place-items:center;min-height:100vh}
  .card{text-align:center;color:#a1a1aa;font-size:14px}
</style></head><body>
<div class="card"><p>Checking your session…</p></div>
<script>
(function(){
  var state = "{{STATE}}";
  var ids = "{{RECORD_IDS}}".split(",").filter(Boolean);
  var tokens = [];
  for (var i = 0; i < ids.length; i++) {
    try {
      var raw = localStorage.getItem("frbr:auth:" + ids[i]);
      if (!raw) continue;
      var entry = JSON.parse(raw);
      if (entry && entry.v === 1 && entry.access_token &&
          entry.expires_at && new Date(entry.expires_at) > new Date() &&
          tokens.indexOf(entry.access_token) < 0) {
        tokens.push(entry.access_token);
      }
    } catch (e) {}
  }
  fetch("/oauth/authorize", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({state: state, tokens: tokens})
  }).then(function(r){ return r.json(); }).then(function(d){
    if (d.redirect) { window.location.href = d.redirect; }
    else { document.querySelector(".card").innerHTML = "<p>" + (d.error_description || "Authorization failed") + "</p>"; }
  }).catch(function(){
    document.querySelector(".card").innerHTML = "<p>Network error</p>";
  });
})();
</script></body></html>`

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
	switch grantType {
	case "authorization_code":
		os.handleAuthorizationCodeGrant(w, r)
	case "refresh_token":
		os.handleRefreshTokenGrant(w, r)
	default:
		oauthWriteError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
	}
}

// ── Authorization Code Grant ────────────────────────────────────────

func (os *oauthServer) handleAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request) {
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
	// An empty secret is accepted intentionally: DCR is open, so client_id/secret
	// pairs aren't a trust boundary — security rests on mandatory PKCE and the
	// redirect_uri allowlist. Claude Code and other public PKCE clients omit the
	// secret. We still reject a *wrong* secret when one is presented. Don't
	// "harden" this into a required-secret check; it would break those clients
	// without adding protection PKCE doesn't already provide.
	if !clientOK || (clientSecret != "" && storedSecret != clientSecret) {
		oauthWriteError(w, http.StatusUnauthorized, "invalid_client", "invalid client credentials")
		return
	}

	// Look up the auth code
	os.sweepExpiredCodes(time.Now())
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

	// The access token was minted when the login legs finished; the code
	// grant just delivers it and opens a refresh family.
	if pending.fbToken == "" {
		oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "login incomplete")
		return
	}
	refreshData := pending.refreshData

	deviceLabel := deviceLabelFromUA(r.UserAgent())
	familyID, jti, err := os.server.newRefreshFamily(refreshData.Subject, refreshData.AuthID, deviceLabel)
	if err != nil {
		oauthWriteError(w, http.StatusInternalServerError, "server_error", "family creation failed")
		return
	}
	refreshData.FamilyID = familyID
	refreshData.JTI = jti

	// Initial issuance is consumed by the client exchanging the code
	// (CLI/MCP), so the refresh token goes in the body.
	os.writeTokenResponse(w, pending.fbToken, refreshData, "", true)
}

// ── Refresh Token Grant ─────────────────────────────────────────────
//
// Accepts a refresh token (form body or HttpOnly cookie) and issues
// a new access + refresh token pair. The grant dispatches on the
// refresh data's Kind to re-mint the right access token:
//
//   "wrapped"  — refresh upstream, re-wrap, new pair
//   "identity" — look up user, re-mint identity token, new pair

func newJTI() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// deviceLabelFromUA renders a User-Agent as a short, human-readable device
// label like "Chrome on Linux", falling back to whichever half is known, and
// finally to a truncated copy of the raw UA when the parser can't identify it.
// Returns "" for an empty UA. We deliberately label by browser/OS only — no IP
// or other network identifier — to keep this free of casual PII.
func deviceLabelFromUA(ua string) string {
	if ua == "" {
		return ""
	}
	a := useragent.Parse(ua)
	browser := a.Name
	// When the library can't identify the browser it dumps the raw UA into Name;
	// a clean browser name never contains these characters.
	if browser == ua || strings.ContainsAny(browser, "/()") {
		browser = ""
	}
	switch {
	case browser != "" && a.OS != "":
		return browser + " on " + a.OS
	case browser != "":
		return browser
	case a.OS != "":
		return a.OS
	default:
		return truncateRunes(ua, 80)
	}
}

// truncateRunes caps s at max bytes without splitting a multi-byte rune.
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func (os *oauthServer) handleRefreshTokenGrant(w http.ResponseWriter, r *http.Request) {
	// Accept refresh token from form body or cookie. The source tells us the
	// client kind: a form body means a CLI/MCP client (no cookie jar, reads the
	// new token from the response body); a cookie means a browser flow (rides
	// the rotated HttpOnly cookie, must not get a readable body copy).
	rt := r.Form.Get("refresh_token")
	fromForm := rt != ""
	if rt == "" {
		if c, err := r.Cookie("refresh_token"); err == nil {
			rt = c.Value
		}
	}
	if rt == "" {
		oauthWriteError(w, http.StatusBadRequest, "invalid_request", "missing refresh_token")
		return
	}

	data, err := os.server.verifyRefreshToken(rt)
	if err != nil {
		oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired refresh token")
		return
	}

	// Token-family rotation. Legacy stateless tokens (no family_id) fail
	// gracefully → re-login required.
	if data.FamilyID == "" {
		oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "refresh token has no family — re-login required")
		return
	}
	fam, ok, err := os.server.store.GetRefreshFamily(data.FamilyID)
	if err != nil {
		oauthWriteError(w, http.StatusInternalServerError, "server_error", "family lookup failed")
		return
	}
	if !ok || fam.Revoked {
		oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "refresh family revoked or not found")
		return
	}
	if fam.ExpiresAt.Before(time.Now()) {
		oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "refresh family expired")
		return
	}
	// 🔒 Binding invariant: the family must match the token's bound record.
	if fam.AuthID != data.AuthID {
		oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "refresh token auth record mismatch")
		return
	}

	// 🔒 Cookie-path authorization. The cookie is SameSite=None (file:// and
	// foreign-origin apps need it), so it attaches to any cross-site request
	// the browser makes to this path. The CORS origin allowlist gates *which
	// origins* may call us at all, but a registered app is still allowed to
	// call — so on the cookie path only (CLI/MCP carry the token in the body
	// and are authorized by possession), the requesting app must actually
	// stand in some relation to the token's auth record: its own gate, or
	// the gate/acts_as of a service it is allowed to use. The token's own
	// data.AuthID is the source of truth — there is no request parameter on
	// this path to spoof.
	if !fromForm {
		appNonce := r.Header.Get("X-App-Nonce")
		if appNonce == "" {
			oauthWriteError(w, http.StatusForbidden, "invalid_grant", "missing app for cookie refresh")
			return
		}
		if !os.server.appMayRefreshRecord(appNonce, data.AuthID) {
			oauthWriteError(w, http.StatusForbidden, "invalid_grant", "app not permitted for this auth record")
			return
		}
	}

	// 🔒 Path/record binding (defense in depth). On the path-scoped route
	// /oauth/token/{authID} the cookie jar routes the right cookie by path;
	// the path segment must agree with the token's sealed record, so a
	// cookie that leaks onto another record's path is rejected at the
	// refresh step rather than minting a token for the wrong record.
	if aidStr := r.PathValue("authID"); aidStr != "" {
		pathAuth, err := strconv.ParseInt(aidStr, 10, 64)
		if err != nil || pathAuth != data.AuthID {
			oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "refresh token auth record mismatch")
			return
		}
	}

	var nextJTI string
	switch {
	case data.JTI == fam.CurrentJTI:
		// Happy path: rotate the family atomically.
		nextJTI = newJTI()
		if ok, err := os.server.store.RotateRefreshFamily(fam.ID, fam.CurrentJTI, nextJTI, time.Now()); err != nil {
			oauthWriteError(w, http.StatusInternalServerError, "server_error", "rotation failed")
			return
		} else if !ok {
			// Lost the race; someone else rotated. Re-read and retry.
			fam2, ok2, err2 := os.server.store.GetRefreshFamily(data.FamilyID)
			if err2 != nil || !ok2 {
				oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "family concurrent update")
				return
			}
			if data.JTI == fam2.PrevJTI && !fam2.RotatedAt.IsZero() && time.Since(fam2.RotatedAt) <= refreshGraceWindow {
				// Grace-window: accept without a second rotate.
				nextJTI = fam2.CurrentJTI
			} else {
				oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "refresh token rotated elsewhere")
				return
			}
		}
	case data.JTI == fam.PrevJTI && !fam.RotatedAt.IsZero() && time.Since(fam.RotatedAt) <= refreshGraceWindow:
		// Grace-window retry: the client sent the previous token within the
		// grace period. Re-issue the current pair without rotating again.
		nextJTI = fam.CurrentJTI
	case data.JTI == fam.PrevJTI:
		// Reuse detected: the previous token is outside the grace window.
		// Revoke the family and reject.
		_ = os.server.store.RevokeRefreshFamily(fam.ID)
		oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "refresh token reuse detected")
		return
	default:
		// Stale or unknown jti — possible theft.
		_ = os.server.store.RevokeRefreshFamily(fam.ID)
		oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "refresh token reuse detected")
		return
	}

	accessToken, newRefreshData, err := os.refreshLegs(data)
	if err != nil {
		oauthWriteError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	// Stamp the new family state into the refresh token.
	newRefreshData.FamilyID = fam.ID
	newRefreshData.JTI = nextJTI

	os.writeTokenResponse(w, accessToken, newRefreshData, "", fromForm)
}

// refreshLegs re-mints an access token from refresh data: the identity is
// re-resolved from the subject (a deleted user can't extend a session; a
// role change propagates within one cycle), and every upstream leg with a
// refresh token is rotated against its record's provider. An upstream that
// refuses costs the whole refresh — re-login is the honest answer.
func (os *oauthServer) refreshLegs(data *freshbreathRefreshData) (string, freshbreathRefreshData, error) {
	s := os.server

	var email, role, name string
	if strings.HasPrefix(data.Subject, "frbr:") {
		user, err := s.userFromSubject(data.Subject)
		if err != nil || user == nil {
			return "", freshbreathRefreshData{}, fmt.Errorf("user not found for %s", data.Subject)
		}
		email, role, name = user.Email, user.Role, user.Name
	} else {
		email = data.UserEmail
	}

	creds := sealedCreds{}
	newUpstreams := map[string]upstreamRefreshLeg{}
	for provider, leg := range data.Upstreams {
		rec, err := s.store.GetAuthRecord(leg.AuthID)
		if err != nil {
			return "", freshbreathRefreshData{}, fmt.Errorf("auth record for %s: %w", provider, err)
		}
		tokenEndpoint := leg.TokenURL
		if tokenEndpoint == "" {
			tokenEndpoint = rec.Descriptor.TokenURL
		}
		if tokenEndpoint == "" {
			return "", freshbreathRefreshData{}, fmt.Errorf("no token endpoint for %s — re-login required", provider)
		}

		form := url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {leg.RefreshToken},
			"client_id":     {rec.Descriptor.ClientID},
		}
		if rec.Descriptor.ClientSecret != "" {
			form.Set("client_secret", rec.Descriptor.ClientSecret)
		}
		if leg.Scopes != "" {
			form.Set("scope", leg.Scopes)
		}
		resp, err := s.httpClient.PostForm(tokenEndpoint, form)
		if err != nil {
			return "", freshbreathRefreshData{}, fmt.Errorf("upstream refresh for %s: %w", provider, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", freshbreathRefreshData{}, fmt.Errorf("upstream refresh for %s returned %d: %s", provider, resp.StatusCode, string(body))
		}
		var tok struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			Scope        string `json:"scope"`
		}
		if err := json.Unmarshal(body, &tok); err != nil {
			return "", freshbreathRefreshData{}, fmt.Errorf("decode upstream response for %s: %w", provider, err)
		}

		newRefresh := tok.RefreshToken
		if newRefresh == "" {
			newRefresh = leg.RefreshToken
		}
		scopes := tok.Scope
		if scopes == "" {
			scopes = leg.Scopes
		}
		creds[provider] = sealedUpstreamData{
			UpstreamToken:    tok.AccessToken,
			UpstreamRefresh:  newRefresh,
			UpstreamTokenURL: tokenEndpoint,
			UpstreamScopes:   scopes,
		}
		newUpstreams[provider] = upstreamRefreshLeg{
			AuthID:       leg.AuthID,
			RefreshToken: newRefresh,
			TokenURL:     tokenEndpoint,
			Scopes:       scopes,
		}
	}

	accessToken, err := s.mintFreshbreathToken(data.Subject, email, role, name, data.AuthID, data.Legs, creds)
	if err != nil {
		return "", freshbreathRefreshData{}, fmt.Errorf("mint access token: %w", err)
	}
	newData := freshbreathRefreshData{
		Subject:   data.Subject,
		UserEmail: email,
		AuthID:    data.AuthID,
		Legs:      data.Legs,
	}
	if len(newUpstreams) > 0 {
		newData.Upstreams = newUpstreams
	}
	return accessToken, newData, nil
}

// ── Token Response Helper ───────────────────────────────────────────
//
// Mint a refresh token, set the HttpOnly cookie, and write the JSON response.

// writeTokenResponse always rotates the refresh token into a fresh HttpOnly
// cookie. The body copy is gated on deliverRefreshInBody: CLI/MCP clients have
// no cookie jar and read it from the body (OAuth's standard contract), but
// browser flows ride the cookie, so echoing it there would only hand a
// readable copy to any script on the page — defeating HttpOnly. Callers pass
// false for cookie-sourced (browser) refreshes.
func (os *oauthServer) writeTokenResponse(w http.ResponseWriter, accessToken string, refreshData freshbreathRefreshData, scope string, deliverRefreshInBody bool) {
	expiresIn := int(accessTokenTTL.Seconds())

	rt, err := os.server.makeRefreshCookie(w, refreshData)
	if err != nil {
		oauthWriteError(w, http.StatusInternalServerError, "server_error", "refresh token issuance failed")
		return
	}

	resp := map[string]interface{}{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
		"scope":        scope,
	}
	if deliverRefreshInBody {
		resp["refresh_token"] = rt
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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

// appMayRefreshRecord reports whether an app stands in some relation to an
// auth record: its own gate resolves to it, or one of its allowed services
// carries it in either slot. The admin nonce answers only for the admin
// record.
func (s *Server) appMayRefreshRecord(appNonce string, authID int64) bool {
	if appNonce == s.adminNonce {
		rec, err := s.adminAuthRecord()
		return err == nil && rec != nil && rec.ID == authID
	}
	app, err := s.store.GetApp(appNonce)
	if err != nil {
		return false
	}
	if gate, err := s.resolveAppGate(app); err == nil && gate != nil && gate.ID == authID {
		return true
	}
	links, err := s.store.GetAppServiceLinks(appNonce)
	if err != nil {
		return false
	}
	for _, link := range links {
		if !link.Allowed {
			continue
		}
		svc, err := s.store.GetService(link.ServiceID)
		if err != nil {
			continue
		}
		if (svc.ActsAs != nil && *svc.ActsAs == authID) || (svc.ProtectedBy != nil && *svc.ProtectedBy == authID) {
			return true
		}
	}
	return false
}

// ── PKCE Verification ───────────────────────────────────────────────

func verifyPKCE(verifier, challenge string) bool {
	// S256: BASE64URL(SHA256(verifier))
	h := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	return computed == challenge
}
