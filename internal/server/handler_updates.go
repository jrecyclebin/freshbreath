package server

// HTTP surface for remote update feeds — thin adapters over the core
// functions in updates_core.go (parse input → call core → format output).
// Split by auth: management endpoints are admin-only via authWrap; the flat
// /check and /apply endpoints are mounted BARE — anonymous by design, the
// whole point being that a hosted app can pull its own updates. They get the
// per-IP rate limiter instead of a login.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// Per-IP fixed-window limits for the anonymous update endpoints. Check is a
// cheap probe (a hosted app may poll it); apply fans out into real downloads,
// so it's much tighter.
const (
	updateCheckRate = 30 // per minute
	updateApplyRate = 6  // per minute
)

// allowUpdateRequest enforces the per-IP window for the anonymous endpoints.
// Unknown IPs (proxies, tests) share the "" bucket.
func (s *Server) allowUpdateRequest(ip string, limit int) bool {
	now := time.Now()
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.updateRate == nil {
		s.updateRate = make(map[string]updateRateCount)
	}
	c := s.updateRate[ip]
	if now.Sub(c.windowStart) >= time.Minute {
		c = updateRateCount{windowStart: now}
	}
	c.count++
	s.updateRate[ip] = c
	return c.count <= limit
}

func requestIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
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
	if !s.allowUpdateRequest(requestIP(r), updateCheckRate) {
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

// handleUpdateApply serves POST /api/updates/apply — anonymous,
// rate-limited, SSE progress. Body {ids?}: named feeds or all receive feeds.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowUpdateRequest(requestIP(r), updateApplyRate) {
		http.Error(w, "Too many requests", http.StatusTooManyRequests)
		return
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
		return s.coreApplyUpdateFeeds(r.Context(), body.IDs, ch)
	})
}
