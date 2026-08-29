package server

// HTTP faces for app databases — thin adapters over the core functions in
// appdb.go (parse input → call core → format output). Two mounts, one core:
// /api/apps/{nonce}/db/* spells the target as "app:<nonce>", /api/db/* is the
// alias with target "global". They differ only in that string; authorization
// happens once, inside coreDB*/gateDBTarget, never at the mount.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// handleAppDB is the /api/apps/{nonce}/db sub-route, dispatched from
// handleAppDetail. parts is the split path (api/apps/{nonce}/db/...).
func (s *Server) handleAppDB(w http.ResponseWriter, r *http.Request, nonce string) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	s.serveDBRoutes(w, r, "app:"+nonce, parts[4:])
}

// handleGlobalDB is the /api/db/* mount — the global-database alias. Same
// core calls as the app mount; only the target string differs.
func (s *Server) handleGlobalDB(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "api" || parts[1] != "db" {
		http.NotFound(w, r)
		return
	}
	s.serveDBRoutes(w, r, "global", parts[2:])
}

// serveDBRoutes dispatches the shared sub-route grammar under both mounts:
//
//	GET    …/db           → list databases
//	POST   …/db/query     → run a statement or batch
//	GET    …/db/watch     → SSE change feed
//	DELETE …/db/{name}    → drop a database
func (s *Server) serveDBRoutes(w http.ResponseWriter, r *http.Request, target string, rest []string) {
	if len(rest) == 0 {
		s.serveDBList(w, r, target)
		return
	}
	switch {
	case len(rest) == 1 && rest[0] == "query":
		s.serveDBQuery(w, r, target)
	case len(rest) == 1 && rest[0] == "watch":
		s.serveDBWatch(w, r, target)
	case len(rest) == 1:
		s.serveDBDelete(w, r, target, rest[0])
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveDBQuery(w http.ResponseWriter, r *http.Request, target string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req dbQueryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	res, err := s.coreDBQuery(actorFromContext(r), target, req, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func (s *Server) serveDBList(w http.ResponseWriter, r *http.Request, target string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dbs, err := s.coreDBList(actorFromContext(r), target)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"databases": dbs})
}

func (s *Server) serveDBDelete(w http.ResponseWriter, r *http.Request, target, name string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.coreDBDelete(actorFromContext(r), target, name); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveDBWatch streams the forward-only change feed for one database as SSE
// (`?db=<name>`, default "app"). v1 sends no id: fields and keeps no replay
// buffer, so a Last-Event-ID header is accepted and politely ignored — a
// reconnecting watcher re-runs its queries instead of expecting the gap.
func (s *Server) serveDBWatch(w http.ResponseWriter, r *http.Request, target string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.gateDBTarget(actorFromContext(r), target); err != nil {
		writeErr(w, err)
		return
	}
	name := r.URL.Query().Get("db")
	if name == "" {
		name = "app"
	}
	ch, stop, err := s.appDBWatchCh(target, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fw := flushWriter{w: w}

	// One comment up front so an event-less database still proves the
	// subscription is live — the client's onOpen fires on first bytes.
	fmt.Fprint(&fw, ": watch\n\n")

	enc := json.NewEncoder(&fw)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if _, err := fmt.Fprint(&fw, "event: change\ndata: "); err != nil {
				return
			}
			if err := enc.Encode(ev); err != nil {
				return
			}
			if _, err := fmt.Fprint(&fw, "\n"); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(&fw, ": hb\n\n"); err != nil {
				return
			}
		}
	}
}
