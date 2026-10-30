package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"

	"poggers.institute/freshbreath/internal/db"
)

// ── The tailcat tunnel ──────────────────────────────────────────────
//
// A superuser can activate a tailcat address: anyone holding it reaches
// this server through a WireGuard tunnel, wherever the two machines are,
// with nothing exposed to the Internet. The address is a secret (it
// carries the tunnel's pre-shared key) and stays the same across restarts
// until it is reset.
//
// Through the tunnel there is one way in: POST /service/tunnel with an
// email and SSH key passphrase. It answers with the token pair a
// passphrase sign-in to the built-in SSH record gets — 15 minutes, then
// refreshed at /oauth/token like any other — so the same session works
// through the tunnel and, wherever that record is a gate, on the main
// listener. Every other request must carry an access token as its bearer:
// no cookies, no OAuth redirects, no open pages.
// Inside, the request goes on as that user at the control panel's door,
// so the admin API, the central MCP and anything behind the panel's gate
// work as they would for them in the panel.

const tunnelStartWait = 45 * time.Second

// tunnelIdentity is what keeps the tunnel's address the same across
// restarts, sealed into the tunnel_identity setting.
type tunnelIdentity struct {
	Key      key.NodePrivate      `json:"key"`
	PSK      tailcat.PresharedKey `json:"psk"`
	RegionID int                  `json:"region_id,omitempty"`
}

// tunnel is the running tailcat server, if any.
type tunnel struct {
	mu   sync.Mutex
	srv  *tailcat.Server
	http *http.Server
	addr string
}

type tunnelStatus struct {
	Enabled bool   `json:"enabled"`
	Address string `json:"address,omitempty"`
}

func (s *Server) tunnelStatus() tunnelStatus {
	s.tunnel.mu.Lock()
	defer s.tunnel.mu.Unlock()
	return tunnelStatus{Enabled: s.tunnel.srv != nil, Address: s.tunnel.addr}
}

// resumeTunnel brings the tunnel back up at boot if it was left on.
func (s *Server) resumeTunnel() {
	if on, _ := s.store.GetSetting("tunnel_enabled"); on != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), tunnelStartWait)
	defer cancel()
	if err := s.startTunnel(ctx); err != nil {
		log.Printf("tunnel: not started: %v", err)
	}
}

// coreSetTunnel turns the tunnel on or off. reset discards its identity
// first, so it comes back (if on) at a new address. Sessions signed in
// through the old address live on; ending those is the sessions list's job.
func (s *Server) coreSetTunnel(ctx context.Context, actor *db.User, enabled, reset bool) (tunnelStatus, error) {
	if err := s.gate(actor, rolesSuperuser); err != nil {
		return tunnelStatus{}, err
	}
	s.stopTunnel()
	if reset {
		if err := s.store.SetSetting("tunnel_identity", ""); err != nil {
			return tunnelStatus{}, cerr(http.StatusInternalServerError, "%v", err)
		}
	}
	flag := ""
	if enabled {
		flag = "1"
		ctx, cancel := context.WithTimeout(ctx, tunnelStartWait)
		defer cancel()
		if err := s.startTunnel(ctx); err != nil {
			return tunnelStatus{}, cerr(http.StatusBadGateway, "could not reach a tailcat relay: %v", err)
		}
	}
	if err := s.store.SetSetting("tunnel_enabled", flag); err != nil {
		return tunnelStatus{}, cerr(http.StatusInternalServerError, "%v", err)
	}
	what := "disabled tunnel"
	if enabled {
		what = "enabled tunnel"
	}
	if reset {
		what += " (new address)"
	}
	s.audit(actor, what, "")
	return s.tunnelStatus(), nil
}

// startTunnel starts the tailcat server under the stored identity (minting
// and storing one the first time) and serves HTTP on its port 80.
func (s *Server) startTunnel(ctx context.Context) error {
	s.tunnel.mu.Lock()
	defer s.tunnel.mu.Unlock()
	if s.tunnel.srv != nil {
		return nil
	}
	id, err := s.loadTunnelIdentity()
	if err != nil {
		return err
	}
	ts := &tailcat.Server{
		Key:          id.Key,
		PresharedKey: id.PSK,
		RegionID:     tailcfg.DERPRegionID(id.RegionID),
		Logf:         func(string, ...any) {}, // the relay chatter is no use to an operator
	}
	ln, err := ts.Listen(ctx, "tcp", ":80")
	if err != nil {
		ts.Close()
		return err
	}
	addr := ts.TailcatAddr()
	if id.RegionID == 0 {
		// Pin the relay the first start picked, so the address holds.
		if ci, err := tailcat.ParseAddr(addr); err == nil && len(ci.Region) > 0 {
			id.RegionID = int(ci.Region[0].RegionID)
			if err := s.storeTunnelIdentity(id); err != nil {
				log.Printf("tunnel: relay not pinned, the address may change on restart: %v", err)
			}
		}
	}
	hs := &http.Server{
		Handler:           s.tunnelHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("tunnel: %v", err)
		}
	}()
	s.tunnel.srv, s.tunnel.http, s.tunnel.addr = ts, hs, string(addr)
	return nil
}

func (s *Server) stopTunnel() {
	s.tunnel.mu.Lock()
	defer s.tunnel.mu.Unlock()
	if s.tunnel.srv == nil {
		return
	}
	s.tunnel.http.Close()
	s.tunnel.srv.Close()
	s.tunnel.srv, s.tunnel.http, s.tunnel.addr = nil, nil, ""
}

func (s *Server) loadTunnelIdentity() (tunnelIdentity, error) {
	var id tunnelIdentity
	if sealed, _ := s.store.GetSetting("tunnel_identity"); sealed != "" {
		plain, err := open(s.deriveSubkey(db.SealSubkeyLabel), sealed)
		if err != nil {
			return id, fmt.Errorf("unseal tunnel identity (reset the tunnel to start over): %w", err)
		}
		if err := json.Unmarshal(plain, &id); err != nil {
			return id, fmt.Errorf("read tunnel identity: %w", err)
		}
		return id, nil
	}
	id = tunnelIdentity{Key: key.NewNode(), PSK: tailcat.NewPresharedKey()}
	return id, s.storeTunnelIdentity(id)
}

func (s *Server) storeTunnelIdentity(id tunnelIdentity) error {
	plain, err := json.Marshal(id)
	if err != nil {
		return err
	}
	sealed, err := seal(s.deriveSubkey(db.SealSubkeyLabel), plain)
	if err != nil {
		return err
	}
	return s.store.SetSetting("tunnel_identity", sealed)
}

// tunnelHandler fronts every request that comes through the tunnel. Past
// the sign-in and the token refresh, a request must carry a Fresh Breath
// token bound to the panel's record or to the built-in SSH record (what a
// tunnel sign-in mints). The panel's own tokens go on as they are; an SSH
// token, when the panel is gated some other way, is exchanged for one at
// the panel's door — the passphrase is the only sign-in the tunnel can
// offer, so it stands in for whatever the panel asks. Cookies are dropped:
// nothing through the tunnel rides a browser session.
func (s *Server) tunnelHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Cookie")
		switch p := r.URL.Path; {
		case p == "/service/tunnel":
			s.handleTunnelLogin(w, r)
			return
		case p == "/oauth/token" || strings.HasPrefix(p, "/oauth/token/"):
			// A refresh is authorized by the refresh token it spends.
			s.ServeHTTP(w, r)
			return
		}
		if err := s.tunnelAuthorize(r); err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="freshbreath-tunnel"`)
			http.Error(w, "Sign in through the tunnel first: POST /service/tunnel with {\"email\", \"passphrase\"}, then send the access token as a Bearer. ("+err.Error()+")", http.StatusUnauthorized)
			return
		}
		s.ServeHTTP(w, r)
	})
}

// tunnelAuthorize checks a tunnel request's bearer and, for an SSH token
// while the panel is gated by another record, swaps in a token for the
// panel's record, minted per request and never seen by the client. It
// keeps the SSH binding as a leg, so services acting as the SSH record
// still get the caller's login.
func (s *Server) tunnelAuthorize(r *http.Request) error {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := s.verifyFreshbreathToken(raw)
	if err != nil || claims == nil {
		return errors.New("no valid token")
	}
	admin, err := s.adminAuthRecord()
	if err != nil {
		return err
	}
	if admin != nil && claims.boundTo(admin.ID) {
		return nil
	}
	sshID, err := s.store.BuiltinAuthID(db.AuthSSHKey)
	if err != nil || !claims.boundTo(sshID) {
		return errors.New("token is not for this server's SSH sign-in")
	}
	if admin == nil {
		return nil // setup mode: the panel's door is open anyway
	}
	user, err := s.userFromSubject(claims.Subject)
	if err != nil || user == nil {
		return errors.New("unknown user")
	}
	if user.Status != "Active" {
		return errors.New("account is not active")
	}
	legs := append([]int64{claims.AuthID}, claims.Legs...)
	tok, err := s.mintFreshbreathToken(claims.Subject, user.Email, user.Role, user.Name, admin.ID, legs, nil)
	if err != nil {
		return errors.New("tunnel sign-in failed")
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// handleTunnelLogin is the tunnel's sign-in: an email and SSH key
// passphrase in, the same token pair a passphrase sign-in to the built-in
// SSH record gets out. The access token lasts 15 minutes; the refresh
// token is spent at /oauth/token/{auth_id}.
func (s *Server) handleTunnelLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, jsonBodyMaxBytes)
	var req struct {
		Email      string `json:"email"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Passphrase == "" {
		http.Error(w, "email and passphrase required", http.StatusBadRequest)
		return
	}
	ip := s.requestIP(r)
	if s.loginFailures.blocked(ip, time.Now()) {
		tooManyLogins(w)
		return
	}
	// One answer for every failure, as on the sign-in form.
	user, err := s.store.GetUserByEmail(req.Email)
	if err != nil || user.Metadata == nil || user.Metadata.SSHKey == nil ||
		!verifyPassphraseBounded(r, user.Metadata.SSHKey, req.Passphrase) {
		s.loginFailures.fail(ip, time.Now())
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	if user.Status != "Active" {
		http.Error(w, "This account is "+strings.ToLower(user.Status)+" — ask an admin to activate it", http.StatusForbidden)
		return
	}
	if s.agentMgr != nil {
		if err := s.agentMgr.AddKey(user.ID, user.Metadata.SSHKey, req.Passphrase, sshAgentTTL); err != nil {
			log.Printf("agent add key for user %d: %v", user.ID, err)
		}
	}

	access, refresh, sshID, err := s.mintTunnelSession(user)
	if err != nil {
		log.Printf("tunnel sign-in for user %d: %v", user.ID, err)
		http.Error(w, "Tunnel sign-in failed", http.StatusInternalServerError)
		return
	}
	_ = s.store.LogAudit(user.ID, user.Email, "login", "tunnel passphrase")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    int(accessTokenTTL.Seconds()),
		"auth_id":       sshID,
	})
}

// mintTunnelSession starts a refresh family for user on the built-in SSH
// record and mints its first pair.
func (s *Server) mintTunnelSession(user *db.User) (access, refresh string, sshID int64, err error) {
	if sshID, err = s.store.BuiltinAuthID(db.AuthSSHKey); err != nil {
		return "", "", 0, err
	}
	subject := subjectForUser(user)
	if access, err = s.mintFreshbreathToken(subject, user.Email, user.Role, user.Name, sshID, nil, nil); err != nil {
		return "", "", 0, err
	}
	rd := freshbreathRefreshData{Subject: subject, UserEmail: user.Email, AuthID: sshID}
	if rd.FamilyID, rd.JTI, err = s.newRefreshFamily(subject, sshID, "tunnel"); err != nil {
		return "", "", 0, err
	}
	if refresh, err = s.mintRefreshToken(rd); err != nil {
		return "", "", 0, err
	}
	return access, refresh, sshID, nil
}

// handleTunnel is the panel's control: GET reports the tunnel, PUT
// {"enabled": bool, "reset": bool} changes it.
func (s *Server) handleTunnel(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.tunnelStatus())
	case http.MethodPut:
		var req struct {
			Enabled bool `json:"enabled"`
			Reset   bool `json:"reset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		st, err := s.coreSetTunnel(r.Context(), userFromContext(r.Context()), req.Enabled, req.Reset)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(st)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
