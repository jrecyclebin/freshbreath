package server

import (
	"bytes"
	"context"
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
	// + form-body refresh); this variant scopes the refresh_token cookie by
	// auth record so a browser holding several records' refresh tokens keeps
	// them in distinct cookie slots (see makeRefreshCookie). Both routes share
	// the handler; the path record id is only consulted on the cookie path.
	s.mux.HandleFunc("/oauth/token/{authID}", s.oauthSrv.handleToken)
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
	s.mux.HandleFunc("/api/auth", s.authWrap(pipeline(s.handleAuthRecords, adminPlus)))
	s.mux.HandleFunc("/api/auth/", s.authWrap(pipeline(s.handleAuthRecordDetail, adminPlus)))

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

	// The gate for this door: the admin record for the control panel, the
	// app's own for a hosted app. The id matters as much as the name —
	// it is the store key, so a page can tell whether it already holds a
	// live credential for its gate without asking the server.
	var gate *db.AuthRecord
	if app, err := s.store.GetApp(appNonce); err == nil {
		gate, _ = s.resolveAppGate(app)
	} else {
		gate, _ = s.adminAuthRecord()
	}
	authRequired := false
	authRecordID := int64(0)
	authRecordName := ""
	authKind := ""
	if gate != nil && gate.Kind != db.AuthAnonymous {
		authRequired = true
		authRecordID = gate.ID
		authRecordName = gate.Name
		authKind = gate.Kind
	}
	return []byte(fmt.Sprintf("window.__HOMESLICE_CONFIG = { apiBase: %q, authRequired: %v, authRecordID: %d, authRecordName: %q, authKind: %q, appNonce: %q, version: %q, commit: %q };\n",
		apiBase, authRequired, authRecordID, authRecordName, authKind, appNonce, s.version, s.commit))
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

	// The door owns the gate: this is the app door, so the app's
	// protected_by governs — the service's own gate does not stack.
	gate, err := s.resolveAppGate(app)
	if err != nil {
		http.Error(w, fmt.Sprintf("Gate resolution failed: %v", err), http.StatusInternalServerError)
		return
	}
	claims, _, err := s.verifyGateRequest(gate, r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	var presentedKey string
	if gate != nil && gate.Kind == db.AuthAPIKey {
		presentedKey = presentedGateKey(gate, r)
	}
	cred, err := s.resolveOutboundCred(svc, gate, claims, presentedKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// X-API-Key is meaningful only against an api_key acts_as record,
	// where it overrides the stored key; anywhere else it's a caller error.
	if xkey := r.Header.Get("X-API-Key"); xkey != "" {
		rec, ok := s.actsAsRecord(svc)
		if !ok || rec.Kind != db.AuthAPIKey {
			http.Error(w, "X-API-Key is only valid when the service acts as an api_key auth record", http.StatusBadRequest)
			return
		}
		cred.Token, cred.Header = xkey, rec.Descriptor.Header
	}

	resp, err := s.serviceDoProxy(svc, r, cred)
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

	// ── The app door's gate ─────────────────────────────────────────
	gate, err := s.resolveAppGate(app)
	if err != nil {
		http.Error(w, fmt.Sprintf("Gate resolution failed: %v", err), http.StatusInternalServerError)
		return
	}
	claims, authUser, err := s.verifyGateRequest(gate, r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	var presentedKey string
	if gate != nil && gate.Kind == db.AuthAPIKey {
		presentedKey = presentedGateKey(gate, r)
	}
	cred, err := s.resolveOutboundCred(svc, gate, claims, presentedKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token := cred.Token
	if raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); cred.Verbatim && !isFreshbreathToken(raw) {
		// An open gate passes a caller's own upstream bearer through
		// verbatim; a Fresh Breath token is not one.
		token = raw
	}

	// Identity built-ins come from the verified gate claims: the resolved
	// user (email + numerical ID) and the token subject. An anonymous gate
	// leaves them empty — tools referencing $token_email and friends then
	// fail, because they require a logged-in caller.
	auth := formats.VirtualAuth{Token: token}
	if claims != nil {
		auth.Email = claims.UserEmail
		auth.Sub = claims.Subject
	}
	if authUser != nil {
		auth.Email = authUser.Email
		auth.UserID = authUser.ID
	}

	// ── Dispatch by service type ────────────────────────────────────
	switch svc.Descriptor.Type {
	case "tasks":
		s.handleTaskCallInner(w, r, svc, token)
	case "virtual":
		s.handleVirtualCallInner(w, r, svc, auth)
	default:
		http.Error(w, "Service type does not support /service/call", http.StatusBadRequest)
	}
}

// actsAsRecord fetches a service's outbound record, if set.
func (s *Server) actsAsRecord(svc *db.Service) (*db.AuthRecord, bool) {
	if svc.ActsAs == nil {
		return nil, false
	}
	rec, err := s.store.GetAuthRecord(*svc.ActsAs)
	if err != nil {
		return nil, false
	}
	return rec, true
}

func (s *Server) handleTaskCallInner(w http.ResponseWriter, r *http.Request, svc *db.Service, token string) {
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
		s.handleTaskExec(w, r, svc, token)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVirtualCallInner(w http.ResponseWriter, r *http.Request, svc *db.Service, auth formats.VirtualAuth) {
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
		s.handleVirtualExec(w, r, svc, auth)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVirtualExec(w http.ResponseWriter, r *http.Request, svc *db.Service, auth formats.VirtualAuth) {
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

	// SQL steps run against the app's database (design/app-databases.md).
	sqlRunner := s.browserSQLRunner(svc, r.Header.Get("X-App-Nonce"), body.Args)

	result, err := formats.ExecuteVirtualTool(s.httpClient, tools, body.Task, body.Args, auth, sqlRunner)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleTaskExec(w http.ResponseWriter, r *http.Request, svc *db.Service, token string) {
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
	// The resolved outbound credential reaches the shell as TASK_TOKEN —
	// how a scheduled task acts as a stored api_key record with nobody
	// present (design/decoupled-auth.md, stated limit).
	if token != "" {
		env = append(env, "TASK_TOKEN="+token)
	}
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
		ProtectedBy *int64 `json:"protected_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	nonce, err := s.coreCreateApp(userFromContext(r.Context()), req.Name, req.Environment, req.URL, req.OwnerID, req.ProtectedBy)
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
			"nonce":        app.Nonce,
			"name":         app.Name,
			"protected_by": app.ProtectedBy,
		})
	case http.MethodPut:
		var req struct {
			Name        string `json:"name"`
			Environment string `json:"environment"`
			URL         string `json:"url"`
			OwnerID     *int64 `json:"owner_id"`
			ProtectedBy *int64 `json:"protected_by"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.coreUpdateApp(userFromContext(r.Context()), nonce, req.Name, req.Environment, req.URL, req.OwnerID, req.ProtectedBy); err != nil {
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
	var req db.ServiceUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	svc, err := s.coreCreateService(userFromContext(r.Context()), req.Name, req.URL, req.Descriptor, req.ProtectedBy, req.ActsAs)
	if err != nil {
		writeErr(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(svc)
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
		if err := s.coreUpdateService(userFromContext(r.Context()), serviceID, req.Name, req.URL, req.Descriptor, req.ProtectedBy, req.ActsAs); err != nil {
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

// ── Auth records API ────────────────────────────────────────────────

func (s *Server) handleAuthRecords(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/auth" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		records, err := s.store.ListAuthRecords()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if records == nil {
			records = []*db.AuthRecord{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"auth": records})
	case http.MethodPost:
		var req struct {
			Name       string            `json:"name"`
			Kind       string            `json:"kind"`
			Descriptor db.AuthDescriptor `json:"descriptor"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		rec, err := s.coreCreateAuth(userFromContext(r.Context()), req.Name, req.Kind, req.Descriptor)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAuthRecordDetail(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "api" || parts[1] != "auth" {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		http.Error(w, "Invalid auth record id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		rec, err := s.store.GetAuthRecord(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	case http.MethodPut:
		var req struct {
			Name       string            `json:"name"`
			Kind       string            `json:"kind"`
			Descriptor db.AuthDescriptor `json:"descriptor"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.coreUpdateAuth(userFromContext(r.Context()), id, req.Name, req.Kind, req.Descriptor); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := s.coreDeleteAuth(userFromContext(r.Context()), id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
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
		rec, err := s.adminAuthRecord()
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if rec == nil {
			// Auth off — synthetic superuser so role checks still work downstream.
			ctx := context.WithValue(r.Context(), userKey, &db.User{ID: -1, Name: "Setup Account", Role: "Superuser", Status: "Active"})
			h(w, r.WithContext(ctx))
			return
		}
		_, user, err := s.verifyGateRequest(rec, r)
		if err != nil || user == nil {
			// The admin gate demands a real user row — a valid ext: token
			// from the right provider is still nobody we know.
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
		families, err := s.store.ListRefreshFamilies(subjectForUser(user))
		if err != nil {
			http.Error(w, fmt.Sprintf("list sessions: %v", err), http.StatusInternalServerError)
			return
		}
		// Convert RefreshFamily to a session summary for the API response.
		sessions := make([]map[string]interface{}, 0, len(families))
		for _, f := range families {
			sessions = append(sessions, map[string]interface{}{
				"id":           f.ID,
				"auth_id":      f.AuthID,
				"device_label": f.DeviceLabel,
				"created_at":   f.CreatedAt,
				"expires_at":   f.ExpiresAt,
				"last_used_at": f.LastUsedAt,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"sessions": sessions})

	case http.MethodDelete:
		if err := s.store.RevokeUserRefreshFamilies(subjectForUser(user)); err != nil {
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
	if !ok || fam.Subject != subjectForUser(user) {
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
		result := map[string]interface{}{"admin_auth_service": nil, "default_app": nil, "mcp_database_mode": "read-only"}
		if v, err := s.store.GetSetting("admin_auth_service"); err == nil && v != "" {
			result["admin_auth_service"] = v
		}
		if v, err := s.store.GetSetting("default_app"); err == nil && v != "" {
			result["default_app"] = v
		}
		if v, err := s.store.GetSetting("mcp_database_mode"); err == nil && v == "full-access" {
			result["mcp_database_mode"] = v
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	case http.MethodPut:
		var body struct {
			AdminAuthService *string `json:"admin_auth_service"`
			DefaultApp       *string `json:"default_app"`
			McpDatabaseMode  *string `json:"mcp_database_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.coreUpdateSettings(userFromContext(r.Context()), body.AdminAuthService, body.DefaultApp, body.McpDatabaseMode); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
