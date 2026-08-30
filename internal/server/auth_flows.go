package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
)

// ── Login legs ──────────────────────────────────────────────────────
//
// A login clears one or more auth records ("legs"): the inbound gate, plus
// the service's acts_as record when it is interactive and different from
// the gate. One pendingAuth walks the whole flow — each leg re-keys it
// under a fresh state — and the final leg mints one token carrying every
// cleared record.

// legsForLogin computes the records a login must clear, gate first.
func (s *Server) legsForLogin(gate *db.AuthRecord, svc *db.Service) ([]*db.AuthRecord, error) {
	var legs []*db.AuthRecord
	if gate != nil && gate.Kind != db.AuthAnonymous {
		legs = append(legs, gate)
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
		state := db.GenNonce()
		s.putPending(state, p)
		return fmt.Sprintf("%s/service/ssh-auth?state=%s", s.config.PublicBaseURL, state), nil
	case db.AuthAPIKey:
		state := db.GenNonce()
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
	case db.AuthOAuth2:
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

// finishLogin mints the token for a fully-cleared pending login and hands
// it off: to the MCP client via a one-shot code, or to the opener via the
// postMessage page (with the refresh cookie set alongside).
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, p *pendingAuth) (string, error) {
	// Pure api_key browser logins carry no token: the store entry is the
	// key itself, verified against the record.
	if p.mcpKey == "" && len(p.done) == 1 && p.done[0].rec.Kind == db.AuthAPIKey {
		rec := p.done[0].rec
		writeCallbackPage(w, p.appState, map[string]interface{}{
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
	if _, err := s.makeRefreshCookie(w, rd); err != nil {
		return "", err
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
	writeCallbackPage(w, p.appState, entry)
	return "", nil
}

// writeCallbackPage hands a finished browser login back to the opener. The
// entry is the frbr:auth:* store record; frbr.js stamps written_at and
// persists it. Correlation is by state; the listener pins the origin.
func writeCallbackPage(w io.Writer, appState string, entry map[string]interface{}) {
	entryJSON, _ := json.Marshal(entry)
	fmt.Fprintf(w, `<!doctype html>
<html>
<body>
  <p>Logged in. You can close this window.</p>
  <script>
    window.opener?.postMessage({
      type: "auth-complete",
      state: %q,
      entry: %s,
    }, "*");
  </script>
</body>
</html>
`, appState, string(entryJSON))
}

// ── /service/login ──────────────────────────────────────────────────

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

	legs, err := s.legsForLogin(gate, svc)
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

	p := &pendingAuth{
		appNonce:  nonce,
		appState:  appState,
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
	if rec.Kind == db.AuthOIDC {
		claims, accessToken, refreshToken, err = s.oidcExchangeCode(r.Context(), rec, code, p.verifier, p.oidcNonce, redirectURI)
	} else {
		claims, accessToken, refreshToken, err = s.oauth2ExchangeCode(r.Context(), rec, p.tokenEndpoint, code, p.verifier, p.clientID, p.clientSecret, redirectURI)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Code exchange failed: %v", err), http.StatusInternalServerError)
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
		http.Error(w, fmt.Sprintf("Login completion failed: %v", err), http.StatusInternalServerError)
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

		// Add decrypted key to the in-process SSH agent with 1h TTL.
		// Agent TTL is decoupled from the web JWT — agent timeout doesn't
		// invalidate the web session, and vice versa.
		if s.agentMgr != nil {
			if err := s.agentMgr.AddKey(user.ID, user.Metadata.SSHKey, req.Passphrase, 1*time.Hour); err != nil {
				log.Printf("agent add key for user %d: %v", user.ID, err)
			}
		}
		_ = s.store.LogAudit(req.Email, "login", "passphrase")

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
