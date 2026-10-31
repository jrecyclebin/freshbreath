package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/tailcat"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/utils"
)

// ── Remotes ─────────────────────────────────────────────────────────
//
// A remote is another Fresh Breath this one reaches through the remote's
// tailcat address, so the panel can drive it as an extension of this
// server. This server keeps only the address (sealed, and fixed for the
// remote's life: it is the remote's identity). Sessions on the remote
// belong to the browser, which keeps them at frbr:tunnel:<id>, and act on
// the remote as whichever of its users signed in. See
// design/remotes-todo.md.

// remoteFingerprint is a short tag for telling remotes apart without
// showing their address: the relay region and the start of the remote's
// path-discovery key, which travels in the clear anyway.
func remoteFingerprint(ci tailcat.ConnInfo) string {
	region := fmt.Sprintf("r%d", ci.RegionID)
	if len(ci.Region) > 0 && ci.Region[0].RegionCode != "" {
		region = ci.Region[0].RegionCode
	}
	disco := strings.TrimPrefix(ci.ServerDiscoPublic.String(), "discokey:")
	if len(disco) > 8 {
		disco = disco[:8]
	}
	return region + "-" + disco
}

func (s *Server) coreCreateRemote(actor *db.User, name, address string) (*db.Remote, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
	name, address = strings.TrimSpace(name), strings.TrimSpace(address)
	if name == "" {
		return nil, cerr(http.StatusBadRequest, "name required")
	}
	ci, err := tailcat.ParseAddr(tailcat.Addr(address))
	if err != nil {
		return nil, cerr(http.StatusBadRequest, "address is not a tailcat address: %v", err)
	}
	r := &db.Remote{
		ID:          utils.GenNonce(),
		Name:        name,
		Address:     address,
		Fingerprint: remoteFingerprint(ci),
		CreatedBy:   actor.ID,
	}
	if err := s.store.CreateRemote(r); err != nil {
		return nil, remoteStoreErr(err)
	}
	s.audit(actor, "added remote", r.Name+" ("+r.Fingerprint+")")
	return s.store.GetRemote(r.ID)
}

func remoteStoreErr(err error) error {
	if errors.Is(err, db.ErrRemoteNameTaken) {
		return cerr(http.StatusConflict, "%v", err)
	}
	return cerr(http.StatusInternalServerError, "%v", err)
}

func (s *Server) coreListRemotes(actor *db.User) ([]*db.Remote, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
	remotes, err := s.store.ListRemotes()
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	return remotes, nil
}

func (s *Server) coreRenameRemote(actor *db.User, id, name string) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if name = strings.TrimSpace(name); name == "" {
		return cerr(http.StatusBadRequest, "name required")
	}
	r, err := s.store.GetRemote(id)
	if err != nil {
		return cerr(http.StatusNotFound, "remote not found")
	}
	if err := s.store.RenameRemote(id, name); err != nil {
		return remoteStoreErr(err)
	}
	s.audit(actor, "renamed remote", r.Name+" → "+name)
	return nil
}

func (s *Server) coreDeleteRemote(actor *db.User, id string) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	r, err := s.store.GetRemote(id)
	if err != nil {
		return cerr(http.StatusNotFound, "remote not found")
	}
	if err := s.store.DeleteRemote(id); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.remoteConns.drop(id)
	s.audit(actor, "deleted remote", r.Name+" ("+r.Fingerprint+")")
	return nil
}

// handleRemotes serves GET (list) and POST {"name", "address"} (add) on
// /api/remotes.
func (s *Server) handleRemotes(w http.ResponseWriter, r *http.Request) {
	actor := actorFromContext(r)
	switch r.Method {
	case http.MethodGet:
		remotes, err := s.coreListRemotes(actor)
		if err != nil {
			writeErr(w, err)
			return
		}
		if remotes == nil {
			remotes = []*db.Remote{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"remotes": remotes})
	case http.MethodPost:
		var body struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, jsonBodyMaxBytes)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		remote, err := s.coreCreateRemote(actor, body.Name, body.Address)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(remote)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRemoteDetail serves /api/remotes/{id}: PUT {"name"} renames,
// DELETE removes. An address can't be changed — a new address is a new
// remote. Anything under /api/remotes/{id}/ goes to the remote.
func (s *Server) handleRemoteDetail(w http.ResponseWriter, r *http.Request) {
	id, rest, sub := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/remotes/"), "/")
	if id == "" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if sub {
		s.proxyRemote(w, r, id, rest)
		return
	}
	actor := actorFromContext(r)
	switch r.Method {
	case http.MethodPut:
		var body map[string]*string
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, jsonBodyMaxBytes)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if _, ok := body["address"]; ok {
			http.Error(w, "A remote's address can't change. Delete the remote and add it again (you'll sign in to it again too).", http.StatusBadRequest)
			return
		}
		if body["name"] == nil {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if err := s.coreRenameRemote(actor, id, *body["name"]); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := s.coreDeleteRemote(actor, id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── The proxy ───────────────────────────────────────────────────────

// remoteConns holds one open tailcat client per remote, made on first use
// and kept warm (bringing a tunnel up takes a relay round trip or two).
// open makes the client for an address; tests swap it for a dialer that
// reaches a test server instead.
type remoteConns struct {
	mu   sync.Mutex
	m    map[string]*remoteConn
	open func(address string) *remoteConn
}

type remoteConn struct {
	client *http.Client
	close  func()
}

// get returns the client for a remote, opening it the first time.
func (rc *remoteConns) get(r *db.Remote) *remoteConn {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if c, ok := rc.m[r.ID]; ok {
		return c
	}
	if rc.m == nil {
		rc.m = make(map[string]*remoteConn)
	}
	open := rc.open
	if open == nil {
		open = openTailcatConn
	}
	c := open(r.Address)
	rc.m[r.ID] = c
	return c
}

// drop closes and forgets a remote's client.
func (rc *remoteConns) drop(id string) {
	rc.mu.Lock()
	c, ok := rc.m[id]
	delete(rc.m, id)
	rc.mu.Unlock()
	if ok {
		c.close()
	}
}

// openTailcatConn is an HTTP client whose every connection is a TCP stream
// to port 80 at the remote's end of the tunnel, whatever host a request
// names. No overall timeout: event streams stay open.
func openTailcatConn(address string) *remoteConn {
	tc := tailcat.NewClient(tailcat.Addr(address))
	tc.Logf = func(string, ...any) {}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return tc.DialTCPPort(ctx, 80)
		},
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       2 * time.Minute,
	}
	return &remoteConn{
		client: &http.Client{Transport: tr},
		close: func() {
			tr.CloseIdleConnections()
			tc.Close()
		},
	}
}

// remoteHopHeaders are the request headers that stop at this server: the
// hop-by-hop set, and what belongs to this server's own session — its
// cookies, its bearer, the page's origin and app. The remote's token
// arrives as X-Tunnel-Token and leaves as the remote's Authorization.
var remoteHopHeaders = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true, "proxy-authorization": true,
	"te": true, "trailer": true, "transfer-encoding": true, "upgrade": true,
	"host": true, "content-length": true, "cookie": true, "authorization": true, "origin": true,
	"x-app-nonce": true, "x-tunnel-token": true, "x-api-key": true,
	"x-forwarded-for": true, "x-forwarded-proto": true, "x-forwarded-host": true,
}

// proxyRemote passes a request on to a remote: /api/remotes/{id}/{rest}
// becomes /{rest} there, as whichever of the remote's users the browser
// signed in as. Nothing is filtered by path: the remote decides what its
// user may do. Responses come back as they are (less cookies and CORS,
// which belong to this server), streamed, so event streams flow.
func (s *Server) proxyRemote(w http.ResponseWriter, r *http.Request, id, rest string) {
	remote, err := s.store.GetRemote(id)
	if err != nil {
		http.Error(w, "remote not found", http.StatusNotFound)
		return
	}
	target := "http://localhost/" + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad remote request", http.StatusBadRequest)
		return
	}
	out.ContentLength = r.ContentLength
	for k, v := range r.Header {
		if !remoteHopHeaders[strings.ToLower(k)] {
			out.Header[k] = v
		}
	}
	if tok := strings.TrimSpace(r.Header.Get("X-Tunnel-Token")); tok != "" {
		out.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(tok, "Bearer "))
	}

	resp, err := s.remoteConns.get(remote).client.Do(out)
	if err != nil {
		http.Error(w, fmt.Sprintf("Could not reach remote %q: %v", remote.Name, err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	s.auditRemoteCall(actorFromContext(r), remote, r.Method, rest, resp.StatusCode)

	copyHeaders(w.Header(), resp.Header)
	// Marks the answer as the remote's, so a client can tell the remote's
	// 401 (its session lapsed) from this server's (the panel's did).
	w.Header().Set("X-Frbr-Remote", "1")
	w.WriteHeader(resp.StatusCode)
	io.Copy(&flushWriter{w: w}, resp.Body)
}

// auditRemoteCall records what this server's users do on a remote, since
// the remote only sees the one user who signed in there. Reads and token
// refreshes are left out — the panel makes them constantly.
func (s *Server) auditRemoteCall(actor *db.User, remote *db.Remote, method, rest string, status int) {
	switch {
	case rest == "service/direct":
		s.audit(actor, "signed in to remote", fmt.Sprintf("%s (%d)", remote.Name, status))
	case method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions:
	case rest == "oauth/token" || strings.HasPrefix(rest, "oauth/token/"):
	default:
		s.audit(actor, "remote call", fmt.Sprintf("%s: %s /%s (%d)", remote.Name, method, rest, status))
	}
}
