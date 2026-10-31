package server

// HTTP surface for remote update feeds — thin adapters over the core
// functions in updates_core.go (parse input → call core → format output).
// Split by auth: management endpoints are admin-only via authWrap. The flat
// /check endpoint is mounted BARE — a hosted app may poll it anonymously —
// and /apply asks for what the targets ask: each op's gate is checked against
// the caller's token, and an open gate needs no proof, so an open app can
// still pull its own updates with no login at all. Both get the per-IP rate
// limiter.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// Per-IP fixed-window limits for the anonymous endpoints. Check is a
// cheap probe (a hosted app may poll it); apply fans out into real downloads,
// so it's much tighter. OAuth client registration (open to anyone too)
// lives in oauth_server.go and shares the same limiter plumbing.
const (
	updateCheckRate = 30 // per minute
	updateApplyRate = 6  // per minute
)

// allowRate enforces a per-IP window for one named bucket — each endpoint
// that admits anonymous callers gets its own, so a flood on one cannot
// lock out another. Unknown IPs (proxies, tests) share the "" address.
func (s *Server) allowRate(bucket, ip string, limit int) bool {
	now := time.Now()
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.updateRate == nil {
		s.updateRate = make(map[string]updateRateCount)
	}
	key := bucket + ":" + ip
	c := s.updateRate[key]
	if now.Sub(c.windowStart) >= time.Minute {
		c = updateRateCount{windowStart: now}
	}
	c.count++
	s.updateRate[key] = c
	return c.count <= limit
}

// writeUpdateStream runs fn with an event channel and drains it to the client
// as text/event-stream. The channel is the seam: core stays testable without
// a response writer. fn runs in the request's context, so a dropped client
// connection cancels core mid-flight.
func (s *Server) writeUpdateStream(w http.ResponseWriter, r *http.Request, fn func(ch chan<- UpdateEvent) error) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fw := flushWriter{w: w}

	ch := make(chan UpdateEvent, 32)
	errc := make(chan error, 1)
	go func() {
		errc <- fn(ch)
		close(ch)
	}()

	enc := json.NewEncoder(&fw)
	for ev := range ch {
		if _, err := fmt.Fprintf(&fw, "event: %s\ndata: ", ev.Event); err != nil {
			return
		}
		if err := enc.Encode(ev); err != nil {
			return
		}
		if _, err := fw.Write([]byte("\n")); err != nil {
			return
		}
	}
	// Drain a deferred error (e.g. apply aborted after the last event).
	<-errc
}

// actorFromContext pulls the authenticated user out of the request context
// (set by authWrap or the act-token dispatch).
func actorFromContext(r *http.Request) *db.User {
	u, _ := r.Context().Value(userKey).(*db.User)
	return u
}

// handleUpdates serves POST/GET /api/updates — create (returns the key the
// one time it exists) and list (never the key).
func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body struct {
			URL    string `json:"url"`
			Mode   string `json:"mode"`
			Name   string `json:"name"`
			KeyHex string `json:"key_hex"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		actor := actorFromContext(r)
		id, key, err := s.coreCreateUpdateFeed(actor, body.URL, body.Mode, body.Name, body.KeyHex)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": id, "key": key})
	case http.MethodGet:
		actor := actorFromContext(r)
		feeds, err := s.coreListUpdateFeeds(actor)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"feeds": feeds})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUpdateDetail serves /api/updates/{id} and subpaths (PUT/DELETE,
// /build, /archive). The exact /check and /apply mounts win over this subtree
// pattern, so those never pass through here.
func (s *Server) handleUpdateDetail(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/updates/")
	parts := strings.Split(rest, "/")

	if len(parts) == 1 {
		s.handleUpdateFeedDetail(w, r, parts[0])
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "build":
			s.handleUpdateBuild(w, r, parts[0])
			return
		case "archive":
			s.handleUpdateArchive(w, r, parts[0])
			return
		}
	}
	http.Error(w, "Not found", http.StatusNotFound)
}

// handleUpdateFeedDetail serves PUT/DELETE /api/updates/{id}.
func (s *Server) handleUpdateFeedDetail(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPut:
		var body map[string]*string
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		actor := actorFromContext(r)
		if err := s.coreUpdateUpdateFeed(actor, id, body["url"], body["name"]); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		actor := actorFromContext(r)
		if err := s.coreDeleteUpdateFeed(actor, id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUpdateBuild serves POST /api/updates/{id}/build — publish mode,
// admin-only, SSE progress, ends with a done event carrying the download URL.
func (s *Server) handleUpdateBuild(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	actor := actorFromContext(r)
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		writeErr(w, err)
		return
	}
	// Pre-flight the feed synchronously so a 404 or mode mismatch is a plain
	// status code rather than an SSE stream that opens with an error event.
	feed, err := s.store.GetUpdateFeed(id)
	if err != nil {
		writeErr(w, cerr(http.StatusNotFound, "update feed not found"))
		return
	}
	if feed.Mode != "publish" {
		writeErr(w, cerr(http.StatusBadRequest, "feed is not in publish mode"))
		return
	}
	var body struct {
		Apps     []string `json:"apps"`
		Services []string `json:"services"`
		Version  string   `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	s.writeUpdateStream(w, r, func(ch chan<- UpdateEvent) error {
		_, err := s.coreBuildArchive(actor, id, body.Apps, body.Services, body.Version, r.Context(), ch)
		return err
	})
}

// handleUpdateArchive serves GET /api/updates/{id}/archive?ref=<nonce> — the
// one-shot download of a built archive. Admin-gated by its mount.
func (s *Server) handleUpdateArchive(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		http.Error(w, "missing ref", http.StatusBadRequest)
		return
	}
	data, ok := s.takePendingArchive(id, ref)
	if !ok {
		http.Error(w, "archive not found or already downloaded", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=update-%s.tar.gz.enc", id))
	w.Write(data)
}

// handleUpdateCheck serves GET /api/updates/check — anonymous, rate-limited,
// returns only feeds with an update available.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowRate("updates/check", s.requestIP(r), updateCheckRate) {
		http.Error(w, "Too many requests", http.StatusTooManyRequests)
		return
	}
	updates := s.coreCheckAllUpdateFeeds()
	if updates == nil {
		updates = []UpdateAvailable{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"updates": updates})
}

// handleUpdateApply serves POST /api/updates/apply — rate-limited, SSE
// progress, gated by the targets' doors. Body {ids?}: named feeds or all
// receive feeds.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowRate("updates/apply", s.requestIP(r), updateApplyRate) {
		http.Error(w, "Too many requests", http.StatusTooManyRequests)
		return
	}
	// Anyone who can access the targets may apply their updates: the
	// caller's token is checked against each op's gate (an open gate needs
	// no proof, so anonymous callers still reach open feeds). The hosted
	// app's own frbr.js session rides in as a Bearer automatically.
	var claims *freshbreathClaims
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		var err error
		claims, err = s.verifyFreshbreathToken(strings.TrimPrefix(ah, "Bearer "))
		if err != nil {
			http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
	}

	s.writeUpdateStream(w, r, func(ch chan<- UpdateEvent) error {
		return s.coreApplyUpdateFeeds(r.Context(), claims, body.IDs, ch)
	})
}

// handleUpdateImport serves POST /api/updates/import — a transfer from
// another server: the body is the archive (a plain tar.gz), the answer an
// SSE stream like apply's. Admin+, by its mount.
func (s *Server) handleUpdateImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	actor := actorFromContext(r)
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		writeErr(w, err)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxArchiveSize))
	if err != nil {
		http.Error(w, fmt.Sprintf("archive too large or unreadable: %v", err), http.StatusBadRequest)
		return
	}
	s.writeUpdateStream(w, r, func(ch chan<- UpdateEvent) error {
		return s.coreImportArchive(r.Context(), actor, data, ch)
	})
}

// handleTransfer serves POST /api/transfer {remote, direction, apps,
// services} — move apps and services between here and a remote, as the
// remote user whose token rides in X-Tunnel-Token (as for the proxy).
// direction "push" (the default) sends ours there; "pull" fetches the
// remote's here, apps and services named as the remote knows them.
// Admin+, SSE progress.
func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	actor := actorFromContext(r)
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Remote    string   `json:"remote"`
		Direction string   `json:"direction"`
		Apps      []string `json:"apps"`
		Services  []string `json:"services"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Direction != "" && body.Direction != "push" && body.Direction != "pull" {
		http.Error(w, `direction must be "push" or "pull"`, http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetRemote(body.Remote); err != nil {
		http.Error(w, "remote not found", http.StatusNotFound)
		return
	}
	token := strings.TrimSpace(r.Header.Get("X-Tunnel-Token"))
	if token == "" {
		http.Error(w, "sign in to the remote first (no X-Tunnel-Token)", http.StatusBadRequest)
		return
	}
	s.writeUpdateStream(w, r, func(ch chan<- UpdateEvent) error {
		return s.coreTransfer(r.Context(), actor, body.Remote, token, body.Direction == "pull", body.Apps, body.Services, ch)
	})
}

// handleUpdateExport serves POST /api/updates/export {apps, services} — the
// other half of a pull: the archive an import takes (a plain tar.gz, with
// what it needs to create each one), for another server to fetch through
// the tunnel. Admin+ by its mount; task services take a Superuser, as for
// building.
func (s *Server) handleUpdateExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	actor := actorFromContext(r)
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Apps     []string `json:"apps"`
		Services []string `json:"services"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	data, _, err := s.packArchive(actor, body.Apps, body.Services, "", true, func(UpdateEvent) error { return nil })
	if err != nil {
		writeErr(w, err)
		return
	}
	s.audit(actor, "exported for a transfer", fmt.Sprintf("%d app(s), %d service(s)", len(body.Apps), len(body.Services)))
	w.Header().Set("Content-Type", "application/gzip")
	w.Write(data)
}
