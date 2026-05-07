package main

import (
  "context"
  "encoding/base64"
  "encoding/json"
  "fmt"
  "io"
  "net/http"
  "net/url"
  "os"
  "strconv"
  "strings"
  "time"

  "github.com/coreos/go-oidc/v3/oidc"
)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
  if origin := r.Header.Get("Origin"); origin != "" {
    if nonce := r.Header.Get("X-App-Nonce"); nonce != "" {
      if app, err := s.store.GetApp(nonce); err == nil && app.URL != "" {
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
  s.mux.Handle("/control/",             http.StripPrefix("/control/", http.FileServer(http.Dir("web/control"))))
  s.mux.HandleFunc("/env.js",          s.handleEnv)
  s.mux.HandleFunc("/setup.js",        s.handleSetupStatic)
  s.mux.HandleFunc("/service/login",   s.handleLogin)
  s.mux.HandleFunc("/service/callback", s.handleCallback)
  s.mux.HandleFunc("/service/{id}/",   s.handleServiceProxy)

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
  s.mux.HandleFunc("/api/settings",         s.authWrap(pipeline(s.handleSettings, superuser)))
  s.mux.HandleFunc("/api/refresh",          s.handleRefresh)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
  if r.URL.Path != "/" {
    http.NotFound(w, r)
    return
  }
  http.Redirect(w, r, "/control", http.StatusFound)
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
  data, err := os.ReadFile("web/control.html")
  if err != nil {
    http.Error(w, "control.html not found", http.StatusInternalServerError)
    return
  }
  w.Header().Set("Content-Type", "text/html; charset=utf-8")
  w.Write(data)
}

func (s *Server) handleEnv(w http.ResponseWriter, r *http.Request) {
  authRequired := false
  authServiceName := ""
  if svcIDStr, _ := s.store.GetSetting("admin_auth_service"); svcIDStr != "" {
    if svcID, err := strconv.ParseInt(svcIDStr, 10, 64); err == nil {
      if svc, err := s.store.GetService(svcID); err == nil {
        authRequired = true
        authServiceName = svc.Name
      }
    }
  }
  w.Header().Set("Content-Type", "application/javascript")
  fmt.Fprintf(w, "window.__HOMESLICE_CONFIG = { apiBase: %q, authRequired: %v, authServiceName: %q };\n",
    s.config.PublicBaseURL, authRequired, authServiceName)
}

func (s *Server) handleSetupStatic(w http.ResponseWriter, r *http.Request) {
  data, err := os.ReadFile("web/setup.js")
  if err != nil {
    http.Error(w, "setup.js not found", http.StatusInternalServerError)
    return
  }
  w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
  w.Write(data)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
  if r.Method != http.MethodGet {
    http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
    return
  }

  if r.Header.Get("X-Admin-Auth") == "1" {
    s.handleAdminLogin(w, r)
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

  app, err := s.store.GetApp(nonce)
  if err != nil {
    http.Error(w, "Unknown app nonce", http.StatusUnauthorized)
    return
  }
  _ = app

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
      isOIDC:        true,
      oidcNonce:     oidcNonce,
      oidcIssuer:    svc.URL,
    }
    s.pendingMu.Unlock()

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
      "type":       "redirect",
      "url":        authURL,
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
    "type":       "redirect",
    "url":        authURL,
  })
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

  redirectURI := s.config.PublicBaseURL + "/service/callback"

  if pending.isAdmin {
    s.handleAdminCallback(w, r, pending, code, redirectURI)
    return
  }

  if pending.isOIDC {
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

    oauth := &OAuthData{
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
    }

    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    writeCallbackPage(w, pending.appState, pending.appNonce, pending.serviceID, pending.serviceURL, oauth)
    return
  }

  oauth, err := s.serviceExchangeCode(r.Context(), pending.tokenEndpoint, code, pending.verifier, pending.clientID, pending.clientSecret, redirectURI)
  if err != nil {
    http.Error(w, fmt.Sprintf("Token exchange failed: %v", err), http.StatusInternalServerError)
    return
  }

  oauth.ClientID      = pending.clientID
  oauth.TokenEndpoint = pending.tokenEndpoint
  oauth.Proxied       = pending.proxied
  oauth.Scopes        = pending.scopes

  // Compute expires_in from ExpiresAt for the browser
  if !oauth.ExpiresAt.IsZero() {
    oauth.ExpiresIn = int(time.Until(oauth.ExpiresAt).Seconds())
    if oauth.ExpiresIn < 0 {
      oauth.ExpiresIn = 0
    }
  }

  w.Header().Set("Content-Type", "text/html; charset=utf-8")
  writeCallbackPage(w, pending.appState, pending.appNonce, pending.serviceID, pending.serviceURL, oauth)
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

  _, err := s.store.GetApp(nonce)
  if err != nil {
    http.Error(w, "Unknown app", http.StatusUnauthorized)
    return
  }

  idStr := r.PathValue("id")
  serviceID, err := strconv.ParseInt(idStr, 10, 64)
  if err != nil {
    http.Error(w, "Invalid service id", http.StatusBadRequest)
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
  if req.Name == "" || req.URL == "" {
    http.Error(w, "name and url required", http.StatusBadRequest)
    return
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
    if req.Name == "" || req.URL == "" {
      http.Error(w, "name and url required", http.StatusBadRequest)
      return
    }
    if err := s.store.UpdateService(serviceID, req.Name, req.URL, req.Descriptor); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.auditLog(r.Context(), "updated service", req.Name)
    w.WriteHeader(http.StatusNoContent)
  case http.MethodDelete:
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
    if err := s.store.UpdateUser(id, req.Name, req.Email, req.Role, req.Status); err != nil {
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

// roles

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
      ctx := context.WithValue(r.Context(), userKey, &User{ID: -1, Name: "setup", Role: "Superuser", Status: "Active"})
      h(w, r.WithContext(ctx))
      return
    }
    user, err := s.verifyAdminToken(r, svcIDStr)
    if err != nil {
      http.Error(w, "Unauthorized", http.StatusUnauthorized)
      return
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

// Pipeline chains middlewares right-to-left (outer to inner).
func pipeline(h http.HandlerFunc, mw ...func(http.HandlerFunc) http.HandlerFunc) http.HandlerFunc {
  for i := len(mw) - 1; i >= 0; i-- {
    h = mw[i](h)
  }
  return h
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

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
  svcIDStr, err := s.store.GetSetting("admin_auth_service")
  if err != nil || svcIDStr == "" {
    http.Error(w, "Admin auth not configured", http.StatusForbidden)
    return
  }
  svcID, err := strconv.ParseInt(svcIDStr, 10, 64)
  if err != nil {
    http.Error(w, "Invalid admin auth service ID", http.StatusInternalServerError)
    return
  }
  svc, err := s.store.GetService(svcID)
  if err != nil {
    http.Error(w, "Admin auth service not found", http.StatusInternalServerError)
    return
  }

  redirectURI := s.config.PublicBaseURL + "/service/callback"
  authURL, state, verifier, oidcNonce, tokenURL, err := s.oidcBeginAuth(r.Context(), svc, redirectURI)
  if err != nil {
    http.Error(w, fmt.Sprintf("Failed to begin admin auth: %v", err), http.StatusInternalServerError)
    return
  }

  s.pendingMu.Lock()
  s.pending[state] = &pendingAuth{
    serviceID:     svc.ID,
    verifier:      verifier,
    clientID:      svc.Descriptor.ClientID,
    clientSecret:  svc.Descriptor.ClientSecret,
    tokenEndpoint: tokenURL,
    scopes:        svc.Descriptor.Scopes,
    isOIDC:        true,
    oidcNonce:     oidcNonce,
    oidcIssuer:    svc.URL,
    isAdmin:       true,
  }
  s.pendingMu.Unlock()

  w.Header().Set("Content-Type", "application/json")
  json.NewEncoder(w).Encode(map[string]interface{}{"type": "redirect", "url": authURL})
}

func (s *Server) handleAdminCallback(w http.ResponseWriter, r *http.Request, pending *pendingAuth, code, redirectURI string) {
  svc, err := s.store.GetService(pending.serviceID)
  if err != nil {
    http.Error(w, "Service not found", http.StatusInternalServerError)
    return
  }

  _, _, _, idTokenRaw, err := s.oidcExchangeCode(r.Context(), svc, code, pending.verifier, pending.oidcNonce, redirectURI)
  if err != nil {
    http.Error(w, fmt.Sprintf("OIDC exchange failed: %v", err), http.StatusInternalServerError)
    return
  }

  email, err := s.verifyIDToken(r.Context(), svc, idTokenRaw)
  if err != nil {
    http.Redirect(w, r, "/control?auth_error=invalid_token", http.StatusFound)
    return
  }
  if _, err := s.store.GetUserByEmail(email); err != nil {
    http.Redirect(w, r, "/control?auth_error=no_user", http.StatusFound)
    return
  }
  _ = s.store.LogAudit(email, "login", "admin panel")

  tokenData, _ := json.Marshal(map[string]interface{}{
    "id_token":       idTokenRaw,
    "service_id":     pending.serviceID,
    "token_endpoint": pending.tokenEndpoint,
    "client_id":      pending.clientID,
  })

  w.Header().Set("Content-Type", "text/html; charset=utf-8")
  fmt.Fprintf(w, `<!doctype html><html><body><script>
localStorage.setItem('frebre_admin',JSON.stringify(%s));
window.location.href='/control';
</script></body></html>`, tokenData)
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

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
  switch r.Method {
  case http.MethodGet:
    svcIDStr, err := s.store.GetSetting("admin_auth_service")
    result := map[string]interface{}{"admin_auth_service": nil}
    if err == nil && svcIDStr != "" {
      result["admin_auth_service"] = svcIDStr
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(result)
  case http.MethodPut:
    var body struct {
      AdminAuthService string `json:"admin_auth_service"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
      http.Error(w, "Invalid JSON", http.StatusBadRequest)
      return
    }
    if body.AdminAuthService != "" {
      if _, err := strconv.ParseInt(body.AdminAuthService, 10, 64); err != nil {
        http.Error(w, "admin_auth_service must be a numeric service ID", http.StatusBadRequest)
        return
      }
    }
    if err := s.store.SetSetting("admin_auth_service", body.AdminAuthService); err != nil {
      http.Error(w, err.Error(), http.StatusInternalServerError)
      return
    }
    s.auditLog(r.Context(), "updated settings", "admin_auth_service")
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

  // Retrieve the token endpoint from the OIDC provider discovery
  provider, err := s.getOIDCProvider(r.Context(), svc.ID, svc.URL)
  if err != nil {
    http.Error(w, "OIDC provider error", http.StatusInternalServerError)
    return
  }
  var providerClaims struct {
    TokenEndpoint string `json:"token_endpoint"`
  }
  if err := provider.Claims(&providerClaims); err != nil || providerClaims.TokenEndpoint == "" {
    http.Error(w, "Could not determine token endpoint", http.StatusInternalServerError)
    return
  }

  form := url.Values{
    "grant_type":    {"refresh_token"},
    "refresh_token": {body.RefreshToken},
    "client_id":     {svc.Descriptor.ClientID},
    "client_secret": {svc.Descriptor.ClientSecret},
  }
  resp, err := s.httpClient.PostForm(providerClaims.TokenEndpoint, form)
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
