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

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
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
// email and SSH key passphrase, which answers with a tunnel access token
// (15 minutes) and refresh token, the panel's own rhythm. Every other
// request must carry the access token as its bearer — no cookies, no
// OAuth redirects, no open pages. The session is a refresh family like any
// panel sign-in, so it is listed, revoked and ended by a passphrase reset
// the same way. Tunnel tokens are good only through the tunnel (the main
// listener refuses their audiences), and only for the address they were
// minted under: resetting the address signs everyone out.
// Inside, the request goes on as that user at the control panel's door,
// so the admin API, the central MCP and anything behind the panel's gate
// work as they would for them in the panel.

const (
	tunnelAudience        = "freshbreath-tunnel"
	tunnelRefreshAudience = "freshbreath-tunnel-refresh"
	tunnelStartWait       = 45 * time.Second
)

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
	pub  string // the server's node public key; tunnel tokens are bound to it
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
// first, so it comes back (if on) at a new address and every tunnel token
// minted under the old one stops working.
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
	s.tunnel.pub = id.Key.Public().String()
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
	s.tunnel.srv, s.tunnel.http, s.tunnel.addr, s.tunnel.pub = nil, nil, "", ""
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

// tunnelHandler fronts every request that comes through the tunnel: the
// sign-in, or a request carrying a tunnel token, which goes on to the
// usual handler as that user at the panel's door. Cookies are dropped —
// nothing through the tunnel rides a browser session.
func (s *Server) tunnelHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Cookie")
		if r.URL.Path == "/service/tunnel" {
			s.handleTunnelLogin(w, r)
			return
		}
		user, err := s.verifyTunnelToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="freshbreath-tunnel"`)
			http.Error(w, "Sign in through the tunnel first: POST /service/tunnel with {\"email\", \"passphrase\"}, then send the token as a Bearer. ("+err.Error()+")", http.StatusUnauthorized)
			return
		}
		// The panel's door takes its own kind of token; hand it one for
		// this user, minted per request and never seen by the client.
		r.Header.Del("Authorization")
		if rec, err := s.adminAuthRecord(); err == nil && rec != nil {
			tok, err := s.mintFreshbreathToken(subjectForUser(user), user.Email, user.Role, user.Name, rec.ID, nil, nil)
			if err != nil {
				http.Error(w, "Tunnel sign-in failed", http.StatusInternalServerError)
				return
			}
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		s.ServeHTTP(w, r)
	})
}

// handleTunnelLogin is the tunnel's only open door. An email and SSH key
// passphrase start a session; a refresh token continues one. Either way
// the answer is a fresh access and refresh token pair.
func (s *Server) handleTunnelLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, jsonBodyMaxBytes)
	var req struct {
		Email        string `json:"email"`
		Passphrase   string `json:"passphrase"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		(req.RefreshToken == "" && (req.Email == "" || req.Passphrase == "")) {
		http.Error(w, "email and passphrase (or refresh_token) required", http.StatusBadRequest)
		return
	}

	var family, jti string
	if req.RefreshToken != "" {
		var err error
		if family, jti, err = s.refreshTunnelSession(req.RefreshToken); err != nil {
			writeErr(w, err)
			return
		}
	} else {
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
		if s.tunnelPub() == "" {
			http.Error(w, "Tunnel sign-in failed: the tunnel is not running", http.StatusServiceUnavailable)
			return
		}
		if s.agentMgr != nil {
			if err := s.agentMgr.AddKey(user.ID, user.Metadata.SSHKey, req.Passphrase, sshAgentTTL); err != nil {
				log.Printf("agent add key for user %d: %v", user.ID, err)
			}
		}
		// The session is bound to the panel's record, as a panel sign-in
		// would be (none in setup mode).
		var authID int64
		if rec, err := s.adminAuthRecord(); err == nil && rec != nil {
			authID = rec.ID
		}
		if family, jti, err = s.newRefreshFamily(subjectForUser(user), authID, "tunnel"); err != nil {
			http.Error(w, "Tunnel sign-in failed", http.StatusInternalServerError)
			return
		}
		_ = s.store.LogAudit(user.ID, user.Email, "login", "tunnel passphrase")
	}

	access, refresh, err := s.mintTunnelTokens(family, jti)
	if err != nil {
		http.Error(w, "Tunnel sign-in failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    int(accessTokenTTL.Seconds()),
	})
}

// refreshTunnelSession spends a tunnel refresh token, rotating its family
// the way the OAuth refresh grant does: the current token moves the family
// on, the previous one is let through again within the grace window (a
// retry after a lost answer), and anything else is taken as theft and ends
// the session.
func (s *Server) refreshTunnelSession(raw string) (family, jti string, err error) {
	claims, fam, _, err := s.parseTunnelToken(raw, tunnelRefreshAudience)
	if err != nil {
		return "", "", cerr(http.StatusUnauthorized, "Invalid refresh token: %v — sign in again", err)
	}
	inGrace := claims.ID == fam.PrevJTI && !fam.RotatedAt.IsZero() && time.Since(fam.RotatedAt) <= refreshGraceWindow
	if claims.ID != fam.CurrentJTI && !inGrace {
		_ = s.store.RevokeRefreshFamily(fam.ID)
		return "", "", cerr(http.StatusUnauthorized, "Refresh token reuse detected — the session is ended, sign in again")
	}
	next := fam.CurrentJTI
	if !inGrace {
		next = newJTI()
		ok, err := s.store.RotateRefreshFamily(fam.ID, fam.CurrentJTI, next, time.Now())
		if err != nil {
			return "", "", cerr(http.StatusInternalServerError, "rotation failed")
		}
		if !ok {
			return "", "", cerr(http.StatusUnauthorized, "Refresh token was rotated elsewhere — sign in again")
		}
	}
	return fam.ID, next, nil
}

// tunnelClaims are carried by both kinds of tunnel token; the audience
// tells an access token from a refresh token.
type tunnelClaims struct {
	josejwt.Claims        // a refresh token's jti is Claims.ID
	Tunnel         string `json:"tun"` // the tunnel's node public key
	Family         string `json:"fam"` // the refresh family the session lives in
}

func (s *Server) tunnelPub() string {
	s.tunnel.mu.Lock()
	defer s.tunnel.mu.Unlock()
	return s.tunnel.pub
}

// mintTunnelTokens mints a session's access token and its refresh token
// (stamped with jti), both bound to the running tunnel's address.
func (s *Server) mintTunnelTokens(family, jti string) (access, refresh string, err error) {
	fam, ok, err := s.store.GetRefreshFamily(family)
	if err != nil || !ok {
		return "", "", fmt.Errorf("family %s: %v", family, err)
	}
	pub := s.tunnelPub()
	if pub == "" {
		return "", "", errors.New("tunnel is not running")
	}
	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: s.deriveSubkey(jwtSignLabel)},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	mint := func(aud, id string, ttl time.Duration) (string, error) {
		return josejwt.Signed(sig).Claims(tunnelClaims{
			Claims: josejwt.Claims{
				Issuer:   "freshbreath",
				Subject:  fam.Subject,
				Audience: josejwt.Audience{aud},
				ID:       id,
				IssuedAt: josejwt.NewNumericDate(now),
				Expiry:   josejwt.NewNumericDate(now.Add(ttl)),
			},
			Tunnel: pub,
			Family: family,
		}).Serialize()
	}
	if access, err = mint(tunnelAudience, "", accessTokenTTL); err != nil {
		return "", "", err
	}
	if refresh, err = mint(tunnelRefreshAudience, jti, refreshTokenTTL); err != nil {
		return "", "", err
	}
	return access, refresh, nil
}

// parseTunnelToken checks a tunnel token of the given audience: signed
// here, for this tunnel address, in a live session of an active user. The
// user and family are re-read, so a revoked session (sign-out, passphrase
// reset) or a deactivated account is out at once.
func (s *Server) parseTunnelToken(raw, audience string) (*tunnelClaims, *db.RefreshFamily, *db.User, error) {
	if raw == "" {
		return nil, nil, nil, errors.New("no token")
	}
	tok, err := josejwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		return nil, nil, nil, errors.New("not a tunnel token")
	}
	var claims tunnelClaims
	if err := tok.Claims(s.deriveSubkey(jwtSignLabel), &claims); err != nil {
		return nil, nil, nil, errors.New("not a tunnel token")
	}
	if err := claims.Claims.Validate(josejwt.Expected{
		Issuer:      "freshbreath",
		AnyAudience: josejwt.Audience{audience},
		Time:        time.Now(),
	}); err != nil {
		return nil, nil, nil, fmt.Errorf("token invalid: %w", err)
	}
	if pub := s.tunnelPub(); pub == "" || claims.Tunnel != pub {
		return nil, nil, nil, errors.New("token is for another tunnel address")
	}
	fam, ok, err := s.store.GetRefreshFamily(claims.Family)
	if err != nil || !ok || fam.Revoked || fam.Subject != claims.Subject || time.Now().After(fam.ExpiresAt) {
		return nil, nil, nil, errors.New("session has ended")
	}
	user, err := s.userFromSubject(claims.Subject)
	if err != nil || user == nil {
		return nil, nil, nil, errors.New("unknown user")
	}
	if user.Status != "Active" {
		return nil, nil, nil, errors.New("account is not active")
	}
	return &claims, fam, user, nil
}

// verifyTunnelToken checks a tunnel access token and returns its user.
func (s *Server) verifyTunnelToken(raw string) (*db.User, error) {
	_, _, user, err := s.parseTunnelToken(raw, tunnelAudience)
	return user, err
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
