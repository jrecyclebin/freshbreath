package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// actTokenTTL is how long a minted act token stays valid. Short by design —
// it's the replay defense: tickets live in memory only, swept after this
// window. A leaked ticket dies on its own clock. Process restart clears
// the map entirely (the no-DB-secrets property is preserved — nothing
// persistable is ever written), so a relaunch revokes every outstanding
// ticket; the TTL is the in-process expiry. See
// skills/freshbreath/guides/publishing.md §3.
const actTokenTTL = 10 * time.Minute

// actTicketPayload is the stored content of an act-ticket: the path+method
// the ticket is pinned to, the user it acts as, and an expiry. Every field
// is fixed at mint time and checked at serve, so the capability can't be
// widened after minting — no swapping path, method, or user. Path is a full
// path+query under /api/*. Unlike the old HMAC shape, the payload never
// travels the wire; only its 10-char ID does, and the ID is opaque without
// the in-memory map.
type actTicketPayload struct {
	Path    string
	Method  string
	Expiry  time.Time
	Subject string // "frbr:<user_id>" — act tokens act as real users only
}

// actTickets is the in-memory ticket store (the hostedRoutes precedent,
// server.go). One map, one mutex. Nothing is persisted; process restart
// clears it. Expired entries are dropped by sweepActTickets, called from
// the cleanup ticker next to ExpireKeys / ExpireSessions. The map is
// indexed by the 10-char ticket ID returned from mintActToken.
type actTickets struct {
	mu  sync.Mutex
	tix map[string]actTicketPayload
}

// mintActToken issues a short-lived capability ticket letting an anonymous
// HTTP client perform one pinned operation on /api/* as user. The ticket
// is a 10-char ID (60 bits, db.GenNonce) over the compact alphabet; the
// payload lives in the in-memory map, not the URL. The caller builds the
// full /api/act/<ticket> URL. An audit entry is logged at mint — the
// security-meaningful event is "user X was granted a capability for path
// Y", not its later exercise.
func (s *Server) mintActToken(user *db.User, method, pathQuery string, ttl time.Duration) (string, error) {
	if !strings.HasPrefix(pathQuery, "/api/") {
		return "", fmt.Errorf("act token path must be under /api/: %q", pathQuery)
	}
	p := actTicketPayload{
		Path:    pathQuery,
		Method:  method,
		Expiry:  time.Now().Add(ttl),
		Subject: subjectForUser(user),
	}
	// Collision retry — 60 bits plus a 10-minute TTL means a hit is
	// vanishingly rare, but GenNonce has no uniqueness guarantee and we
	// must not overwrite a live ticket. A few tries is plenty.
	for i := 0; i < 8; i++ {
		id := db.GenNonce()
		s.actTickets.mu.Lock()
		if _, exists := s.actTickets.tix[id]; !exists {
			s.actTickets.tix[id] = p
			s.actTickets.mu.Unlock()
			_ = s.store.LogAudit(subjectForUser(user), "act_token_mint", pathQuery)
			return id, nil
		}
		s.actTickets.mu.Unlock()
	}
	return "", errors.New("act token: could not mint unique id after retries")
}

// lookupActTicket returns the payload for a ticket ID, checking expiry.
// An expired or unknown ticket yields an error. It does NOT delete the
// entry (sweepActTickets owns that), so a retried read of an expired
// ticket keeps reporting "expired" rather than flipping to "unknown".
func (s *Server) lookupActTicket(id string) (*actTicketPayload, error) {
	s.actTickets.mu.Lock()
	defer s.actTickets.mu.Unlock()
	p, ok := s.actTickets.tix[id]
	if !ok {
		return nil, errors.New("act token: unknown ticket")
	}
	if time.Now().After(p.Expiry) {
		return nil, errors.New("act token: expired")
	}
	out := p // copy so callers can't mutate the map entry
	return &out, nil
}

// sweepActTickets drops expired act tickets. Called from the cleanup ticker
// (server.go) next to the other periodic sweeps. Cheap: a map walk under
// the ticket mutex.
func (s *Server) sweepActTickets(now time.Time) {
	s.actTickets.mu.Lock()
	defer s.actTickets.mu.Unlock()
	for id, p := range s.actTickets.tix {
		if now.After(p.Expiry) {
			delete(s.actTickets.tix, id)
		}
	}
}

// handleAct serves /api/act/{ticket}. It's mounted bare (not authWrap'd) and
// anonymous by design: it authenticates via the capability ticket, not a
// bearer. After looking up the ticket, it resolves the named user fresh
// from the DB — so a demoted user's outstanding tickets stop working —
// pins the method, and re-dispatches through s.mux with the user
// pre-stashed in the context. authWrap then sees the pre-set userKey and
// short-circuits its bearer check, so every downstream role/authz gate
// runs exactly as for a bearer request. The only other setter of userKey
// is authWrap; the trust boundary stays a two-member club.
func (s *Server) handleAct(w http.ResponseWriter, r *http.Request) {
	ticket := strings.TrimPrefix(r.URL.Path, "/api/act/")
	if ticket == "" || strings.Contains(ticket, "/") {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	payload, err := s.lookupActTicket(ticket)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != payload.Method {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	user, err := s.userFromSubject(payload.Subject)
	if err != nil || user == nil || user.Status != "Active" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	parsed, err := url.Parse(payload.Path)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	r.URL = parsed
	r.RequestURI = parsed.RequestURI() // keep in sync with the rewritten URL
	ctx := context.WithValue(r.Context(), userKey, user)
	s.mux.ServeHTTP(w, r.WithContext(ctx))
}
