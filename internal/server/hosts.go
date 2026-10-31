package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/utils"
)

// ── Hosts ───────────────────────────────────────────────────────────
//
// The DNS names, besides its own, that this server answers to. Apps are
// published under them one level down (https://<host>/<slug>), and apps
// sharing a host share an origin, so they share creds on purpose. Admins
// keep the list, on the Remotes page. See design/hosts-todo.md.

// hostInput is everything about a host that can be set.
type hostInput struct {
	Name        string  `json:"name"`
	OwnerID     *int64  `json:"owner_id"`
	ProtectedBy *int64  `json:"protected_by"`
	DefaultApp  string  `json:"default_app"`
	Members     []int64 `json:"members"`
}

var hostLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// normalizeHostName cleans up a host name as typed: lowercase, no trailing
// dot. A URL's worth of extra (scheme, port, path) is refused rather than
// trimmed, so nobody thinks a port or path was kept.
func normalizeHostName(raw string) (string, error) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if name == "" {
		return "", cerr(http.StatusBadRequest, "host name required")
	}
	if strings.ContainsAny(name, ":/@") {
		return "", cerr(http.StatusBadRequest, "%q isn't a bare host name - leave out the scheme, port and path (like blog.example.com)", raw)
	}
	if net.ParseIP(name) != nil || name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return "", cerr(http.StatusBadRequest, "%q names a machine, not a host - IP addresses and localhost are always answered", raw)
	}
	if len(name) > 253 {
		return "", cerr(http.StatusBadRequest, "host name is too long")
	}
	for _, label := range strings.Split(name, ".") {
		if !hostLabelRe.MatchString(label) {
			return "", cerr(http.StatusBadRequest, "%q isn't a valid host name", raw)
		}
	}
	return name, nil
}

// checkHost validates in and turns it into the host to store over current
// (nil for a new host).
func (s *Server) checkHost(in hostInput, current *db.Host) (*db.Host, error) {
	name, err := normalizeHostName(in.Name)
	if err != nil {
		return nil, err
	}
	if s.knownHosts[name] {
		return nil, cerr(http.StatusBadRequest, "%s is this server's own name - the control panel lives there", name)
	}
	if in.OwnerID == nil {
		return nil, cerr(http.StatusBadRequest, "a host needs an owner")
	}
	var currentOwner *int64
	if current != nil {
		currentOwner = current.OwnerID
	}
	if err := s.validateOwner(in.OwnerID, currentOwner); err != nil {
		return nil, err
	}
	if err := s.validateAuthSlot("protected_by", in.ProtectedBy); err != nil {
		return nil, err
	}
	if in.DefaultApp != "" {
		app, err := s.store.GetApp(in.DefaultApp)
		if err != nil {
			return nil, cerr(http.StatusBadRequest, "the default app must be one of the apps on %s", name)
		}
		if host, slug := appHostMount(app); host != name || slug == "" {
			return nil, cerr(http.StatusBadRequest, "the default app must be one of the apps on %s", name)
		}
	}
	members, err := s.resolveHostMembers(in.Members)
	if err != nil {
		return nil, err
	}
	h := &db.Host{
		Name:        name,
		OwnerID:     in.OwnerID,
		ProtectedBy: in.ProtectedBy,
		DefaultApp:  in.DefaultApp,
		Members:     members,
	}
	if current != nil {
		h.ID, h.CreatedBy = current.ID, current.CreatedBy
	}
	return h, nil
}

// resolveHostMembers keeps the Member accounts from ids: they're the ones
// a host lets create apps. Admins can already, and Read-only accounts
// can't create anything, so both are pruned like any member list.
func (s *Server) resolveHostMembers(ids []int64) ([]int64, error) {
	kept, err := s.resolveMembers(ids)
	if err != nil {
		return nil, err
	}
	members := []int64{}
	for _, uid := range kept {
		if u, err := s.store.GetUser(uid); err == nil && u.Role == "Member" && !slices.Contains(members, uid) {
			members = append(members, uid)
		}
	}
	return members, nil
}

func hostStoreErr(err error) error {
	if errors.Is(err, db.ErrHostNameTaken) {
		return cerr(http.StatusConflict, "%v", err)
	}
	return cerr(http.StatusInternalServerError, "%v", err)
}

// appsOnHost names the apps whose URL is on host.
func (s *Server) appsOnHost(host string) ([]string, error) {
	apps, err := s.store.ListHostedApps()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, a := range apps {
		if appHostName(a) == host {
			names = append(names, a.Name)
		}
	}
	return names, nil
}

// coreListHosts lists every host for an Admin, and a Member's own hosts
// (the ones they can create apps on) for a Member.
func (s *Server) coreListHosts(actor *db.User) ([]*db.Host, error) {
	if err := s.gate(actor, rolesMemberPlus); err != nil {
		return nil, err
	}
	hosts, err := s.store.ListHosts()
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	out := []*db.Host{}
	for _, h := range hosts {
		if roleIn(actor.Role, rolesAdminPlus) || slices.Contains(h.Members, actor.ID) {
			out = append(out, h)
		}
	}
	return out, nil
}

// memberHostFor is the host a Member may create an app at url on: a listed
// host they're a member of, with the app one level down.
func (s *Server) memberHostFor(actor *db.User, url string) (*db.Host, error) {
	mine, err := s.coreListHosts(actor)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(mine))
	for i, h := range mine {
		names[i] = h.Name
	}
	if len(mine) == 0 {
		return nil, cerr(http.StatusForbidden, "forbidden: you aren't a member of any host, so you can't create apps")
	}
	host, slug := appHostMount(&db.App{URL: url})
	if slug != "" {
		for _, h := range mine {
			if strings.EqualFold(h.Name, host) {
				return h, nil
			}
		}
	}
	return nil, cerr(http.StatusForbidden, "forbidden: you can create apps only one level down on your hosts (%s), like https://%s/my-app", strings.Join(names, ", "), names[0])
}

func (s *Server) coreCreateHost(actor *db.User, in hostInput) (*db.Host, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
	h, err := s.checkHost(in, nil)
	if err != nil {
		return nil, err
	}
	h.ID, h.CreatedBy = utils.GenNonce(), actor.ID
	if err := s.store.CreateHost(h); err != nil {
		return nil, hostStoreErr(err)
	}
	s.rebuildHostedRoutes()
	s.audit(actor, "added host", h.Name)
	return s.store.GetHost(h.ID)
}

// coreUpdateHost rewrites a host whole. Renaming one that apps are on is
// refused: their URLs name it, and they'd quietly stop being served.
func (s *Server) coreUpdateHost(actor *db.User, id string, in hostInput) (*db.Host, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
	current, err := s.store.GetHost(id)
	if err != nil {
		return nil, cerr(http.StatusNotFound, "%v", err)
	}
	h, err := s.checkHost(in, current)
	if err != nil {
		return nil, err
	}
	if h.Name != current.Name {
		if err := s.refuseIfInUse(current.Name, "rename"); err != nil {
			return nil, err
		}
	}
	if err := s.store.UpdateHost(h); err != nil {
		return nil, hostStoreErr(err)
	}
	s.rebuildHostedRoutes()
	s.audit(actor, "updated host", h.Name)
	return s.store.GetHost(h.ID)
}

func (s *Server) coreDeleteHost(actor *db.User, id string) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	h, err := s.store.GetHost(id)
	if err != nil {
		return cerr(http.StatusNotFound, "%v", err)
	}
	if err := s.refuseIfInUse(h.Name, "delete"); err != nil {
		return err
	}
	if err := s.store.DeleteHost(id); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.rebuildHostedRoutes()
	s.audit(actor, "deleted host", h.Name)
	return nil
}

func (s *Server) refuseIfInUse(host, verb string) error {
	names, err := s.appsOnHost(host)
	if err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	if len(names) > 0 {
		return cerr(http.StatusConflict, "can't %s %s while apps are on it: %s", verb, host, strings.Join(names, ", "))
	}
	return nil
}

// ── HTTP ──

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	actor := actorFromContext(r)
	switch r.Method {
	case http.MethodGet:
		hosts, err := s.coreListHosts(actor)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"hosts": hosts})
	case http.MethodPost:
		var in hostInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, jsonBodyMaxBytes)).Decode(&in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		h, err := s.coreCreateHost(actor, in)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(h)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleHostDetail serves /api/hosts/{id}: PUT rewrites the host whole,
// DELETE removes it.
func (s *Server) handleHostDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/hosts/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	actor := actorFromContext(r)
	switch r.Method {
	case http.MethodPut:
		var in hostInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, jsonBodyMaxBytes)).Decode(&in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		h, err := s.coreUpdateHost(actor, id, in)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(h)
	case http.MethodDelete:
		if err := s.coreDeleteHost(actor, id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, fmt.Sprintf("Method %s not allowed", r.Method), http.StatusMethodNotAllowed)
	}
}
