package server

import (
	"context"
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
	resource            string // /mcp/{slug}

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
	expiresAt        time.Time // pendingAuthTTL from creation; link usable until then
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

	// Resolve the service to authenticate against.
	// Central (/mcp) uses the admin auth service; virtual (/mcp/{slug}) uses the named service.
	var svc *db.Service
	var mcpServiceURL, serviceType string

	if slug == "/mcp" {
		svcIDStr, err := os.server.store.GetSetting("admin_auth_service")
		if err != nil || svcIDStr == "" {
			oauthWriteError(w, http.StatusForbidden, "invalid_scope", "admin auth not configured — log in to the control panel first")
			return
		}
		svcID, err := parseID(svcIDStr)
		if err != nil {
			oauthWriteError(w, http.StatusInternalServerError, "server_error", "invalid admin auth service ID")
			return
		}
		svc, err = os.server.store.GetService(svcID)
		if err != nil {
			oauthWriteError(w, http.StatusInternalServerError, "server_error", "admin auth service not found")
			return
		}
		mcpServiceURL = "/mcp"
		serviceType = "mcp-central"
	} else {
		svc, err = os.server.store.GetServiceByURL(slug)
		if err != nil {
			oauthWriteError(w, http.StatusNotFound, "invalid_scope", "virtual service not found for resource")
			return
		}
		mcpServiceURL = svc.URL
		serviceType = "mcp-endpoint"
	}

	// Store the MCP client's pending auth request.
	mcpPendingKey := rand.Text()
	os.server.putMCPPending(mcpPendingKey, &mcpPendingAuth{
		clientID:            clientID,
		redirectURI:         redirectURI,
		state:               state,
		codeChallenge:       codeChallenge,
		codeChallengeMethod: codeChallengeMethod,
		resource:            resource,
		serviceID:           svc.ID,
		serviceURL:          mcpServiceURL,
	})

	callbackRedirectURI := os.server.config.PublicBaseURL + "/service/callback"

	// SSH — redirect to passphrase form.
	if svc.Descriptor.Type == "ssh" {
		stateKey := rand.Text()
		os.server.putPending(stateKey, &pendingAuth{
			serviceID:   svc.ID,
			serviceURL:  svc.URL,
			appNonce:    mcpPendingKey,
			appState:    mcpPendingKey,
			serviceType: "ssh",
		})
		http.Redirect(w, r, fmt.Sprintf("%s/service/ssh-auth?state=%s&service_id=%d&mcp=1",
			os.server.config.PublicBaseURL, stateKey, svc.ID), http.StatusFound)
		return
	}

	// API key — redirect to key entry form.
	if svc.Descriptor.Auth == "key" {
		stateKey := rand.Text()
		os.server.putPending(stateKey, &pendingAuth{
			serviceID:   svc.ID,
			serviceURL:  svc.URL,
			appNonce:    mcpPendingKey,
			appState:    mcpPendingKey,
			serviceType: "apikey",
		})
		http.Redirect(w, r, fmt.Sprintf("%s/service/apikey-auth?state=%s&service_id=%d&mcp=1",
			os.server.config.PublicBaseURL, stateKey, svc.ID), http.StatusFound)
		return
	}

	// OIDC or generic OAuth — begin upstream auth flow.
	pa := &pendingAuth{
		serviceID:   svc.ID,
		serviceURL:  svc.URL,
		appNonce:    mcpPendingKey,
		appState:    mcpPendingKey,
		scopes:      svc.Descriptor.Scopes,
		proxied:     svc.Descriptor.Proxied,
		serviceType: serviceType,
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

	os.server.putPending(oauthState, pa)

	http.Redirect(w, r, authURL, http.StatusFound)
}

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

	// Mint a Freshbreath access token for all cases.
	var accessToken string
	var refreshData freshbreathRefreshData
	if isFreshbreathToken(pending.upstreamToken) {
		// Token already issued by Freshbreath — pass it through.
		accessToken = pending.upstreamToken
		existing, _ := os.server.verifyFreshbreathToken(pending.upstreamToken)
		refreshData = freshbreathRefreshData{
			Kind:      "identity",
			ServiceID: existing.ServiceID, // auth service the identity is bound to
			UserEmail: pending.userEmail,
			UserRole:  existing.UserRole,
			UserName:  existing.UserName,
		}
	} else {
		// External OAuth — wrap with sealed upstream data.
		upstream := &sealedUpstreamData{
			UpstreamToken:    pending.upstreamToken,
			UpstreamRefresh:  pending.upstreamRefresh,
			UpstreamTokenURL: pending.upstreamTokenURL,
			UpstreamScopes:   pending.upstreamScopes,
		}
		jwt, err := os.server.mintFreshbreathToken("wrapped", pending.userEmail, "", "", pending.serviceID, upstream)
		if err != nil {
			oauthWriteError(w, http.StatusInternalServerError, "server_error", "token issuance failed")
			return
		}
		accessToken = jwt
		refreshData = freshbreathRefreshData{
			Kind:             "wrapped",
			ServiceID:        pending.serviceID,
			UserEmail:        pending.userEmail,
			UpstreamRefresh:  pending.upstreamRefresh,
			UpstreamTokenURL: pending.upstreamTokenURL,
			UpstreamScopes:   pending.upstreamScopes,
		}
	}

	// Create a refresh family for this login session.
	deviceLabel := deviceLabelFromUA(r.UserAgent())
	familyID, jti, err := os.server.newRefreshFamily(pending.userEmail, pending.serviceID, deviceLabel)
	if err != nil {
		oauthWriteError(w, http.StatusInternalServerError, "server_error", "family creation failed")
		return
	}
	refreshData.FamilyID = familyID
	refreshData.JTI = jti

	// Mint a refresh token and write the response. Initial issuance is consumed
	// by the client exchanging the code (CLI/MCP), so the refresh token goes in
	// the body.
	os.writeTokenResponse(w, accessToken, refreshData, pending.upstreamScopes, true)
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
	// 🔒 Binding invariant: the family must match the token's bound service.
	if fam.ServiceID != data.ServiceID {
		oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "refresh token service mismatch")
		return
	}

	// 🔒 Cookie-path authorization. The cookie is SameSite=None (file:// and
	// foreign-origin apps need it), so it attaches to any cross-site request the
	// browser makes to this path. The CORS origin allowlist gates *which origins*
	// may call us at all, but a registered app is still allowed to call — so on
	// its own the allowlist can't stop app A from harvesting a token issued for
	// app A's connection to service X, or stop one app's page from refreshing
	// another app's cookie. So on the cookie path only (CLI/MCP carry the token
	// in the body and are authorized by possession), we bind the request's app
	// to the token's sealed service: the app must be permitted that service.
	// The token's own data.ServiceID is the source of truth — there is no
	// service_id request parameter on this path to spoof.
	if !fromForm {
		appNonce := r.Header.Get("X-App-Nonce")
		if appNonce == "" {
			oauthWriteError(w, http.StatusForbidden, "invalid_grant", "missing app for cookie refresh")
			return
		}
		if appNonce == os.server.adminNonce {
			// The admin control panel is the only consumer of the ephemeral
			// adminNonce; it only ever holds an identity token for the
			// configured admin auth service. Restrict it to that service
			// rather than blanket-trusting it, so a leaked adminNonce can't
			// refresh arbitrary services' tokens.
			svcIDStr, _ := os.server.store.GetSetting("admin_auth_service")
			adminSvcID, err := parseID(svcIDStr)
			if err != nil || data.ServiceID != adminSvcID {
				oauthWriteError(w, http.StatusForbidden, "invalid_grant", "admin refresh not bound to admin auth service")
				return
			}
		} else {
			allowed, err := os.server.store.IsServiceAllowedForApp(appNonce, data.ServiceID)
			if err != nil || !allowed {
				oauthWriteError(w, http.StatusForbidden, "invalid_grant", "app not permitted for this service")
				return
			}
		}
	}

	// 🔒 Path/service binding (defense in depth). On the path-scoped route
	// /oauth/token/{serviceID} the cookie jar routes the right cookie by path;
	// the path segment must agree with the token's sealed service, so a stale
	// legacy cookie (Path=/oauth/token, from before path-scoping) that leaks
	// onto a /oauth/token/<id> request is rejected at the refresh step rather
	// than minting a token for the wrong service.
	if sidStr := r.PathValue("serviceID"); sidStr != "" {
		pathSvc, err := strconv.ParseInt(sidStr, 10, 64)
		if err != nil || pathSvc != data.ServiceID {
			oauthWriteError(w, http.StatusBadRequest, "invalid_grant", "refresh token service mismatch")
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

	var accessToken string
	var newRefreshData freshbreathRefreshData
	var scope string

	switch data.Kind {
	case "wrapped":
		accessToken, newRefreshData, scope, err = os.refreshWrapped(data)
	case "identity":
		accessToken, newRefreshData, err = os.refreshIdentity(data)
	default:
		oauthWriteError(w, http.StatusBadRequest, "invalid_grant", fmt.Sprintf("unknown token kind: %s", data.Kind))
		return
	}
	if err != nil {
		oauthWriteError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	// Stamp the new family state into the refresh token.
	newRefreshData.FamilyID = fam.ID
	newRefreshData.JTI = nextJTI

	os.writeTokenResponse(w, accessToken, newRefreshData, scope, fromForm)
}

// refreshWrapped refreshes the upstream OAuth token, then re-wraps it
// into a new Freshbreath access token.
func (os *oauthServer) refreshWrapped(data *freshbreathRefreshData) (string, freshbreathRefreshData, string, error) {
	if data.UpstreamRefresh == "" {
		return "", freshbreathRefreshData{}, "", fmt.Errorf("no upstream refresh token available — re-login required")
	}
	svc, err := os.server.store.GetService(data.ServiceID)
	if err != nil {
		return "", freshbreathRefreshData{}, "", fmt.Errorf("service not found: %w", err)
	}

	// Resolve the upstream token endpoint.
	tokenEndpoint, err := os.server.resolveTokenEndpoint(context.Background(), svc)
	if err != nil {
		return "", freshbreathRefreshData{}, "", fmt.Errorf("resolve token endpoint: %w", err)
	}
	if data.UpstreamTokenURL != "" {
		// Validate the stored endpoint matches the service config.
		clientNorm := strings.TrimSuffix(data.UpstreamTokenURL, "/")
		serverNorm := strings.TrimSuffix(tokenEndpoint, "/")
		if clientNorm != serverNorm {
			tokenEndpoint = data.UpstreamTokenURL
		}
	}

	clientID := svc.Descriptor.ClientID
	if clientID == "" {
		return "", freshbreathRefreshData{}, "", fmt.Errorf("no client_id for service — re-login required")
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {data.UpstreamRefresh},
		"client_id":     {clientID},
	}
	if svc.Descriptor.ClientSecret != "" {
		form.Set("client_secret", svc.Descriptor.ClientSecret)
	}
	if data.UpstreamScopes != "" {
		form.Set("scope", data.UpstreamScopes)
	}

	resp, err := os.server.httpClient.PostForm(tokenEndpoint, form)
	if err != nil {
		return "", freshbreathRefreshData{}, "", fmt.Errorf("upstream refresh failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", freshbreathRefreshData{}, "", fmt.Errorf("upstream refresh returned %d: %s", resp.StatusCode, string(body))
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", freshbreathRefreshData{}, "", fmt.Errorf("decode upstream response: %w", err)
	}

	// Re-wrap into a Freshbreath access token.
	upstreamRefresh := tok.RefreshToken
	if upstreamRefresh == "" {
		upstreamRefresh = data.UpstreamRefresh
	}
	upstream := &sealedUpstreamData{
		UpstreamToken:    tok.AccessToken,
		UpstreamRefresh:  upstreamRefresh,
		UpstreamTokenURL: tokenEndpoint,
		UpstreamScopes:   tok.Scope,
	}
	jwt, err := os.server.mintFreshbreathToken("wrapped", data.UserEmail, "", "", data.ServiceID, upstream)
	if err != nil {
		return "", freshbreathRefreshData{}, "", fmt.Errorf("mint wrapped token: %w", err)
	}

	newRefreshData := freshbreathRefreshData{
		Kind:             "wrapped",
		ServiceID:        data.ServiceID,
		UserEmail:        data.UserEmail,
		UpstreamRefresh:  upstreamRefresh,
		UpstreamTokenURL: tokenEndpoint,
		UpstreamScopes:   tok.Scope,
	}
	return jwt, newRefreshData, tok.Scope, nil
}

// refreshIdentity re-mints an identity access token for the user. The user
// must still exist — refresh re-resolves them from the DB, so a deleted user
// can't extend their session, and a role change propagates within one cycle.
func (os *oauthServer) refreshIdentity(data *freshbreathRefreshData) (string, freshbreathRefreshData, error) {
	user, err := os.server.store.GetUserByEmail(data.UserEmail)
	if err != nil {
		return "", freshbreathRefreshData{}, fmt.Errorf("user not found: %w", err)
	}
	jwt, err := os.server.mintFreshbreathToken("identity", user.Email, user.Role, user.Name, data.ServiceID, nil)
	if err != nil {
		return "", freshbreathRefreshData{}, fmt.Errorf("mint identity token: %w", err)
	}
	newRefreshData := freshbreathRefreshData{
		Kind:      "identity",
		ServiceID: data.ServiceID, // preserve the auth-service binding across refresh
		UserEmail: user.Email,
		UserRole:  user.Role,
		UserName:  user.Name,
	}
	return jwt, newRefreshData, nil
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

// ── Token Issuance ────────────────────────────────────────────────
//
// handleToken issues access + refresh tokens for both authorization_code
// and refresh_token grants. All Freshbreath tokens are unified under
// freshbreathClaims, minted by mintFreshbreathToken.

// verifyAndUnwrapToken checks if a Bearer token is a Freshbreath-wrapped JWT
// for a specific virtual service. Returns the full claims (with Upstream*
// fields populated) on success. Returns (nil, nil) if the token is not
// a Freshbreath JWT — the caller should use the raw token as-is.
func (s *Server) verifyAndUnwrapToken(raw string, expectedServiceID int64) (*freshbreathClaims, error) {
	claims, err := s.verifyFreshbreathToken(raw)
	if err != nil {
		return nil, err
	}
	if claims == nil {
		return nil, nil
	}
	if claims.Kind != "wrapped" {
		return nil, fmt.Errorf("expected wrapped token, got kind=%s", claims.Kind)
	}
	if claims.ServiceID != expectedServiceID {
		return nil, fmt.Errorf("wrapped token service_id mismatch")
	}
	return claims, nil
}

// ── PKCE Verification ───────────────────────────────────────────────

func verifyPKCE(verifier, challenge string) bool {
	// S256: BASE64URL(SHA256(verifier))
	h := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	return computed == challenge
}
