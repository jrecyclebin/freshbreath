package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

func createActUser(t *testing.T, srv *Server) *db.User {
	t.Helper()
	u, err := srv.store.CreateUser("Ada", "ada@example.com", "Admin", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// ── Pure mint/lookup ──

func TestActTokenRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok, err := srv.mintActToken(ada, http.MethodGet, "/api/apps", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(tok) != 10 {
		t.Fatalf("ticket id len = %d, want 10 (compact alphabet)", len(tok))
	}
	p, err := srv.lookupActTicket(tok)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if p.Path != "/api/apps" || p.Method != http.MethodGet || p.Subject != subjectForUser(ada) {
		t.Fatalf("payload mismatch: %+v", p)
	}
}

func TestActTokenExpired(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok, err := srv.mintActToken(ada, http.MethodGet, "/api/apps", -time.Second)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := srv.lookupActTicket(tok); err == nil {
		t.Fatal("lookup: expected expired error, got nil")
	}
}

func TestActTokenUnknown(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.lookupActTicket("nope000000"); err == nil {
		t.Fatal("lookup: expected unknown-ticket error, got nil")
	}
}

func TestActTokenMintRejectsNonAPI(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	if _, err := srv.mintActToken(ada, http.MethodGet, "/mcp/foo", 5*time.Minute); err == nil {
		t.Fatal("mint: expected error for non-/api/ path, got nil")
	}
}

// Sweep frees entries: an expired ticket is dropped from the map by
// sweepActTickets, after which lookup reports unknown (not expired) —
// matching the contract documented on lookupActTicket.
func TestActTokenSweepFreesEntries(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok, err := srv.mintActToken(ada, http.MethodGet, "/api/apps", -time.Second)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	srv.sweepActTickets(time.Now())
	srv.actTickets.mu.Lock()
	_, present := srv.actTickets.tix[tok]
	srv.actTickets.mu.Unlock()
	if present {
		t.Fatal("sweep left an expired ticket in the map")
	}
}

// ── Dispatch through the mux ──
//
// These hit /api/act/{ticket} via srv.ServeHTTP so the mount, origin bypass,
// handleAct, the authWrap short-circuit, and a real downstream handler all
// run. The act-token user is Ada (a real Admin); /api/me echoes back the
// context user, so asserting Ada's email — and NOT "Setup Account" — proves
// the short-circuit honored the act-token's user rather than letting the
// auth-off synthetic superuser clobber it.

func TestActTokenDispatch(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok, err := srv.mintActToken(ada, http.MethodGet, "/api/me", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rr := testRequest(t, srv, http.MethodGet, "/api/act/"+tok, nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "ada@example.com") {
		t.Fatalf("expected act-token user Ada in body, got: %s", body)
	}
	if strings.Contains(body, "Setup Account") {
		t.Fatalf("synthetic superuser leaked through — short-circuit broken? body: %s", body)
	}
}

func TestActTokenDispatchMethodPin(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok, err := srv.mintActToken(ada, http.MethodGet, "/api/me", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rr := testRequest(t, srv, http.MethodPut, "/api/act/"+tok, nil, nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 for method mismatch", rr.Code)
	}
}

func TestActTokenDispatchExpired(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok, _ := srv.mintActToken(ada, http.MethodGet, "/api/me", -time.Second)
	rr := testRequest(t, srv, http.MethodGet, "/api/act/"+tok, nil, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for expired token", rr.Code)
	}
}

func TestActTokenDispatchUnknown(t *testing.T) {
	srv := newTestServer(t)
	rr := testRequest(t, srv, http.MethodGet, "/api/act/nope000000", nil, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for unknown ticket", rr.Code)
	}
}

func TestActTokenDispatchInactiveUser(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok, _ := srv.mintActToken(ada, http.MethodGet, "/api/me", 5*time.Minute)
	// Demote Ada after minting; the fresh re-resolve must reject her now-Inactive account.
	if err := srv.store.UpdateUser(ada.ID, "Ada", "ada@example.com", "Admin", "Inactive", nil); err != nil {
		t.Fatalf("demote: %v", err)
	}
	rr := testRequest(t, srv, http.MethodGet, "/api/act/"+tok, nil, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for inactive user", rr.Code)
	}
}

// Restart clears: a fresh Server has an empty ticket map, so a ticket
// minted on one server is unknown on another. (Process restart in prod is
// the same shape — the map is in-memory only.)
func TestActTokenRestartClears(t *testing.T) {
	srv1 := newTestServer(t)
	ada := createActUser(t, srv1)
	tok, err := srv1.mintActToken(ada, http.MethodGet, "/api/apps", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := srv1.lookupActTicket(tok); err != nil {
		t.Fatalf("lookup on originating server: %v", err)
	}
	srv2 := newTestServer(t)
	if _, err := srv2.lookupActTicket(tok); err == nil {
		t.Fatal("lookup on a fresh server: expected unknown-ticket error, got nil (map should be empty after restart)")
	}
}
