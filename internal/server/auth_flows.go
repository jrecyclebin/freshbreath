package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
	"poggers.institute/freshbreath/internal/utils"
)

// ── Login legs ──────────────────────────────────────────────────────
//
// A login clears one or more auth records ("legs"): the inbound gate, plus
// the service's acts_as record when it is interactive and different from
// the gate — or, for an mcp service, whatever auth its MCP server demands. One pendingAuth walks the whole flow — each leg re-keys it
// under a fresh state — and the final leg mints one token carrying every
// cleared record.

// legsForLogin computes the records a login must clear, gate first. An mcp
// service ignores acts_as: its MCP server says what it wants, and an OAuth
// answer only works proxied — the upstream token stays server-side and the
// proxy injects it, so the browser never holds it.
func (s *Server) legsForLogin(ctx context.Context, gate *db.AuthRecord, svc *db.Service) ([]*db.AuthRecord, error) {
	var legs []*db.AuthRecord
	if gate != nil && gate.Kind != db.AuthAnonymous {
		legs = append(legs, gate)
	}
	if svc != nil && svc.Descriptor.Type == "mcp" {
		rec, err := s.mcpAuthRecord(ctx, svc)
		if err != nil {
			return nil, err
		}
		if rec != nil {
			if !svc.Descriptor.Proxied {
				return nil, fmt.Errorf("MCP server %q requires OAuth, which only works through the proxy — mark the service proxied", svc.Name)
			}
			legs = append(legs, rec)
		}
		return legs, nil
	}
	if svc != nil && svc.ActsAs != nil {
		rec, err := s.store.GetAuthRecord(*svc.ActsAs)
		if err != nil {
			return nil, err
		}
		if authInteractive(rec) && (gate == nil || rec.ID != gate.ID) {
			legs = append(legs, rec)
		}
	}
	return legs, nil
}

// legCovered reports whether an existing verified token already satisfies a
// leg: bound to the record, and carrying its provider credential when the
// kind is interactive.
func legCovered(claims *freshbreathClaims, rec *db.AuthRecord) bool {
	if claims == nil || !claims.boundTo(rec.ID) {
		return false
	}
	if authInteractive(rec) {
		_, ok := claims.Creds[authProvider(rec)]
		return ok
	}
	return true
}

// legFromClaims reconstructs a completed leg from an already-verified
// token, so a login that shares records with a previous one re-runs only
// its missing legs.
func (s *Server) legFromClaims(rec *db.AuthRecord, claims *freshbreathClaims) *completedLeg {
	leg := &completedLeg{rec: rec, email: claims.UserEmail, name: claims.UserName}
	if u, _ := s.userFromSubject(claims.Subject); u != nil {
		leg.user = u
	}
	if parts := strings.SplitN(claims.Subject, ":", 3); len(parts) == 3 && parts[0] == "ext" {
		leg.sub = parts[2]
	}
	if cred, ok := claims.Creds[authProvider(rec)]; ok {
		c := cred
		leg.upstream = &c
	}
	return leg
}

// beginLeg starts the flow for pending's current leg and returns the URL
// the user's browser should visit. The pending state is stored under a
// fresh key: the OAuth state for interactive kinds, a generated one for the
// local forms.
func (s *Server) beginLeg(ctx context.Context, p *pendingAuth) (string, error) {
	rec := p.current()
	redirectURI := s.config.PublicBaseURL + "/service/callback"
	switch rec.Kind {
	case db.AuthSSHKey:
		state := utils.GenNonce()
		s.putPending(state, p)
		return fmt.Sprintf("%s/service/ssh-auth?state=%s", s.config.PublicBaseURL, state), nil
	case db.AuthAPIKey:
		state := utils.GenNonce()
		s.putPending(state, p)
		return fmt.Sprintf("%s/service/apikey-auth?state=%s", s.config.PublicBaseURL, state), nil
	case db.AuthOIDC:
		authURL, state, verifier, oidcNonce, tokenURL, err := s.oidcBeginAuth(ctx, rec, redirectURI)
		if err != nil {
			return "", err
		}
		p.verifier, p.oidcNonce, p.tokenEndpoint = verifier, oidcNonce, tokenURL
		p.clientID, p.clientSecret = rec.Descriptor.ClientID, rec.Descriptor.ClientSecret
		s.putPending(state, p)
		return authURL, nil
	case db.AuthOAuth2, db.AuthMCP:
		authURL, clientID, clientSecret, tokenURL, state, verifier, err := s.oauth2BeginAuth(ctx, rec, redirectURI)
		if err != nil {
			return "", err
		}
		p.verifier, p.clientID, p.clientSecret, p.tokenEndpoint = verifier, clientID, clientSecret, tokenURL
		s.putPending(state, p)
		return authURL, nil
	}
	return "", fmt.Errorf("auth kind %q cannot start a login leg", rec.Kind)
}

// completeLeg advances a pending login past a cleared leg. It returns the
// URL the browser should move to next — the following leg, or back to the
// MCP client — or "" when the login finished as a browser flow and the
// postMessage page was written to w.
func (s *Server) completeLeg(w http.ResponseWriter, r *http.Request, p *pendingAuth, leg *completedLeg) (string, error) {
	p.done = append(p.done, leg)
	p.at++
	if p.at < len(p.legs) {
		return s.beginLeg(r.Context(), p)
	}
	return s.finishLogin(w, r, p)
}

// appNeedsClientKey reports whether a browser app must hold raw api_key
// material: only when it is allowed a non-proxied service. The admin door
// never does. Errors fail closed (no key handoff) — a token still works
// for every proxied path.
func (s *Server) appNeedsClientKey(appNonce string) bool {
	if appNonce == "" || appNonce == s.adminNonce {
		return false
	}
	direct, err := s.store.AppLinksDirectService(appNonce)
	if err != nil {
		log.Printf("app %s: check direct services: %v", appNonce, err)
		return false
	}
	return direct
}

// finishLogin mints the token for a fully-cleared pending login and hands
// it off: to the MCP client via a one-shot code, or to the opener via the
// postMessage page (with the refresh cookie set alongside).
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, p *pendingAuth) (string, error) {
	// Pure api_key browser logins hand the key itself to the browser —
	// but only when the app actually calls some service directly. An app
	// whose linked services are all proxied (or the admin door) gets a
	// standard token instead, so the stored key never leaves the server;
	// the proxy injects it upstream (FRBR-7).
	if p.mcpKey == "" && len(p.done) == 1 && p.done[0].rec.Kind == db.AuthAPIKey && s.appNeedsClientKey(p.appNonce) {
		rec := p.done[0].rec
		// No token, so no refresh family of its own — but the gate pass
		// needs one to be revocable, so the key handoff gets one too.
		subject, _ := s.legIdentity(p.done[0])
		familyID, _, err := s.newRefreshFamily(subject, rec.ID, deviceLabelFromUA(r.UserAgent()))
		if err != nil {
			return "", err
		}
		s.grantGatePass(w, r, subject, familyID, []int64{rec.ID})
		writeCallbackPage(w, p, map[string]interface{}{
			"v": 1, "auth_id": rec.ID, "kind": rec.Kind,
			"key": p.done[0].presentedKey, "header": rec.Descriptor.Header,
		})
		return "", nil
	}

	token, rd, err := s.mintForLegs(p.done, p.primaryID)
	if err != nil {
		return "", err
	}

	// MCP flow: park the finished token on the client's pending auth and
	// send the browser back with a one-shot code.
	if p.mcpKey != "" {
		mcp, ok, _ := s.getMCPPending(p.mcpKey)
		if !ok {
			return "", fmt.Errorf("MCP auth session expired")
		}
		mcp.fbToken = token
		mcp.refreshData = rd
		code := s.oauthSrv.issueCode(mcp)
		return fmt.Sprintf("%s?code=%s&state=%s", mcp.redirectURI, code, mcp.state), nil
	}

	// Browser flow: refresh family + HttpOnly cookie, then the store entry
	// via postMessage.
	familyID, jti, err := s.newRefreshFamily(rd.Subject, rd.AuthID, deviceLabelFromUA(r.UserAgent()))
	if err != nil {
		log.Printf("create refresh family: %v", err)
	} else {
		rd.FamilyID, rd.JTI = familyID, jti
	}
	if _, err := s.makeRefreshCookie(w, r, rd); err != nil {
		return "", err
	}
	if rd.FamilyID != "" {
		s.grantGatePass(w, r, rd.Subject, rd.FamilyID, append([]int64{rd.AuthID}, rd.Legs...))
	}

	primary := p.done[len(p.done)-1].rec
	for _, leg := range p.done {
		if leg.rec.ID == rd.AuthID {
			primary = leg.rec
		}
	}
	entry := map[string]interface{}{
		"v":            1,
		"auth_id":      rd.AuthID,
		"kind":         primary.Kind,
		"provider":     authProvider(primary),
		"access_token": token,
		"token_type":   "Bearer",
		"expires_at":   time.Now().Add(accessTokenTTL).UTC().Format(time.RFC3339),
		"subject":      rd.Subject,
	}
	if len(rd.Legs) > 0 {
		entry["legs"] = rd.Legs
	}
	writeCallbackPage(w, p, entry)
	return "", nil
}

// writeCallbackPage hands a finished browser login back to the app.
// entry is the frbr:auth:* store record; frbr.js stamps written_at and
// persists it. Correlation is by state; the listener pins the origin.
//
// A popup flow has an opener to postMessage to. A top-level flow — the
// auto-login redirect — does not, so the page writes the store itself
// and bounces back to returnTo; both only work on this origin, which is
// why frbr.js only starts the flow same-origin. (The page writing the
// store bends "frbr.js is the sole writer" — but this page is server
// kin, not app code.)
func writeCallbackPage(w io.Writer, p *pendingAuth, entry map[string]interface{}) {
	entryJSON, _ := json.Marshal(entry)
	lead := "Logged in. You can close this window."
	redirect := ""
	if p.returnTo != "" {
		lead = "Logged in. Returning to the app…"
		redirect = fmt.Sprintf("window.location.replace(%q);", p.returnTo)
	}
	fmt.Fprintf(w, `<!doctype html>
<html>
<body>
  <p>%s</p>
  <script>
    (function () {
      var entry = %s;
      if (window.opener && !window.opener.closed) {
        window.opener.postMessage({
          type: "auth-complete",
          state: %q,
          entry: entry,
        }, "*");
        return;
      }
      entry.written_at = new Date().toISOString();
      try {
        localStorage.setItem("frbr:auth:" + entry.auth_id, JSON.stringify(entry));
      } catch (e) {
        document.querySelector("p").textContent =
          "Logged in, but the browser would not store the session.";
        return;
      }
      %s
    })();
  </script>
</body>
</html>
`, lead, string(entryJSON), p.appState, redirect)
}

// returnAllowed judges a login's return target the way the CORS check
// judges an Origin: a relative path is this origin's own, and an absolute
// URL only the app's registered one. Anything else — another scheme,
// another host, a protocol-relative trick — stays out, so a login URL
// can't be aimed at some origin that has nothing to do with the app.
func returnAllowed(app *db.App, ret string) bool {
	if strings.HasPrefix(ret, "/") && !strings.HasPrefix(ret, "//") {
		return true
	}
	u, err := url.Parse(ret)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	return u.Scheme+"://"+u.Host == appRegisteredOrigin(app)
}

// ── The gate pass ───────────────────────────────────────────────────
//
// A signed cookie listing the inbound records this browser has cleared,
// each paired with the refresh family its login created. It lets the page
// handlers (hosted apps, the control panel) enforce a gate before serving
// anything. It holds no credential — tokens stay in the client's store —
// and it is a view pass only: no API route may read it, or it becomes an
// ambient credential open to CSRF.
//
// Liveness comes from the family, not the cookie: a signed-out, revoked or
// expired family voids its grants at once, and a deleted user voids them
// all. The families table holds JTIs, never credentials.

const (
	gatePassCookie = "frbr_gate"
	gatePassLabel  = "freshbreath/gate-pass"
)

type gatePass struct {
	Subject string      `json:"sub"`
	Grants  []gateGrant `json:"grants"`
}

type gateGrant struct {
	AuthID   int64  `json:"a"`
	FamilyID string `json:"f"`
}

func (s *Server) signGatePass(payload string) string {
	mac := hmac.New(sha256.New, s.deriveSubkey(gatePassLabel))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// readGatePass returns the request's gate pass, or nil when there is none
// or its signature doesn't hold.
func (s *Server) readGatePass(r *http.Request) *gatePass {
	c, err := r.Cookie(gatePassCookie)
	if err != nil {
		return nil
	}
	payload, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.signGatePass(payload))) {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	var pass gatePass
	if json.Unmarshal(raw, &pass) != nil {
		return nil
	}
	return &pass
}

// writeGatePass sets the cookie, or clears it when no grants remain.
// SameSite=Lax: the pass must ride a top-level navigation in from another
// site, and nothing else cross-site needs it.
func (s *Server) writeGatePass(w http.ResponseWriter, r *http.Request, pass *gatePass) {
	cookie := &http.Cookie{
		Name:     gatePassCookie,
		Path:     "/",
		HttpOnly: true,
		Secure:   schemeOf(r) == "https" || s.config.TLSCertFile != "",
		SameSite: http.SameSiteLaxMode,
	}
	if pass == nil || len(pass.Grants) == 0 {
		cookie.MaxAge = -1
	} else {
		raw, _ := json.Marshal(pass)
		payload := base64.RawURLEncoding.EncodeToString(raw)
		cookie.Value = payload + "." + s.signGatePass(payload)
		cookie.MaxAge = int(refreshTokenTTL.Seconds())
	}
	http.SetCookie(w, cookie)
}

// grantGatePass records a finished browser login on the pass: each cleared
// record, under the login's family. A different subject starts a fresh
// pass — one browser, one person's grants.
func (s *Server) grantGatePass(w http.ResponseWriter, r *http.Request, subject, familyID string, authIDs []int64) {
	pass := s.readGatePass(r)
	if pass == nil || pass.Subject != subject {
		pass = &gatePass{Subject: subject}
	}
	kept := pass.Grants[:0]
	for _, g := range pass.Grants {
		if !slices.Contains(authIDs, g.AuthID) {
			kept = append(kept, g)
		}
	}
	for _, id := range authIDs {
		kept = append(kept, gateGrant{AuthID: id, FamilyID: familyID})
	}
	pass.Grants = kept
	s.writeGatePass(w, r, pass)
}

// gatePassUser checks the request's pass against a gate. ok reports a live
// grant for the record; user is the subject's row (nil for ext: subjects).
func (s *Server) gatePassUser(r *http.Request, gate *db.AuthRecord) (user *db.User, ok bool) {
	pass := s.readGatePass(r)
	if pass == nil {
		return nil, false
	}
	user, err := s.userFromSubject(pass.Subject)
	if err != nil {
		return nil, false // a frbr: subject whose user is gone
	}
	for _, g := range pass.Grants {
		if g.AuthID != gate.ID {
			continue
		}
		fam, found, err := s.store.GetRefreshFamily(g.FamilyID)
		if err == nil && found && !fam.Revoked && fam.ExpiresAt.After(time.Now()) && fam.Subject == pass.Subject {
			return user, true
		}
	}
	return nil, false
}

// passPageGate decides whether a page request may be served behind gate.
// It writes the response itself when not: a navigation is sent into the
// login, which comes back here with the pass set; anything else (an asset,
// a fetch) gets a 401, since redirecting a subresource into a login helps
// no one. The control panel additionally wants a real user, as its API
// does.
func (s *Server) passPageGate(w http.ResponseWriter, r *http.Request, appNonce string, gate *db.AuthRecord) bool {
	if gateIsOpen(gate) {
		return true
	}
	user, ok := s.gatePassUser(r, gate)
	if ok && (appNonce != s.adminNonce || user != nil) {
		return true
	}
	if ok {
		gatePageError(w, http.StatusForbidden, "Not a Fresh Breath user",
			"You signed in, but that account has no Fresh Breath user. Ask an admin to add you.")
		return false
	}
	if !isNavigation(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	// A login that just finished and still left no pass means the browser
	// isn't keeping the cookie. Sending it round again would loop forever.
	if cameFromLogin(r) {
		gatePageError(w, http.StatusUnauthorized, "Sign-in didn't stick",
			"You signed in, but this browser didn't keep the sign-in cookie. Check that cookies are allowed for this site.")
		return false
	}

	legs, err := s.legsForLogin(r.Context(), gate, nil)
	if err != nil || len(legs) == 0 {
		http.Error(w, fmt.Sprintf("Gate resolution failed: %v", err), http.StatusInternalServerError)
		return false
	}
	p := &pendingAuth{
		appNonce:  appNonce,
		appState:  utils.GenNonce(),
		returnTo:  r.URL.RequestURI(),
		legs:      legs,
		primaryID: legs[len(legs)-1].ID,
	}
	next, err := s.beginLeg(r.Context(), p)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to begin auth: %v", err), http.StatusInternalServerError)
		return false
	}
	http.Redirect(w, r, next, http.StatusFound)
	return false
}

// isNavigation reports whether a request is the browser loading a page, as
// opposed to a subresource or a script's fetch.
func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// cameFromLogin reports whether a request was sent by one of our own login
// pages — the callback bouncing back, or a form that finished in place.
func cameFromLogin(r *http.Request) bool {
	ref, err := url.Parse(r.Header.Get("Referer"))
	if err != nil || ref.Host != r.Host {
		return false
	}
	switch ref.Path {
	case "/service/callback", "/service/ssh-auth", "/service/apikey-auth":
		return true
	}
	return false
}

func gatePageError(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	title, detail = html.EscapeString(title), html.EscapeString(detail)
	io.WriteString(w, `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Fresh Breath — `+title+`</title>
<style>`+authFormStyle+`</style></head><body>
<div class="card"><h1>`+title+`</h1><p class="lead">`+detail+`</p></div>
</body></html>`)
}

// handleLogout signs this browser out of one record: every family that
// cleared it is revoked, and every grant riding those families leaves the
// pass. The refresh cookies die with their families.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	authID, err := strconv.ParseInt(r.URL.Query().Get("auth_id"), 10, 64)
	if err != nil {
		http.Error(w, "auth_id required", http.StatusBadRequest)
		return
	}
	pass := s.readGatePass(r)
	if pass == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var ended []string
	for _, g := range pass.Grants {
		if g.AuthID == authID && !slices.Contains(ended, g.FamilyID) {
			if err := s.store.RevokeRefreshFamily(g.FamilyID); err != nil {
				log.Printf("logout: revoke family: %v", err)
			}
			ended = append(ended, g.FamilyID)
		}
	}
	pass.Grants = slices.DeleteFunc(pass.Grants, func(g gateGrant) bool {
		return slices.Contains(ended, g.FamilyID)
	})
	s.writeGatePass(w, r, pass)
	w.WriteHeader(http.StatusNoContent)
}

// ── /service/login ──────────────────────────────────────────────

// serviceInfo is the service metadata a caller needs to build a proxy
// around what it just logged in to. Every /service/login answer carries it
// when a url was named, so the browser never has to ask twice.
func serviceInfo(svc *db.Service) map[string]interface{} {
	if svc == nil {
		return nil
	}
	return map[string]interface{}{
		"id": svc.ID, "url": svc.URL,
		"proxied": svc.Descriptor.Proxied, "type": svc.Descriptor.Type,
	}
}

// legInfo describes the records a login must clear, in order.
func legInfo(legs []*db.AuthRecord) []map[string]interface{} {
	out := make([]map[string]interface{}, len(legs))
	for i, rec := range legs {
		out[i] = map[string]interface{}{
			"auth_id": rec.ID, "kind": rec.Kind,
			"provider": authProvider(rec), "name": rec.Name,
		}
	}
	return out
}

// handleLogin answers two questions at one route.
//
// With resolve=1 it is a pure query: what would a login to this door cost?
// The answer is "anonymous" (nothing) or "legs" (these records, in this
// order) — no flow starts, no state is stored. That is how the browser
// learns which store entries are worth presenting, since it cannot know a
// door's gate before asking.
//
// Without it, the caller is committing: it presents whatever bearer it
// found and gets "anonymous", "ok" (that bearer already covers every leg),
// or a redirect into the first leg it does not. A bearer covering some legs
// runs only the rest.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resolveOnly := r.URL.Query().Get("resolve") != ""
	appState := r.URL.Query().Get("state")
	if appState == "" && !resolveOnly {
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

	// Resolve the gate for this door: the admin record for the control
	// panel's ephemeral nonce, the app's protected_by otherwise.
	var gate *db.AuthRecord
	var app *db.App
	var err error
	isAdmin := nonce == s.adminNonce
	if isAdmin {
		gate, err = s.adminAuthRecord()
	} else {
		app, err = s.store.GetApp(nonce)
		if err != nil {
			http.Error(w, "Unknown app nonce", http.StatusUnauthorized)
			return
		}
		gate, err = s.resolveAppGate(app)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Gate resolution failed: %v", err), http.StatusInternalServerError)
		return
	}

	// An optional url names a service; its acts_as may add a leg. The gate
	// stays the app's — a service reached through an app answers to the
	// app's door.
	var svc *db.Service
	if serviceURL := r.URL.Query().Get("url"); serviceURL != "" {
		if isAdmin {
			http.Error(w, "The control panel logs in to its gate, not to services", http.StatusForbidden)
			return
		}
		svc, err = s.store.GetServiceByURL(serviceURL)
		if err != nil {
			http.Error(w, "Service not registered", http.StatusForbidden)
			return
		}
		allowed, err := s.store.IsServiceAllowedForApp(nonce, svc.ID)
		if err != nil || !allowed {
			http.Error(w, "Service not approved for this app", http.StatusForbidden)
			return
		}
	}

	legs, err := s.legsForLogin(r.Context(), gate, svc)
	if err != nil {
		http.Error(w, fmt.Sprintf("Legs resolution failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if len(legs) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "anonymous", "service": serviceInfo(svc),
		})
		return
	}

	if resolveOnly {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "legs", "legs": legInfo(legs), "service": serviceInfo(svc),
		})
		return
	}

	// A presented bearer may cover some or all legs.
	var claims *freshbreathClaims
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		claims, _ = s.verifyFreshbreathToken(strings.TrimPrefix(ah, "Bearer "))
	}
	var done []*completedLeg
	var todo []*db.AuthRecord
	for _, rec := range legs {
		if legCovered(claims, rec) {
			done = append(done, s.legFromClaims(rec, claims))
		} else {
			todo = append(todo, rec)
		}
	}
	if len(todo) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "ok", "service": serviceInfo(svc),
		})
		return
	}

	// A return path sends a finished top-level login back into the app
	// that started it — the leg the auto-login redirect plays.
	ret := r.URL.Query().Get("return")
	if ret != "" && !returnAllowed(app, ret) {
		http.Error(w, "Invalid return path", http.StatusBadRequest)
		return
	}

	p := &pendingAuth{
		appNonce:  nonce,
		appState:  appState,
		returnTo:  ret,
		legs:      todo,
		done:      done,
		primaryID: legs[len(legs)-1].ID,
	}

	url, err := s.beginLeg(r.Context(), p)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to begin auth: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"type":    "redirect",
		"url":     url,
		"legs":    legInfo(legs),
		"service": serviceInfo(svc),
	})
}

// ── /service/callback ───────────────────────────────────────────────

// handleCallback receives the upstream provider redirect for an
// interactive leg, exchanges the code, and advances the pending login.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "Missing code or state", http.StatusBadRequest)
		return
	}

	p, ok, expired := s.getPending(state)
	if !ok && expired {
		http.Error(w, "Auth state expired — restart the login flow", http.StatusBadRequest)
		return
	}
	if !ok {
		http.Error(w, "Unknown auth state", http.StatusBadRequest)
		return
	}
	rec := p.current()
	if !authInteractive(rec) {
		http.Error(w, "Unexpected callback for this auth kind", http.StatusBadRequest)
		return
	}

	redirectURI := s.config.PublicBaseURL + "/service/callback"
	var claims *OIDCClaims
	var accessToken, refreshToken string
	var err error
	switch rec.Kind {
	case db.AuthOIDC:
		claims, accessToken, refreshToken, err = s.oidcExchangeCode(r.Context(), rec, code, p.verifier, p.oidcNonce, redirectURI)
	case db.AuthMCP:
		// No identity to learn: a random subject keeps each MCP-only
		// login its own session rather than one shared by every visitor.
		claims = &OIDCClaims{Subject: utils.GenNonce()}
		accessToken, refreshToken, err = s.mcpExchangeCode(r.Context(), rec, code, p.verifier, redirectURI)
	default:
		claims, accessToken, refreshToken, err = s.oauth2ExchangeCode(r.Context(), rec, p.tokenEndpoint, code, p.verifier, p.clientID, p.clientSecret, redirectURI)
	}
	if err != nil {
		s.callbackError(w, r, rec, "Code exchange failed", err)
		return
	}

	leg := &completedLeg{
		rec:   rec,
		email: claims.Email,
		name:  claims.Name,
		sub:   claims.Subject,
		upstream: &sealedUpstreamData{
			UpstreamToken:    accessToken,
			UpstreamRefresh:  refreshToken,
			UpstreamTokenURL: p.tokenEndpoint,
			UpstreamScopes:   rec.Descriptor.Scopes,
		},
	}

	next, err := s.completeLeg(w, r, p, leg)
	if err != nil {
		s.callbackError(w, r, rec, "Login completion failed", err)
		return
	}
	if next != "" {
		http.Redirect(w, r, next, http.StatusFound)
	}
}

// ── Local credential forms ──────────────────────────────────────────
//
// The ssh-auth and apikey-auth forms serve any leg of any flow. Their POST
// responses are uniform: JSON {"redirect": url} when the browser should
// move on (next leg, MCP client), else the final postMessage page as HTML —
// the form JS navigates or document.writes accordingly.

// oauthHint maps common RFC 6749 token-endpoint error codes to a
// plain-language hint for the callback error page.
func oauthHint(ierr *oauth2.RetrieveError) string {
	switch ierr.ErrorCode {
	case "invalid_client":
		return "The provider rejected the client credentials — the client ID or client secret configured for this service is likely wrong."
	case "invalid_grant":
		return "The authorization code was rejected — it may have expired, already been used, or not match this redirect URI / client."
	case "redirect_uri_mismatch":
		return "The provider says the redirect URI does not match one registered for this client."
	case "unauthorized_client":
		return "The client is not authorized to use this grant type."
	case "invalid_scope":
		return "The provider rejected one of the requested scopes."
	}
	return ""
}

// callbackError renders the auth-form-styled error page for a failed
// callback. Errors are HTML-escaped; a nested oauth2.RetrieveError gets an
// operator hint appended.
func (s *Server) callbackError(w http.ResponseWriter, r *http.Request, rec *db.AuthRecord, title string, err error) {
	hint := ""
	var rerr *oauth2.RetrieveError
	if errors.As(err, &rerr) {
		hint = oauthHint(rerr)
	}

	log.Printf("callback (%s) %s: %v", rec.Name, title, err)

	page := callbackErrorHTML
	page = strings.Replace(page, "{{TITLE}}", html.EscapeString(title), 1)
	page = strings.Replace(page, "{{DETAIL}}", html.EscapeString(err.Error()), 1)
	page = strings.Replace(page, "{{HINT}}", html.EscapeString(hint), 1)

	// Collapse the hint block when there is nothing actionable to show.
	if hint == "" {
		page = strings.Replace(page, "<div class=\"hint\">", "<div class=\"hint\" style=\"display:none\">", 1)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte(page))
}

const callbackErrorHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Fresh Breath — Sign-in error</title>
<style>` + authFormStyle + `
  .detail{color:#a1a1aa;font-size:13px;line-height:1.6;word-break:break-word;background:#0f0f11;border:1px solid #27272a;border-radius:8px;padding:12px;margin-top:12px}
  .hint{color:#fbbf24;font-size:13px;line-height:1.6;margin-top:16px;background:rgba(99,102,241,.08);border:1px solid #27272a;border-radius:8px;padding:12px}
  .hint strong{display:block;color:#fbbf24;margin-bottom:4px}
</style></head><body>
<div class="card">
  <h1>{{TITLE}}</h1>
  <p class="lead">The sign-in flow for this service could not continue. Details below for the server log.</p>
  <div class="detail">{{DETAIL}}</div>
  <div class="hint">
    <strong>Likely cause</strong>
    {{HINT}}
  </div>
</div>
</body></html>`

// respondLeg completes a form-cleared leg and writes the right response:
// JSON {"redirect"} to move the browser on, or the final page as HTML.
func (s *Server) respondLeg(w http.ResponseWriter, r *http.Request, p *pendingAuth, leg *completedLeg) {
	next, err := s.completeLeg(&htmlOnFirstWrite{w: w}, r, p, leg)
	if err != nil {
		http.Error(w, fmt.Sprintf("Login completion failed: %v", err), http.StatusInternalServerError)
		return
	}
	if next != "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"redirect": next})
	}
}

// htmlOnFirstWrite stamps a text/html content type the moment a body write
// happens, leaving redirect-only completions free to answer JSON.
type htmlOnFirstWrite struct {
	w       http.ResponseWriter
	started bool
}

func (h *htmlOnFirstWrite) Header() http.Header { return h.w.Header() }
func (h *htmlOnFirstWrite) WriteHeader(code int) {
	h.started = true
	h.w.WriteHeader(code)
}
func (h *htmlOnFirstWrite) Write(p []byte) (int, error) {
	if !h.started {
		h.w.Header().Set("Content-Type", "text/html; charset=utf-8")
		h.started = true
	}
	return h.w.Write(p)
}

// handleSSHAuth is the passphrase login form: the browser face of an
// ssh_key auth record. GET renders; POST verifies the passphrase against
// the user's stored SSH key and advances the flow.
func (s *Server) handleSSHAuth(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state := r.URL.Query().Get("state")
		if state == "" {
			http.Error(w, "Missing state parameter", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(strings.Replace(sshAuthFormHTML, "{{STATE}}", state, 1)))

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

		// Kept alive until its TTL so a mistyped passphrase (or a
		// back-button revisit) can be retried.
		p, ok, expired := s.getPending(req.State)
		ok = ok && p.at < len(p.legs) && p.current().Kind == db.AuthSSHKey
		if !ok && expired {
			http.Error(w, "Auth state expired — restart the login flow", http.StatusBadRequest)
			return
		}
		if !ok {
			http.Error(w, "Unknown auth state", http.StatusBadRequest)
			return
		}

		user, err := s.store.GetUserByEmail(req.Email)
		if err != nil {
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}
		if user.Metadata == nil || user.Metadata.SSHKey == nil {
			http.Error(w, "No SSH key configured for this user", http.StatusUnauthorized)
			return
		}
		if !sshkit.VerifyPassphrase(user.Metadata.SSHKey, req.Passphrase) {
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}
		// Checked after the passphrase so an account's status isn't
		// readable by anyone who knows its email.
		if user.Status != "Active" {
			http.Error(w, "This account is "+strings.ToLower(user.Status)+" — ask an admin to activate it", http.StatusForbidden)
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
		_ = s.store.LogAudit(user.ID, req.Email, "login", "passphrase")

		s.respondLeg(w, r, p, &completedLeg{rec: p.current(), user: user})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAPIKeyAuth is the key-entry form: the browser face of an api_key
// gate. The typed key must match the record's stored key.
func (s *Server) handleAPIKeyAuth(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state := r.URL.Query().Get("state")
		if state == "" {
			http.Error(w, "Missing state parameter", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(strings.Replace(apiKeyAuthFormHTML, "{{STATE}}", state, 1)))

	case http.MethodPost:
		var req struct {
			State  string `json:"state"`
			APIKey string `json:"api_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if req.State == "" || req.APIKey == "" {
			http.Error(w, "state and api_key required", http.StatusBadRequest)
			return
		}

		p, ok, expired := s.getPending(req.State)
		ok = ok && p.at < len(p.legs) && p.current().Kind == db.AuthAPIKey
		if !ok && expired {
			http.Error(w, "Auth state expired — restart the login flow", http.StatusBadRequest)
			return
		}
		if !ok {
			http.Error(w, "Unknown auth state", http.StatusBadRequest)
			return
		}

		rec := p.current()
		if rec.Descriptor.Key == "" || !keysEqual(req.APIKey, rec.Descriptor.Key) {
			http.Error(w, "Invalid API key", http.StatusUnauthorized)
			return
		}

		s.respondLeg(w, r, p, &completedLeg{rec: rec, presentedKey: req.APIKey})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

const authFormStyle = `
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
`

// authFormScript is shared by both forms: POST the credentials, then either
// follow a JSON redirect (next leg / MCP client) or render the returned
// final page.
const authFormScript = `
  function submitAuth(url, body, btn, errEl, idleLabel) {
    btn.disabled = true;
    fetch(url, {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)})
      .then(function(r){
        if (!r.ok) {
          r.text().then(function(t){errEl.textContent=t||'Login failed';errEl.className='err show'});
          btn.disabled=false; btn.textContent=idleLabel;
          return;
        }
        var ct = r.headers.get('Content-Type') || '';
        if (ct.indexOf('application/json') >= 0) {
          r.json().then(function(d){ if (d.redirect) window.location.href = d.redirect; });
        } else {
          r.text().then(function(html){document.open();document.write(html);document.close()});
        }
      })
      .catch(function(){errEl.textContent='Network error';errEl.className='err show';btn.disabled=false;btn.textContent=idleLabel});
  }
`

const sshAuthFormHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in — Fresh Breath</title>
<style>` + authFormStyle + `</style></head><body>
<div class="card">
  <h1>Fresh Breath Login</h1>
  <p class="lead">Sign in with your passphrase.</p>
  <div class="err" id="err"></div>
  <form id="f">
    <label for="e">Email</label>
    <input id="e" type="email" required autocomplete="email" autofocus/>
    <label for="p">Passphrase</label>
    <input id="p" type="password" required autocomplete="current-password"/>
    <button type="submit" id="btn">Sign in</button>
  </form>
</div>
<script>` + authFormScript + `
(function(){
  var state="{{STATE}}";
  document.getElementById('f').onsubmit=function(ev){
    ev.preventDefault();
    var btn=document.getElementById('btn'), errEl=document.getElementById('err');
    errEl.className='err'; errEl.textContent=''; btn.textContent='Signing in…';
    submitAuth('/service/ssh-auth',
      {state:state,email:document.getElementById('e').value,passphrase:document.getElementById('p').value},
      btn, errEl, 'Sign in');
  };
})();
</script></body></html>`

// handlePassphraseLink is the page behind an invite or reset link: the
// link's token is the whole credential, so it is mounted bare. GET shows
// the form for the link's user; POST sets the passphrase.
func (s *Server) handlePassphraseLink(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Referrer-Policy", "no-referrer")
		token := r.URL.Query().Get("token")
		u, err := s.passphraseLinkUser(token)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(strings.Replace(passphraseLinkErrorHTML, "{{MESSAGE}}", html.EscapeString(err.Error()), 1)))
			return
		}
		tokenJS, _ := json.Marshal(token)
		page := strings.NewReplacer(
			"{{EMAIL}}", html.EscapeString(u.Email),
			"{{TOKEN}}", string(tokenJS),
		).Replace(passphraseLinkFormHTML)
		w.Write([]byte(page))

	case http.MethodPost:
		var req struct {
			Token      string `json:"token"`
			Passphrase string `json:"passphrase"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if _, err := s.coreSetPassphraseByLink(req.Token, req.Passphrase); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

const passphraseLinkFormHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Set passphrase — Fresh Breath</title>
<style>` + authFormStyle + `
  .ok{color:#4ade80;font-size:14px;line-height:1.5}
  .ok a{color:#a5b4fc}
</style></head><body>
<div class="card">
  <h1>Set your passphrase</h1>
  <p class="lead">For <b>{{EMAIL}}</b>. You'll use it to sign in to Fresh Breath. Setting it creates a new SSH key for your account.</p>
  <div class="err" id="err"></div>
  <form id="f">
    <label for="p">New passphrase</label>
    <input id="p" type="password" required minlength="8" autocomplete="new-password" autofocus/>
    <label for="c">Confirm passphrase</label>
    <input id="c" type="password" required minlength="8" autocomplete="new-password"/>
    <button type="submit" id="btn">Set passphrase</button>
  </form>
  <p class="ok" id="ok" style="display:none">Your passphrase is set. <a href="/control">Sign in to the control panel</a>.</p>
</div>
<script>
(function(){
  var token={{TOKEN}};
  document.getElementById('f').onsubmit=function(ev){
    ev.preventDefault();
    var btn=document.getElementById('btn'), errEl=document.getElementById('err');
    var p=document.getElementById('p').value, c=document.getElementById('c').value;
    errEl.className='err';
    if (p.length<8) { errEl.textContent='Use at least 8 characters.'; errEl.className='err show'; return; }
    if (p!==c) { errEl.textContent='The passphrases don\'t match.'; errEl.className='err show'; return; }
    btn.disabled=true; btn.textContent='Saving…';
    fetch('/service/passphrase', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({token:token, passphrase:p})})
      .then(function(r){
        if (r.ok) {
          document.getElementById('f').style.display='none';
          document.getElementById('ok').style.display='block';
          return;
        }
        r.text().then(function(t){errEl.textContent=t||'Could not set passphrase';errEl.className='err show'});
        btn.disabled=false; btn.textContent='Set passphrase';
      })
      .catch(function(){errEl.textContent='Network error';errEl.className='err show';btn.disabled=false;btn.textContent='Set passphrase'});
  };
})();
</script></body></html>`

const passphraseLinkErrorHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Link expired — Fresh Breath</title>
<style>` + authFormStyle + `</style></head><body>
<div class="card">
  <h1>Link not valid</h1>
  <p class="lead">{{MESSAGE}}</p>
</div>
</body></html>`

const apiKeyAuthFormHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>API Key — Fresh Breath</title>
<style>` + authFormStyle + `</style></head><body>
<div class="card">
  <h1>API Key</h1>
  <p class="lead">Enter the API key for this service.</p>
  <div class="err" id="err"></div>
  <form id="f">
    <label for="k">API Key</label>
    <input id="k" type="password" required autofocus/>
    <button type="submit" id="btn">Submit</button>
  </form>
</div>
<script>` + authFormScript + `
(function(){
  var state="{{STATE}}";
  document.getElementById('f').onsubmit=function(ev){
    ev.preventDefault();
    var btn=document.getElementById('btn'), errEl=document.getElementById('err');
    errEl.className='err'; errEl.textContent=''; btn.textContent='Submitting…';
    submitAuth('/service/apikey-auth',
      {state:state,api_key:document.getElementById('k').value},
      btn, errEl, 'Submit');
  };
})();
</script></body></html>`
