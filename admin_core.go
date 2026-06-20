package main

import (
  "errors"
  "fmt"
  "net/http"
  "os"
  "path/filepath"
  "strings"
)

// trimMCPSlug returns the virtual-service slug from a /mcp/<slug> URL.
func trimMCPSlug(url string) string {
  return strings.TrimPrefix(url, "/mcp/")
}

// ── Admin Core ──────────────────────────────────────────────────────
//
// Transport-agnostic business logic for the admin API. Both the HTTP
// REST handlers (handler.go) and the central MCP tools (mcp_central.go)
// expose the same operations; the logic lives here once and each
// transport is a thin adapter: parse input → call core → format output.
//
// Core functions take an already-authenticated *User actor (for audit)
// plus plain arguments, and return data or a *coreErr carrying an HTTP
// status. Role gating stays at the transport boundary, where it differs
// (HTTP middleware vs MCP per-tool roleGate), so the security surface is
// unchanged.

// coreErr is a transport-agnostic error carrying an HTTP status code.
// HTTP handlers map it via writeErr; MCP tools just stringify it.
type coreErr struct {
  status int
  msg    string
}

func (e *coreErr) Error() string { return e.msg }

// cerr builds a *coreErr with a formatted message.
func cerr(status int, format string, a ...interface{}) *coreErr {
  return &coreErr{status: status, msg: fmt.Sprintf(format, a...)}
}

// writeErr writes err to an HTTP response, honoring a *coreErr's status.
func writeErr(w http.ResponseWriter, err error) {
  var ce *coreErr
  if errors.As(err, &ce) {
    http.Error(w, ce.msg, ce.status)
    return
  }
  http.Error(w, err.Error(), http.StatusInternalServerError)
}

// ── Shared helpers ──────────────────────────────────────────────────

// actorName resolves the audit actor label for a user: name, then email,
// then user:ID, then "unknown".
func actorName(u *User) string {
  if u == nil {
    return "unknown"
  }
  if u.Name != "" {
    return u.Name
  }
  if u.Email != "" {
    return u.Email
  }
  return fmt.Sprintf("user:%d", u.ID)
}

// audit logs an audit entry attributed to the given actor.
func (s *Server) audit(actor *User, action, target string) {
  _ = s.store.LogAudit(actorName(actor), action, target)
}

// auditApp logs an app action, resolving the app's display name from its
// nonce (falling back to the nonce itself).
func (s *Server) auditApp(actor *User, action, nonce string) {
  target := nonce
  if app, err := s.store.GetApp(nonce); err == nil {
    target = app.Name
  }
  s.audit(actor, action, target)
}

// defaultServiceURL fills in the implicit URL for service types that don't
// carry a remote one (tasks:// and /mcp/ for virtual). Returns url unchanged
// when already set, or "" when the type needs an explicit URL.
func defaultServiceURL(url, name string, d ServiceDescriptor) string {
  if url != "" {
    return url
  }
  switch d.Type {
  case "tasks":
    return "tasks://" + slugify(name)
  case "virtual":
    return "/mcp/" + slugify(name)
  }
  return ""
}

// syncVirtualMCP keeps the in-memory virtual MCP registry in step with a
// service write: virtual services are (re)registered; anything else has its
// old /mcp/ slug removed.
func (s *Server) syncVirtualMCP(svc *Service, oldURL string) {
  if svc.Descriptor.Type == "virtual" {
    s.virtualMCPs.add(s, svc)
  } else if oldURL != "" {
    s.virtualMCPs.remove(trimMCPSlug(oldURL))
  }
}

// publicSSHInfo returns the non-secret view of an SSH key (public key,
// fingerprint, type) — what's safe to hand back to a caller.
func publicSSHInfo(k *SSHKeyInfo) *SSHKeyInfo {
  if k == nil {
    return nil
  }
  return &SSHKeyInfo{
    PublicKey:   k.PublicKey,
    Fingerprint: k.Fingerprint,
    KeyType:     k.KeyType,
  }
}

// ── App operations ──────────────────────────────────────────────────

func (s *Server) coreCreateApp(actor *User, name, env, url string, ownerID *int64) (string, error) {
  if name == "" {
    return "", cerr(http.StatusBadRequest, "name required")
  }
  nonce, err := s.store.CreateApp(name, env, url, ownerID)
  if err != nil {
    return "", cerr(http.StatusInternalServerError, "%v", err)
  }
  s.audit(actor, "created app", name)
  return nonce, nil
}

func (s *Server) coreUpdateApp(actor *User, nonce, name, env, url string, ownerID *int64) error {
  if err := s.store.UpdateApp(nonce, name, env, url, ownerID); err != nil {
    return cerr(http.StatusInternalServerError, "%v", err)
  }
  s.rebuildHostedRoutes()
  s.audit(actor, "updated app", name)
  return nil
}

func (s *Server) coreDeleteApp(actor *User, nonce string) error {
  app, err := s.store.GetApp(nonce)
  if err != nil {
    return cerr(http.StatusNotFound, "app not found: %v", err)
  }
  if err := s.store.DeleteApp(nonce); err != nil {
    return cerr(http.StatusInternalServerError, "%v", err)
  }
  os.RemoveAll(filepath.Join("apps", nonce))
  s.rebuildHostedRoutes()
  s.audit(actor, "deleted app", app.Name)
  return nil
}

func (s *Server) coreSetAppMembers(actor *User, nonce string, members []int64) error {
  if err := s.store.SetAppMembers(nonce, members); err != nil {
    return cerr(http.StatusInternalServerError, "%v", err)
  }
  s.auditApp(actor, "updated app members", nonce)
  return nil
}

func (s *Server) coreSetAppServices(actor *User, nonce string, services []int64) error {
  if err := s.store.SetAppServiceLinks(nonce, services); err != nil {
    return cerr(http.StatusInternalServerError, "%v", err)
  }
  s.auditApp(actor, "updated app services", nonce)
  return nil
}

// ── Service operations ──────────────────────────────────────────────

func (s *Server) coreCreateService(actor *User, name, url string, d ServiceDescriptor) (*Service, error) {
  if name == "" {
    return nil, cerr(http.StatusBadRequest, "name required")
  }
  url = defaultServiceURL(url, name, d)
  if url == "" {
    return nil, cerr(http.StatusBadRequest, "url required")
  }
  id, err := s.store.RegisterService(name, url, d)
  if err != nil {
    return nil, cerr(http.StatusInternalServerError, "%v", err)
  }
  svc := &Service{ID: id, Name: name, URL: url, Descriptor: d}
  s.syncVirtualMCP(svc, "")
  s.audit(actor, "created service", name)
  return svc, nil
}

// coreUpdateService replaces a service's fields. Callers wanting patch
// semantics (fill blanks from the existing record) should resolve those
// before calling. The built-in SSH service keeps its name and URL.
func (s *Server) coreUpdateService(actor *User, id int64, name, url string, d ServiceDescriptor) error {
  existing, err := s.store.GetService(id)
  if err != nil {
    return cerr(http.StatusNotFound, "service not found: %v", err)
  }
  if name == "" {
    return cerr(http.StatusBadRequest, "name required")
  }
  url = defaultServiceURL(url, name, d)
  if url == "" {
    return cerr(http.StatusBadRequest, "url required")
  }
  if existing.Descriptor.Type == "ssh" {
    name = existing.Name
    url = existing.URL
  }
  if err := s.store.UpdateService(id, name, url, d); err != nil {
    return cerr(http.StatusInternalServerError, "%v", err)
  }
  s.syncVirtualMCP(&Service{ID: id, Name: name, URL: url, Descriptor: d}, existing.URL)
  s.audit(actor, "updated service", name)
  return nil
}

func (s *Server) coreDeleteService(actor *User, id int64) error {
  svc, err := s.store.GetService(id)
  if err != nil {
    return cerr(http.StatusNotFound, "service not found: %v", err)
  }
  if svc.Descriptor.Type == "ssh" {
    return cerr(http.StatusForbidden, "cannot delete built-in SSH service")
  }
  if err := s.store.DeleteService(id); err != nil {
    return cerr(http.StatusInternalServerError, "%v", err)
  }
  if svc.Descriptor.Type == "virtual" {
    s.virtualMCPs.remove(trimMCPSlug(svc.URL))
  }
  s.audit(actor, "deleted service", svc.Name)
  return nil
}

// ── User operations ─────────────────────────────────────────────────

func (s *Server) coreCreateUser(actor *User, name, email, role, status string) (*User, error) {
  if name == "" || email == "" {
    return nil, cerr(http.StatusBadRequest, "name and email required")
  }
  u, err := s.store.CreateUser(name, email, role, status)
  if err != nil {
    return nil, cerr(http.StatusInternalServerError, "%v", err)
  }
  s.audit(actor, "created user", name)
  return u, nil
}

func (s *Server) coreUpdateUser(actor *User, id int64, name, email, role, status string, meta *UserMetadata) error {
  if err := s.store.UpdateUser(id, name, email, role, status, meta); err != nil {
    return cerr(http.StatusInternalServerError, "%v", err)
  }
  s.audit(actor, "updated user", name)
  return nil
}

func (s *Server) coreDeleteUser(actor, target *User) error {
  if err := s.store.DeleteUser(target.ID); err != nil {
    return cerr(http.StatusInternalServerError, "%v", err)
  }
  s.audit(actor, "deleted user", target.Name)
  return nil
}

func (s *Server) coreSetUserApps(actor *User, id int64, apps []string) error {
  if err := s.store.SetUserApps(id, apps); err != nil {
    return cerr(http.StatusInternalServerError, "%v", err)
  }
  s.audit(actor, "updated user apps", fmt.Sprintf("user:%d", id))
  return nil
}

// coreGenerateSSHKey generates and stores an SSH key for target, returning
// the public key info. actor is credited in the audit log.
func (s *Server) coreGenerateSSHKey(actor, target *User, passphrase string) (*SSHKeyInfo, error) {
  if len(passphrase) < 8 {
    return nil, cerr(http.StatusBadRequest, "passphrase must be at least 8 characters")
  }
  if target.Metadata != nil && target.Metadata.SSHKey != nil {
    return nil, cerr(http.StatusConflict, "SSH key already exists — delete it first")
  }
  keyInfo, err := GenerateSSHKey(passphrase)
  if err != nil {
    return nil, cerr(http.StatusInternalServerError, "key generation failed: %v", err)
  }
  meta := target.Metadata
  if meta == nil {
    meta = &UserMetadata{}
  }
  meta.SSHKey = keyInfo
  if err := s.store.UpdateUser(target.ID, target.Name, target.Email, target.Role, target.Status, meta); err != nil {
    return nil, cerr(http.StatusInternalServerError, "%v", err)
  }
  s.audit(actor, "generated SSH key for user", target.Email)
  return publicSSHInfo(keyInfo), nil
}

func (s *Server) coreDeleteSSHKey(actor, target *User) error {
  if target.Metadata == nil || target.Metadata.SSHKey == nil {
    return cerr(http.StatusNotFound, "no SSH key to delete")
  }
  if err := s.store.UpdateUser(target.ID, target.Name, target.Email, target.Role, target.Status, &UserMetadata{}); err != nil {
    return cerr(http.StatusInternalServerError, "%v", err)
  }
  s.audit(actor, "deleted SSH key for user", target.Email)
  return nil
}

// ── Settings operations ─────────────────────────────────────────────

// coreUpdateSettings applies the non-nil settings fields, validating each.
func (s *Server) coreUpdateSettings(actor *User, adminAuthService, defaultApp *string) error {
  if adminAuthService != nil {
    if *adminAuthService != "" {
      if _, err := parseID(*adminAuthService); err != nil {
        return cerr(http.StatusBadRequest, "admin_auth_service must be a numeric service ID")
      }
    }
    if err := s.store.SetSetting("admin_auth_service", *adminAuthService); err != nil {
      return cerr(http.StatusInternalServerError, "%v", err)
    }
    s.audit(actor, "updated settings", "admin_auth_service")
  }
  if defaultApp != nil {
    if *defaultApp != "" && *defaultApp != "control" && !s.isHostedNonce(*defaultApp) {
      return cerr(http.StatusBadRequest, "default_app must be a hosted app nonce or \"control\"")
    }
    if err := s.store.SetSetting("default_app", *defaultApp); err != nil {
      return cerr(http.StatusInternalServerError, "%v", err)
    }
    s.audit(actor, "updated settings", "default_app")
  }
  return nil
}

// isHostedNonce reports whether nonce maps to a currently hosted app route.
func (s *Server) isHostedNonce(nonce string) bool {
  s.hostedMu.RLock()
  defer s.hostedMu.RUnlock()
  for _, n := range s.hostedRoutes {
    if n == nonce {
      return true
    }
  }
  return false
}
