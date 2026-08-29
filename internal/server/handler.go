package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/formats"
	"poggers.institute/freshbreath/internal/sshkit"

	"github.com/coreos/go-oidc/v3/oidc"
)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		if !s.originAllowed(r, origin) {
			http.Error(w, "Origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}

	if r.Method == http.MethodOptions {
		s.handleCORSOptions(w, r)
		return
	}

	s.mux.ServeHTTP(w, r)
}

// originAllowed checks whether the request's Origin should be permitted.
// Same-origin requests (Origin matches scheme://r.Host) are always allowed.
// file:// pages (Origin: null) are always allowed.
// Cross-origin requests are allowed if the origin is in the allowedOrigins map
// (built from app URLs on startup and after app changes).
func (s *Server) originAllowed(r *http.Request, origin string) bool {
	// Always allow the OPTIONS call to go through, we gate everything else
	if r.Method == http.MethodOptions {
		return true
	}
	// Local file:/// always allowed
	if origin == "null" {
		return true
	}
	// Same-origin — always fine.
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	if scheme+"://"+r.Host == origin {
		return true
	}
	// For everything else, expect an app nonce and ensure only the
	// app's registered URL is allowed.
	appNonce := r.Header.Get("X-App-Nonce")
	if appNonce == "" {
		appNonce = r.URL.RawQuery
	}
	if appNonce == "" {
		appNonce = s.adminNonce
	}
	if app, err := s.store.GetApp(appNonce); err == nil && app.URL != "" {
		appURL, _ := url.Parse(app.URL)
		appOrigin := appURL.Scheme + "://" + appURL.Host
		return appOrigin == origin
	}
	return false
}

func (s *Server) handleCORSOptions(w http.ResponseWriter, r *http.Request) {
	if h := r.Header.Get("Access-Control-Request-Headers"); h != "" {
		w.Header().Set("Access-Control-Allow-Headers", h)
	}
	if m := r.Header.Get("Access-Control-Request-Method"); m != "" {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) SetupRoutes() {
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/control", s.handleControl)
	controlDir := filepath.Join(s.config.Dir, "web", "control")
	controlFiles := http.StripPrefix("/control/", http.FileServer(http.Dir(controlDir)))
	s.mux.HandleFunc("/control/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/control/")
		if rel != "" {
			if fi, err := os.Stat(filepath.Join(controlDir, rel)); err == nil && !fi.IsDir() {
				controlFiles.ServeHTTP(w, r)
				return
			}
		}
		s.handleControl(w, r)
	})
	s.mux.HandleFunc("/env.js", s.handleEnv)
	s.mux.HandleFunc("/frbr.js", s.handleFrbr)
	s.mux.HandleFunc("/service/login", s.handleLogin)
	s.mux.HandleFunc("/service/callback", s.handleCallback)
	s.mux.HandleFunc("/service/refresh", s.handleRefresh)
	s.mux.HandleFunc("/service/ssh-auth", s.handleSSHAuth)
	s.mux.HandleFunc("/service/apikey-auth", s.handleAPIKeyAuth)
	s.mux.HandleFunc("/service/{id}/", s.handleServiceProxy)
	s.mux.HandleFunc("/service/call/{name}", s.handleServiceCall)

	// MCP endpoints for virtual services — single routes dispatch by slug
	s.mux.HandleFunc("/mcp/{name}", s.handleMCP)
	s.mux.HandleFunc("/.well-known/oauth-protected-resource/mcp/{name}", s.handleMCPPRM)

	// Central MCP server — exposes admin API as MCP tools
	s.setupCentralMCP()
	s.mux.HandleFunc("/mcp", s.handleCentralMCP)
	s.mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", s.handleCentralMCPPRM)

	// OAuth authorization server endpoints (Freshbreath acts as auth server for MCP clients)
	s.mux.HandleFunc("/.well-known/oauth-authorization-server", s.oauthSrv.handleMetadata)
	s.mux.HandleFunc("/.well-known/oauth-authorization-server/", s.oauthSrv.handleMetadata) // resource-specific path
	s.mux.HandleFunc("/oauth/register", s.oauthSrv.handleRegister)
	s.mux.HandleFunc("/oauth/authorize", s.oauthSrv.handleAuthorize)
	s.mux.HandleFunc("/oauth/token", s.oauthSrv.handleToken)
	// Path-scoped token endpoint for browser cookie refresh. The advertised
	// /oauth/token above stays the OAuth-standard endpoint (CLI/MCP code grant
	// + form-body refresh); this variant scopes the refresh_token cookie by the
	// service id so a browser holding several services' refresh tokens keeps
	// them in distinct cookie slots (see makeRefreshCookie). Both routes share
	// the handler; the path service id is only consulted on the cookie path.
	s.mux.HandleFunc("/oauth/token/{serviceID}", s.oauthSrv.handleToken)
	s.mux.HandleFunc("/oauth/jwks", s.oauthSrv.handleJWKS)

	// Admin API — role-gated
	superuser := requireAnyRole(rolesSuperuser...)
	adminPlus := requireAnyRole(rolesAdminPlus...)
	anyRole := requireAnyRole(rolesAll...)

	// Act-token dispatch — anonymous by design; handleAct verifies its own
	// capability token (see act_token.go). Bare on purpose, NOT authWrap'd.
	s.mux.HandleFunc("/api/act/", s.handleAct)

	s.mux.HandleFunc("/api/apps", s.authWrap(pipeline(s.handleApps, anyRole)))
	s.mux.HandleFunc("/api/apps/", s.authWrap(pipeline(s.handleAppDetail, anyRole)))
	s.mux.HandleFunc("/api/services", s.authWrap(pipeline(s.handleServices, adminPlus)))
	s.mux.HandleFunc("/api/services/", s.authWrap(pipeline(s.handleServiceDetail, adminPlus)))

	// Global databases (design/app-databases.md) — the alias mount. Any
	// authenticated role rides the mount; gateDBTarget decides who may touch
	// a global database (admin+), so the permission lives in one place.
	s.mux.HandleFunc("/api/db", s.authWrap(pipeline(s.handleGlobalDB, anyRole)))
	s.mux.HandleFunc("/api/db/", s.authWrap(pipeline(s.handleGlobalDB, anyRole)))
	s.mux.HandleFunc("/api/users", s.authWrap(pipeline(s.handleUsers, adminPlus)))
	s.mux.HandleFunc("/api/users/", s.authWrap(pipeline(s.handleUserDetail, adminPlus)))
	s.mux.HandleFunc("/api/roles", s.authWrap(pipeline(s.handleRoles, anyRole)))
	s.mux.HandleFunc("/api/audit", s.authWrap(pipeline(s.handleAudit, anyRole)))
	s.mux.HandleFunc("/api/me", s.authWrap(pipeline(s.handleMe, anyRole)))
	s.mux.HandleFunc("/api/me/ssh-key", s.authWrap(pipeline(s.handleSSHKey, anyRole)))
	s.mux.HandleFunc("/api/me/sessions", s.authWrap(pipeline(s.handleSessions, anyRole)))
	s.mux.HandleFunc("/api/me/sessions/", s.authWrap(pipeline(s.handleSessionDetail, anyRole)))
	s.mux.HandleFunc("/api/settings", s.authWrap(pipeline(s.handleSettings, superuser)))

	// Remote update feeds (design/remote-updates.md). Management is
	// admin-only; check/apply are mounted BARE — anonymous by design, the
	// point being that a hosted app can pull its own updates. Their exact
	// patterns win over the /api/updates/ subtree, so they never pass
	// through the admin mount.
	s.mux.HandleFunc("/api/updates", s.authWrap(pipeline(s.handleUpdates, adminPlus)))
	s.mux.HandleFunc("/api/updates/", s.authWrap(pipeline(s.handleUpdateDetail, adminPlus)))
	s.mux.HandleFunc("/api/updates/check", s.handleUpdateCheck)
	s.mux.HandleFunc("/api/updates/apply", s.handleUpdateApply)

	// SSH host keys (TOFU) — same access gate as SSH sessions
	s.mux.HandleFunc("/ssh/known-hosts", s.authWrap(pipeline(s.handleSSHHostKeys, s.requireAppServiceAccess("ssh"))))
	s.mux.HandleFunc("/ssh/known-hosts/", s.authWrap(pipeline(s.handleSSHHostKeyDetail, s.requireAppServiceAccess("ssh"))))

	// SSH sessions — admin+ gets access via adminNonce; members via app service check
	s.mux.HandleFunc("/ssh/sessions", s.authWrap(pipeline(s.handleSSHSessions, s.requireAppServiceAccess("ssh"))))
	s.mux.HandleFunc("/ssh/sessions/", s.authWrap(pipeline(s.handleSSHSessionDetail, s.requireAppServiceAccess("ssh"))))

	// Git over SSH — same access gate as SSH sessions (sign/branches/pull/commit)
	s.mux.HandleFunc("/ssh/git/sign", s.authWrap(pipeline(s.handleGitSign, s.requireAppServiceAccess("ssh"))))
	s.mux.HandleFunc("/ssh/git/branches", s.authWrap(pipeline(s.handleGitBranches, s.requireAppServiceAccess("ssh"))))
	s.mux.HandleFunc("/ssh/git/pull", s.authWrap(pipeline(s.handleGitPull, s.requireAppServiceAccess("ssh"))))
	s.mux.HandleFunc("/ssh/git/commit", s.authWrap(pipeline(s.handleGitCommit, s.requireAppServiceAccess("ssh"))))

	// File sync — same access gate as SSH sessions
	s.mux.HandleFunc("/sync/files/diff", s.authWrap(pipeline(s.handleSyncDiff, s.requireAppServiceAccess("ssh"))))
	s.mux.HandleFunc("/sync/files/{path...}", s.authWrap(pipeline(s.handleSyncFileOps, s.requireAppServiceAccess("ssh"))))
	s.mux.HandleFunc("/sync/files", s.authWrap(pipeline(s.handleSyncList, s.requireAppServiceAccess("ssh"))))

	s.rebuildHostedRoutes()

	// Mount MCP endpoints for existing virtual services.
	s.mountAllVirtualMCP()
}

// mountAllVirtualMCP discovers all virtual services and registers their MCP entries.
func (s *Server) mountAllVirtualMCP() {
	services, err := s.store.ListServices()
	if err != nil {
		return
	}
	for _, svc := range services {
		if svc.Descriptor.Type == "virtual" && strings.HasPrefix(svc.URL, "/mcp/") {
			s.virtualMCPs.add(s, svc)
		}
	}
}

// slugify converts a string to a lowercase hyphenated URL slug.
func slugify(s string) string {
	var b strings.Builder
	prev := true
	for _, r := range strings.ToLower(s) {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if alnum {
			b.WriteRune(r)
			prev = false
		} else if !prev {
			b.WriteByte('-')
			prev = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// appSlug returns the URL path segment for a hosted app.
// If the app URL has no scheme it IS the route; otherwise derive from name.
func appSlug(app *db.App) string {
	if app.URL != "" && !strings.Contains(app.URL, "://") {
		return strings.Trim(app.URL, "/")
	}
	return slugify(app.Name)
}

// rebuildHostedRoutes reloads the slug→app route map. An app is routable iff
// at least one deployment slot dir exists on disk (web, staging, production);
// the details blob is only a report, not the gate.
func (s *Server) rebuildHostedRoutes() {
	apps, err := s.store.ListHostedApps()
	if err != nil {
		log.Printf("rebuildHostedRoutes: %v", err)
		return
	}
	routes := make(map[string]hostedApp, len(apps))
	for _, a := range apps {
		slug := appSlug(a)
		if slug == "" || !s.appHasSlotDir(a.Nonce) {
			continue
		}
		routes[slug] = hostedApp{nonce: a.Nonce, environment: a.Environment}
	}
	s.hostedMu.Lock()
	s.hostedRoutes = routes
	s.hostedMu.Unlock()
}

// appHasSlotDir reports whether any deployment slot dir exists for the app.
func (s *Server) appHasSlotDir(nonce string) bool {
	base := filepath.Join(s.config.DataDir, "apps", nonce)
	for _, dir := range []string{"web", "staging", "production"} {
		if fi, err := os.Stat(filepath.Join(base, dir)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		if nonce, _ := s.store.GetSetting("default_app"); nonce != "" && nonce != "control" {
			s.hostedMu.RLock()
			for slug, ha := range s.hostedRoutes {
				if ha.nonce == nonce {
					s.hostedMu.RUnlock()
					http.Redirect(w, r, "/"+slug+"/", http.StatusFound)
					return
				}
			}
			s.hostedMu.RUnlock()
		}
		http.Redirect(w, r, "/control", http.StatusFound)
		return
	}
	s.handleHostedApp(w, r)
}

func (s *Server) handleHostedApp(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	slug, rest, _ := strings.Cut(path, "/")

	// An @slot suffix picks a deployment slot explicitly: @dev, @staging, @prod.
	// Slugs themselves are alnum+dash, so '@' is unambiguous.
	slot, explicitSlot := "", false
	if i := strings.IndexByte(slug, '@'); i >= 0 {
		slot, explicitSlot, slug = slug[i+1:], true, slug[:i]
	}

	s.hostedMu.RLock()
	ha, ok := s.hostedRoutes[slug]
	s.hostedMu.RUnlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	// Redirect /app-name → /app-name/ so relative asset paths resolve correctly.
	if rest == "" && !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
		return
	}

	// Resolve the slot's on-disk dir. Without an explicit @slot the app's
	// Environment picks the default; unknown slots and undeployed dirs 404
	// rather than silently falling back to another slot.
	dirName := slotDirName(slot, explicitSlot, ha.environment)
	if dirName == "" {
		http.NotFound(w, r)
		return
	}
	webDir := filepath.Join(s.config.DataDir, "apps", ha.nonce, dirName)
	if fi, err := os.Stat(webDir); err != nil || !fi.IsDir() {
		http.NotFound(w, r)
		return
	}

	if rest != "" {
		clean := filepath.Clean(rest)
		if !strings.HasPrefix(clean, "..") {
			filePath := filepath.Join(webDir, clean)
			if fi, err := os.Stat(filePath); err == nil && !fi.IsDir() {
				http.ServeFile(w, r, filePath)
				return
			}
		}
	}

	// SPA fallback
	f, err := os.Open(filepath.Join(webDir, "index.html"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	http.ServeContent(w, r, "index.html", fi.ModTime(), f)
}

// slotDirName maps a requested slot to its on-disk dir under the app folder.
// An explicit @slot must be one of dev/staging/prod; otherwise the app's
// Environment names the default (empty/unknown means Development). Returns ""
// for an unrecognized explicit slot.
func slotDirName(slot string, explicit bool, environment string) string {
	if explicit {
		switch slot {
		case "dev":
			return "web"
		case "staging":
			return "staging"
		case "prod":
			return "production"
		}
		return ""
	}
	switch environment {
	case "Staging":
		return "staging"
	case "Production":
		return "production"
	}
	return "web"
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(filepath.Join(s.config.Dir, "web", "control.html"))
	if err != nil {
		http.Error(w, "control.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) renderEnvJS(r *http.Request) []byte {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	apiBase := scheme + "://" + r.Host

	// Use the request's nonce (header or query param), falling back to admin.
	appNonce := r.Header.Get("X-App-Nonce")
	if appNonce == "" {
		appNonce = r.URL.RawQuery
	}
	if appNonce == "" {
		appNonce = s.adminNonce
	}

	authRequired := false
	authServiceName := ""
	authServiceURL := ""
	authServiceType := ""
	if svcIDStr, _ := s.store.GetSetting("admin_auth_service"); svcIDStr != "" {
		if svcID, err := strconv.ParseInt(svcIDStr, 10, 64); err == nil {
			if svc, err := s.store.GetService(svcID); err == nil {
				authRequired = true
				authServiceName = svc.Name
				authServiceURL = svc.URL
				authServiceType = svc.Descriptor.Type
			}
		}
	}
	return []byte(fmt.Sprintf("window.__HOMESLICE_CONFIG = { apiBase: %q, authRequired: %v, authServiceName: %q, authServiceURL: %q, authServiceType: %q, appNonce: %q, version: %q, commit: %q };\n",
		apiBase, authRequired, authServiceName, authServiceURL, authServiceType, appNonce, s.version, s.commit))
}

func (s *Server) handleEnv(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Write(s.renderEnvJS(r))
}

func (s *Server) handleFrbr(w http.ResponseWriter, r *http.Request) {
	frbrPath := filepath.Join(s.config.Dir, "web", "frbr.js")
	data, err := os.ReadFile(frbrPath)
	if err != nil {
		http.Error(w, "frbr.js not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(s.renderEnvJS(r))
	w.Write(data)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	appState := r.URL.Query().Get("state")
	if appState == "" {
		http.Error(w, "Missing state parameter", http.StatusBadRequest)
		return
	}

	nonce := r.Header.Get("X-App-Nonce")
	if nonce == "" {
		nonce = r.URL.Query().Get("app_nonce")
	}
	if nonce == "" {
		http.Error(w, "Missing X-App-Nonce", http.StatusBadRequest)
		return
	}

	// Admin nonce is ephemeral (not in the apps table).
	// Regular app nonces must resolve to a registered app.
	isAdmin := nonce == s.adminNonce
	if !isAdmin {
		if _, err := s.store.GetApp(nonce); err != nil {
			http.Error(w, "Unknown app nonce", http.StatusUnauthorized)
			return
		}
	}

	serviceURL := r.URL.Query().Get("url")
	if serviceURL == "" {
		http.Error(w, "Missing url parameter", http.StatusBadRequest)
		return
	}

	svc, err := s.store.GetServiceByURL(serviceURL)
	if err != nil {
		http.Error(w, "Service not registered", http.StatusForbidden)
		return
	}

	// Tasks services don't use login
	if svc.Descriptor.Type == "tasks" {
		http.Error(w, "Task services don't login - instead pass in an `authService` object to ServiceProxy.", http.StatusForbidden)
		return
	}

	// Browser logins require an explicit app/service link. The admin panel
	// may only log in to the configured admin auth service.
	if !s.canLoginToService(nonce, svc.ID) {
		http.Error(w, "Service not approved for this app", http.StatusForbidden)
		return
	}

	// SSH auth — return a URL to the passphrase form
	if svc.Descriptor.Type == "ssh" {
		state := db.GenNonce()
		s.putPending(state, &pendingAuth{
			serviceID:   svc.ID,
			serviceURL:  svc.URL,
			appNonce:    nonce,
			appState:    appState,
			serviceType: "ssh",
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "ssh",
			"url":  fmt.Sprintf(s.config.PublicBaseURL+"/service/ssh-auth?state=%s&service_id=%d", state, svc.ID),
		})
		return
	}

	redirectURI := s.config.PublicBaseURL + "/service/callback"

	if svc.Descriptor.Type == "oidc" {
		authURL, state, verifier, oidcNonce, tokenURL, err := s.oidcBeginAuth(r.Context(), svc, redirectURI)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to begin OIDC auth: %v", err), http.StatusInternalServerError)
			return
		}
		s.putPending(state, &pendingAuth{
			serviceID:     svc.ID,
			serviceURL:    svc.URL,
			appNonce:      nonce,
			appState:      appState,
			verifier:      verifier,
			clientID:      svc.Descriptor.ClientID,
			clientSecret:  svc.Descriptor.ClientSecret,
			tokenEndpoint: tokenURL,
			scopes:        svc.Descriptor.Scopes,
			proxied:       svc.Descriptor.Proxied,
			serviceType:   "oidc",
			oidcNonce:     oidcNonce,
			oidcIssuer:    svc.URL,
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "redirect",
			"url":  authURL,
		})
		return
	}

	// API-key auth — no OAuth flow, just return service info as JSON
	if svc.Descriptor.Auth == "key" {
		if svc.Descriptor.APIKey != "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"type":       "key-auth-complete",
				"state":      appState,
				"appNonce":   nonce,
				"apiKey":     svc.Descriptor.APIKey,
				"apiHeader":  svc.Descriptor.Header,
				"serviceID":  svc.ID,
				"serviceURL": svc.URL,
				"auth":       "key",
				"proxied":    svc.Descriptor.Proxied,
			})
			return
		}

		// Services with no default API-key — redirect to key entry form
		state := db.GenNonce()
		s.putPending(state, &pendingAuth{
			serviceID:   svc.ID,
			serviceURL:  svc.URL,
			appNonce:    nonce,
			appState:    appState,
			serviceType: "apikey",
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "redirect",
			"url":  fmt.Sprintf(s.config.PublicBaseURL+"/service/apikey-auth?state=%s&service_id=%d", state, svc.ID),
		})
		return
	}

	authURL, clientID, clientSecret, tokenURL, state, verifier, err := s.serviceBeginAuth(r.Context(), svc, redirectURI)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to begin auth: %v", err), http.StatusInternalServerError)
		return
	}

	s.putPending(state, &pendingAuth{
		serviceID:     svc.ID,
		serviceURL:    svc.URL,
		appNonce:      nonce,
		appState:      appState,
		verifier:      verifier,
		clientID:      clientID,
		clientSecret:  clientSecret,
		tokenEndpoint: tokenURL,
		scopes:        svc.Descriptor.Scopes,
		proxied:       svc.Descriptor.Proxied,
		serviceType:   svc.Descriptor.Type,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type": "redirect",
		"url":  authURL,
	})
}

// canLoginToService decides whether the app identified by nonce may initiate
// a browser login for svc. The admin nonce is restricted to the configured
// admin auth service; regular apps must have an allowed app_service_links row.
func (s *Server) canLoginToService(nonce string, svcID int64) bool {
	if nonce == s.adminNonce {
		svcIDStr, _ := s.store.GetSetting("admin_auth_service")
		adminSvcID, err := parseID(svcIDStr)
		return err == nil && adminSvcID == svcID
	}
	allowed, err := s.store.IsServiceAllowedForApp(nonce, svcID)
	return err == nil && allowed
}

// completeAuth writes the postMessage callback page for a completed auth flow.
// Used by both OIDC and SSH callback paths. If the token is Freshbreath-issued,
// it also mints a refresh token and sets the HttpOnly cookie.
func (s *Server) completeAuth(w http.ResponseWriter, r *http.Request, pending *pendingAuth, oauth *db.OAuthData) {
	// If the access token is Freshbreath-issued, set the refresh cookie.
	if isFreshbreathToken(oauth.AccessToken) {
		if claims, _ := s.verifyFreshbreathToken(oauth.AccessToken); claims != nil {
			s.setRefreshCookie(w, r, claims)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	writeCallbackPage(w, pending.appState, pending.appNonce, pending.serviceID, pending.serviceURL, oauth)
}

// completeMCPAuth handles the callback for MCP OAuth flows.
// Exchanges the upstream code, stores the upstream token in the mcpPendingAuth,
// generates a Freshbreath auth code, and redirects to the MCP client's redirect_uri.
func (s *Server) completeMCPAuth(w http.ResponseWriter, r *http.Request, pending *pendingAuth, code string) {
	mcpPending, ok, _ := s.getMCPPending(pending.appNonce)
	if !ok {
		http.Error(w, "MCP auth session expired", http.StatusBadRequest)
		return
	}

	redirectURI := s.config.PublicBaseURL + "/service/callback"

	// Exchange upstream code for token.
	// Virtual services with OIDC providers use the OIDC path;
	// others use the generic OAuth path.
	var upstreamToken, upstreamRefresh, upstreamTokenURL string
	var upstreamExpiry time.Time
	var userEmail string
	var upstreamScopes string

	svc, err := s.store.GetService(pending.serviceID)
	if err != nil {
		http.Error(w, "Service not found", http.StatusInternalServerError)
		return
	}

	if svc.Descriptor.Type == "oidc" {
		claims, accessToken, refreshToken, err := s.oidcExchangeCode(r.Context(), svc, code, pending.verifier, pending.oidcNonce, redirectURI)
		if err != nil {
			http.Error(w, fmt.Sprintf("OIDC exchange failed: %v", err), http.StatusInternalServerError)
			return
		}
		upstreamToken = accessToken
		upstreamRefresh = refreshToken
		upstreamTokenURL = pending.tokenEndpoint
		upstreamExpiry = time.Unix(claims.Expiry, 0)
		userEmail = firstNonEmpty(claims.Email, claims.Subject)
		upstreamScopes = pending.scopes
	} else {
		// Generic OAuth (GitHub, etc.)
		oauth, err := s.serviceExchangeCode(r.Context(), pending.tokenEndpoint, code, pending.verifier, pending.clientID, pending.clientSecret, redirectURI)
		if err != nil {
			http.Error(w, fmt.Sprintf("Token exchange failed: %v", err), http.StatusInternalServerError)
			return
		}
		upstreamToken = oauth.AccessToken
		upstreamRefresh = oauth.RefreshToken
		upstreamTokenURL = oauth.TokenEndpoint
		upstreamExpiry = oauth.ExpiresAt
		upstreamScopes = oauth.Scopes

		// Try to extract email from the upstream token's claims or userinfo.
		// For non-OIDC providers, the access token is opaque — resolve via service login.
		if oauth.Claims != nil {
			if email, ok := oauth.Claims["email"].(string); ok && email != "" {
				userEmail = email
			}
			if sub, ok := oauth.Claims["sub"].(string); ok && userEmail == "" {
				userEmail = sub
			}
		}
	}

	if userEmail == "" {
		userEmail = "mcp-user"
	}

	// For the central MCP, mint a central JWT that carries the user's role.
	if pending.serviceType == "mcp-central" {
		user, err := s.store.GetUserByEmail(userEmail)
		if err != nil {
			http.Error(w, fmt.Sprintf("User not found: %s", userEmail), http.StatusForbidden)
			return
		}
		centralJWT, err := s.mintFreshbreathToken("identity", user.Email, user.Role, "", svc.ID, nil)
		if err != nil {
			http.Error(w, "Token generation failed", http.StatusInternalServerError)
			return
		}
		// Generate a Freshbreath auth code that maps to a pending auth
		// where the upstream token IS the central JWT.
		mcpPending.upstreamToken = centralJWT
		mcpPending.upstreamRefresh = ""
		mcpPending.upstreamTokenURL = ""
		mcpPending.upstreamExpiry = upstreamExpiry
		mcpPending.userEmail = userEmail
		mcpPending.upstreamScopes = ""
	} else {
		// Populate the MCP pending auth with the upstream token
		mcpPending.upstreamToken = upstreamToken
		mcpPending.upstreamRefresh = upstreamRefresh
		mcpPending.upstreamTokenURL = upstreamTokenURL
		mcpPending.upstreamExpiry = upstreamExpiry
		mcpPending.userEmail = userEmail
		mcpPending.upstreamScopes = upstreamScopes
	}

	// Generate a Freshbreath auth code and store it
	fbCode := rand.Text()
	s.oauthSrv.sweepExpiredCodes(time.Now())
	s.oauthSrv.codesMu.Lock()
	s.oauthSrv.codes[fbCode] = &mcpAuthCode{
		pending: mcpPending,
		issued:  time.Now(),
	}
	s.oauthSrv.codesMu.Unlock()

	// Redirect to the MCP client's redirect_uri with the code and their original state
	redirectURL := fmt.Sprintf("%s?code=%s&state=%s", mcpPending.redirectURI, fbCode, mcpPending.state)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// completeMCPDirectAuth finalises an MCP flow for credential types that skip upstream OAuth
// (SSH, API key). Returns the redirect URL the browser should navigate to, or writes an
// error response and returns ("", false).
func (s *Server) completeMCPDirectAuth(w http.ResponseWriter, appNonce, token, email, scopes string, expiry time.Time) (string, bool) {
	mcp, ok, _ := s.getMCPPending(appNonce)
	if !ok {
		http.Error(w, "MCP auth session expired", http.StatusBadRequest)
		return "", false
	}
	mcp.upstreamToken = token
	mcp.userEmail = email
	mcp.upstreamExpiry = expiry
	mcp.upstreamScopes = scopes

	code := rand.Text()
	s.oauthSrv.sweepExpiredCodes(time.Now())
	s.oauthSrv.codesMu.Lock()
	s.oauthSrv.codes[code] = &mcpAuthCode{pending: mcp, issued: time.Now()}
	s.oauthSrv.codesMu.Unlock()

	return fmt.Sprintf("%s?code=%s&state=%s", mcp.redirectURI, code, mcp.state), true
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "Missing code or state", http.StatusBadRequest)
		return
	}

	pending, ok, expired := s.getPending(state)

	if !ok && expired {
		http.Error(w, "Auth state expired — restart the login flow", http.StatusBadRequest)
		return
	}
	if !ok {
		http.Error(w, "Unknown auth state", http.StatusBadRequest)
		return
	}

	// SSH auth never reaches /service/callback — it completes via /service/ssh-auth.
	if pending.serviceType == "ssh" {
		http.Error(w, "Unexpected callback for SSH auth", http.StatusBadRequest)
		return
	}

	// MCP auth flows — after upstream exchange, redirect to the MCP client's redirect_uri
	if pending.serviceType == "mcp-endpoint" || pending.serviceType == "mcp-central" {
		s.completeMCPAuth(w, r, pending, code)
		return
	}

	redirectURI := s.config.PublicBaseURL + "/service/callback"

	if pending.serviceType == "oidc" {
		svc, err := s.store.GetService(pending.serviceID)
		if err != nil {
			http.Error(w, "Service not found", http.StatusInternalServerError)
			return
		}
		// OIDC is an identity proof: exchange the code to learn who the user
		// is, then mint a Fresh Breath identity token. The provider's
		// access/refresh tokens are deliberately discarded — the identity
		// token refreshes locally and never calls the provider again.
		//
		// We mint even when there's no matching Fresh Breath user yet: the
		// token is a usable identity (apps read .data.claims) but bounces off
		// the admin gates, which re-resolve the user from the DB. Role/name
		// come from the user record when it exists — never from the provider's
		// claims, which must not be trusted for authorization.
		claims, _, _, err := s.oidcExchangeCode(r.Context(), svc, code, pending.verifier, pending.oidcNonce, redirectURI)
		if err != nil {
			http.Error(w, fmt.Sprintf("OIDC exchange failed: %v", err), http.StatusInternalServerError)
			return
		}

		userEmail := firstNonEmpty(claims.Email, claims.Subject)
		role, name := "", claims.Name
		if user, err := s.store.GetUserByEmail(userEmail); err == nil {
			role, name = user.Role, firstNonEmpty(user.Name, claims.Name)
		}

		idToken, err := s.mintFreshbreathToken("identity", userEmail, role, name, pending.serviceID, nil)
		if err != nil {
			http.Error(w, "Token generation failed", http.StatusInternalServerError)
			return
		}

		// Provider profile claims remain handy for display.
		mergedClaims := make(map[string]interface{})
		for k, v := range claims.Raw {
			mergedClaims[k] = v
		}
		mergedClaims["sub"] = claims.Subject
		mergedClaims["iss"] = claims.Issuer
		if claims.Email != "" {
			mergedClaims["email"] = claims.Email
		}
		if claims.Name != "" {
			mergedClaims["name"] = claims.Name
		}
		if claims.Picture != "" {
			mergedClaims["picture"] = claims.Picture
		}

		s.completeAuth(w, r, pending, &db.OAuthData{
			ClientID:    pending.clientID,
			AccessToken: idToken,
			TokenType:   "Bearer",
			ExpiresAt:   time.Now().Add(accessTokenTTL),
			Claims:      mergedClaims,
			IDToken:     idToken,
		})
		return
	}

	// Generic OAuth (MCP, API) — exchange code for token
	oauth, err := s.serviceExchangeCode(r.Context(), pending.tokenEndpoint, code, pending.verifier, pending.clientID, pending.clientSecret, redirectURI)
	if err != nil {
		http.Error(w, fmt.Sprintf("Token exchange failed: %v", err), http.StatusInternalServerError)
		return
	}

	oauth.ClientID = pending.clientID
	oauth.TokenEndpoint = pending.tokenEndpoint
	oauth.Proxied = pending.proxied
	oauth.Scopes = pending.scopes

	s.completeAuth(w, r, pending, oauth)
}

func writeCallbackPage(w io.Writer, appState, appNonce string, serviceID int64, serviceURL string, oauth *db.OAuthData) {
	dataJSON, _ := json.Marshal(oauth)
	fmt.Fprintf(w, `<!doctype html>
<html>
<body>
  <p>Logged in. You can close this window.</p>
  <script>
    window.opener?.postMessage({
      type: "auth-complete",
      state: %q,
      appNonce: %q,
      serviceID: %d,
      serviceURL: %q,
      data: %s,
    }, "*");
  </script>
</body>
</html>
`, appState, appNonce, serviceID, serviceURL, string(dataJSON))
}

func (s *Server) handleServiceProxy(w http.ResponseWriter, r *http.Request) {
	nonce := r.Header.Get("X-App-Nonce")
	if nonce == "" {
		http.Error(w, "Missing X-App-Nonce header", http.StatusUnauthorized)
		return
	}

	idStr := r.PathValue("id")
	serviceID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid service id", http.StatusBadRequest)
		return
	}

	// Non-admin apps: only allow proxying services that are approved for this app.
	app, err := s.store.GetApp(nonce)
	if err != nil {
		http.Error(w, "Unknown app", http.StatusUnauthorized)
		return
	}
	allowed, err := s.store.IsServiceAllowedForApp(app.Nonce, serviceID)
	if err != nil || !allowed {
		http.Error(w, "Service not approved for this app", http.StatusForbidden)
		return
	}

	svc, err := s.store.GetService(serviceID)
	if err != nil {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	// Browser sends its own Authorization header — forward it through.
	// Token management (refresh, retry) is handled client-side.
	// If the service has an admin-set API key and the request doesn't
	// already have an Authorization header, inject it.
	resp, err := s.serviceDoProxy(svc, r)
	if err != nil {
		http.Error(w, fmt.Sprintf("Proxy error: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	flw := &flushWriter{w: w}
	io.Copy(flw, resp.Body)
}

// ── Admin API ──

// ── Tasks service ───────────────────────────────────────────────────────

// loadTasksForService reads and parses the tasks file for a service.
func (s *Server) loadTasksForService(svc *db.Service) ([]formats.Task, error) {
	path := filepath.Join(s.config.DataDir, "tasks", svc.Name+".txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tasks file not found: %s: %w", path, err)
	}
	return formats.ParseTasksFile(data), nil
}

// serviceToolSummary is a lightweight, safe-to-expose view of a task or
// virtual tool: name and description only, no script bodies.
type serviceToolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// loadServiceToolSummaries returns the tool names and descriptions exposed by
// a tasks or virtual service. Other service types return an error. A missing
// file returns an empty list so newly-created services don't error in the UI.
func (s *Server) loadServiceToolSummaries(svc *db.Service) ([]serviceToolSummary, error) {
	switch svc.Descriptor.Type {
	case "tasks":
		tasks, err := s.loadTasksForService(svc)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return []serviceToolSummary{}, nil
			}
			return nil, err
		}
		out := make([]serviceToolSummary, len(tasks))
		for i, t := range tasks {
			out[i] = serviceToolSummary{Name: t.Name, Description: t.Desc}
		}
		return out, nil
	case "virtual":
		tools, err := formats.LoadVirtualTools(s.config.DataDir, svc.Name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return []serviceToolSummary{}, nil
			}
			return nil, err
		}
		out := make([]serviceToolSummary, len(tools))
		for i, t := range tools {
			out[i] = serviceToolSummary{Name: t.Name, Description: t.Description}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("service type %q has no tool list", svc.Descriptor.Type)
	}
}

func (s *Server) handleServiceCall(w http.ResponseWriter, r *http.Request) {
	// ── Auth & access ────────────────────────────────────────────────────
	nonce := r.Header.Get("X-App-Nonce")
	if nonce == "" {
		http.Error(w, "Missing X-App-Nonce header", http.StatusUnauthorized)
		return
	}

	slug := r.PathValue("name")

	// Try tasks:// first, then /mcp/ for virtual services.
	svc, err := s.store.GetServiceByURL("tasks://" + slug)
	if err != nil {
		svc, err = s.store.GetServiceByURL("/mcp/" + slug)
		if err != nil {
			http.Error(w, "Service not found", http.StatusNotFound)
			return
		}
	}

	app, err := s.store.GetApp(nonce)
	if err != nil {
		http.Error(w, "Unknown app", http.StatusUnauthorized)
		return
	}
	allowed, err := s.store.IsServiceAllowedForApp(app.Nonce, svc.ID)
	if err != nil || !allowed {
		http.Error(w, "Service not approved for this app", http.StatusForbidden)
		return
	}

	// ── Optional token auth via a referenced service ──────────────────
	if svc.Descriptor.AuthServiceID != "" {
		authSvcID, err := strconv.ParseInt(svc.Descriptor.AuthServiceID, 10, 64)
		if err != nil {
			http.Error(w, "Invalid auth_service_id on service", http.StatusInternalServerError)
			return
		}
		_, err = s.verifyTaskToken(r, authSvcID)
		if err != nil {
			http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
	}

	// ── Dispatch by service type ────────────────────────────────────
	switch svc.Descriptor.Type {
	case "tasks":
		s.handleTaskCallInner(w, r, svc)
	case "virtual":
		s.handleVirtualCallInner(w, r, svc)
	default:
		http.Error(w, "Service type does not support /service/call", http.StatusBadRequest)
	}
}

func (s *Server) handleTaskCallInner(w http.ResponseWriter, r *http.Request, svc *db.Service) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := s.loadTasksForService(svc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		tools := make([]map[string]string, len(tasks))
		for i, t := range tasks {
			tools[i] = map[string]string{"name": t.Name, "description": t.Desc}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"tools": tools})

	case http.MethodPost:
		s.handleTaskExec(w, r, svc)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVirtualCallInner(w http.ResponseWriter, r *http.Request, svc *db.Service) {
	switch r.Method {
	case http.MethodGet:
		tools, err := formats.LoadVirtualTools(s.config.DataDir, svc.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"tools": formats.VirtualToolSummaries(tools)})

	case http.MethodPost:
		s.handleVirtualExec(w, r, svc)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVirtualExec(w http.ResponseWriter, r *http.Request, svc *db.Service) {
	tools, err := formats.LoadVirtualTools(s.config.DataDir, svc.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	var body struct {
		Task string                 `json:"task"`
		Args map[string]interface{} `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Task == "" {
		http.Error(w, "Missing 'task' field", http.StatusBadRequest)
		return
	}

	// Extract bearer token if present.
	token := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	}

	result, err := formats.ExecuteVirtualTool(s.httpClient, tools, body.Task, body.Args, token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleTaskExec(w http.ResponseWriter, r *http.Request, svc *db.Service) {
	tasks, err := s.loadTasksForService(svc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// ── Parse request: JSON or multipart (for file uploads) ──────────────
	var taskName string
	var args map[string]interface{}
	type fileArg struct {
		name string // original filename (e.g. "test.png")
		data []byte
	}
	var fileArgs map[string]fileArg // arg name → file arg

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "Invalid multipart body", http.StatusBadRequest)
			return
		}
		args = make(map[string]interface{})
		fileArgs = make(map[string]fileArg)
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "Error reading multipart", http.StatusBadRequest)
				return
			}
			name := part.FormName()
			if name == "task" {
				b, _ := io.ReadAll(part)
				taskName = strings.TrimSpace(string(b))
			} else if part.FileName() != "" {
				b, _ := io.ReadAll(part)
				fileArgs[name] = fileArg{name: part.FileName(), data: b}
			} else {
				b, _ := io.ReadAll(part)
				// Try to parse as JSON value; fall back to string.
				var val interface{}
				if json.Unmarshal(b, &val) == nil {
					args[name] = val
				} else {
					args[name] = string(b)
				}
			}
		}
	} else {
		var body struct {
			Task string                 `json:"task"`
			Args map[string]interface{} `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		taskName = body.Task
		args = body.Args
		fileArgs = nil
	}

	if taskName == "" {
		http.Error(w, "Missing 'task' field", http.StatusBadRequest)
		return
	}

	// ── Find the task ────────────────────────────────────────────────────
	var task *formats.Task
	for i := range tasks {
		if tasks[i].Name == taskName {
			task = &tasks[i]
			break
		}
	}
	if task == nil {
		http.Error(w, fmt.Sprintf("Task %q not found", taskName), http.StatusNotFound)
		return
	}

	// ── Prepare env vars ────────────────────────────────────────────────
	env := os.Environ()
	env = append(env, "TASK="+taskName)
	if args != nil {
		for k, v := range args {
			env = append(env, "TASK_"+strings.ToUpper(k)+"="+taskArgValue(v))
		}
	}

	// ── Write file args to temp dir ─────────────────────────────────────
	var tmpDir string
	if len(fileArgs) > 0 {
		tmpDir, err = os.MkdirTemp("", "fbr-task-*")
		if err != nil {
			http.Error(w, "Failed to create temp dir", http.StatusInternalServerError)
			return
		}
		defer os.RemoveAll(tmpDir)

		for key, fa := range fileArgs {
			fp := filepath.Join(tmpDir, fa.name)
			if err := os.WriteFile(fp, fa.data, 0600); err != nil {
				http.Error(w, fmt.Sprintf("Failed to write file arg %q", key), http.StatusInternalServerError)
				return
			}
			env = append(env, "TASK_"+strings.ToUpper(key)+"="+fp)
		}
	}

	// ── Execute ─────────────────────────────────────────────────────────
	shell, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "powershell", "-Command"
	}
	cmd := exec.CommandContext(r.Context(), shell, flag, task.Script)
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	execErr := cmd.Run()

	// ── Build MCP-format response ───────────────────────────────────────
	result := map[string]interface{}{
		"content": []map[string]interface{}{{
			"type": "text",
			"text": stdout.String(),
		}},
		"isError": execErr != nil,
	}
	if execErr != nil && stderr.Len() > 0 {
		result["content"] = append(
			result["content"].([]map[string]interface{}),
			map[string]interface{}{"type": "text", "text": stderr.String()},
		)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// taskArgValue formats a task argument for injection into a shell environment.
// Strings and numbers pass through as plain text; arrays, maps, and booleans
// are encoded as JSON so scripts can parse structured values.
func taskArgValue(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case bool:
		return strconv.FormatBool(val)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

// apps

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/apps" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.handleCreateApp(w, r)
	case http.MethodGet:
		s.handleListApps(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func userFromContext(ctx context.Context) *db.User {
	u, _ := ctx.Value(userKey).(*db.User)
	return u
}

func (s *Server) canAccessApp(ctx context.Context, nonce string) bool {
	u := userFromContext(ctx)
	if u == nil {
		return false
	}
	if u.Role == "Superuser" || u.Role == "Admin" {
		return true
	}
	ok, _ := s.store.IsAppMember(nonce, u.ID)
	return ok
}

func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Environment string `json:"environment"`
		URL         string `json:"url"`
		OwnerID     *int64 `json:"owner_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	nonce, err := s.coreCreateApp(userFromContext(r.Context()), req.Name, req.Environment, req.URL, req.OwnerID)
	if err != nil {
		writeErr(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"nonce": nonce, "name": req.Name})
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	var apps []map[string]interface{}
	var err error
	u := userFromContext(r.Context())
	if u != nil && (u.Role == "Member" || u.Role == "Read-only") {
		apps, err = s.store.ListAppsForUser(u.ID)
	} else {
		apps, err = s.store.ListApps()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"apps": apps})
}

func (s *Server) handleAppDetail(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "apps" {
		http.NotFound(w, r)
		return
	}

	nonce := parts[2]

	// Sub-route: /api/apps/{nonce}/members
	if len(parts) >= 4 && parts[3] == "members" {
		s.handleAppMembers(w, r, nonce)
		return
	}

	// Sub-route: /api/apps/{nonce}/services
	if len(parts) >= 4 && parts[3] == "services" {
		s.handleAppServices(w, r, nonce)
		return
	}

	// Sub-route: /api/apps/{nonce}/web
	if len(parts) >= 4 && parts[3] == "web" {
		s.handleAppWeb(w, r, nonce)
		return
	}

	// Sub-route: /api/apps/{nonce}/db — query/watch/list/drop app databases
	// (design/app-databases.md). Dispatched before the app lookup: the gate
	// is gateDBTarget inside the core, and a database route shouldn't 404 on
	// an unknown nonce before auth gets its say.
	if len(parts) >= 4 && parts[3] == "db" {
		s.handleAppDB(w, r, nonce)
		return
	}

	// Sub-route: /api/apps/{nonce}/deploy
	if len(parts) >= 4 && parts[3] == "deploy" {
		s.handleAppDeploy(w, r, nonce)
		return
	}

	app, err := s.store.GetApp(nonce)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if !s.canAccessApp(r.Context(), nonce) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"nonce": app.Nonce,
			"name":  app.Name,
		})
	case http.MethodPut:
		var req struct {
			Name        string `json:"name"`
			Environment string `json:"environment"`
			URL         string `json:"url"`
			OwnerID     *int64 `json:"owner_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.coreUpdateApp(userFromContext(r.Context()), nonce, req.Name, req.Environment, req.URL, req.OwnerID); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := s.coreDeleteApp(userFromContext(r.Context()), nonce); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAppMembers(w http.ResponseWriter, r *http.Request, nonce string) {
	if !s.canAccessApp(r.Context(), nonce) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		ids, err := s.store.ListAppMembers(nonce)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"members": ids})
	case http.MethodPut:
		var req struct {
			Members []int64 `json:"members"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.coreSetAppMembers(userFromContext(r.Context()), nonce, req.Members); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAppServices(w http.ResponseWriter, r *http.Request, nonce string) {
	if !s.canAccessApp(r.Context(), nonce) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		links, err := s.store.GetAppServiceLinks(nonce)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"services": links})
	case http.MethodPut:
		var req struct {
			Services []int64 `json:"services"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.coreSetAppServices(userFromContext(r.Context()), nonce, req.Services); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAppWeb(w http.ResponseWriter, r *http.Request, nonce string) {
	actor := userFromContext(r.Context())

	// ?file=<relpath> selects single-file GET/PUT/DELETE on the web dir;
	// without it the bulk archive ops below run unchanged. PUT is only
	// valid with ?file= — it's a full-file raw-body replace; patches stay
	// MCP-only (no PATCH verb over HTTP).
	if file := r.URL.Query().Get("file"); file != "" {
		switch r.Method {
		case http.MethodGet:
			data, err := s.coreReadAppFile(actor, nonce, file, 0, 0)
			if err != nil {
				writeErr(w, err)
				return
			}
			w.Header().Set("Content-Type", http.DetectContentType(data))
			w.Write(data)
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read failed", http.StatusInternalServerError)
				return
			}
			if err := s.coreWriteAppFile(actor, nonce, file, data, ""); err != nil {
				writeErr(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			if err := s.coreDeleteAppFile(actor, nonce, file); err != nil {
				writeErr(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		data, slug, err := s.coreDownloadAppWeb(actor, nonce)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+slug+`.zip"`)
		w.Write(data)

	case http.MethodPost:
		if err := r.ParseMultipartForm(50 << 20); err != nil {
			http.Error(w, "file too large (50MB max)", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing 'file' field", http.StatusBadRequest)
			return
		}
		defer file.Close()

		data, readErr := io.ReadAll(file)
		if readErr != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		route, err := s.coreUploadAppWeb(actor, nonce, data, header.Filename)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"route": route})

	case http.MethodDelete:
		if err := s.coreDeleteAppWeb(actor, nonce); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAppDeploy copies one deployment slot over another.
// POST /api/apps/{nonce}/deploy with {"source": "dev|staging", "target":
// "staging|prod"} (source defaults to dev).
func (s *Server) handleAppDeploy(w http.ResponseWriter, r *http.Request, nonce string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	route, err := s.coreDeployApp(userFromContext(r.Context()), nonce, req.Source, req.Target)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"route": route})
}

// services

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/services" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.handleCreateService(w, r)
	case http.MethodGet:
		s.handleListServices(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string               `json:"name"`
		URL        string               `json:"url"`
		Descriptor db.ServiceDescriptor `json:"descriptor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	svc, err := s.coreCreateService(userFromContext(r.Context()), req.Name, req.URL, req.Descriptor)
	if err != nil {
		writeErr(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":         svc.ID,
		"name":       svc.Name,
		"url":        svc.URL,
		"descriptor": svc.Descriptor,
	})
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	services, err := s.store.ListServices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"services": services})
}

func (s *Server) handleServiceDetail(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "services" {
		http.NotFound(w, r)
		return
	}

	serviceID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		http.Error(w, "Invalid service id", http.StatusBadRequest)
		return
	}

	svc, err := s.store.GetService(serviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Sub-route: /api/services/{id}/apps
	if len(parts) >= 4 && parts[3] == "apps" {
		s.handleServiceApps(w, r, serviceID)
		return
	}

	// Sub-route: /api/services/{id}/tools
	if len(parts) >= 4 && parts[3] == "tools" {
		s.handleServiceTools(w, r, svc)
		return
	}

	// Sub-route: /api/services/{id}/files
	if len(parts) >= 4 && parts[3] == "files" {
		s.handleServiceFiles(w, r, serviceID)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(svc)
	case http.MethodPut:
		var req db.ServiceUpdate
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.coreUpdateService(userFromContext(r.Context()), serviceID, req.Name, req.URL, req.Descriptor); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := s.coreDeleteService(userFromContext(r.Context()), serviceID); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleServiceApps(w http.ResponseWriter, r *http.Request, serviceID int64) {
	apps, err := s.store.GetAppsUsingService(serviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"apps": apps})
}

func (s *Server) handleServiceTools(w http.ResponseWriter, r *http.Request, svc *db.Service) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tools, err := s.loadServiceToolSummaries(svc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"tools": tools})
}

func (s *Server) handleServiceFiles(w http.ResponseWriter, r *http.Request, serviceID int64) {
	actor := userFromContext(r.Context())

	switch r.Method {
	case http.MethodGet:
		data, filename, err := s.coreDownloadServiceFiles(actor, serviceID)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.Write(data)

	case http.MethodPost:
		if err := r.ParseMultipartForm(50 << 20); err != nil {
			http.Error(w, "file too large (50MB max)", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing 'file' field", http.StatusBadRequest)
			return
		}
		defer file.Close()

		data, readErr := io.ReadAll(file)
		if readErr != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		route, err := s.coreUploadServiceFiles(actor, serviceID, data, header.Filename)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"route": route})

	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		if err := s.coreWriteServiceFile(actor, serviceID, data, ""); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		if err := s.coreDeleteServiceFiles(actor, serviceID); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// users

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/users" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Name   string `json:"name"`
			Email  string `json:"email"`
			Role   string `json:"role"`
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		user, err := s.coreCreateUser(userFromContext(r.Context()), req.Name, req.Email, req.Role, req.Status)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(user)
	case http.MethodGet:
		users, err := s.store.ListUsers()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, u := range users {
			apps, _ := s.store.GetUserApps(u.ID)
			u.Apps = apps
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"users": users})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleUserDetail(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "users" {
		http.NotFound(w, r)
		return
	}

	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		http.Error(w, "Invalid user id", http.StatusBadRequest)
		return
	}

	// Sub-route: /api/users/{id}/apps
	if len(parts) >= 4 && parts[3] == "apps" {
		s.handleUserApps(w, r, id)
		return
	}

	// Sub-route: /api/users/{id}/ssh-key (admin-only)
	if len(parts) >= 4 && parts[3] == "ssh-key" {
		actor, _ := r.Context().Value(userKey).(*db.User)
		if actor == nil || (actor.Role != "Superuser" && actor.Role != "Admin") {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		s.handleUserSSHKey(w, r, id)
		return
	}

	user, err := s.store.GetUser(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(user)
	case http.MethodPut:
		var req struct {
			Name   string `json:"name"`
			Email  string `json:"email"`
			Role   string `json:"role"`
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		// Preserve existing metadata (e.g. SSH key) — the update request
		// only carries name/email/role/status.
		if err := s.coreUpdateUser(userFromContext(r.Context()), id, req.Name, req.Email, req.Role, req.Status, user.Metadata); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := s.coreDeleteUser(userFromContext(r.Context()), user); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleUserApps(w http.ResponseWriter, r *http.Request, userID int64) {
	switch r.Method {
	case http.MethodGet:
		nonces, err := s.store.GetUserApps(userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"apps": nonces})
	case http.MethodPut:
		var req struct {
			Apps []string `json:"apps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.coreSetUserApps(userFromContext(r.Context()), userID, req.Apps); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUserSSHKey lets admins manage SSH keys for other users.
func (s *Server) handleUserSSHKey(w http.ResponseWriter, r *http.Request, userID int64) {
	user, err := s.store.GetUser(userID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		info := (*sshkit.SSHKeyInfo)(nil)
		if user.Metadata != nil && user.Metadata.SSHKey != nil {
			info = user.Metadata.SSHKey
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ssh_key": info})

	case http.MethodPost:
		var req struct {
			Passphrase string `json:"passphrase"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		info, err := s.coreGenerateSSHKey(userFromContext(r.Context()), user, req.Passphrase)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ssh_key": info})

	case http.MethodDelete:
		if err := s.coreDeleteSSHKey(userFromContext(r.Context()), user); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRoles(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/roles" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		roles, err := s.store.ListRoles()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"roles": roles})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// audit

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/audit" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		entries, err := s.store.ListAudit(100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"audit": entries})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Admin auth helpers ---

type contextKey string

const userKey contextKey = "user"

func (s *Server) authWrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Pre-authenticated path: if userKey is already set, trust the caller and
		// skip bearer verification. Today the only other setter is handleAct
		// (act-token dispatch); the boundary is a two-member club. Without this
		// the auth-off synthetic user below would clobber the act-token's user.
		if u, ok := r.Context().Value(userKey).(*db.User); ok && u != nil {
			h(w, r)
			return
		}
		svcIDStr, err := s.store.GetSetting("admin_auth_service")
		if err != nil || svcIDStr == "" {
			// Auth off — synthetic superuser so role checks still work downstream.
			ctx := context.WithValue(r.Context(), userKey, &db.User{ID: -1, Name: "Setup Account", Role: "Superuser", Status: "Active"})
			h(w, r.WithContext(ctx))
			return
		}
		user, err := s.verifyAdminToken(r, svcIDStr)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if user.ID > 0 {
			s.lastSeenMu.Lock()
			if time.Since(s.lastSeenAt[user.ID]) > time.Minute {
				s.lastSeenAt[user.ID] = time.Now()
				s.lastSeenMu.Unlock()
				_ = s.store.TouchLastSeen(user.ID)
			} else {
				s.lastSeenMu.Unlock()
			}
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	}
}

// requireAnyRole returns middleware that allows only listed roles.
// Must come *after* authWrap (so userKey is populated).
func requireAnyRole(roles ...string) func(http.HandlerFunc) http.HandlerFunc {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			u, ok := r.Context().Value(userKey).(*db.User)
			if !ok || u == nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if !allowed[u.Role] {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next(w, r)
		}
	}
}

const appNonceKey contextKey = "appNonce"

// Pipeline chains middlewares right-to-left (outer to inner).
func pipeline(h http.HandlerFunc, mw ...func(http.HandlerFunc) http.HandlerFunc) http.HandlerFunc {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// requireAppServiceAccess returns middleware that checks:
//  1. X-App-Nonce header is present and corresponds to a real app
//  2. The authenticated user is a member of that app (or admin/superuser)
//  3. The app has a service of the given type with `allowed = true`
//
// On success, the app nonce is stored in the request context.
//
// This gates both the SSH session/sync API and the service proxy:
// a Member can only reach services that the app owner has approved.
func (s *Server) requireAppServiceAccess(serviceType string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			nonce := r.Header.Get("X-App-Nonce")
			if nonce == "" {
				http.Error(w, "Missing X-App-Nonce header", http.StatusUnauthorized)
				return
			}

			app, err := s.store.GetApp(nonce)
			if err != nil {
				http.Error(w, "Unknown app", http.StatusUnauthorized)
				return
			}

			user, _ := r.Context().Value(userKey).(*db.User)
			if user == nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Superuser/Admin bypass membership checks.
			if user.Role != "Superuser" && user.Role != "Admin" {
				member, err := s.store.IsAppMember(app.Nonce, user.ID)
				if err != nil || !member {
					http.Error(w, "Not a member of this app", http.StatusForbidden)
					return
				}
			}

			// Find a service of the requested type that's allowed for this app.
			links, err := s.store.GetAppServiceLinks(app.Nonce)
			if err != nil {
				http.Error(w, "Failed to check app services", http.StatusInternalServerError)
				return
			}

			found := false
			for _, link := range links {
				if !link.Allowed {
					continue
				}
				svc, err := s.store.GetService(link.ServiceID)
				if err != nil {
					continue
				}
				if svc.Descriptor.Type == serviceType {
					found = true
					break
				}
			}
			if !found {
				http.Error(w, fmt.Sprintf("App does not have %s service access", serviceType), http.StatusForbidden)
				return
			}

			next(w, r.WithContext(context.WithValue(r.Context(), appNonceKey, nonce)))
		}
	}
}

func (s *Server) verifyAdminToken(r *http.Request, serviceID string) (*db.User, error) {
	svcID, err := strconv.ParseInt(serviceID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid service ID in settings")
	}
	return s.verifyTaskToken(r, svcID)
}

// verifyTaskToken verifies a Bearer token against a referenced auth service.
// Used by tasks services that require token auth. Returns the authenticated
// user on success.
func (s *Server) verifyTaskToken(r *http.Request, authSvcID int64) (*db.User, error) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, fmt.Errorf("missing bearer token")
	}
	idTokenRaw := strings.TrimPrefix(authHeader, "Bearer ")

	svc, err := s.store.GetService(authSvcID)
	if err != nil {
		return nil, fmt.Errorf("auth service not found")
	}
	email, err := s.verifyIDToken(r.Context(), svc, idTokenRaw)
	if err != nil {
		return nil, err
	}
	user, err := s.store.GetUserByEmail(email)
	if err != nil {
		return nil, fmt.Errorf("user not found for %s", email)
	}
	return user, nil
}

func isFreshbreathToken(raw string) bool {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var peek struct {
		Iss string `json:"iss"`
	}
	json.Unmarshal(payload, &peek)
	return peek.Iss == "freshbreath"
}

// setRefreshCookie creates a refresh family, mints a refresh token for the
// given claims, and sets it as an HttpOnly cookie on the response.
func (s *Server) setRefreshCookie(w http.ResponseWriter, r *http.Request, claims *freshbreathClaims) {
	refreshData := freshbreathRefreshData{
		Kind:      claims.Kind,
		UserEmail: claims.UserEmail,
		UserRole:  claims.UserRole,
		UserName:  claims.UserName,
		ServiceID: claims.ServiceID,
	}
	if claims.Kind == "wrapped" {
		refreshData.UpstreamRefresh = claims.UpstreamRefresh
		refreshData.UpstreamTokenURL = claims.UpstreamTokenURL
		refreshData.UpstreamScopes = claims.UpstreamScopes
	}

	deviceLabel := deviceLabelFromUA(r.UserAgent())
	familyID, jti, err := s.newRefreshFamily(claims.UserEmail, claims.ServiceID, deviceLabel)
	if err != nil {
		// Log but don't block the login — the user can still use their
		// access token. Refresh will fail until they re-login.
		fmt.Fprintf(os.Stderr, "create refresh family: %v\n", err)
		s.makeRefreshCookie(w, refreshData)
		return
	}
	refreshData.FamilyID = familyID
	refreshData.JTI = jti
	s.makeRefreshCookie(w, refreshData)
}

func (s *Server) verifyIDToken(ctx context.Context, svc *db.Service, raw string) (string, error) {
	if isFreshbreathToken(raw) {
		claims, err := s.verifyFreshbreathToken(raw)
		if err != nil {
			return "", err
		}
		if claims == nil {
			return "", fmt.Errorf("not a freshbreath token")
		}
		// Service binding: an identity token is only valid against the service
		// that minted it. Without this, a token issued by *any* OIDC service
		// would authenticate against *this* one — letting an attacker who can
		// register an account at some other service under a victim's email
		// impersonate that victim here (e.g. log into the admin MCP).
		if claims.Kind != "identity" || claims.ServiceID != svc.ID {
			return "", fmt.Errorf("token was not issued by this auth service")
		}
		return claims.UserEmail, nil
	}
	provider, err := s.getOIDCProvider(ctx, svc.ID, svc.URL)
	if err != nil {
		return "", fmt.Errorf("OIDC provider: %w", err)
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: svc.Descriptor.ClientID}).Verify(ctx, raw)
	if err != nil {
		return "", fmt.Errorf("token verification: %w", err)
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return "", fmt.Errorf("claims extraction: %w", err)
	}
	if claims.Email == "" {
		return "", fmt.Errorf("no email in token")
	}
	return claims.Email, nil
}

func (s *Server) getOIDCProvider(ctx context.Context, serviceID int64, issuer string) (*oidc.Provider, error) {
	s.oidcProvidersMu.RLock()
	p, ok := s.oidcProviders[serviceID]
	s.oidcProvidersMu.RUnlock()
	if ok {
		return p, nil
	}

	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery for %s: %w", issuer, err)
	}

	s.oidcProvidersMu.Lock()
	s.oidcProviders[serviceID] = p
	s.oidcProvidersMu.Unlock()
	return p, nil
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, _ := r.Context().Value(userKey).(*db.User)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"user": user})
}

func (s *Server) handleSSHKey(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(userKey).(*db.User)
	if user == nil || user.ID < 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	s.handleUserSSHKey(w, r, user.ID)
}

// handleSessions handles GET /api/me/sessions (list active sessions) and
// DELETE /api/me/sessions (revoke all sessions for the current user).
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(userKey).(*db.User)
	if user == nil || user.ID < 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		families, err := s.store.ListRefreshFamilies(user.Email)
		if err != nil {
			http.Error(w, fmt.Sprintf("list sessions: %v", err), http.StatusInternalServerError)
			return
		}
		// Convert RefreshFamily to a session summary for the API response.
		sessions := make([]map[string]interface{}, 0, len(families))
		for _, f := range families {
			sessions = append(sessions, map[string]interface{}{
				"id":           f.ID,
				"service_id":   f.ServiceID,
				"device_label": f.DeviceLabel,
				"created_at":   f.CreatedAt,
				"expires_at":   f.ExpiresAt,
				"last_used_at": f.LastUsedAt,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"sessions": sessions})

	case http.MethodDelete:
		if err := s.store.RevokeUserRefreshFamilies(user.Email); err != nil {
			http.Error(w, fmt.Sprintf("revoke sessions: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSessionDetail handles DELETE /api/me/sessions/{id} — revoke a specific session.
func (s *Server) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(userKey).(*db.User)
	if user == nil || user.ID < 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract session ID from path: /api/me/sessions/{id}
	sessionID := strings.TrimPrefix(r.URL.Path, "/api/me/sessions/")
	if sessionID == "" {
		http.Error(w, "session ID required", http.StatusBadRequest)
		return
	}

	// Verify the session belongs to this user before revoking.
	fam, ok, err := s.store.GetRefreshFamily(sessionID)
	if err != nil {
		http.Error(w, fmt.Sprintf("lookup session: %v", err), http.StatusInternalServerError)
		return
	}
	if !ok || fam.UserEmail != user.Email {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	if err := s.store.RevokeRefreshFamily(sessionID); err != nil {
		http.Error(w, fmt.Sprintf("revoke session: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSSHSessions handles POST /ssh/sessions — open a new SSH session.
func (s *Server) handleSSHSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := r.Context().Value(userKey).(*db.User)
	if user == nil || user.ID < 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Host == "" {
		http.Error(w, "host is required", http.StatusBadRequest)
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}
	if req.Username == "" {
		req.Username = user.Email
	}

	session, err := s.sessionMgr.Open(user.ID, req.Host, req.Port, req.Username)
	if err != nil {
		if errors.Is(err, sshkit.ErrNoKey) {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to open SSH session: %v", err), http.StatusBadGateway)
		return
	}

	_ = s.store.LogAudit(user.Email, "ssh_session_open", fmt.Sprintf("%s@%s:%d", req.Username, req.Host, req.Port))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"sessionId": session.ID,
		"expiresAt": session.ExpiresAt,
	})
}

// handleSSHSessionDetail handles GET/DELETE /ssh/sessions/{id}.
func (s *Server) handleSSHSessionDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		// Fallback: parse from URL path for Go <1.22 style
		id = strings.TrimPrefix(r.URL.Path, "/ssh/sessions/")
	}
	if id == "" {
		http.Error(w, "Missing session ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		session, err := s.sessionMgr.Get(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"sessionId":   session.ID,
			"host":        session.Host,
			"port":        session.Port,
			"username":    session.Username,
			"connectedAt": session.ConnectedAt,
			"expiresAt":   session.ExpiresAt,
		})

	case http.MethodDelete:
		user, _ := r.Context().Value(userKey).(*db.User)
		if err := s.sessionMgr.Close(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if user != nil {
			_ = s.store.LogAudit(user.Email, "ssh_session_close", id)
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSSHHostKeys handles GET /ssh/known-hosts — list all stored host keys.
func (s *Server) handleSSHHostKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rows, err := s.store.DB().Query("SELECT host, port, fingerprint, trusted_at FROM ssh_host_keys ORDER BY host, port")
	if err != nil {
		http.Error(w, "Failed to list host keys", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var keys []map[string]interface{}
	for rows.Next() {
		var host string
		var port int
		var fp, trustedAt string
		if err := rows.Scan(&host, &port, &fp, &trustedAt); err != nil {
			continue
		}
		keys = append(keys, map[string]interface{}{
			"host":        host,
			"port":        port,
			"fingerprint": fp,
			"trustedAt":   trustedAt,
		})
	}
	if keys == nil {
		keys = []map[string]interface{}{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"keys": keys})
}

// handleSSHHostKeyDetail handles DELETE /ssh/known-hosts/{host}:{port}
// Removes a stored host key so a changed key can be accepted on next connect.
func (s *Server) handleSSHHostKeyDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		id = strings.TrimPrefix(r.URL.Path, "/ssh/known-hosts/")
	}
	if id == "" {
		http.Error(w, "Missing host key identifier", http.StatusBadRequest)
		return
	}

	// Parse "host:port" from the path segment
	host, portStr, err := net.SplitHostPort(id)
	if err != nil {
		http.Error(w, "Invalid host:port format", http.StatusBadRequest)
		return
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	if port == 0 {
		port = 22
	}

	if err := s.store.DeleteSSHHostKey(host, port); err != nil {
		http.Error(w, "Failed to delete host key", http.StatusInternalServerError)
		return
	}

	user, _ := r.Context().Value(userKey).(*db.User)
	if user != nil {
		_ = s.store.LogAudit(user.Email, "ssh_host_key_delete", fmt.Sprintf("%s:%d", host, port))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		result := map[string]interface{}{"admin_auth_service": nil, "default_app": nil}
		if v, err := s.store.GetSetting("admin_auth_service"); err == nil && v != "" {
			result["admin_auth_service"] = v
		}
		if v, err := s.store.GetSetting("default_app"); err == nil && v != "" {
			result["default_app"] = v
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	case http.MethodPut:
		var body struct {
			AdminAuthService *string `json:"admin_auth_service"`
			DefaultApp       *string `json:"default_app"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.coreUpdateSettings(userFromContext(r.Context()), body.AdminAuthService, body.DefaultApp); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RefreshToken  string `json:"refresh_token"`
		ServiceID     int64  `json:"service_id"`
		ClientID      string `json:"client_id,omitempty"`
		TokenEndpoint string `json:"token_endpoint,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if body.RefreshToken == "" || body.ServiceID == 0 {
		http.Error(w, "refresh_token and service_id required", http.StatusBadRequest)
		return
	}
	svc, err := s.store.GetService(body.ServiceID)
	if err != nil {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	// Resolve token endpoint ourselves using the service's config.
	tokenEndpoint, err := s.resolveTokenEndpoint(r.Context(), svc)
	if err != nil {
		http.Error(w, "Could not determine token endpoint", http.StatusInternalServerError)
		return
	}

	// If client sent a token_endpoint, validate it matches our service — prevents
	// confused-deputy attacks where service_id is trusted but endpoint is swapped.
	if body.TokenEndpoint != "" {
		clientNorm := strings.TrimSuffix(body.TokenEndpoint, "/")
		serverNorm := strings.TrimSuffix(tokenEndpoint, "/")
		if clientNorm != serverNorm {
			http.Error(w, "token_endpoint does not match service", http.StatusBadRequest)
			return
		}
	}

	// client_id: descriptor wins for pre-registered; body wins for DCR services
	clientID := svc.Descriptor.ClientID
	if clientID == "" {
		clientID = body.ClientID
	}
	if clientID == "" {
		http.Error(w, "client_id required", http.StatusBadRequest)
		return
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {body.RefreshToken},
		"client_id":     {clientID},
	}
	if svc.Descriptor.ClientSecret != "" {
		form.Set("client_secret", svc.Descriptor.ClientSecret)
	}
	if svc.Descriptor.Scopes != "" {
		form.Set("scope", svc.Descriptor.Scopes)
	}

	resp, err := s.httpClient.PostForm(tokenEndpoint, form)
	if err != nil {
		http.Error(w, fmt.Sprintf("Refresh request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// handleSSHAuth handles the SSH passphrase login flow.
// GET renders the email + passphrase form.
// POST verifies credentials and completes auth via postMessage.
func (s *Server) handleSSHAuth(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state := r.URL.Query().Get("state")
		if state == "" {
			http.Error(w, "Missing state parameter", http.StatusBadRequest)
			return
		}
		isMCP := r.URL.Query().Get("mcp") == "1"
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := strings.Replace(sshAuthFormHTML, "{{STATE}}", state, 1)
		html = strings.Replace(html, "{{IS_MCP}}", fmt.Sprintf("%v", isMCP), 1)
		w.Write([]byte(html))

	case http.MethodPost:
		var req struct {
			State      string `json:"state"`
			Email      string `json:"email"`
			Passphrase string `json:"passphrase"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if req.State == "" || req.Email == "" || req.Passphrase == "" {
			http.Error(w, "state, email, and passphrase required", http.StatusBadRequest)
			return
		}

		// Look up the pending auth entry. Kept alive until its TTL so a
		// mistyped passphrase (or a back-button revisit) can be retried.
		pending, ok, expired := s.getPending(req.State)
		ok = ok && pending.serviceType == "ssh"

		if !ok && expired {
			http.Error(w, "Auth state expired — restart the login flow", http.StatusBadRequest)
			return
		}
		if !ok {
			http.Error(w, "Unknown auth state", http.StatusBadRequest)
			return
		}

		// Look up the user by email
		user, err := s.store.GetUserByEmail(req.Email)
		if err != nil {
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}
		if user.Metadata == nil || user.Metadata.SSHKey == nil {
			http.Error(w, "No SSH key configured for this user", http.StatusUnauthorized)
			return
		}

		// Verify the passphrase
		if !sshkit.VerifyPassphrase(user.Metadata.SSHKey, req.Passphrase) {
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}

		// Add decrypted key to the in-process SSH agent with 1h TTL.
		// Agent TTL is decoupled from the web JWT — agent timeout doesn't
		// invalidate the web session, and vice versa.
		if s.agentMgr != nil {
			if err := s.agentMgr.AddKey(user.ID, user.Metadata.SSHKey, req.Passphrase, 1*time.Hour); err != nil {
				log.Printf("agent add key for user %d: %v", user.ID, err)
			}
		}

		_ = s.store.LogAudit(req.Email, "login", "admin panel (SSH)")

		// Mint a panel JWT for this user
		idToken, err := s.mintFreshbreathToken("identity", user.Email, user.Role, user.Name, pending.serviceID, nil)
		if err != nil {
			http.Error(w, "Token generation failed", http.StatusInternalServerError)
			return
		}

		now := time.Now()

		// MCP flow — return redirect URL for the form's JS to navigate to
		if _, hasMCP, _ := s.getMCPPending(pending.appNonce); hasMCP {
			redirectURL, ok := s.completeMCPDirectAuth(w, pending.appNonce, idToken, user.Email, "", now.Add(accessTokenTTL))
			if !ok {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"redirect": redirectURL})
			return
		}

		s.completeAuth(w, r, pending, &db.OAuthData{
			AccessToken: idToken,
			TokenType:   "Bearer",
			ExpiresAt:   now.Add(accessTokenTTL),
			Claims: map[string]interface{}{
				"email": user.Email,
				"name":  user.Name,
				"sub":   fmt.Sprintf("user:%d", user.ID),
				"iss":   "freshbreath",
			},
			IDToken: idToken,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

const sshAuthFormHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>SSH Login — Fresh Breath</title>
<style>
  *{box-sizing:border-box;margin:0;padding:0}
  body{font-family:system-ui,-apple-system,sans-serif;background:#0f0f11;color:#e4e4e7;display:grid;place-items:center;min-height:100vh}
  .card{background:#18181b;border:1px solid #27272a;border-radius:12px;padding:32px;width:100%;max-width:380px}
  h1{font-size:18px;font-weight:600;margin-bottom:4px}
  p.lead{color:#71717a;font-size:14px;margin-bottom:24px}
  label{display:block;font-size:13px;color:#a1a1aa;margin-bottom:6px;font-weight:500}
  input{width:100%;padding:10px 12px;border:1px solid #27272a;border-radius:8px;background:#0f0f11;color:#e4e4e7;font-size:14px;margin-bottom:16px;outline:none}
  input:focus{border-color:#6366f1}
  button{width:100%;padding:10px;border:none;border-radius:8px;background:#6366f1;color:#fff;font-size:14px;font-weight:600;cursor:pointer}
  button:hover{background:#4f46e5}
  button:disabled{opacity:.5;cursor:not-allowed}
  .err{color:#f87171;font-size:13px;margin-bottom:12px;display:none}
  .err.show{display:block}
</style></head><body>
<div class="card">
  <h1>SSH Authentication</h1>
  <p class="lead">Sign in with your SSH key passphrase.</p>
  <div class="err" id="err"></div>
  <form id="f">
    <label for="e">Email</label>
    <input id="e" type="email" required autocomplete="email" autofocus/>
    <label for="p">Passphrase</label>
    <input id="p" type="password" required autocomplete="current-password"/>
    <button type="submit" id="btn">Sign in</button>
  </form>
</div>
<script>
(function(){
  var state="{{STATE}}";
  var isMCP="{{IS_MCP}}"==="true";
  document.getElementById('f').onsubmit=function(ev){
    ev.preventDefault();
    var btn=document.getElementById('btn');
    var errEl=document.getElementById('err');
    errEl.className='err';
    errEl.textContent='';
    btn.disabled=true;btn.textContent='Signing in…';
    fetch('/service/ssh-auth',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({state:state,email:document.getElementById('e').value,passphrase:document.getElementById('p').value})
    }).then(function(r){
      if(!r.ok){
        r.text().then(function(t){errEl.textContent=t||'Login failed';errEl.className='err show'});
        btn.disabled=false;btn.textContent='Sign in';
        return;
      }
      if(isMCP){
        r.json().then(function(d){if(d.redirect){window.location.href=d.redirect;}});
      } else {
        r.text().then(function(html){document.open();document.write(html);document.close()});
      }
    }).catch(function(){errEl.textContent='Network error';errEl.className='err show';btn.disabled=false;btn.textContent='Sign in'});
  };
})();
</script></body></html>`

// handleAPIKeyAuth handles the API key entry flow.
// GET renders the key entry form.
// POST accepts the key and completes auth via postMessage or MCP redirect.
func (s *Server) handleAPIKeyAuth(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state := r.URL.Query().Get("state")
		if state == "" {
			http.Error(w, "Missing state parameter", http.StatusBadRequest)
			return
		}
		svcID := r.URL.Query().Get("service_id")
		isMCP := r.URL.Query().Get("mcp") == "1"
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := strings.Replace(apiKeyAuthFormHTML, "{{STATE}}", state, 1)
		html = strings.Replace(html, "{{SERVICE_ID}}", svcID, 1)
		html = strings.Replace(html, "{{IS_MCP}}", fmt.Sprintf("%v", isMCP), 1)
		w.Write([]byte(html))

	case http.MethodPost:
		var req struct {
			State  string `json:"state"`
			APIKey string `json:"api_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if req.State == "" || req.APIKey == "" {
			http.Error(w, "state and api_key required", http.StatusBadRequest)
			return
		}

		pending, ok, expired := s.getPending(req.State)
		ok = ok && pending.serviceType == "apikey"

		if !ok && expired {
			http.Error(w, "Auth state expired — restart the login flow", http.StatusBadRequest)
			return
		}
		if !ok {
			http.Error(w, "Unknown auth state", http.StatusBadRequest)
			return
		}

		// MCP flow — complete via redirect to the MCP client's redirect_uri
		if _, hasMCP, _ := s.getMCPPending(pending.appNonce); hasMCP {
			redirectURL, ok := s.completeMCPDirectAuth(w, pending.appNonce, req.APIKey, "api-key-user", "", time.Now().Add(365*24*time.Hour))
			if !ok {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"redirect": redirectURL})
			return
		}

		// Browser (non-MCP) flow — return a fabricated token
		now := time.Now()
		svc, err := s.store.GetService(pending.serviceID)
		if err != nil {
			http.Error(w, "Service not found", http.StatusInternalServerError)
			return
		}

		_ = s.store.LinkAppService(pending.appNonce, pending.serviceID)

		// Fabricate a simple token wrapping the API key
		s.completeAuth(w, r, pending, &db.OAuthData{
			AccessToken: req.APIKey,
			TokenType:   "Bearer",
			ExpiresAt:   now.Add(24 * time.Hour * 365), // long-lived
			Claims: map[string]interface{}{
				"sub": "api-key-user",
				"iss": "freshbreath",
			},
			Proxied: svc.Descriptor.Proxied,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

const apiKeyAuthFormHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>API Key — Fresh Breath</title>
<style>
  *{box-sizing:border-box;margin:0;padding:0}
  body{font-family:system-ui,-apple-system,sans-serif;background:#0f0f11;color:#e4e4e7;display:grid;place-items:center;min-height:100vh}
  .card{background:#18181b;border:1px solid #27272a;border-radius:12px;padding:32px;width:100%;max-width:380px}
  h1{font-size:18px;font-weight:600;margin-bottom:4px}
  p.lead{color:#71717a;font-size:14px;margin-bottom:24px}
  label{display:block;font-size:13px;color:#a1a1aa;margin-bottom:6px;font-weight:500}
  input{width:100%;padding:10px 12px;border:1px solid #27272a;border-radius:8px;background:#0f0f11;color:#e4e4e7;font-size:14px;margin-bottom:16px;outline:none}
  input:focus{border-color:#6366f1}
  button{width:100%;padding:10px;border:none;border-radius:8px;background:#6366f1;color:#fff;font-size:14px;font-weight:600;cursor:pointer}
  button:hover{background:#4f46e5}
  button:disabled{opacity:.5;cursor:not-allowed}
  .err{color:#f87171;font-size:13px;margin-bottom:12px;display:none}
  .err.show{display:block}
</style></head><body>
<div class="card">
  <h1>API Key Authentication</h1>
  <p class="lead">Enter your API key or access token.</p>
  <div class="err" id="err"></div>
  <form id="f">
    <label for="k">API Key</label>
    <input id="k" type="password" required autofocus/>
    <button type="submit" id="btn">Submit</button>
  </form>
</div>
<script>
(function(){
  var state="{{STATE}}";
  var serviceId="{{SERVICE_ID}}";
  var isMCP="{{IS_MCP}}"==="true";
  document.getElementById('f').onsubmit=function(ev){
    ev.preventDefault();
    var btn=document.getElementById('btn');
    var errEl=document.getElementById('err');
    errEl.className='err';
    errEl.textContent='';
    btn.disabled=true;btn.textContent='Submitting…';
    fetch('/service/apikey-auth',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({state:state,api_key:document.getElementById('k').value})
    }).then(function(r){
      if(!r.ok){
        r.text().then(function(t){errEl.textContent=t||'Submission failed';errEl.className='err show'});
        btn.disabled=false;btn.textContent='Submit';
        return;
      }
      if(isMCP){
        r.json().then(function(d){
          if(d.redirect){window.location.href=d.redirect;}
        });
      } else {
        r.text().then(function(html){document.open();document.write(html);document.close()});
      }
    }).catch(function(){errEl.textContent='Network error';errEl.className='err show';btn.disabled=false;btn.textContent='Submit'});
  };
})();
</script></body></html>`
