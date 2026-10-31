package server

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/formats"
	"poggers.institute/freshbreath/internal/sshkit"
)

// mcpSlug returns the slug a service answers MCP at (/mcp/<slug>), or ""
// for a URL with no MCP mount of its own. Virtual services are registered
// as /mcp/<slug>, task services as tasks://<slug>; the two share the /mcp/
// namespace, as they already share /service/call/<slug>.
func mcpSlug(url string) string {
	if slug, ok := strings.CutPrefix(url, "/mcp/"); ok {
		return slug
	}
	if slug, ok := strings.CutPrefix(url, "tasks://"); ok {
		return slug
	}
	return ""
}

// ── Admin Core ──────────────────────────────────────────────────────
//
// Transport-agnostic business logic for the admin API. Both the HTTP
// REST handlers (handler.go) and the central MCP tools (mcp_central.go)
// expose the same operations; the logic lives here once and each
// transport is a thin adapter: parse input → call core → format output.
//
// Core functions take an already-authenticated *db.User actor and self-enforce
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

	// assignableMemberRoles are the roles an app or service member may hold.
	// Member and Read-only accounts are staffed by the member lists; Admins
	// and Superusers already reach everything, so having them on a list
	// grants nothing and hides the real staff. A Member promoted past this
	// keeps any existing link only until the list is next rewritten — the
	// rewrite is the prune point (resolveMembers).
	assignableMemberRoles = []string{"Member", "Read-only"}
)

// roleIn reports whether role is in the allowed set. Used by MCP visibility
// gating, which has the role string in hand at registration time.
func roleIn(role string, allowed []string) bool {
	return slices.Contains(allowed, role)
}

// gate returns a 403 *coreErr unless actor holds one of the allowed roles.
// Core functions call it as their first statement — before input validation —
// so unauthorized callers can't probe validation behavior.
func (s *Server) gate(actor *db.User, allowed []string) error {
	if actor != nil && roleIn(actor.Role, allowed) {
		return nil
	}
	return cerr(http.StatusForbidden, "forbidden: requires %s", strings.Join(allowed, " or "))
}

// resolveMembers validates a member-list rewrite and returns the assignees:
// every ID must name a real user, and only Member/Read-only accounts stay.
// Admin+ users are pruned rather than refused — a promoted member's stale
// link drops out here when the list is rewritten, instead of 400-ing a save
// the list itself forced (see assignableMemberRoles).
func (s *Server) resolveMembers(members []int64) ([]int64, error) {
	kept := make([]int64, 0, len(members))
	for _, uid := range members {
		u, err := s.store.GetUser(uid)
		if err != nil {
			return nil, cerr(http.StatusBadRequest, "user %d: %v", uid, err)
		}
		if roleIn(u.Role, assignableMemberRoles) {
			kept = append(kept, uid)
		}
	}
	return kept, nil
}

// validateOwner checks an app's owner: none, an Admin+ account, or — on
// update only — the app's current owner unchanged. That last clause keeps a
// demoted owner from stranding the app's own form: they stay attached until
// someone actively picks a different owner, whom the picker (and this check)
// constrains to Admin+.
func (s *Server) validateOwner(ownerID *int64, currentOwnerID *int64) error {
	if ownerID == nil {
		return nil
	}
	if currentOwnerID != nil && *ownerID == *currentOwnerID {
		return nil
	}
	u, err := s.store.GetUser(*ownerID)
	if err != nil {
		return cerr(http.StatusBadRequest, "owner %d: %v", *ownerID, err)
	}
	if !roleIn(u.Role, rolesAdminPlus) {
		return cerr(http.StatusBadRequest, "owner must be an Admin or Superuser, not %s", u.Role)
	}
	return nil
}

// plural returns "s" for counts above 1, for audit strings.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// gateApp returns a 403 *coreErr unless the actor is Admin+ or a member of
// the app identified by nonce. It is the single source of truth for app-level
// access in both HTTP and MCP paths.
func (s *Server) gateApp(actor *db.User, nonce string) error {
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

// gateWrite refuses Read-only accounts. Membership decides which apps and
// services a user reaches; this decides whether they may change them.
func (s *Server) gateWrite(actor *db.User) error {
	if actor != nil && actor.Role == "Read-only" {
		return cerr(http.StatusForbidden, "forbidden: read-only accounts can't make changes")
	}
	return nil
}

// gateAppWrite is gateApp for operations that change an app's files or
// deployments: Read-only members can look but not touch.
func (s *Server) gateAppWrite(actor *db.User, nonce string) error {
	if err := s.gateWrite(actor); err != nil {
		return err
	}
	return s.gateApp(actor, nonce)
}

// gateTaskService refuses anyone but a Superuser when a service is a task
// service. Task scripts run as the server's own OS account, so creating,
// changing or reading one is as good as owning the machine — that power
// stays with the top role rather than every Admin or service member.
func (s *Server) gateTaskService(actor *db.User, types ...string) error {
	if slices.Contains(types, "tasks") {
		return s.gate(actor, rolesSuperuser)
	}
	return nil
}

// gateServiceFile returns a 403 *coreErr unless the actor is Admin+ or one
// of the service's members — the users an admin has trusted with its
// definition file. The service-file twin of gateApp.
func (s *Server) gateServiceFile(actor *db.User, serviceID int64) error {
	if actor != nil && roleIn(actor.Role, rolesAdminPlus) {
		return nil
	}
	if actor == nil {
		return cerr(http.StatusForbidden, "forbidden: not a member of this service")
	}
	ok, err := s.store.IsServiceMember(serviceID, actor.ID)
	if err != nil {
		return cerr(http.StatusInternalServerError, "membership check failed: %v", err)
	}
	if !ok {
		return cerr(http.StatusForbidden, "forbidden: not a member of this service")
	}
	return nil
}

// gateUserAccount allows actor to manage target's account: Admin+ for most
// users, but only a Superuser may touch a Superuser — otherwise an Admin
// could edit, delete or reset their way into the higher role.
func (s *Server) gateUserAccount(actor, target *db.User) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if target != nil && target.Role == "Superuser" {
		return s.gate(actor, rolesSuperuser)
	}
	return nil
}

// gateRoleGrant checks that role is a real role and that actor may hand it
// out: only a Superuser makes Superusers. An empty role passes (the store
// defaults it to Member).
func (s *Server) gateRoleGrant(actor *db.User, role string) error {
	if role == "" {
		return nil
	}
	if !roleIn(role, rolesAll) {
		return cerr(http.StatusBadRequest, "unknown role %q", role)
	}
	if role == "Superuser" {
		return s.gate(actor, rolesSuperuser)
	}
	return nil
}

// gateSelfOrAdmin allows the action when actor operates on their own account,
// or when actor may manage target's (gateUserAccount). Backs the SSH-key ops
// that serve both the self-service (generate_my_ssh_key) and admin
// (generate_user_ssh_key) surfaces from a single core function.
func (s *Server) gateSelfOrAdmin(actor, target *db.User) error {
	if actor != nil && target != nil && actor.ID == target.ID {
		return nil
	}
	return s.gateUserAccount(actor, target)
}

// ── Shared helpers ──────────────────────────────────────────────────

// actorName resolves the audit actor label for a user: name, then email,
// then user:ID, then "unknown".
func actorName(u *db.User) string {
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
func (s *Server) audit(actor *db.User, action, target string) {
	var id int64
	if actor != nil {
		id = actor.ID
	}
	_ = s.store.LogAudit(id, actorName(actor), action, target)
}

// auditScope is the actor filter for reading the audit log: Admin+ see
// everyone's entries (0), everyone else only their own.
func auditScope(actor *db.User) int64 {
	if actor == nil {
		return -1 // matches no row
	}
	if roleIn(actor.Role, rolesAdminPlus) {
		return 0
	}
	return actor.ID
}

// auditApp logs an app action, resolving the app's display name from its
// nonce (falling back to the nonce itself).
func (s *Server) auditApp(actor *db.User, action, nonce string) {
	target := nonce
	if app, err := s.store.GetApp(nonce); err == nil {
		target = app.Name
	}
	s.audit(actor, action, target)
}

// defaultServiceURL fills in the implicit URL for service types that don't
// carry a remote one (tasks:// and /mcp/ for virtual). Returns url unchanged
// when already set, or "" when the type needs an explicit URL.
func defaultServiceURL(url, name string, d db.ServiceDescriptor) string {
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

// syncMCPMount keeps the in-memory MCP mounts in step with a service
// write: a renamed service leaves its old /mcp/ slug behind, and virtual
// and task services are (re)mounted at their current one.
func (s *Server) syncMCPMount(svc *db.Service, oldURL string) {
	if oldURL != "" && oldURL != svc.URL {
		s.mcpMounts.remove(mcpSlug(oldURL))
	}
	s.mcpMounts.add(s, svc)
}

// publicSSHInfo returns the non-secret view of an SSH key (public key,
// fingerprint, type) — what's safe to hand back to a caller.
func publicSSHInfo(k *sshkit.SSHKeyInfo) *sshkit.SSHKeyInfo {
	if k == nil {
		return nil
	}
	return &sshkit.SSHKeyInfo{
		PublicKey:   k.PublicKey,
		Fingerprint: k.Fingerprint,
		KeyType:     k.KeyType,
	}
}

// ── App operations ──────────────────────────────────────────────────

// reservedAppSlugs are the top-level paths Fresh Breath serves itself; an
// app mounted at one would never be reached.
var reservedAppSlugs = map[string]bool{
	"api": true, "control": true, "mcp": true, "service": true, "oauth": true,
}

// normalizeAppURL cleans up where an app is hosted. A remote URL
// (https://blog.example.com) keeps its location, with the scheme and host
// lowercased. Anything else is a mount point on this server, stored as the
// bare slug: "/blog/" becomes "blog". Subdirectories, reserved paths and
// characters a slug can't carry are refused. Blank stays blank — the app
// is then mounted at its name's slug, or runs from disk.
func normalizeAppURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", cerr(http.StatusBadRequest, "app url %q is not a valid URL", raw)
		}
		u.Scheme = strings.ToLower(u.Scheme)
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", cerr(http.StatusBadRequest, "app url %q must be http or https", raw)
		}
		u.Host = strings.ToLower(u.Host)
		if u.Path == "/" {
			u.Path = ""
		}
		return u.String(), nil
	}
	slug := strings.ToLower(strings.Trim(raw, "/"))
	if first, rest, sub := strings.Cut(slug, "/"); sub {
		return "", cerr(http.StatusBadRequest,
			"Apps cannot be hosted at subdirectories - create a '%s' app and publish to its '%s' folder to achieve this.", first, rest)
	}
	if slug == "" || slugify(slug) != slug {
		return "", cerr(http.StatusBadRequest, "app url %q must be a slug of letters, digits and dashes (like 'blog') or a full https:// URL", raw)
	}
	if reservedAppSlugs[slug] {
		return "", cerr(http.StatusBadRequest, "'%s' is reserved by Fresh Breath - pick another path for this app", slug)
	}
	return slug, nil
}

// validateAuthSlot checks that an optional slot reference points at a real
// auth record. Every kind is eligible in both slots — what a kind means just
// differs by slot — so existence is the whole check.
func (s *Server) validateAuthSlot(slot string, id *int64) error {
	if id == nil {
		return nil
	}
	if _, err := s.store.GetAuthRecord(*id); err != nil {
		return cerr(http.StatusBadRequest, "%s: %v", slot, err)
	}
	return nil
}

func (s *Server) coreCreateApp(actor *db.User, name, env, url string, ownerID *int64, protectedBy *int64) (string, error) {
	return s.coreCreateAppWithNonce(actor, "", name, env, url, ownerID, protectedBy)
}

// coreCreateAppWithNonce creates an app under a nonce it already has on
// another server (an import), or a fresh one for "".
func (s *Server) coreCreateAppWithNonce(actor *db.User, nonce, name, env, url string, ownerID *int64, protectedBy *int64) (string, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return "", err
	}
	if name == "" {
		return "", cerr(http.StatusBadRequest, "name required")
	}
	url, err := normalizeAppURL(url)
	if err != nil {
		return "", err
	}
	if err := s.validateOwner(ownerID, nil); err != nil {
		return "", err
	}
	if err := s.validateAuthSlot("protected_by", protectedBy); err != nil {
		return "", err
	}
	nonce, err = s.store.CreateAppWithNonce(nonce, name, env, url, ownerID, protectedBy)
	if err != nil {
		return "", cerr(http.StatusInternalServerError, "%v", err)
	}
	s.rebuildHostedRoutes()
	s.syncAppServiceMount(nonce)
	s.audit(actor, "created app", name)
	return nonce, nil
}

func (s *Server) coreUpdateApp(actor *db.User, nonce, name, env, url string, ownerID *int64, protectedBy *int64) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	app, err := s.store.GetApp(nonce)
	if err != nil {
		return cerr(http.StatusNotFound, "app not found: %v", err)
	}
	if url, err = normalizeAppURL(url); err != nil {
		return err
	}
	if err := s.validateOwner(ownerID, app.OwnerID); err != nil {
		return err
	}
	if err := s.validateAuthSlot("protected_by", protectedBy); err != nil {
		return err
	}
	if err := s.store.UpdateApp(nonce, name, env, url, ownerID, protectedBy); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.rebuildHostedRoutes()
	s.syncAppServiceMount(nonce)
	s.audit(actor, "updated app", name)
	return nil
}

func (s *Server) coreDeleteApp(actor *db.User, nonce string) error {
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
	s.syncAppServiceMount(nonce)
	s.audit(actor, "deleted app", app.Name)
	return nil
}

func (s *Server) coreSetAppMembers(actor *db.User, nonce string, members []int64) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	kept, err := s.resolveMembers(members)
	if err != nil {
		return err
	}
	if pruned := len(members) - len(kept); pruned > 0 {
		s.auditApp(actor, fmt.Sprintf("updated app members (pruned %d non-member user%s)", pruned, plural(pruned)), nonce)
	} else {
		s.auditApp(actor, "updated app members", nonce)
	}
	if err := s.store.SetAppMembers(nonce, kept); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	_ = s.store.TouchApp(nonce)
	return nil
}

func (s *Server) coreSetAppServices(actor *db.User, nonce string, services []int64) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if err := s.store.SetAppServiceLinks(nonce, services); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	_ = s.store.TouchApp(nonce)
	s.auditApp(actor, "updated app services", nonce)
	return nil
}

// ── Service operations ──────────────────────────────────────────────

// checkServiceName refuses a name that can't serve as a file name: tasks
// and virtual definitions live at <dir>/<name>.txt, and a service's type
// can change after it's named, so every service is held to this.
func checkServiceName(name string) error {
	if name == "" {
		return cerr(http.StatusBadRequest, "name required")
	}
	if !safeFileName(name) {
		return cerr(http.StatusBadRequest, "service names can't contain slashes or NUL, or start with a dot")
	}
	return nil
}

// safeFileName reports whether name names a file directly inside a
// directory on every platform we run on.
func safeFileName(name string) bool {
	return name != "" && name[0] != '.' && !strings.ContainsAny(name, "/\\\x00")
}

func (s *Server) coreCreateService(actor *db.User, name, url string, d db.ServiceDescriptor, protectedBy, actsAs *int64) (*db.Service, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
	if err := checkServiceName(name); err != nil {
		return nil, err
	}
	if strings.HasPrefix(name, appServicePrefix) {
		return nil, cerr(http.StatusBadRequest, "service names starting with %q are reserved for app services", appServicePrefix)
	}
	if !db.ValidServiceType(d.Type) {
		return nil, cerr(http.StatusBadRequest, "unknown service type %q", d.Type)
	}
	if err := s.gateTaskService(actor, d.Type); err != nil {
		return nil, err
	}
	url = defaultServiceURL(url, name, d)
	if url == "" {
		return nil, cerr(http.StatusBadRequest, "url required")
	}
	if err := s.validateDBDescriptor(d); err != nil {
		return nil, err
	}
	if err := s.validateAuthSlot("protected_by", protectedBy); err != nil {
		return nil, err
	}
	if d.Type == "mcp" {
		actsAs = nil // the MCP server decides its own outbound auth
	}
	if err := s.validateAuthSlot("acts_as", actsAs); err != nil {
		return nil, err
	}
	id, err := s.store.RegisterService(name, url, d, protectedBy, actsAs)
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	svc := &db.Service{ID: id, Name: name, URL: url, Descriptor: d, ProtectedBy: protectedBy, ActsAs: actsAs}
	s.syncMCPMount(svc, "")
	s.audit(actor, "created service", name)
	return svc, nil
}

// coreUpdateService replaces a service's fields. Callers wanting patch
// semantics (fill blanks from the existing record) should resolve those
// before calling. The built-in SSH service keeps its name and URL.
func (s *Server) coreUpdateService(actor *db.User, id int64, name, url string, d db.ServiceDescriptor, protectedBy, actsAs *int64) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	existing, err := s.store.GetService(id)
	if err != nil {
		return cerr(http.StatusNotFound, "service not found: %v", err)
	}
	if err := s.gateTaskService(actor, existing.Descriptor.Type, d.Type); err != nil {
		return err
	}
	if err := checkServiceName(name); err != nil {
		return err
	}
	if strings.HasPrefix(name, appServicePrefix) && name != existing.Name {
		return cerr(http.StatusBadRequest, "service names starting with %q are reserved for app services", appServicePrefix)
	}
	if !db.ValidServiceType(d.Type) {
		return cerr(http.StatusBadRequest, "unknown service type %q", d.Type)
	}
	url = defaultServiceURL(url, name, d)
	if url == "" {
		return cerr(http.StatusBadRequest, "url required")
	}
	if existing.Descriptor.Type == "ssh" {
		name = existing.Name
		url = existing.URL
	}
	if err := s.validateDBDescriptor(d); err != nil {
		return err
	}
	if err := s.validateAuthSlot("protected_by", protectedBy); err != nil {
		return err
	}
	if d.Type == "mcp" {
		actsAs = nil // the MCP server decides its own outbound auth
	}
	if err := s.validateAuthSlot("acts_as", actsAs); err != nil {
		return err
	}
	if err := s.store.UpdateService(id, name, url, d, protectedBy, actsAs); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.syncMCPMount(&db.Service{ID: id, Name: name, URL: url, Descriptor: d, ProtectedBy: protectedBy, ActsAs: actsAs}, existing.URL)
	s.audit(actor, "updated service", name)
	return nil
}

// coreSetServiceMembers replaces the users trusted with a service's
// definition file.
func (s *Server) coreSetServiceMembers(actor *db.User, id int64, members []int64) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	svc, err := s.store.GetService(id)
	if err != nil {
		return cerr(http.StatusNotFound, "service not found: %v", err)
	}
	if err := s.gateTaskService(actor, svc.Descriptor.Type); err != nil {
		return err
	}
	kept, err := s.resolveMembers(members)
	if err != nil {
		return err
	}
	if pruned := len(members) - len(kept); pruned > 0 {
		s.audit(actor, fmt.Sprintf("updated service members (pruned %d non-member user%s)", pruned, plural(pruned)), svc.Name)
	} else {
		s.audit(actor, "updated service members", svc.Name)
	}
	if err := s.store.SetServiceMembers(id, kept); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	_ = s.store.TouchService(id)
	return nil
}

// ── Auth record operations ──────────────────────────────────────────

func (s *Server) coreCreateAuth(actor *db.User, name, kind string, d db.AuthDescriptor) (*db.AuthRecord, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, cerr(http.StatusBadRequest, "name required")
	}
	if !db.ValidAuthKind(kind) {
		return nil, cerr(http.StatusBadRequest, "unknown auth kind %q", kind)
	}
	rec, err := s.store.CreateAuthRecord(name, kind, d)
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "created auth record", name)
	return rec, nil
}

// gateAdminRecord refuses anyone but a Superuser when rec is the admin
// gate. Whoever edits that record picks the provider that vouches for
// every Fresh Breath user — the Superusers' emails included — so changing
// it is as good as holding the top role.
func (s *Server) gateAdminRecord(actor *db.User, rec *db.AuthRecord) error {
	if s.isAdminAuthRecord(rec) {
		return s.gate(actor, rolesSuperuser)
	}
	return nil
}

func (s *Server) coreUpdateAuth(actor *db.User, id int64, name, kind string, d db.AuthDescriptor) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if existing, err := s.store.GetAuthRecord(id); err == nil {
		if err := s.gateAdminRecord(actor, existing); err != nil {
			return err
		}
	}
	if name == "" {
		return cerr(http.StatusBadRequest, "name required")
	}
	if !db.ValidAuthKind(kind) {
		return cerr(http.StatusBadRequest, "unknown auth kind %q", kind)
	}
	if err := s.store.UpdateAuthRecord(id, name, kind, d); err != nil {
		return cerr(http.StatusBadRequest, "%v", err)
	}
	s.audit(actor, "updated auth record", name)
	return nil
}

func (s *Server) coreDeleteAuth(actor *db.User, id int64) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	rec, err := s.store.GetAuthRecord(id)
	if err != nil {
		return cerr(http.StatusNotFound, "%v", err)
	}
	// Deleting the admin gate would lock everyone out — the setting would
	// name a record that isn't there.
	if s.isAdminAuthRecord(rec) {
		return cerr(http.StatusConflict, "auth record %q is the admin gate; choose another in Settings first", rec.Name)
	}
	if err := s.store.DeleteAuthRecord(id); err != nil {
		return cerr(http.StatusConflict, "%v", err)
	}
	s.audit(actor, "deleted auth record", rec.Name)
	return nil
}

func (s *Server) coreDeleteService(actor *db.User, id int64) error {
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
	if err := s.gateTaskService(actor, svc.Descriptor.Type); err != nil {
		return err
	}
	if err := s.store.DeleteService(id); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.mcpMounts.remove(mcpSlug(svc.URL))
	s.audit(actor, "deleted service", svc.Name)
	return nil
}

// ── User operations ─────────────────────────────────────────────────

func (s *Server) coreCreateUser(actor *db.User, name, email, role, status string) (*db.User, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
	if err := s.gateRoleGrant(actor, role); err != nil {
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

func (s *Server) coreUpdateUser(actor *db.User, id int64, name, email, role, status string, meta *db.UserMetadata) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	target, err := s.store.GetUser(id)
	if err != nil {
		return cerr(http.StatusNotFound, "%v", err)
	}
	if err := s.gateUserAccount(actor, target); err != nil {
		return err
	}
	if err := s.gateRoleGrant(actor, role); err != nil {
		return err
	}
	if err := s.store.UpdateUser(id, name, email, role, status, meta); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "updated user", name)
	return nil
}

func (s *Server) coreDeleteUser(actor, target *db.User) error {
	if err := s.gateUserAccount(actor, target); err != nil {
		return err
	}
	if err := s.store.DeleteUser(target.ID); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	// The account is gone, so is everything it was signed in with: its
	// refresh families (and the gate grants riding them) and the SSH key a
	// passphrase login unlocked in the agent.
	if err := s.store.RevokeUserRefreshFamilies(subjectForUser(target)); err != nil {
		return cerr(http.StatusInternalServerError, "revoke sessions: %v", err)
	}
	if s.agentMgr != nil {
		s.agentMgr.RemoveKey(target.ID)
	}
	s.audit(actor, "deleted user", target.Name)
	return nil
}

func (s *Server) coreSetUserApps(actor *db.User, id int64, apps []string) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	target, err := s.store.GetUser(id)
	if err != nil {
		return cerr(http.StatusNotFound, "%v", err)
	}
	if err := s.gateUserAccount(actor, target); err != nil {
		return err
	}
	if err := s.store.SetUserApps(id, apps); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	// The membership edit shows up on each app's own page too, so stamp them.
	for _, nonce := range apps {
		_ = s.store.TouchApp(nonce)
	}
	s.audit(actor, "updated user apps", fmt.Sprintf("user:%d", id))
	return nil
}

// coreGenerateSSHKey generates and stores an SSH key for target, returning
// the public key info. actor is credited in the audit log.
func (s *Server) coreGenerateSSHKey(actor, target *db.User, passphrase string) (*sshkit.SSHKeyInfo, error) {
	if err := s.gateSelfOrAdmin(actor, target); err != nil {
		return nil, err
	}
	if len(passphrase) < 8 {
		return nil, cerr(http.StatusBadRequest, "passphrase must be at least 8 characters")
	}
	if target.Metadata != nil && target.Metadata.SSHKey != nil {
		return nil, cerr(http.StatusConflict, "SSH key already exists — delete it first")
	}
	keyInfo, err := sshkit.GenerateSSHKey(passphrase)
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "key generation failed: %v", err)
	}
	meta := target.Metadata
	if meta == nil {
		meta = &db.UserMetadata{}
	}
	meta.SSHKey = keyInfo
	if err := s.store.UpdateUser(target.ID, target.Name, target.Email, target.Role, target.Status, meta); err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "generated SSH key for user", target.Email)
	return publicSSHInfo(keyInfo), nil
}

func (s *Server) coreDeleteSSHKey(actor, target *db.User) error {
	if err := s.gateSelfOrAdmin(actor, target); err != nil {
		return err
	}
	if target.Metadata == nil || target.Metadata.SSHKey == nil {
		return cerr(http.StatusNotFound, "no SSH key to delete")
	}
	meta := *target.Metadata
	meta.SSHKey = nil
	if err := s.store.UpdateUser(target.ID, target.Name, target.Email, target.Role, target.Status, &meta); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	// As with a passphrase reset: a key is deleted when it's no longer
	// trusted, so the agent's decrypted copy and the sessions signed in
	// with it go too.
	if s.agentMgr != nil {
		s.agentMgr.RemoveKey(target.ID)
	}
	if err := s.store.RevokeUserRefreshFamilies(subjectForUser(target)); err != nil {
		return cerr(http.StatusInternalServerError, "revoke sessions: %v", err)
	}
	s.audit(actor, "deleted SSH key for user", target.Email)
	return nil
}

// coreUpdateProfile lets a user change their own name and email. Role,
// status and metadata stay as they are. The email is also what an OIDC or
// OAuth2 login is matched on, so changing it changes which upstream
// identity lands on this account.
func (s *Server) coreUpdateProfile(actor *db.User, name, email string) (*db.User, error) {
	if actor == nil || actor.ID <= 0 {
		return nil, cerr(http.StatusBadRequest, "no user account to update")
	}
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" || email == "" {
		return nil, cerr(http.StatusBadRequest, "name and email required")
	}
	u, err := s.store.GetUser(actor.ID)
	if err != nil {
		return nil, cerr(http.StatusNotFound, "%v", err)
	}
	if err := s.store.UpdateUser(u.ID, name, email, u.Role, u.Status, u.Metadata); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, cerr(http.StatusConflict, "that name or email is already taken")
		}
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	u.Name, u.Email = name, email
	s.audit(u, "updated own profile", email)
	return u, nil
}

// ── Passphrase links ────────────────────────────────────────────────
//
// A passphrase link lets a user choose their own SSH key passphrase: an
// admin mints one (an invite for a new user, a reset for anyone else) and
// sends it on. Opening it sets a new passphrase, which means a new SSH key —
// the private key is sealed under the passphrase, so the old one can't be
// re-sealed without it.

// passphraseLinkTTL is how long a minted passphrase link stays usable.
const passphraseLinkTTL = 7 * 24 * time.Hour

// hashLinkToken is the stored form of a passphrase link token.
func hashLinkToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// coreCreatePassphraseLink mints a passphrase link for target, replacing any
// earlier one, and returns its URL and expiry.
func (s *Server) coreCreatePassphraseLink(actor, target *db.User) (string, time.Time, error) {
	if err := s.gateUserAccount(actor, target); err != nil {
		return "", time.Time{}, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", time.Time{}, cerr(http.StatusInternalServerError, "token generation failed: %v", err)
	}
	token := fmt.Sprintf("%d.%s", target.ID, base64.RawURLEncoding.EncodeToString(secret))
	expires := time.Now().UTC().Add(passphraseLinkTTL).Truncate(time.Second)
	meta := db.UserMetadata{}
	if target.Metadata != nil {
		meta = *target.Metadata
	}
	meta.PassphraseReset = &db.PassphraseReset{TokenHash: hashLinkToken(token), ExpiresAt: expires}
	if err := s.store.UpdateUser(target.ID, target.Name, target.Email, target.Role, target.Status, &meta); err != nil {
		return "", time.Time{}, cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "created passphrase link for user", target.Email)
	return s.config.PublicBaseURL + "/service/passphrase?token=" + token, expires, nil
}

// passphraseLinkUser resolves a link token to its user. Unknown, used and
// expired tokens all fail alike.
func (s *Server) passphraseLinkUser(token string) (*db.User, error) {
	invalid := cerr(http.StatusBadRequest, "This link is invalid or has expired — ask an admin for a new one.")
	idStr, _, ok := strings.Cut(token, ".")
	if !ok {
		return nil, invalid
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return nil, invalid
	}
	u, err := s.store.GetUser(id)
	if err != nil || u.Metadata == nil || u.Metadata.PassphraseReset == nil {
		return nil, invalid
	}
	pr := u.Metadata.PassphraseReset
	if subtle.ConstantTimeCompare([]byte(pr.TokenHash), []byte(hashLinkToken(token))) != 1 || time.Now().After(pr.ExpiresAt) {
		return nil, invalid
	}
	return u, nil
}

// coreSetPassphraseByLink spends a passphrase link: the user gets a fresh
// SSH key sealed under the new passphrase, an invited user becomes active,
// and the link stops working.
func (s *Server) coreSetPassphraseByLink(token, passphrase string) (*db.User, error) {
	u, err := s.passphraseLinkUser(token)
	if err != nil {
		return nil, err
	}
	if len(passphrase) < 8 {
		return nil, cerr(http.StatusBadRequest, "passphrase must be at least 8 characters")
	}
	keyInfo, err := sshkit.GenerateSSHKey(passphrase)
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "key generation failed: %v", err)
	}
	meta := *u.Metadata
	meta.SSHKey = keyInfo
	meta.PassphraseReset = nil
	status := u.Status
	if status == "Invited" {
		status = "Active"
	}
	if err := s.store.UpdateUser(u.ID, u.Name, u.Email, u.Role, status, &meta); err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	// The agent may still hold the old key, decrypted at the last login.
	if s.agentMgr != nil {
		s.agentMgr.RemoveKey(u.ID)
	}
	// A reset is how a user recovers from a leaked passphrase; sessions
	// signed in with the old one end here too.
	if err := s.store.RevokeUserRefreshFamilies(subjectForUser(u)); err != nil {
		return nil, cerr(http.StatusInternalServerError, "revoke sessions: %v", err)
	}
	u.Status, u.Metadata = status, &meta
	s.audit(u, "set passphrase from link", u.Email)
	return u, nil
}

// ── Settings operations ─────────────────────────────────────────────

// coreUpdateSettings applies the non-nil settings fields, validating each.
func (s *Server) coreUpdateSettings(actor *db.User, adminAuthService, defaultApp, mcpDBMode *string, requireVerifiedEmail *bool) error {
	if err := s.gate(actor, rolesSuperuser); err != nil {
		return err
	}
	if adminAuthService != nil {
		if *adminAuthService != "" {
			id, err := parseID(*adminAuthService)
			if err != nil {
				return cerr(http.StatusBadRequest, "admin_auth_service must be a numeric auth record ID")
			}
			rec, err := s.store.GetAuthRecord(id)
			if err != nil {
				return cerr(http.StatusBadRequest, "admin_auth_service: %v", err)
			}
			// The admin gate must prove who is calling; kinds with no
			// identity can't back it.
			if rec.Kind == db.AuthAnonymous || rec.Kind == db.AuthAPIKey {
				return cerr(http.StatusBadRequest, "admin_auth_service must be an identity-bearing auth record (ssh_key, oidc, or oauth2)")
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
	if mcpDBMode != nil {
		if *mcpDBMode != "read-only" && *mcpDBMode != "full-access" {
			return cerr(http.StatusBadRequest, `mcp_database_mode must be "read-only" or "full-access"`)
		}
		if err := s.store.SetSetting("mcp_database_mode", *mcpDBMode); err != nil {
			return cerr(http.StatusInternalServerError, "%v", err)
		}
		s.audit(actor, "updated settings", "mcp_database_mode")
	}
	if requireVerifiedEmail != nil {
		if err := s.store.SetSetting("admin_require_verified_email", strconv.FormatBool(*requireVerifiedEmail)); err != nil {
			return cerr(http.StatusInternalServerError, "%v", err)
		}
		s.audit(actor, "updated settings", "admin_require_verified_email")
	}
	return nil
}

// coreGetSettings returns the settings to a Superuser. It checks the role
// itself rather than trusting that only a Superuser's MCP server lists
// get_settings.
func (s *Server) coreGetSettings(actor *db.User) (map[string]interface{}, error) {
	if err := s.gate(actor, rolesSuperuser); err != nil {
		return nil, err
	}
	return s.settingsView(), nil
}

// settingsView is the settings as the panel and get_settings show them.
func (s *Server) settingsView() map[string]interface{} {
	result := map[string]interface{}{
		"admin_auth_service": nil, "default_app": nil, "mcp_database_mode": "read-only",
		"admin_require_verified_email": s.adminRequiresVerifiedEmail(),
	}
	if v, err := s.store.GetSetting("admin_auth_service"); err == nil && v != "" {
		result["admin_auth_service"] = v
	}
	if v, err := s.store.GetSetting("default_app"); err == nil && v != "" {
		result["default_app"] = v
	}
	if v, err := s.store.GetSetting("mcp_database_mode"); err == nil && v == "full-access" {
		result["mcp_database_mode"] = v
	}
	return result
}

// adminRequiresVerifiedEmail reports whether the admin gate links a login
// to a user only by an email the provider explicitly marked verified.
func (s *Server) adminRequiresVerifiedEmail() bool {
	v, err := s.store.GetSetting("admin_require_verified_email")
	return err == nil && v == "true"
}

// ── App Web operations ─────────────────────────────────────────────

// appFileDir resolves an app file area to its directory. "web" (the default)
// is the Development slot the web server hosts; "data" sits beside it and is
// never served — private storage such as user uploads.
func (s *Server) appFileDir(nonce, area string) (string, error) {
	switch area {
	case "", "web":
		return filepath.Join(s.config.DataDir, "apps", nonce, "web"), nil
	case "data":
		return filepath.Join(s.config.DataDir, "apps", nonce, "data"), nil
	}
	return "", cerr(http.StatusBadRequest, `area must be "web" or "data"`)
}

// appFile is one entry in an app's web or data directory.
type appFile struct {
	Path string `json:"path"` // slash-separated path relative to the web dir
	Size int64  `json:"size"` // bytes
}

// coreListAppFiles lists the files in an app's web or data area, sorted by path.
// An app with no uploaded files lists empty (not an error). If search is
// non-empty, only files whose path or content contains the term (case-
// insensitive) are returned.
func (s *Server) coreListAppFiles(actor *db.User, nonce, area, search string) ([]appFile, error) {
	if err := s.gateApp(actor, nonce); err != nil {
		return nil, err
	}
	if _, err := s.store.GetApp(nonce); err != nil {
		return nil, cerr(http.StatusNotFound, "app not found: %v", err)
	}
	webDir, err := s.appFileDir(nonce, area)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(webDir); os.IsNotExist(err) {
		return []appFile{}, nil
	}
	files := []appFile{}
	err = filepath.Walk(webDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(webDir, path)
		relSlash := filepath.ToSlash(rel)
		if search != "" && !fileMatchesSearch(webDir, relSlash, search) {
			return nil
		}
		files = append(files, appFile{Path: relSlash, Size: fi.Size()})
		return nil
	})
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "list failed: %v", err)
	}
	slices.SortFunc(files, func(a, b appFile) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// appFileMatch is one matching line from searchAppFiles.
type appFileMatch struct {
	Nonce string `json:"nonce"`
	App   string `json:"app"`
	Path  string `json:"path"` // slash-separated path relative to the web dir
	Line  int    `json:"line"` // 1-based
	Text  string `json:"text"`
}

// Search output caps: a whole-server search or a minified bundle could
// otherwise produce an unbounded result.
const (
	searchMaxMatches  = 500
	searchMaxLineText = 300
)

// coreSearchAppFiles greps an app file area (web or data) for a regular expression (RE2
// syntax), returning matching lines with 1-based line numbers, like rg. With
// a nonce it searches that app; without one it searches every app the actor
// can list (all apps for admin+, their own apps otherwise). Binary files are
// skipped, long lines are clipped, and results stop at searchMaxMatches with
// truncated=true.
func (s *Server) coreSearchAppFiles(actor *db.User, nonce, area, pattern string, ignoreCase bool) ([]appFileMatch, bool, error) {
	if _, err := s.appFileDir(nonce, area); err != nil {
		return nil, false, err
	}
	if pattern == "" {
		return nil, false, cerr(http.StatusBadRequest, "pattern is required")
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false, cerr(http.StatusBadRequest, "invalid pattern: %v", err)
	}

	type appRef struct{ nonce, name string }
	var apps []appRef
	if nonce != "" {
		if err := s.gateApp(actor, nonce); err != nil {
			return nil, false, err
		}
		app, err := s.store.GetApp(nonce)
		if err != nil {
			return nil, false, cerr(http.StatusNotFound, "app not found: %v", err)
		}
		apps = append(apps, appRef{app.Nonce, app.Name})
	} else {
		if actor == nil {
			return nil, false, cerr(http.StatusForbidden, "forbidden")
		}
		var rows []map[string]interface{}
		if roleIn(actor.Role, rolesAdminPlus) {
			rows, err = s.store.ListApps()
		} else {
			rows, err = s.store.ListAppsForUser(actor.ID)
		}
		if err != nil {
			return nil, false, cerr(http.StatusInternalServerError, "list apps failed: %v", err)
		}
		for _, row := range rows {
			n, _ := row["nonce"].(string)
			name, _ := row["name"].(string)
			apps = append(apps, appRef{n, name})
		}
	}

	matches := []appFileMatch{}
	for _, app := range apps {
		webDir, _ := s.appFileDir(app.nonce, area)
		if _, err := os.Stat(webDir); os.IsNotExist(err) {
			continue
		}
		var paths []string
		err := filepath.Walk(webDir, func(path string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return err
			}
			paths = append(paths, path)
			return nil
		})
		if err != nil {
			return nil, false, cerr(http.StatusInternalServerError, "search failed: %v", err)
		}
		slices.Sort(paths)
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil || bytes.IndexByte(data, 0) >= 0 {
				continue // unreadable or binary
			}
			rel, _ := filepath.Rel(webDir, path)
			for i, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSuffix(line, "\r")
				if !re.MatchString(line) {
					continue
				}
				if len(matches) == searchMaxMatches {
					return matches, true, nil
				}
				if len(line) > searchMaxLineText {
					line = clipAroundMatch(line, re)
				}
				matches = append(matches, appFileMatch{
					Nonce: app.nonce,
					App:   app.name,
					Path:  filepath.ToSlash(rel),
					Line:  i + 1,
					Text:  line,
				})
			}
		}
	}
	return matches, false, nil
}

// clipAroundMatch cuts a long line down to searchMaxLineText bytes centered
// near the first match, marking cut ends with "…". Keeps minified files
// readable in search results.
func clipAroundMatch(line string, re *regexp.Regexp) string {
	start := 0
	if loc := re.FindStringIndex(line); loc != nil {
		start = max(0, loc[0]-searchMaxLineText/3)
	}
	end := min(len(line), start+searchMaxLineText)
	text := strings.ToValidUTF8(line[start:end], "")
	if start > 0 {
		text = "…" + text
	}
	if end < len(line) {
		text += "…"
	}
	return text
}

// coreDownloadAppWeb returns the app's web directory as a zip archive.
func (s *Server) coreDownloadAppWeb(actor *db.User, nonce string) ([]byte, string, error) {
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
	name := appSlug(app)
	if name == "" {
		name = slugify(app.Name)
	}
	return buf.Bytes(), name, nil
}

// uploadFile is one file from an upload request. Name is the client's
// filename, which may carry a relative path ("css/site.css").
type uploadFile struct {
	Name string
	Data []byte
}

// uploadEntry is one file of an uploaded bundle: a slash path plus an
// opener, so zip entries stream straight to disk instead of being
// decompressed into memory.
type uploadEntry struct {
	path string
	open func() (io.ReadCloser, error)
}

// gatherUploadEntries turns an uploaded bundle (any .zip expanded in place)
// into flat entries, refusing a bundle whose uncompressed total runs past
// uploadExtractMaxBytes before anything is written: a 100MB upload can
// carry a zip that expands to far more than that on disk.
func gatherUploadEntries(files []uploadFile) ([]uploadEntry, error) {
	var entries []uploadEntry
	var total int64
	for _, f := range files {
		rel, err := cleanAppFilePath(f.Name)
		if err != nil {
			return nil, cerr(http.StatusBadRequest, "%s: %v", f.Name, err)
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(strings.ToLower(rel), ".zip") {
			data := f.Data
			total += int64(len(data))
			entries = append(entries, uploadEntry{rel, func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(data)), nil
			}})
			continue
		}
		zr, err := zip.NewReader(bytes.NewReader(f.Data), int64(len(f.Data)))
		if err != nil {
			return nil, cerr(http.StatusBadRequest, "zip error: %s: %v", f.Name, err)
		}
		zipDir := path.Dir(rel) // a zip expands where it was uploaded
		for _, zf := range zr.File {
			if zf.FileInfo().IsDir() {
				continue
			}
			clean := path.Clean(path.Join(zipDir, filepath.ToSlash(zf.Name)))
			if clean == "." || path.IsAbs(clean) || strings.HasPrefix(clean, "..") {
				continue // unsafe entry
			}
			total += int64(zf.UncompressedSize64)
			entries = append(entries, uploadEntry{clean, zf.Open})
		}
		if total > uploadExtractMaxBytes {
			return nil, cerr(http.StatusBadRequest, "%s expands past the %dMB cap", f.Name, uploadExtractMaxBytes>>20)
		}
	}
	if total > uploadExtractMaxBytes {
		return nil, cerr(http.StatusBadRequest, "upload extracts past the %dMB cap", uploadExtractMaxBytes>>20)
	}
	return entries, nil
}

// coreUploadAppWeb replaces an app's web directory (the Development slot)
// with an uploaded bundle: one or more files, where any .zip is expanded in
// place. The whole set is treated like one zip — a single top-level folder
// shared by every file is unwrapped, and if there's no index.html the first
// .html alphabetically becomes it — so a lone page, a zip, a pile of files
// and a dropped folder all publish the same way. Everything is validated
// before the old files are cleared.
func (s *Server) coreUploadAppWeb(actor *db.User, nonce string, files []uploadFile) (string, error) {
	if err := s.gateAppWrite(actor, nonce); err != nil {
		return "", err
	}
	app, err := s.store.GetApp(nonce)
	if err != nil {
		return "", cerr(http.StatusNotFound, "app not found: %v", err)
	}
	if len(files) == 0 {
		return "", cerr(http.StatusBadRequest, "no files uploaded")
	}

	entries, err := gatherUploadEntries(files)
	if err != nil {
		return "", err
	}

	// Unwrap a single top-level folder shared by every entry.
	topDirs := map[string]bool{}
	hasRootFiles := false
	for _, e := range entries {
		if top, _, found := strings.Cut(e.path, "/"); found {
			topDirs[top] = true
		} else {
			hasRootFiles = true
		}
	}
	if !hasRootFiles && len(topDirs) == 1 {
		for i := range entries {
			_, entries[i].path, _ = strings.Cut(entries[i].path, "/")
		}
	}

	// Find the entry point: index.html, else the first .html alphabetically.
	var htmlFiles []string
	hasIndex := false
	for _, e := range entries {
		if e.path == "index.html" {
			hasIndex = true
		}
		if strings.HasSuffix(strings.ToLower(e.path), ".html") {
			htmlFiles = append(htmlFiles, e.path)
		}
	}
	if !hasIndex {
		if len(htmlFiles) == 0 {
			return "", cerr(http.StatusBadRequest, "no HTML file found in upload")
		}
		sort.Strings(htmlFiles)
		for i := range entries {
			if entries[i].path == htmlFiles[0] {
				entries[i].path = "index.html"
			}
		}
	}

	webDir := filepath.Join(s.config.DataDir, "apps", nonce, "web")
	if err := os.RemoveAll(webDir); err != nil {
		return "", cerr(http.StatusInternalServerError, "failed to clear web dir")
	}
	var written int64
	for _, e := range entries {
		dest := filepath.Join(webDir, filepath.FromSlash(e.path))
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return "", cerr(http.StatusInternalServerError, "mkdir: %v", err)
		}
		rc, err := e.open()
		if err != nil {
			return "", cerr(http.StatusBadRequest, "open %s: %v", e.path, err)
		}
		out, err := os.Create(dest)
		if err != nil {
			rc.Close()
			return "", cerr(http.StatusInternalServerError, "create: %v", err)
		}
		// The metadata total was pre-checked in gatherUploadEntries; this
		// copy-time bound is the backstop for a zip whose central directory
		// lies about its sizes.
		n, copyErr := io.Copy(out, io.LimitReader(rc, uploadExtractMaxBytes-written+1))
		written += n
		if copyErr == nil && written > uploadExtractMaxBytes {
			copyErr = fmt.Errorf("extracts past the %dMB cap", uploadExtractMaxBytes>>20)
		}
		out.Close()
		rc.Close()
		if copyErr != nil {
			return "", cerr(http.StatusBadRequest, "write %s: %v", e.path, copyErr)
		}
	}

	now := time.Now().UTC()
	details := app.Details
	if details == nil {
		details = &db.AppDetails{}
	}
	details.LastUploaded = &now
	if err := s.store.UpdateAppDetails(nonce, details); err != nil {
		return "", cerr(http.StatusInternalServerError, "failed to save details")
	}
	s.rebuildHostedRoutes()
	s.audit(actor, "uploaded web files", app.Name)
	return appLocation(app), nil
}

// coreUploadAppData adds uploaded files to an app's data area at their
// relative paths, replacing same-named files and leaving the rest alone.
// Files are stored as-is (zips are not expanded). All paths are checked
// before anything is written. Returns the written paths.
func (s *Server) coreUploadAppData(actor *db.User, nonce string, files []uploadFile) ([]string, error) {
	if len(files) == 0 {
		return nil, cerr(http.StatusBadRequest, "no files uploaded")
	}
	paths := make([]string, len(files))
	for i, f := range files {
		rel, err := cleanAppFilePath(f.Name)
		if err != nil {
			return nil, cerr(http.StatusBadRequest, "%s: %v", f.Name, err)
		}
		paths[i] = filepath.ToSlash(rel)
	}
	for i, f := range files {
		if err := s.coreWriteAppFile(actor, nonce, "data", paths[i], f.Data, ""); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

// coreDeleteAppWeb removes an app's web directory (the Development slot).
// Staging/production deploy records survive: what's live elsewhere stays live.
func (s *Server) coreDeleteAppWeb(actor *db.User, nonce string) error {
	if err := s.gateAppWrite(actor, nonce); err != nil {
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
	details := app.Details
	if details == nil {
		details = &db.AppDetails{}
	}
	details.LastUploaded = nil
	if err := s.store.UpdateAppDetails(nonce, details); err != nil {
		return cerr(http.StatusInternalServerError, "failed to save details")
	}
	s.rebuildHostedRoutes()
	s.audit(actor, "removed web files", app.Name)
	return nil
}

// deploySlotDirs maps deploy API slot names to on-disk dirs. Development is
// the web upload folder; the URL scheme uses the same dev/staging/prod names.
var deploySlotDirs = map[string]string{
	"dev":     "web",
	"staging": "staging",
	"prod":    "production",
}

// coreDeployApp copies one deployment slot over another, replacing the target
// entirely. Source may be dev or staging (dev when empty); target staging or
// prod. Returns the deployed slot's URL route.
func (s *Server) coreDeployApp(actor *db.User, nonce, source, target string) (string, error) {
	if err := s.gateAppWrite(actor, nonce); err != nil {
		return "", err
	}
	if source == "" {
		source = "dev"
	}
	srcName, ok := deploySlotDirs[source]
	if !ok || source == "prod" {
		return "", cerr(http.StatusBadRequest, "invalid source slot %q (dev or staging)", source)
	}
	dstName, ok := deploySlotDirs[target]
	if !ok || target == "dev" {
		return "", cerr(http.StatusBadRequest, "invalid target slot %q (staging or prod)", target)
	}
	app, err := s.store.GetApp(nonce)
	if err != nil {
		return "", cerr(http.StatusNotFound, "app not found: %v", err)
	}
	base := filepath.Join(s.config.DataDir, "apps", nonce)
	srcDir := filepath.Join(base, srcName)
	if fi, err := os.Stat(srcDir); err != nil || !fi.IsDir() {
		return "", cerr(http.StatusNotFound, "%s slot is empty; nothing to deploy", source)
	}
	dstDir := filepath.Join(base, dstName)
	if err := os.RemoveAll(dstDir); err != nil {
		return "", cerr(http.StatusInternalServerError, "failed to clear %s slot", target)
	}
	if err := copyDir(srcDir, dstDir); err != nil {
		return "", cerr(http.StatusInternalServerError, "deploy failed: %v", err)
	}

	now := time.Now().UTC()
	details := app.Details
	if details == nil {
		details = &db.AppDetails{}
	}
	if target == "staging" {
		details.LastDeployedStaging = &now
	} else {
		details.LastDeployedProduction = &now
	}
	if err := s.store.UpdateAppDetails(nonce, details); err != nil {
		return "", cerr(http.StatusInternalServerError, "failed to save details")
	}
	s.rebuildHostedRoutes()
	s.audit(actor, "deployed "+source+" → "+target, app.Name)
	// An origin with "@prod" right after it would read as a username.
	loc := appLocation(app)
	if strings.Contains(loc, "://") {
		loc += "/"
	}
	return loc + "@" + target, nil
}

// copyDir recursively copies the contents of src into dst, preserving file
// modes. dst is created if missing.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		out := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(out, 0755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(w, in)
		in.Close()
		closeErr := w.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// cleanAppFilePath validates a path relative to an app's web directory and
// returns a platform-relative path. It rejects empty, absolute, and upward-
// traversing paths.
func cleanAppFilePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	rel := filepath.Clean(filepath.FromSlash(p))
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid path")
	}
	return rel, nil
}

// sliceBytes returns a slice of data from offset up to offset+limit. A zero
// or over-large limit reads to the end of the data.
func sliceBytes(data []byte, offset, limit int64) []byte {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	end := int64(len(data))
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return data[offset:end]
}

// fileMatchesSearch reports whether a file's path or content contains term
// (case-insensitive). Read errors are treated as non-matching.
func fileMatchesSearch(webDir, relPath, term string) bool {
	lower := strings.ToLower(term)
	if strings.Contains(strings.ToLower(relPath), lower) {
		return true
	}
	data, err := os.ReadFile(filepath.Join(webDir, relPath))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), lower)
}

// coreReadAppFile reads all or part of a file from an app's web or data area.
// offset is a zero-based byte position; limit is the maximum bytes to return.
// A zero limit reads to the end of the file.
func (s *Server) coreReadAppFile(actor *db.User, nonce, area, filePath string, offset, limit int64) ([]byte, error) {
	if err := s.gateApp(actor, nonce); err != nil {
		return nil, err
	}
	if _, err := s.store.GetApp(nonce); err != nil {
		return nil, cerr(http.StatusNotFound, "app not found: %v", err)
	}
	rel, err := cleanAppFilePath(filePath)
	if err != nil {
		return nil, cerr(http.StatusBadRequest, "%v", err)
	}
	dir, err := s.appFileDir(nonce, area)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cerr(http.StatusNotFound, "file not found")
		}
		return nil, cerr(http.StatusInternalServerError, "read failed: %v", err)
	}
	return sliceBytes(data, offset, limit), nil
}

// coreStatAppFile returns the size and sniffed content type of a file in an
// app's web or data area without reading the whole file. Used by the MCP
// read_app_file transport:"http" and threshold-escape paths to populate
// {size, content_type} without loading a potentially large file into memory.
func (s *Server) coreStatAppFile(actor *db.User, nonce, area, filePath string) (int64, string, error) {
	if err := s.gateApp(actor, nonce); err != nil {
		return 0, "", err
	}
	if _, err := s.store.GetApp(nonce); err != nil {
		return 0, "", cerr(http.StatusNotFound, "app not found: %v", err)
	}
	rel, err := cleanAppFilePath(filePath)
	if err != nil {
		return 0, "", cerr(http.StatusBadRequest, "%v", err)
	}
	dir, err := s.appFileDir(nonce, area)
	if err != nil {
		return 0, "", err
	}
	size, ct, err := sniffFile(filepath.Join(dir, rel))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "", cerr(http.StatusNotFound, "file not found")
		}
		return 0, "", cerr(http.StatusInternalServerError, "stat failed: %v", err)
	}
	return size, ct, nil
}

// sniffFile returns the size and sniffed content type of a file, reading at
// most 512 bytes (http.DetectContentType only consults the first 512). It lets
// callers populate {size, content_type} without loading a large file.
func sniffFile(fullPath string) (size int64, contentType string, err error) {
	info, err := os.Stat(fullPath)
	if err != nil {
		return 0, "", err
	}
	f, err := os.Open(fullPath)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	return info.Size(), http.DetectContentType(head[:n]), nil
}

// replaceUniqueText replaces the single occurrence of old in src with repl.
// It returns an error if old is not found or appears more than once.
func replaceUniqueText(src, old, repl []byte) ([]byte, error) {
	count := bytes.Count(src, old)
	if count == 0 {
		return nil, fmt.Errorf("old_text not found")
	}
	if count > 1 {
		return nil, fmt.Errorf("old_text is not unique (%d occurrences)", count)
	}
	return bytes.Replace(src, old, repl, 1), nil
}

// coreWriteAppFile writes or patches a file in an app's web or data area. If
// oldText is empty the entire file is replaced. Otherwise the single occurrence
// of oldText in the existing file is replaced with data. Only web writes count
// as a publish (LastUploaded, hosted routes).
func (s *Server) coreWriteAppFile(actor *db.User, nonce, area, filePath string, data []byte, oldText string) error {
	if err := s.gateAppWrite(actor, nonce); err != nil {
		return err
	}
	app, err := s.store.GetApp(nonce)
	if err != nil {
		return cerr(http.StatusNotFound, "app not found: %v", err)
	}
	rel, err := cleanAppFilePath(filePath)
	if err != nil {
		return cerr(http.StatusBadRequest, "%v", err)
	}

	dir, err := s.appFileDir(nonce, area)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(dir, rel)

	var newData []byte
	if oldText == "" {
		newData = data
	} else {
		existing, err := os.ReadFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				return cerr(http.StatusNotFound, "file not found")
			}
			return cerr(http.StatusInternalServerError, "read failed: %v", err)
		}
		newData, err = replaceUniqueText(existing, []byte(oldText), data)
		if err != nil {
			return cerr(http.StatusBadRequest, "%v", err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return cerr(http.StatusInternalServerError, "failed to create directory")
	}
	if err := os.WriteFile(fullPath, newData, 0644); err != nil {
		return cerr(http.StatusInternalServerError, "write failed")
	}

	if area != "data" {
		now := time.Now().UTC()
		details := app.Details
		if details == nil {
			details = &db.AppDetails{}
		}
		details.LastUploaded = &now
		if err := s.store.UpdateAppDetails(nonce, details); err != nil {
			return cerr(http.StatusInternalServerError, "failed to save details")
		}
		s.rebuildHostedRoutes()
	}
	_ = s.store.TouchApp(nonce)
	s.audit(actor, "wrote app file", app.Name+"/"+appFileAuditPath(area, rel))
	return nil
}

// coreDeleteAppFile removes a single file from an app's web or data area.
func (s *Server) coreDeleteAppFile(actor *db.User, nonce, area, filePath string) error {
	if err := s.gateAppWrite(actor, nonce); err != nil {
		return err
	}
	app, err := s.store.GetApp(nonce)
	if err != nil {
		return cerr(http.StatusNotFound, "app not found: %v", err)
	}
	rel, err := cleanAppFilePath(filePath)
	if err != nil {
		return cerr(http.StatusBadRequest, "%v", err)
	}
	dir, err := s.appFileDir(nonce, area)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, rel)); err != nil {
		if os.IsNotExist(err) {
			return cerr(http.StatusNotFound, "file not found")
		}
		return cerr(http.StatusInternalServerError, "delete failed")
	}
	if area != "data" {
		s.rebuildHostedRoutes()
	}
	_ = s.store.TouchApp(nonce)
	s.audit(actor, "deleted app file", app.Name+"/"+appFileAuditPath(area, rel))
	return nil
}

// appFileAuditPath labels data-area files in the audit log so they aren't
// mistaken for published web files.
func appFileAuditPath(area, rel string) string {
	rel = filepath.ToSlash(rel)
	if area == "data" {
		return "data:" + rel
	}
	return rel
}

// ── App services ────────────────────────────────────────────────────
//
// Every app has its own virtual service, implicit rather than a row in the
// services table. It starts blank and gains tools when its definition is
// written, then works like any virtual service: MCP at /mcp/app:<nonce>,
// HTTP at /service/call/app:<nonce>, and the usual service-file tools
// under the name app:<nonce>. The nonce is the name — unique by
// construction and stable across renames, so the mount never moves — and
// the bare /mcp/app: (or /service/call/app:) resolves to the current
// app's nonce from the request (X-App-Nonce, Referer, ?loc=), so a page
// can name its own service without naming its nonce. It answers to the
// app: the app's gate protects it, app members may edit it, only the app
// itself may call it over HTTP, and its SQL steps use the app's own
// database. Its ID is the app's negated, which can't collide with a real
// service. The definition lives at apps/<nonce>/service.txt — outside
// every served slot — and goes with the app.

// appServicePrefix starts every app service's name and slug.
const appServicePrefix = "app:"

// appService builds an app's implicit virtual service. The slug is the
// nonce, so /mcp/app:<nonce> is a fixed address no rename ever moves.
func appService(app *db.App) *db.Service {
	slug := appServicePrefix + app.Nonce
	return &db.Service{
		ID:          -app.ID,
		Name:        slug,
		URL:         "/mcp/" + slug,
		Descriptor:  db.ServiceDescriptor{Type: "virtual", DatabaseTarget: "app:" + app.Nonce},
		ProtectedBy: app.ProtectedBy,
		AppNonce:    app.Nonce,
	}
}

// appServiceByNonce finds the app service for an app nonce (the part
// after "app:").
func (s *Server) appServiceByNonce(nonce string) (*db.Service, error) {
	app, err := s.store.GetApp(nonce)
	if err != nil {
		return nil, fmt.Errorf("no app with nonce %q", nonce)
	}
	return appService(app), nil
}

// syncAppServiceMount refreshes an app's service mount after the app or
// its definition changes — or unmounts it once the app is gone. The slug
// itself never changes (it's the nonce); what needs refreshing is the
// gate (protected_by is baked into the mounted entry) and the tools.
func (s *Server) syncAppServiceMount(nonce string) {
	s.mcpMounts.removeApp(nonce)
	if app, err := s.store.GetApp(nonce); err == nil {
		s.mcpMounts.add(s, appService(app))
	}
}

// loadVirtualTools parses a virtual service's definition. An app service
// with nothing written yet is blank, not missing.
func (s *Server) loadVirtualTools(svc *db.Service) ([]formats.VirtualTool, error) {
	path := serviceDefinitionPath(s.config.DataDir, svc)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) && svc.AppNonce != "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("virtual file not found: %s: %w", path, err)
	}
	return formats.ParseVirtualFile(data)
}

// ── Service File operations ─────────────────────────────────────────

// serviceDefinitionPath returns the on-disk definition path for tasks and
// virtual services, or "" for other service types — and for a name that
// would leave its directory (rows that predate checkServiceName).
func serviceDefinitionPath(dataDir string, svc *db.Service) string {
	if svc.AppNonce != "" {
		return filepath.Join(dataDir, "apps", svc.AppNonce, "service.txt")
	}
	if !safeFileName(svc.Name) {
		return ""
	}
	switch svc.Descriptor.Type {
	case "tasks":
		return filepath.Join(dataDir, "tasks", svc.Name+".txt")
	case "virtual":
		return filepath.Join(dataDir, "virtual", svc.Name+".txt")
	}
	return ""
}

// definitionService gates and loads the service whose definition file an
// operation touches: a registered tasks/virtual service (Admin+ or a service
// member), or — for a negative ID — an app's own service (Admin+ or an app
// member).
func (s *Server) definitionService(actor *db.User, id int64) (*db.Service, error) {
	if id < 0 {
		app, err := s.store.GetAppByID(-id)
		if err != nil {
			return nil, cerr(http.StatusNotFound, "service not found: %v", err)
		}
		if err := s.gateApp(actor, app.Nonce); err != nil {
			return nil, err
		}
		return appService(app), nil
	}
	if err := s.gateServiceFile(actor, id); err != nil {
		return nil, err
	}
	svc, err := s.store.GetService(id)
	if err != nil {
		return nil, cerr(http.StatusNotFound, "service not found: %v", err)
	}
	// Membership grants a virtual definition; a task script is Superuser's.
	if err := s.gateTaskService(actor, svc.Descriptor.Type); err != nil {
		return nil, err
	}
	if svc.Descriptor.Type != "tasks" && svc.Descriptor.Type != "virtual" {
		return nil, cerr(http.StatusBadRequest, "service type %q does not support file publishing", svc.Descriptor.Type)
	}
	if err := checkServiceName(svc.Name); err != nil {
		return nil, cerr(http.StatusBadRequest, "rename this service first: %s", err)
	}
	return svc, nil
}

// touchService bumps a registered service's updated_at; an app service
// touches its app.
func (s *Server) touchService(svc *db.Service) {
	if svc.AppNonce != "" {
		_ = s.store.TouchApp(svc.AppNonce)
		return
	}
	_ = s.store.TouchService(svc.ID)
}

// coreDownloadServiceFiles returns a service's published definition file.
// Only tasks and virtual services support file publishing; the returned file
// is the raw plain-text definition.
func (s *Server) coreDownloadServiceFiles(actor *db.User, id int64) ([]byte, string, error) {
	svc, err := s.definitionService(actor, id)
	if err != nil {
		return nil, "", err
	}

	path := serviceDefinitionPath(s.config.DataDir, svc)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", cerr(http.StatusNotFound, "no service files uploaded")
		}
		return nil, "", cerr(http.StatusInternalServerError, "read failed: %v", err)
	}
	return data, filepath.Base(path), nil
}

// coreUploadServiceFiles writes files for a service from raw content.
// Only tasks and virtual services support file publishing; they each accept a
// single plain-text file stored in their existing definition directory.
func (s *Server) coreUploadServiceFiles(actor *db.User, id int64, data []byte, filename string) (string, error) {
	if err := s.gateWrite(actor); err != nil {
		return "", err
	}
	svc, err := s.definitionService(actor, id)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(strings.ToLower(filename), ".zip") {
		return "", cerr(http.StatusBadRequest, "zip uploads are not supported for %s services", svc.Descriptor.Type)
	}

	path := serviceDefinitionPath(s.config.DataDir, svc)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", cerr(http.StatusInternalServerError, "failed to create service dir")
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", cerr(http.StatusInternalServerError, "write failed")
	}
	s.mcpMounts.add(s, svc)

	s.touchService(svc)
	s.audit(actor, "uploaded service files", svc.Name)
	return svc.URL, nil
}

// coreDeleteServiceFiles removes a service's published definition file.
// Only tasks and virtual services support file publishing.
func (s *Server) coreDeleteServiceFiles(actor *db.User, id int64) error {
	if err := s.gateWrite(actor); err != nil {
		return err
	}
	svc, err := s.definitionService(actor, id)
	if err != nil {
		return err
	}

	path := serviceDefinitionPath(s.config.DataDir, svc)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return cerr(http.StatusInternalServerError, "failed to remove service file")
	}
	if svc.AppNonce != "" {
		s.mcpMounts.add(s, svc) // back to blank, still mounted
	} else {
		s.mcpMounts.remove(mcpSlug(svc.URL))
	}

	s.touchService(svc)
	s.audit(actor, "removed service files", svc.Name)
	return nil
}

// coreReadServiceFile reads all or part of a tasks/virtual service definition
// file. offset is a zero-based byte position; limit is the maximum bytes to
// return. A zero limit reads to the end of the file.
func (s *Server) coreReadServiceFile(actor *db.User, id int64, offset, limit int64) ([]byte, string, error) {
	svc, err := s.definitionService(actor, id)
	if err != nil {
		return nil, "", err
	}

	path := serviceDefinitionPath(s.config.DataDir, svc)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", cerr(http.StatusNotFound, "no service files uploaded")
		}
		return nil, "", cerr(http.StatusInternalServerError, "read failed: %v", err)
	}
	return sliceBytes(data, offset, limit), filepath.Base(path), nil
}

// coreStatServiceFile returns the size and sniffed content type of a
// tasks/virtual service definition file without reading the whole file.
// Symmetric to coreStatAppFile for the MCP service-file read paths.
func (s *Server) coreStatServiceFile(actor *db.User, id int64) (int64, string, error) {
	svc, err := s.definitionService(actor, id)
	if err != nil {
		return 0, "", err
	}
	size, ct, err := sniffFile(serviceDefinitionPath(s.config.DataDir, svc))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "", cerr(http.StatusNotFound, "no service files uploaded")
		}
		return 0, "", cerr(http.StatusInternalServerError, "stat failed: %v", err)
	}
	return size, ct, nil
}

// coreWriteServiceFile writes or patches a tasks/virtual service definition
// file. If oldText is empty the entire file is replaced. Otherwise the single
// occurrence of oldText in the existing file is replaced with data.
func (s *Server) coreWriteServiceFile(actor *db.User, id int64, data []byte, oldText string) error {
	if err := s.gateWrite(actor); err != nil {
		return err
	}
	svc, err := s.definitionService(actor, id)
	if err != nil {
		return err
	}

	path := serviceDefinitionPath(s.config.DataDir, svc)

	var newData []byte
	if oldText == "" {
		newData = data
	} else {
		existing, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return cerr(http.StatusNotFound, "file not found")
			}
			return cerr(http.StatusInternalServerError, "read failed: %v", err)
		}
		newData, err = replaceUniqueText(existing, []byte(oldText), data)
		if err != nil {
			return cerr(http.StatusBadRequest, "%v", err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return cerr(http.StatusInternalServerError, "failed to create service dir")
	}
	if err := os.WriteFile(path, newData, 0644); err != nil {
		return cerr(http.StatusInternalServerError, "write failed")
	}
	s.mcpMounts.add(s, svc)

	s.touchService(svc)
	s.audit(actor, "wrote service file", svc.Name)
	return nil
}

// coreListServiceFiles returns the tasks/virtual service definition file as a
// single-item listing. If search is non-empty, the file is only returned when
// its content contains the term (case-insensitive).
func (s *Server) coreListServiceFiles(actor *db.User, id int64, search string) ([]appFile, error) {
	svc, err := s.definitionService(actor, id)
	if err != nil {
		return nil, err
	}

	path := serviceDefinitionPath(s.config.DataDir, svc)
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []appFile{}, nil
		}
		return nil, cerr(http.StatusInternalServerError, "stat failed: %v", err)
	}
	if search != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, cerr(http.StatusInternalServerError, "read failed: %v", err)
		}
		if !strings.Contains(strings.ToLower(string(data)), strings.ToLower(search)) {
			return []appFile{}, nil
		}
	}
	return []appFile{{Path: filepath.Base(path), Size: fi.Size()}}, nil
}

// isHostedNonce reports whether nonce maps to a currently hosted app route.
func (s *Server) isHostedNonce(nonce string) bool {
	s.hostedMu.RLock()
	defer s.hostedMu.RUnlock()
	for _, ha := range s.hostedRoutes {
		if ha.nonce == nonce {
			return true
		}
	}
	for _, ha := range s.hostedHosts {
		if ha.nonce == nonce {
			return true
		}
	}
	return false
}
