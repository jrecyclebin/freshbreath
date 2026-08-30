package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// sshPending is a login flow waiting on the built-in passphrase leg.
func sshPending(t *testing.T, srv *Server) *pendingAuth {
	t.Helper()
	rec := builtinAuth(t, srv, db.AuthSSHKey)
	return &pendingAuth{legs: []*db.AuthRecord{rec}, primaryID: rec.ID}
}

// A failed credential attempt must not kill the login state: the user can
// correct their password (or revisit the link) while the TTL is still valid.
func TestPendingStateSurvivesBadCredentials(t *testing.T) {
	srv := newTestServer(t)
	state := "test-state-123"
	srv.putPending(state, sshPending(t, srv))

	post := func() *httptest.ResponseRecorder {
		body := strings.NewReader(`{"state":"` + state + `","email":"nobody@example.com","passphrase":"wrong"}`)
		return testRequest(t, srv, "POST", "/service/ssh-auth", body, nil)
	}

	first := post()
	if first.Code != 401 {
		t.Fatalf("first attempt: got %d, want 401", first.Code)
	}
	second := post()
	if second.Code != 401 {
		t.Fatalf("retry: got %d, want 401 (state must survive the bad attempt); body: %s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "Invalid credentials") {
		t.Fatalf("retry body = %q, want credentials error, not unknown state", second.Body.String())
	}
}

// State reaches its TTL: lookup reports "expired" (distinct from "unknown"),
// and the entry is swept so it can't be reused beyond the TTL.
func TestPendingStateExpiry(t *testing.T) {
	srv := newTestServer(t)
	state := "expired-state"
	srv.putPending(state, sshPending(t, srv))

	srv.pendingMu.Lock()
	srv.pending[state].expiresAt = time.Now().Add(-time.Minute)
	srv.pendingMu.Unlock()

	body := strings.NewReader(`{"state":"` + state + `","email":"a@b.c","passphrase":"x"}`)
	rec := testRequest(t, srv, "POST", "/service/ssh-auth", body, nil)
	if rec.Code != 400 {
		t.Fatalf("got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("body = %q, want mention of expiry", rec.Body.String())
	}
	srv.pendingMu.Lock()
	_, stillThere := srv.pending[state]
	srv.pendingMu.Unlock()
	if stillThere {
		t.Fatal("expired entry should have been swept by the lookup")
	}

	// Unknown (never seen) state reports as unknown, not expired.
	body2 := strings.NewReader(`{"state":"never-seen","email":"a@b.c","passphrase":"x"}`)
	rec2 := testRequest(t, srv, "POST", "/service/ssh-auth", body2, nil)
	if rec2.Code != 400 || !strings.Contains(rec2.Body.String(), "Unknown") {
		t.Fatalf("unknown state: got %d %q", rec2.Code, rec2.Body.String())
	}
}

// putPending sweeps stale entries opportunistically, so abandoned logins
// can't accumulate until restart.
func TestPendingSweepOnPut(t *testing.T) {
	srv := newTestServer(t)
	stale, fresh := sshPending(t, srv), sshPending(t, srv)
	stale.expiresAt = time.Now().Add(-time.Hour)
	fresh.expiresAt = time.Now().Add(time.Hour)
	srv.pendingMu.Lock()
	srv.pending["stale"] = stale
	srv.pending["fresh"] = fresh
	srv.pendingMu.Unlock()

	srv.putPending("new", sshPending(t, srv))

	srv.pendingMu.Lock()
	_, staleKept := srv.pending["stale"]
	_, freshKept := srv.pending["fresh"]
	srv.pendingMu.Unlock()
	if staleKept {
		t.Error("stale entry survived the sweep")
	}
	if !freshKept {
		t.Error("fresh entry wrongly swept")
	}
}

// MCP flow state behaves the same way: survives until its TTL, swept after.
func TestMCPPendingExpiry(t *testing.T) {
	srv := newTestServer(t)
	srv.putMCPPending("mcp-key", &mcpPendingAuth{})

	if _, ok, _ := srv.getMCPPending("mcp-key"); !ok {
		t.Fatal("fresh MCP pending entry not found")
	}
	v, _ := srv.mcpAuthPending.Load("mcp-key")
	v.(*mcpPendingAuth).expiresAt = time.Now().Add(-time.Minute)

	if _, ok, expired := srv.getMCPPending("mcp-key"); ok || !expired {
		t.Fatalf("expired entry: ok=%v expired=%v, want ok=false expired=true", ok, expired)
	}
	if _, found := srv.mcpAuthPending.Load("mcp-key"); found {
		t.Fatal("expired MCP entry should be deleted on lookup")
	}
}
