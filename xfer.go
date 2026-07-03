package main

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// Transfer tokens hand an LLM a plain URL instead of a base64 blob. An MCP tool
// mints a short-lived, single-use token bound to one action on one app; the
// holder then GETs (download) or POSTs (upload) raw bytes to /api/xfer/<token>.
// Possession of the token is the capability — the originating tool already
// gated the actor, and the actor is re-gated at redemption for good measure.

const transferTTL = 5 * time.Minute

const xferMaxUpload = 50 << 20 // 50MB, matching the multipart web-upload limit

// transferEntry is a pending file transfer. Action is "download" or "upload";
// Filename is the upload's target name (ignored for downloads).
// A token targets either an app (Nonce) or a service (ServiceID), never both.
type transferEntry struct {
	Action    string
	Nonce     string  // app nonce; set for app-targeted transfers
	ServiceID int64   // service ID; set for service-targeted transfers
	Filename  string
	Actor     *User
	ExpiresAt time.Time
}

func (e *transferEntry) appTarget() bool     { return e.Nonce != "" }
func (e *transferEntry) serviceTarget() bool { return e.ServiceID != 0 }

// newTransfer mints a token for action on the given app, bound to actor, and
// returns the full /api/xfer URL. Expired entries are pruned on each call.
func (s *Server) newTransfer(action, nonce, filename string, actor *User) string {
	return s.newTransferEntry(&transferEntry{
		Action:   action,
		Nonce:    nonce,
		Filename: filename,
		Actor:    actor,
	})
}

// newServiceTransfer mints a token for action on the given service, bound to actor.
func (s *Server) newServiceTransfer(action string, serviceID int64, filename string, actor *User) string {
	return s.newTransferEntry(&transferEntry{
		Action:    action,
		ServiceID: serviceID,
		Filename:  filename,
		Actor:     actor,
	})
}

func (s *Server) newTransferEntry(e *transferEntry) string {
	s.xfersMu.Lock()
	defer s.xfersMu.Unlock()

	if s.xfers == nil {
		s.xfers = make(map[string]*transferEntry)
	}
	now := time.Now()
	for tok, ex := range s.xfers {
		if now.After(ex.ExpiresAt) {
			delete(s.xfers, tok)
		}
	}

	token := rand.Text()
	e.ExpiresAt = now.Add(transferTTL)
	s.xfers[token] = e
	return s.config.PublicBaseURL + "/api/xfer/" + token
}

// takeTransfer consumes a token: it returns the entry and removes it, so a
// token works at most once. A missing or expired token returns nil.
func (s *Server) takeTransfer(token string) *transferEntry {
	s.xfersMu.Lock()
	defer s.xfersMu.Unlock()

	e := s.xfers[token]
	if e == nil {
		return nil
	}
	delete(s.xfers, token)
	if time.Now().After(e.ExpiresAt) {
		return nil
	}
	return e
}

// handleXfer redeems a transfer token. GET serves a download; POST accepts an
// upload's raw body. The token is consumed regardless of outcome.
func (s *Server) handleXfer(w http.ResponseWriter, r *http.Request) {
	e := s.takeTransfer(r.PathValue("token"))
	if e == nil {
		http.Error(w, "invalid or expired transfer token", http.StatusNotFound)
		return
	}

	switch {
	case r.Method == http.MethodGet && e.Action == "download":
		if e.appTarget() {
			data, slug, err := s.coreDownloadAppWeb(e.Actor, e.Nonce)
			if err != nil {
				writeErr(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="`+slug+`.zip"`)
			w.Write(data)
			return
		}
		data, filename, err := s.coreDownloadServiceFiles(e.Actor, e.ServiceID)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.Write(data)

	case r.Method == http.MethodPost && e.Action == "upload":
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, xferMaxUpload))
		if err != nil {
			http.Error(w, "file too large (50MB max)", http.StatusBadRequest)
			return
		}
		var route string
		if e.appTarget() {
			route, err = s.coreUploadAppWeb(e.Actor, e.Nonce, data, e.Filename)
		} else {
			route, err = s.coreUploadServiceFiles(e.Actor, e.ServiceID, data, e.Filename)
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"route": route})

	default:
		http.Error(w, "method not allowed for this token", http.StatusMethodNotAllowed)
	}
}
