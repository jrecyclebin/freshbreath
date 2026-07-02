package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
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
// Core functions take an already-authenticated *User actor and self-enforce
// authorization as their first act: each begins with s.gate(...) (or
// gateSelfOrAdmin for the dual self/admin SSH ops). This is the single source
// of truth for "who may do what" — the HTTP and MCP transports no longer
// carry their own role checks for these operations; they call core and
// surface the 403 *coreErr it returns. MCP still filters tool *visibility* by
// role at registration time, but reads that decision from the same tier vars
// below.
//
// Deliberate trade: a wrong-role request now travels past transport auth into
// core before the 403 fires. Since gate() runs before any store call or input
// validation, nothing mutates and nothing leaks. MCP visibility still means a
// Member never even sees the admin tools.
//
// Read operations (list/get) have no core wrapper and stay in the transports;
// their member-scoping (e.g. ListAppsForUser vs ListApps) is result shaping,
// not a 401/403 gate, so it lives next to the query.

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

// ── Authorization ───────────────────────────────────────────────────

// Role tiers. The one place these sets are spelled out; core enforcement
// (gate, runtime actor) and MCP tool visibility (roleIn, build-time role
// string) both derive from them.
var (
	rolesAll       = []string{"Superuser", "Admin", "Member", "Read-only"}
	rolesAdminPlus = []string{"Superuser", "Admin"}
	rolesSuperuser = []string{"Superuser"}
)

// roleIn reports whether role is in the allowed set. Used by MCP visibility
// gating, which has the role string in hand at registration time.
func roleIn(role string, allowed []string) bool {
	return slices.Contains(allowed, role)
}

// gate returns a 403 *coreErr unless actor holds one of the allowed roles.
// Core functions call it as their first statement — before input validation —
// so unauthorized callers can't probe validation behavior.
func (s *Server) gate(actor *User, allowed []string) error {
	if actor != nil && roleIn(actor.Role, allowed) {
		return nil
	}
	return cerr(http.StatusForbidden, "forbidden: requires %s", strings.Join(allowed, " or "))
}

// gateApp returns a 403 *coreErr unless the actor is Admin+ or a member of
// the app identified by nonce. It is the single source of truth for app-level
// access in both HTTP and MCP paths.
func (s *Server) gateApp(actor *User, nonce string) error {
	if actor != nil && roleIn(actor.Role, rolesAdminPlus) {
		return nil
	}
	if actor == nil || nonce == "" {
		return cerr(http.StatusForbidden, "forbidden: not a member of this app")
	}
	ok, err := s.store.IsAppMember(nonce, actor.ID)
	if err != nil {
		return cerr(http.StatusInternalServerError, "membership check failed: %v", err)
	}
	if !ok {
		return cerr(http.StatusForbidden, "forbidden: not a member of this app")
	}
	return nil
}

// gateSelfOrAdmin allows the action when actor operates on their own account,
// or when actor is Admin+. Backs the SSH-key ops that serve both the
// self-service (generate_my_ssh_key) and admin (generate_user_ssh_key)
// surfaces from a single core function.
func (s *Server) gateSelfOrAdmin(actor, target *User) error {
	if actor != nil && target != nil && actor.ID == target.ID {
		return nil
	}
	return s.gate(actor, rolesAdminPlus)
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
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return "", err
	}
	if name == "" {
		return "", cerr(http.StatusBadRequest, "name required")
	}
	nonce, err := s.store.CreateApp(name, env, url, ownerID)
	if err != nil {
		return "", cerr(http.StatusInternalServerError, "%v", err)
	}
	s.rebuildHostedRoutes()
	s.audit(actor, "created app", name)
	return nonce, nil
}

func (s *Server) coreUpdateApp(actor *User, nonce, name, env, url string, ownerID *int64) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if err := s.store.UpdateApp(nonce, name, env, url, ownerID); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.rebuildHostedRoutes()
	s.audit(actor, "updated app", name)
	return nil
}

func (s *Server) coreDeleteApp(actor *User, nonce string) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	app, err := s.store.GetApp(nonce)
	if err != nil {
		return cerr(http.StatusNotFound, "app not found: %v", err)
	}
	if err := s.store.DeleteApp(nonce); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	os.RemoveAll(filepath.Join(s.config.DataDir, "apps", nonce))
	s.rebuildHostedRoutes()
	s.audit(actor, "deleted app", app.Name)
	return nil
}

func (s *Server) coreSetAppMembers(actor *User, nonce string, members []int64) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if err := s.store.SetAppMembers(nonce, members); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.auditApp(actor, "updated app members", nonce)
	return nil
}

func (s *Server) coreSetAppServices(actor *User, nonce string, services []int64) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if err := s.store.SetAppServiceLinks(nonce, services); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.auditApp(actor, "updated app services", nonce)
	return nil
}

// ── Service operations ──────────────────────────────────────────────

func (s *Server) coreCreateService(actor *User, name, url string, d ServiceDescriptor) (*Service, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
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
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
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
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
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
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
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
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if err := s.store.UpdateUser(id, name, email, role, status, meta); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "updated user", name)
	return nil
}

func (s *Server) coreDeleteUser(actor, target *User) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if err := s.store.DeleteUser(target.ID); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "deleted user", target.Name)
	return nil
}

func (s *Server) coreSetUserApps(actor *User, id int64, apps []string) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if err := s.store.SetUserApps(id, apps); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "updated user apps", fmt.Sprintf("user:%d", id))
	return nil
}

// coreGenerateSSHKey generates and stores an SSH key for target, returning
// the public key info. actor is credited in the audit log.
func (s *Server) coreGenerateSSHKey(actor, target *User, passphrase string) (*SSHKeyInfo, error) {
	if err := s.gateSelfOrAdmin(actor, target); err != nil {
		return nil, err
	}
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
	if err := s.gateSelfOrAdmin(actor, target); err != nil {
		return err
	}
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
	if err := s.gate(actor, rolesSuperuser); err != nil {
		return err
	}
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

// ── App Web operations ─────────────────────────────────────────────

// appFile is one entry in an app's hosted web directory.
type appFile struct {
	Path string `json:"path"` // slash-separated path relative to the web dir
	Size int64  `json:"size"` // bytes
}

// coreListAppWeb lists the files in an app's web directory, sorted by path.
// An app with no uploaded files lists empty (not an error).
func (s *Server) coreListAppWeb(actor *User, nonce string) ([]appFile, error) {
	if err := s.gateApp(actor, nonce); err != nil {
		return nil, err
	}
	if _, err := s.store.GetApp(nonce); err != nil {
		return nil, cerr(http.StatusNotFound, "app not found: %v", err)
	}
	webDir := filepath.Join(s.config.DataDir, "apps", nonce, "web")
	if _, err := os.Stat(webDir); os.IsNotExist(err) {
		return []appFile{}, nil
	}
	files := []appFile{}
	err := filepath.Walk(webDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(webDir, path)
		files = append(files, appFile{Path: filepath.ToSlash(rel), Size: fi.Size()})
		return nil
	})
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "list failed: %v", err)
	}
	slices.SortFunc(files, func(a, b appFile) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// coreDownloadAppWeb returns the app's web directory as a zip archive.
func (s *Server) coreDownloadAppWeb(actor *User, nonce string) ([]byte, string, error) {
	if err := s.gateApp(actor, nonce); err != nil {
		return nil, "", err
	}
	app, err := s.store.GetApp(nonce)
	if err != nil {
		return nil, "", cerr(http.StatusNotFound, "app not found: %v", err)
	}
	webDir := filepath.Join(s.config.DataDir, "apps", nonce, "web")
	if _, err := os.Stat(webDir); os.IsNotExist(err) {
		return nil, "", cerr(http.StatusNotFound, "no web files uploaded")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err = filepath.Walk(webDir, func(path string, fi os.FileInfo, err error) error {
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
		zw.Close()
		return nil, "", cerr(http.StatusInternalServerError, "zip failed: %v", err)
	}
	if err := zw.Close(); err != nil {
		return nil, "", cerr(http.StatusInternalServerError, "zip close failed: %v", err)
	}
	return buf.Bytes(), appSlug(app), nil
}

// coreUploadAppWeb writes web files for an app from raw content. The
// filename determines handling: .html is saved as index.html; .zip is
// extracted via extractZip.
func (s *Server) coreUploadAppWeb(actor *User, nonce string, data []byte, filename string) (string, error) {
	if err := s.gateApp(actor, nonce); err != nil {
		return "", err
	}
	app, err := s.store.GetApp(nonce)
	if err != nil {
		return "", cerr(http.StatusNotFound, "app not found: %v", err)
	}
	webDir := filepath.Join(s.config.DataDir, "apps", nonce, "web")
	if err := os.RemoveAll(webDir); err != nil {
		return "", cerr(http.StatusInternalServerError, "failed to clear web dir")
	}
	if err := os.MkdirAll(webDir, 0755); err != nil {
		return "", cerr(http.StatusInternalServerError, "failed to create web dir")
	}

	name := strings.ToLower(filename)
	if strings.HasSuffix(name, ".html") {
		if err := os.WriteFile(filepath.Join(webDir, "index.html"), data, 0644); err != nil {
			return "", cerr(http.StatusInternalServerError, "write failed")
		}
	} else if strings.HasSuffix(name, ".zip") {
		if err := extractZip(bytes.NewReader(data), int64(len(data)), webDir); err != nil {
			return "", cerr(http.StatusBadRequest, "zip error: %v", err)
		}
	} else {
		return "", cerr(http.StatusBadRequest, "unsupported file type (.html or .zip only)")
	}

	now := time.Now().UTC()
	details := app.Details
	if details == nil {
		details = &AppDetails{}
	}
	details.LastUploaded = &now
	if err := s.store.UpdateAppDetails(nonce, details); err != nil {
		return "", cerr(http.StatusInternalServerError, "failed to save details")
	}
	s.rebuildHostedRoutes()
	s.audit(actor, "uploaded web files", app.Name)
	return "/" + appSlug(app), nil
}

// coreDeleteAppWeb removes an app's web directory and clears its details.
func (s *Server) coreDeleteAppWeb(actor *User, nonce string) error {
	if err := s.gateApp(actor, nonce); err != nil {
		return err
	}
	app, err := s.store.GetApp(nonce)
	if err != nil {
		return cerr(http.StatusNotFound, "app not found: %v", err)
	}
	webDir := filepath.Join(s.config.DataDir, "apps", nonce, "web")
	if err := os.RemoveAll(webDir); err != nil {
		return cerr(http.StatusInternalServerError, "failed to remove web dir")
	}
	if err := s.store.UpdateAppDetails(nonce, &AppDetails{}); err != nil {
		return cerr(http.StatusInternalServerError, "failed to save details")
	}
	s.rebuildHostedRoutes()
	s.audit(actor, "removed web files", app.Name)
	return nil
}

// extractZip extracts a zip archive to destDir, auto-detecting the content
// root (unwrapping a single top-level folder if present) and ensuring an
// index.html exists (renaming the first .html alphabetically if absent).
// Accepts a size parameter so it can be called from both multipart (whole
// stream) and core (known-length buffer) paths.
func extractZip(r io.ReaderAt, size int64, destDir string) error {
	zr, err := zip.NewReader(r, size)
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
