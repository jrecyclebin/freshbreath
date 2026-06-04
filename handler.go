package main

import (
  "archive/zip"
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
  "sort"
  "strconv"
  "strings"
  "time"

  "github.com/coreos/go-oidc/v3/oidc"
)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
  if origin := r.Header.Get("Origin"); origin != "" {
    if nonce := r.Header.Get("X-App-Nonce"); nonce != "" {
      // Admin panel nonce — verify origin matches our own base URL
      if nonce == s.adminNonce {
        if u, err := url.Parse(s.config.PublicBaseURL); err == nil && u.Scheme+"://"+u.Host != origin {
          http.Error(w, "Origin not allowed", http.StatusForbidden)
          return
        }
      } else if app, err := s.store.GetApp(nonce); err == nil && app.URL != "" {
        if appURL, err := url.Parse(app.URL); err != nil || appURL.Scheme+"://"+appURL.Host != origin {
          http.Error(w, "Origin not allowed", http.StatusForbidden)
          return
        }
      }
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
  s.mux.HandleFunc("/",                s.handleIndex)
  s.mux.HandleFunc("/control",         s.handleControl)
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
  s.mux.HandleFunc("/env.js",          s.handleEnv)
  s.mux.HandleFunc("/frbr.js",         s.handleFrbr)
  s.mux.HandleFunc("/service/login",   s.handleLogin)
  s.mux.HandleFunc("/service/callback", s.handleCallback)
  s.mux.HandleFunc("/service/refresh",  s.handleRefresh)
  s.mux.HandleFunc("/service/ssh-auth", s.handleSSHAuth)
  s.mux.HandleFunc("/service/{id}/",   s.handleServiceProxy)
  s.mux.HandleFunc("/service/task/{name}", s.handleTaskCall)

  // Admin API — role-gated
  superuser := requireAnyRole("Superuser")
  adminPlus := requireAnyRole("Superuser", "Admin")
  anyRole   := requireAnyRole("Superuser", "Admin", "Member", "Read-only")

  s.mux.HandleFunc("/api/apps",              s.authWrap(pipeline(s.handleApps, anyRole)))
  s.mux.HandleFunc("/api/apps/",             s.authWrap(pipeline(s.handleAppDetail, anyRole)))
  s.mux.HandleFunc("/api/services",          s.authWrap(pipeline(s.handleServices, adminPlus)))
  s.mux.HandleFunc("/api/services/",         s.authWrap(pipeline(s.handleServiceDetail, adminPlus)))
  s.mux.HandleFunc("/api/users",             s.authWrap(pipeline(s.handleUsers, adminPlus)))
  s.mux.HandleFunc("/api/users/",            s.authWrap(pipeline(s.handleUserDetail, adminPlus)))
  s.mux.HandleFunc("/api/roles",             s.authWrap(pipeline(s.handleRoles, anyRole)))
  s.mux.HandleFunc("/api/audit",             s.authWrap(pipeline(s.handleAudit, anyRole)))
  s.mux.HandleFunc("/api/me",               s.authWrap(pipeline(s.handleMe, anyRole)))
  s.mux.HandleFunc("/api/me/ssh-key",       s.authWrap(pipeline(s.handleSSHKey, anyRole)))
  s.mux.HandleFunc("/api/settings",         s.authWrap(pipeline(s.handleSettings, superuser)))

  // SSH host keys (TOFU) — same access gate as SSH sessions
  s.mux.HandleFunc("/ssh/known-hosts",       s.authWrap(pipeline(s.handleSSHHostKeys, s.requireAppServiceAccess("ssh"))))
  s.mux.HandleFunc("/ssh/known-hosts/",      s.authWrap(pipeline(s.handleSSHHostKeyDetail, s.requireAppServiceAccess("ssh"))))

  // SSH sessions — admin+ gets access via adminNonce; members via app service check
  s.mux.HandleFunc("/ssh/sessions",     s.authWrap(pipeline(s.handleSSHSessions, s.requireAppServiceAccess("ssh"))))
  s.mux.HandleFunc("/ssh/sessions/",    s.authWrap(pipeline(s.handleSSHSessionDetail, s.requireAppServiceAccess("ssh"))))

  // File sync — same access gate as SSH sessions
  s.mux.HandleFunc("/sync/files/diff",                    s.authWrap(pipeline(s.handleSyncDiff, s.requireAppServiceAccess("ssh"))))
  s.mux.HandleFunc("/sync/files/{path...}",              s.authWrap(pipeline(s.handleSyncFileOps, s.requireAppServiceAccess("ssh"))))
  s.mux.HandleFunc("/sync/files",                        s.authWrap(pipeline(s.handleSyncList, s.requireAppServiceAccess("ssh"))))

  s.rebuildHostedRoutes()
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
func appSlug(app *App) string {
  if app.URL != "" && !strings.Contains(app.URL, "://") {
    return strings.Trim(app.URL, "/")
  }
  return slugify(app.Name)
}

// rebuildHostedRoutes reloads the slug→nonce map from the DB.
func (s *Server) rebuildHostedRoutes() {
  apps, err := s.store.ListHostedApps()
  if err != nil {
    log.Printf("rebuildHostedRoutes: %v", err)
    return
  }
  routes := make(map[string]string, len(apps))
  for _, a := range apps {
    if slug := appSlug(a); slug != "" {
      routes[slug] = a.Nonce
    }
  }
  s.hostedMu.Lock()
  s.hostedRoutes = routes
  s.hostedMu.Unlock()
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
  if r.URL.Path == "/" {
    if nonce, _ := s.store.GetSetting("default_app"); nonce != "" && nonce != "control" {
      s.hostedMu.RLock()
      for slug, n := range s.hostedRoutes {
        if n == nonce {
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

  s.hostedMu.RLock()
  nonce, ok := s.hostedRoutes[slug]
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

  webDir := filepath.Join("apps", nonce, "web")

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

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
  data, err := os.ReadFile(filepath.Join(s.config.Dir, "web", "control.html"))
  if err != nil {
    http.Error(w, "control.html not found", http.StatusInternalServerError)
    return
  }
  w.Header().Set("Content-Type", "text/html; charset=utf-8")
  w.Write(data)
}

func (s *Server) renderEnvJS() []byte {
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
  return []byte(fmt.Sprintf("window.__HOMESLICE_CONFIG = { apiBase: %q, authRequired: %v, authServiceName: %q, authServiceURL: %q, authServiceType: %q, adminNonce: %q, version: %q, commit: %q };\n",
    s.config.PublicBaseURL, authRequired, authServiceName, authServiceURL, authServiceType, s.adminNonce, version, commit))
}

func (s *Server) handleEnv(w http.ResponseWriter, r *http.Request) {
  w.Header().Set("Content-Type", "application/javascript")
  w.Write(s.renderEnvJS())
}

func (s *Server) handleFrbr(w http.ResponseWriter, r *http.Request) {
  data, err := os.ReadFile("web/frbr.js")
  if err != nil {
    http.Error(w, "frbr.js not found", http.StatusInternalServerError)
    return
  }
  w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
  w.Write(s.renderEnvJS())
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

  // SSH auth — return a URL to the passphrase form
  if svc.Descriptor.Type == "ssh" {
    state := genNonce()
    s.pendingMu.Lock()
    s.pending[state] = &pendingAuth{
      serviceID:   svc.ID,
      serviceURL:  svc.URL,
      appNonce:    nonce,
      appState:    appState,
      serviceType: "ssh",
    }
    s.pendingMu.Unlock()

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
      "type": "ssh",
      "url":  fmt.Sprintf(s.config.PublicBaseURL + "/service/ssh-auth?state=%s&service_id=%d", state, svc.ID),
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
    s.pendingMu.Lock()
    s.pending[state] = &pendingAuth{
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
    }
    s.pendingMu.Unlock()

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
      "type": "redirect",
      "url":  authURL,
    })
    return
  }

  // API-key auth — no OAuth flow, just return service info as JSON
  if svc.Descriptor.Auth == "key" {
    _ = s.store.LinkAppService(nonce, svc.ID)
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
      "hasKey":     svc.Descriptor.APIKey != "",
    })
    return
  }

  authURL, clientID, clientSecret, tokenURL, state, verifier, err := s.serviceBeginAuth(r.Context(), svc, redirectURI)
  if err != nil {
    http.Error(w, fmt.Sprintf("Failed to begin auth: %v", err), http.StatusInternalServerError)
    return
  }

  s.pendingMu.Lock()
  s.pending[state] = &pendingAuth{
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
  }
  s.pendingMu.Unlock()

  // Record first use (auto-connect) and check if blocked
  _ = s.store.LinkAppService(nonce, svc.ID)
  links, err := s.store.GetAppServiceLinks(nonce)
  if err == nil {
    for _, link := range links {
      if link.ServiceID == svc.ID && !link.Allowed {
        http.Error(w, "Service is blocked for this app", http.StatusForbidden)
        return
      }
    }
  }

  w.Header().Set("Content-Type", "application/json")
  json.NewEncoder(w).Encode(map[string]interface{}{
    "type": "redirect",
    "url":  authURL,
  })
}

// completeAuth writes the postMessage callback page for a completed auth flow.
// Used by both OIDC and SSH callback paths.
func (s *Server) completeAuth(w http.ResponseWriter, pending *pendingAuth, oauth *OAuthData) {
  w.Header().Set("Content-Type", "text/html; charset=utf-8")
  writeCallbackPage(w, pending.appState, pending.appNonce, pending.serviceID, pending.serviceURL, oauth)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
  code := r.URL.Query().Get("code")
  state := r.URL.Query().Get("state")
  if code == "" || state == "" {
    http.Error(w, "Missing code or state", http.StatusBadRequest)
    return
  }

  s.pendingMu.Lock()
  pending, ok := s.pending[state]
  if ok {
    delete(s.pending, state)
  }
  s.pendingMu.Unlock()

  if !ok {
    http.Error(w, "Unknown or expired state", http.StatusBadRequest)
    return
  }

  // SSH auth never reaches /service/callback — it completes via /service/ssh-auth.
  if pending.serviceType == "ssh" {
    http.Error(w, "Unexpected callback for SSH auth", http.StatusBadRequest)
    return
  }

  redirectURI := s.config.PublicBaseURL + "/service/callback"

  if pending.serviceType == "oidc" {
    svc, err := s.store.GetService(pending.serviceID)
    if err != nil {
      http.Error(w, "Service not found", http.StatusInternalServerError)
      return
    }
    claims, accessToken, refreshToken, idToken, err := s.oidcExchangeCode(r.Context(), svc, code, pending.verifier, pending.oidcNonce, redirectURI)
    if err != nil {
      http.Error(w, fmt.Sprintf("OIDC exchange failed: %v", err), http.StatusInternalServerError)
      return
    }

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

    s.completeAuth(w, pending, &OAuthData{
      ClientID:      pending.clientID,
      AccessToken:   accessToken,
      RefreshToken:  refreshToken,
      TokenType:     "Bearer",
      TokenEndpoint: pending.tokenEndpoint,
      ExpiresAt:     time.Unix(claims.Expiry, 0),
      Claims:        mergedClaims,
      IDToken:       idToken,
      Proxied:       pending.proxied,
      Scopes:        pending.scopes,
    })
    return
  }

  // Generic OAuth (MCP, API) — exchange code for token
  oauth, err := s.serviceExchangeCode(r.Context(), pending.tokenEndpoint, code, pending.verifier, pending.clientID, pending.clientSecret, redirectURI)
  if err != nil {
    http.Error(w, fmt.Sprintf("Token exchange failed: %v", err), http.StatusInternalServerError)
    return
  }

  oauth.ClientID      = pending.clientID
  oauth.TokenEndpoint = pending.tokenEndpoint
  oauth.Proxied       = pending.proxied
  oauth.Scopes        = pending.scopes

  s.completeAuth(w, pending, oauth)
}

func writeCallbackPage(w io.Writer, appState, appNonce string, serviceID int64, serviceURL string, oauth *OAuthData) {
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
  if nonce != s.adminNonce {
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

// Task represents a single named script parsed from a tasks file.
type Task struct {
  Name   string `json:"name"`
  Desc   string `json:"description"`
  Script string `json:"-"` // never serialized
}

// parseTaskHeader checks whether a line is a valid [task-name] header.
// Returns (name, desc, true) on success, or ("", "", false) otherwise.
// A valid header must start with '[', contain a ']' on the same line,
// and have a non-empty name between them.
func parseTaskHeader(line string) (name, desc string, ok bool) {
  if !strings.HasPrefix(line, "[") {
    return "", "", false
  }
  closeIdx := strings.Index(line, "]")
  if closeIdx < 1 { // no ']' or empty name like "[]"
    return "", "", false
  }
  name = line[1:closeIdx]
  desc = strings.TrimSpace(line[closeIdx+1:])
  return name, desc, true
}

// parseTasksFile parses the task definitions from a tasks file.
//
// Format: a [task-name] header optionally followed by a description
// on the same line, then the script body until the next header or EOF.
//
//	[greet] Say hello to someone
//	echo "Hello, $TASK_NAME"
//	[build] Compile the project
//	make all
//
// Lines starting with '[' that don't parse as a valid header are treated
// as script body, so bash expressions like ${arr[0]} are safe.
func parseTasksFile(data []byte) []Task {
  var tasks []Task
  var cur *Task
  for _, line := range strings.Split(string(data), "\n") {
    if name, desc, ok := parseTaskHeader(line); ok {
      if cur != nil {
        tasks = append(tasks, *cur)
      }
      cur = &Task{Name: name, Desc: desc}
    } else if cur != nil {
      if cur.Script != "" {
        cur.Script += "\n"
      }
      cur.Script += line
    }
  }
  if cur != nil {
    tasks = append(tasks, *cur)
  }
  return tasks
}

// loadTasksForService reads and parses the tasks file for a service.
func (s *Server) loadTasksForService(svc *Service) ([]Task, error) {
  path := filepath.Join(s.config.Dir, "tasks", svc.Name+".txt")
  data, err := os.ReadFile(path)
  if err != nil {
    return nil, fmt.Errorf("tasks file not found: %s", path)
  }
  return parseTasksFile(data), nil
}

func (s *Server) handleTaskCall(w http.ResponseWriter, r *http.Request) {
  // ── Auth & access ────────────────────────────────────────────────────
  nonce := r.Header.Get("X-App-Nonce")
  if nonce == "" {
    http.Error(w, "Missing X-App-Nonce header", http.StatusUnauthorized)
    return
  }

  slug := r.PathValue("name")

  svc, err := s.store.GetServiceByURL("tasks://" + slug)
  if err != nil {
    http.Error(w, "Task service not found", http.StatusNotFound)
    return
  }

  if nonce != s.adminNonce {
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

  // ── Route: GET = list tools, POST = call tool ────────────────────────
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

func (s *Server) handleTaskExec(w http.ResponseWriter, r *http.Request, svc *Service) {
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
  var task *Task
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
      jsonVal, err := json.Marshal(v)
      if err != nil {
        jsonVal = []byte(fmt.Sprintf("%v", v))
      }
      env = append(env, "TASK_"+strings.ToUpper(k)+"="+string(jsonVal))
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
  cmd := exec.CommandContext(r.Context(), "sh", "-c", task.Script)
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

func userFromContext(ctx context.Context) *User {
  u, _ := ctx.Value(userKey).(*User)
  return u
}

func isAdminOrSuperuser(ctx context.Context) bool {
  u := userFromContext(ctx)
  return u != nil && (u.Role == "Superuser" || u.Role == "Admin")
}

func auditActorName(ctx context.Context) string {
  if u := userFromContext(ctx); u != nil {
    if u.Name != "" {
      return u.Name
    }
    return fmt.Sprintf("user:%d", u.ID)
  }
  return "unknown"
}

func (s *Server) auditLog(ctx context.Context, action, target string) {
  _ = s.store.LogAudit(auditActorName(ctx), action, target)
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
  if !isAdminOrSuperuser(r.Context()) {
    http.Error(w, "Forbidden", http.StatusForbidden)
    return
  }
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
  if req.Name == "" {
    http.Error(w, "Name required", http.StatusBadRequest)
    return
  }

  nonce, err := s.store.CreateApp(req.Name, req.Environment, req.URL, req.OwnerID)
  if err != nil {
    http.Error(w, err.Error(), http.StatusInternalServerError)
    return
  }

  w.Header().Set("Content-Type", "application/json")
  s.auditLog(r.Context(), "created app", req.Name)
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
    if !isAdminOrSuperuser(r.Context()) {
      http.Error(w, "Forbidden", http.StatusForbidden)
      return
    }
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
    if err := s.store.UpdateApp(nonce, req.Name, req.Environment, req.URL, req.OwnerID); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.rebuildHostedRoutes()
    s.auditLog(r.Context(), "updated app", req.Name)
    w.WriteHeader(http.StatusNoContent)
  case http.MethodDelete:
    if !isAdminOrSuperuser(r.Context()) {
      http.Error(w, "Forbidden", http.StatusForbidden)
      return
    }
    if err := s.store.DeleteApp(nonce); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    os.RemoveAll(filepath.Join("apps", nonce))
    s.rebuildHostedRoutes()
    s.auditLog(r.Context(), "deleted app", app.Name)
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
    if !isAdminOrSuperuser(r.Context()) {
      http.Error(w, "Forbidden", http.StatusForbidden)
      return
    }
    var req struct {
      Members []int64 `json:"members"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
      http.Error(w, "Invalid JSON", http.StatusBadRequest)
      return
    }
    if err := s.store.SetAppMembers(nonce, req.Members); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    if app, err := s.store.GetApp(nonce); err == nil {
      s.auditLog(r.Context(), "updated app members", app.Name)
    } else {
      s.auditLog(r.Context(), "updated app members", nonce)
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
    if !isAdminOrSuperuser(r.Context()) {
      http.Error(w, "Forbidden", http.StatusForbidden)
      return
    }
    var req struct {
      Services []int64 `json:"services"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
      http.Error(w, "Invalid JSON", http.StatusBadRequest)
      return
    }
    if err := s.store.SetAppServiceLinks(nonce, req.Services); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    if app, err := s.store.GetApp(nonce); err == nil {
      s.auditLog(r.Context(), "updated app services", app.Name)
    } else {
      s.auditLog(r.Context(), "updated app services", nonce)
    }
    w.WriteHeader(http.StatusNoContent)
  default:
    http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
  }
}

func (s *Server) handleAppWeb(w http.ResponseWriter, r *http.Request, nonce string) {
  if !isAdminOrSuperuser(r.Context()) {
    http.Error(w, "Forbidden", http.StatusForbidden)
    return
  }
  app, err := s.store.GetApp(nonce)
  if err != nil {
    http.Error(w, err.Error(), http.StatusNotFound)
    return
  }
  webDir := filepath.Join("apps", nonce, "web")

  switch r.Method {
  case http.MethodGet:
    if _, err := os.Stat(webDir); os.IsNotExist(err) {
      http.Error(w, "no web files uploaded", http.StatusNotFound)
      return
    }
    slug := appSlug(app)
    w.Header().Set("Content-Type", "application/zip")
    w.Header().Set("Content-Disposition", `attachment; filename="`+slug+`.zip"`)
    zw := zip.NewWriter(w)
    err := filepath.Walk(webDir, func(path string, fi os.FileInfo, err error) error {
      if err != nil || fi.IsDir() {
        return err
      }
      rel, _ := filepath.Rel(webDir, path)
      fw, err := zw.Create(filepath.ToSlash(rel))
      if err != nil {
        return err
      }
      f, err := os.Open(path)
      if err != nil {
        return err
      }
      defer f.Close()
      _, err = io.Copy(fw, f)
      return err
    })
    if err != nil {
      // Headers already sent; nothing useful we can do except close cleanly.
      zw.Close()
      return
    }
    zw.Close()

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

    if err := os.RemoveAll(webDir); err != nil {
      http.Error(w, "failed to clear web dir", http.StatusInternalServerError)
      return
    }
    if err := os.MkdirAll(webDir, 0755); err != nil {
      http.Error(w, "failed to create web dir", http.StatusInternalServerError)
      return
    }

    name := strings.ToLower(header.Filename)
    if strings.HasSuffix(name, ".html") {
      data, err := io.ReadAll(file)
      if err != nil {
        http.Error(w, "read failed", http.StatusInternalServerError)
        return
      }
      if err := os.WriteFile(filepath.Join(webDir, "index.html"), data, 0644); err != nil {
        http.Error(w, "write failed", http.StatusInternalServerError)
        return
      }
    } else if strings.HasSuffix(name, ".zip") {
      if err := extractZip(file, webDir); err != nil {
        http.Error(w, "zip error: "+err.Error(), http.StatusBadRequest)
        return
      }
    } else {
      http.Error(w, "unsupported file type (.html or .zip only)", http.StatusBadRequest)
      return
    }

    now := time.Now().UTC()
    details := app.Details
    if details == nil {
      details = &AppDetails{}
    }
    details.LastUploaded = &now
    if err := s.store.UpdateAppDetails(nonce, details); err != nil {
      http.Error(w, "failed to save details", http.StatusInternalServerError)
      return
    }
    s.rebuildHostedRoutes()
    s.auditLog(r.Context(), "uploaded web files", app.Name)

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"route": "/" + appSlug(app)})

  case http.MethodDelete:
    if err := os.RemoveAll(webDir); err != nil {
      http.Error(w, "failed to remove web dir", http.StatusInternalServerError)
      return
    }
    if err := s.store.UpdateAppDetails(nonce, &AppDetails{}); err != nil {
      http.Error(w, "failed to save details", http.StatusInternalServerError)
      return
    }
    s.rebuildHostedRoutes()
    s.auditLog(r.Context(), "removed web files", app.Name)
    w.WriteHeader(http.StatusNoContent)

  default:
    http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
  }
}

// extractZip extracts a zip archive to destDir, auto-detecting the content root
// (unwrapping a single top-level folder if present) and ensuring an index.html
// exists (renaming the first .html alphabetically if index.html is absent).
func extractZip(r io.Reader, destDir string) error {
  data, err := io.ReadAll(r)
  if err != nil {
    return fmt.Errorf("read: %w", err)
  }
  zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
  if err != nil {
    return fmt.Errorf("open zip: %w", err)
  }

  // Determine effective root: if all file entries share a single top-level
  // directory and there are no files directly at the root, strip that prefix.
  topDirs := map[string]bool{}
  hasRootFiles := false
  for _, f := range zr.File {
    name := filepath.ToSlash(f.Name)
    if name == "" || strings.HasSuffix(name, "/") {
      continue
    }
    if idx := strings.Index(name, "/"); idx >= 0 {
      topDirs[name[:idx]] = true
    } else {
      hasRootFiles = true
    }
  }
  root := ""
  if !hasRootFiles && len(topDirs) == 1 {
    for dir := range topDirs {
      root = dir + "/"
    }
  }

  // Find the entry point HTML: prefer index.html, else first .html alphabetically.
  var htmlFiles []string
  hasIndex := false
  for _, f := range zr.File {
    name := filepath.ToSlash(f.Name)
    if !strings.HasPrefix(name, root) || strings.HasSuffix(name, "/") {
      continue
    }
    rel := strings.TrimPrefix(name, root)
    if rel == "index.html" {
      hasIndex = true
      break
    }
    if strings.HasSuffix(rel, ".html") {
      htmlFiles = append(htmlFiles, rel)
    }
  }
  var entryPoint string
  if !hasIndex {
    if len(htmlFiles) == 0 {
      return fmt.Errorf("no HTML file found in zip")
    }
    sort.Strings(htmlFiles)
    entryPoint = htmlFiles[0]
  }

  // Extract files.
  for _, f := range zr.File {
    if f.FileInfo().IsDir() {
      continue
    }
    name := filepath.ToSlash(f.Name)
    if !strings.HasPrefix(name, root) {
      continue
    }
    rel := strings.TrimPrefix(name, root)
    if rel == "" {
      continue
    }
    if entryPoint != "" && rel == entryPoint {
      rel = "index.html"
    }
    clean := filepath.Clean(rel)
    if strings.HasPrefix(clean, "..") {
      continue
    }
    destPath := filepath.Join(destDir, clean)
    if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
      return fmt.Errorf("mkdir: %w", err)
    }
    rc, err := f.Open()
    if err != nil {
      return fmt.Errorf("open entry: %w", err)
    }
    out, err := os.Create(destPath)
    if err != nil {
      rc.Close()
      return fmt.Errorf("create: %w", err)
    }
    _, copyErr := io.Copy(out, rc)
    out.Close()
    rc.Close()
    if copyErr != nil {
      return fmt.Errorf("extract: %w", copyErr)
    }
  }
  return nil
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
    Name       string            `json:"name"`
    URL        string            `json:"url"`
    Descriptor ServiceDescriptor `json:"descriptor"`
  }
  if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
    http.Error(w, "Invalid JSON", http.StatusBadRequest)
    return
  }
  if req.Name == "" {
    http.Error(w, "name required", http.StatusBadRequest)
    return
  }
  if req.URL == "" && req.Descriptor.Type != "tasks" {
    http.Error(w, "url required", http.StatusBadRequest)
    return
  }
  // Tasks services don't have a remote URL — use a slugified name.
  if req.URL == "" {
    req.URL = "tasks://" + slugify(req.Name)
  }

  id, err := s.store.RegisterService(req.Name, req.URL, req.Descriptor)
  if err != nil {
    http.Error(w, err.Error(), http.StatusInternalServerError)
    return
  }

  w.Header().Set("Content-Type", "application/json")
  s.auditLog(r.Context(), "created service", req.Name)
  json.NewEncoder(w).Encode(map[string]interface{}{
    "id":         id,
    "name":       req.Name,
    "url":        req.URL,
    "descriptor": req.Descriptor,
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

  switch r.Method {
  case http.MethodGet:
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(svc)
  case http.MethodPut:
    var req ServiceUpdate
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
      http.Error(w, "Invalid JSON", http.StatusBadRequest)
      return
    }
    if req.Name == "" {
      http.Error(w, "name required", http.StatusBadRequest)
      return
    }
    if req.URL == "" && req.Descriptor.Type != "tasks" {
      http.Error(w, "url required", http.StatusBadRequest)
      return
    }
    if req.URL == "" {
      req.URL = "tasks://" + slugify(req.Name)
    }
    // Built-in SSH service: preserve name and URL
    if svc.Descriptor.Type == "ssh" {
      req.Name = svc.Name
      req.URL = svc.URL
    }
    if err := s.store.UpdateService(serviceID, req.Name, req.URL, req.Descriptor); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.auditLog(r.Context(), "updated service", req.Name)
    w.WriteHeader(http.StatusNoContent)
  case http.MethodDelete:
    if svc.Descriptor.Type == "ssh" {
      http.Error(w, "Cannot delete built-in SSH service", http.StatusForbidden)
      return
    }
    if err := s.store.DeleteService(serviceID); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.auditLog(r.Context(), "deleted service", svc.Name)
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
    if req.Name == "" || req.Email == "" {
      http.Error(w, "name and email required", http.StatusBadRequest)
      return
    }
    user, err := s.store.CreateUser(req.Name, req.Email, req.Role, req.Status)
    if err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.auditLog(r.Context(), "created user", req.Name)
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
    actor, _ := r.Context().Value(userKey).(*User)
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
    if err := s.store.UpdateUser(id, req.Name, req.Email, req.Role, req.Status, user.Metadata); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.auditLog(r.Context(), "updated user", req.Name)
    w.WriteHeader(http.StatusNoContent)
  case http.MethodDelete:
    if err := s.store.DeleteUser(id); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.auditLog(r.Context(), "deleted user", user.Name)
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
    if err := s.store.SetUserApps(userID, req.Apps); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.auditLog(r.Context(), "updated user apps", fmt.Sprintf("user:%d", userID))
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
    info := (*SSHKeyInfo)(nil)
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
    if len(req.Passphrase) < 8 {
      http.Error(w, "Passphrase must be at least 8 characters", http.StatusBadRequest)
      return
    }
    if user.Metadata != nil && user.Metadata.SSHKey != nil {
      http.Error(w, "SSH key already exists — delete it first", http.StatusConflict)
      return
    }

    keyInfo, err := GenerateSSHKey(req.Passphrase)
    if err != nil {
      http.Error(w, fmt.Sprintf("Key generation failed: %v", err), http.StatusInternalServerError)
      return
    }

    meta := user.Metadata
    if meta == nil {
      meta = &UserMetadata{}
    }
    meta.SSHKey = keyInfo
    if err := s.store.UpdateUser(user.ID, user.Name, user.Email, user.Role, user.Status, meta); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.auditLog(r.Context(), "generated SSH key for user", user.Email)

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{"ssh_key": &SSHKeyInfo{
      PublicKey:   keyInfo.PublicKey,
      Fingerprint: keyInfo.Fingerprint,
      KeyType:     keyInfo.KeyType,
    }})

  case http.MethodDelete:
    if user.Metadata == nil || user.Metadata.SSHKey == nil {
      http.Error(w, "No SSH key to delete", http.StatusNotFound)
      return
    }
    meta := &UserMetadata{}
    if err := s.store.UpdateUser(user.ID, user.Name, user.Email, user.Role, user.Status, meta); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.auditLog(r.Context(), "deleted SSH key for user", user.Email)
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
    svcIDStr, err := s.store.GetSetting("admin_auth_service")
    if err != nil || svcIDStr == "" {
      // Auth off — synthetic superuser so role checks still work downstream.
      ctx := context.WithValue(r.Context(), userKey, &User{ID: -1, Name: "Setup Account", Role: "Superuser", Status: "Active"})
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
  for _, r := range roles { allowed[r] = true }
  return func(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
      u, ok := r.Context().Value(userKey).(*User)
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
//   1. X-App-Nonce header is present and corresponds to a real app
//   2. The authenticated user is a member of that app (or admin/superuser)
//   3. The app has a service of the given type with `allowed = true`
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

      // Admin nonce — skip app membership/service checks (admin panel).
      if nonce == s.adminNonce {
        next(w, r.WithContext(context.WithValue(r.Context(), appNonceKey, nonce)))
        return
      }

      app, err := s.store.GetApp(nonce)
      if err != nil {
        http.Error(w, "Unknown app", http.StatusUnauthorized)
        return
      }

      user, _ := r.Context().Value(userKey).(*User)
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

func (s *Server) verifyAdminToken(r *http.Request, serviceID string) (*User, error) {
  authHeader := r.Header.Get("Authorization")
  if !strings.HasPrefix(authHeader, "Bearer ") {
    return nil, fmt.Errorf("missing bearer token")
  }
  idTokenRaw := strings.TrimPrefix(authHeader, "Bearer ")

  svcID, err := strconv.ParseInt(serviceID, 10, 64)
  if err != nil {
    return nil, fmt.Errorf("invalid service ID in settings")
  }
  svc, err := s.store.GetService(svcID)
  if err != nil {
    return nil, fmt.Errorf("admin auth service not found")
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

// verifyTaskToken verifies a Bearer token against a referenced auth service.
// Used by tasks services that require token auth. Returns the authenticated
// user on success.
func (s *Server) verifyTaskToken(r *http.Request, authSvcID int64) (*User, error) {
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
  var peek struct{ Iss string `json:"iss"` }
  json.Unmarshal(payload, &peek)
  return peek.Iss == "freshbreath"
}

func (s *Server) verifyIDToken(ctx context.Context, svc *Service, raw string) (string, error) {
  if isFreshbreathToken(raw) {
    return s.verifyFreshbreathToken(raw)
  }
  provider, err := s.getOIDCProvider(ctx, svc.ID, svc.URL)
  if err != nil {
    return "", fmt.Errorf("OIDC provider: %w", err)
  }
  idToken, err := provider.Verifier(&oidc.Config{ClientID: svc.Descriptor.ClientID}).Verify(ctx, raw)
  if err != nil {
    return "", fmt.Errorf("token verification: %w", err)
  }
  var claims struct{ Email string `json:"email"` }
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
  user, _ := r.Context().Value(userKey).(*User)
  w.Header().Set("Content-Type", "application/json")
  json.NewEncoder(w).Encode(map[string]interface{}{"user": user})
}

func (s *Server) handleSSHKey(w http.ResponseWriter, r *http.Request) {
  user, _ := r.Context().Value(userKey).(*User)
  if user == nil || user.ID < 0 {
    http.Error(w, "Unauthorized", http.StatusUnauthorized)
    return
  }

  s.handleUserSSHKey(w, r, user.ID)
}

// handleSSHSessions handles POST /ssh/sessions — open a new SSH session.
func (s *Server) handleSSHSessions(w http.ResponseWriter, r *http.Request) {
  if r.Method != http.MethodPost {
    http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
    return
  }

  user, _ := r.Context().Value(userKey).(*User)
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
    if errors.Is(err, ErrNoKey) {
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
      "sessionId":    session.ID,
      "host":         session.Host,
      "port":         session.Port,
      "username":     session.Username,
      "connectedAt":  session.ConnectedAt,
      "expiresAt":    session.ExpiresAt,
    })

  case http.MethodDelete:
    user, _ := r.Context().Value(userKey).(*User)
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

  rows, err := s.store.db.Query("SELECT host, port, fingerprint, trusted_at FROM ssh_host_keys ORDER BY host, port")
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

  user, _ := r.Context().Value(userKey).(*User)
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
    if body.AdminAuthService != nil {
      if *body.AdminAuthService != "" {
        if _, err := strconv.ParseInt(*body.AdminAuthService, 10, 64); err != nil {
          http.Error(w, "admin_auth_service must be a numeric service ID", http.StatusBadRequest)
          return
        }
      }
      if err := s.store.SetSetting("admin_auth_service", *body.AdminAuthService); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
      }
      s.auditLog(r.Context(), "updated settings", "admin_auth_service")
    }
    if body.DefaultApp != nil {
      if *body.DefaultApp != "" && *body.DefaultApp != "control" {
        found := false
        s.hostedMu.RLock()
        for _, n := range s.hostedRoutes {
          if n == *body.DefaultApp {
            found = true
            break
          }
        }
        s.hostedMu.RUnlock()
        if !found {
          http.Error(w, "default_app must be a hosted app nonce or \"control\"", http.StatusBadRequest)
          return
        }
      }
      if err := s.store.SetSetting("default_app", *body.DefaultApp); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
      }
      s.auditLog(r.Context(), "updated settings", "default_app")
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
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    html := strings.Replace(sshAuthFormHTML, "{{STATE}}", state, 1)
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

    // Look up the pending auth entry
    s.pendingMu.Lock()
    pending, ok := s.pending[req.State]
    if ok && pending.serviceType == "ssh" {
      delete(s.pending, req.State)
    } else {
      ok = false
    }
    s.pendingMu.Unlock()

    if !ok {
      http.Error(w, "Unknown or expired auth state", http.StatusBadRequest)
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
    if !VerifyPassphrase(user.Metadata.SSHKey, req.Passphrase) {
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

    // Fabricate a JWT for this user
    idToken, err := s.fabricateIDToken(user.Email, user.Name, fmt.Sprintf("user:%d", user.ID))
    if err != nil {
      http.Error(w, "Token generation failed", http.StatusInternalServerError)
      return
    }

    now := time.Now()
    s.completeAuth(w, pending, &OAuthData{
      AccessToken: idToken,
      TokenType:   "Bearer",
      ExpiresAt:   now.Add(24 * time.Hour),
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
      r.text().then(function(html){document.open();document.write(html);document.close()});
    }).catch(function(){errEl.textContent='Network error';errEl.className='err show';btn.disabled=false;btn.textContent='Sign in'});
  };
})();
</script></body></html>`
