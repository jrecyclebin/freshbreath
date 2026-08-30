package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// mintRawActToken signs an arbitrary payload, bypassing mintActToken's /api/
// scope guard so we can probe the verify-side guard independently.
func mintRawActToken(s *Server, p actTokenPayload) string {
	plain, _ := json.Marshal(&p)
	enc := base64.RawURLEncoding.EncodeToString(plain)
	sig := s.actTokenMAC(enc)
	return enc + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// tamperedSigToken returns a well-formed token whose payload is good's but
// whose signature was minted for a different payload — so it decodes cleanly
// but fails the HMAC compare. Exercises the signature-mismatch path, not a
// base64 decode failure.
func tamperedSigToken(s *Server, user *db.User, method, path string) string {
	good, _ := s.mintActToken(user, method, path, 5*time.Minute)
	other, _ := s.mintActToken(user, http.MethodDelete, "/api/_tamper_other", 5*time.Minute)
	enc, _, _ := strings.Cut(good, ".")
	_, sig, _ := strings.Cut(other, ".")
	return enc + "." + sig
}

func createActUser(t *testing.T, srv *Server) *db.User {
	t.Helper()
	u, err := srv.store.CreateUser("Ada", "ada@example.com", "Admin", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// ── Pure mint/verify ──

func TestActTokenRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok, err := srv.mintActToken(ada, http.MethodGet, "/api/apps", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	p, err := srv.verifyActToken(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.Path != "/api/apps" || p.Method != http.MethodGet || p.Subject != subjectForUser(ada) {
		t.Fatalf("payload mismatch: %+v", p)
	}
}

func TestActTokenTamperedSignature(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	if _, err := srv.verifyActToken(tamperedSigToken(srv, ada, http.MethodGet, "/api/apps")); err == nil {
		t.Fatal("verify: expected signature mismatch, got nil")
	}
}

func TestActTokenExpired(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok, err := srv.mintActToken(ada, http.MethodGet, "/api/apps", -time.Second)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := srv.verifyActToken(tok); err == nil {
		t.Fatal("verify: expected expired error, got nil")
	}
}

func TestActTokenMintRejectsNonAPI(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	if _, err := srv.mintActToken(ada, http.MethodGet, "/mcp/foo", 5*time.Minute); err == nil {
		t.Fatal("mint: expected error for non-/api/ path, got nil")
	}
}

func TestActTokenVerifyRejectsNonAPI(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	// Hand-craft a /mcp/ payload (mintActToken would refuse it) and sign it
	// correctly; verify must still reject it — the scope guard isn't only at mint.
	p := actTokenPayload{
		Path:    "/mcp/foo",
		Method:  http.MethodGet,
		Expiry:  time.Now().Add(5 * time.Minute).Unix(),
		Subject: subjectForUser(ada),
	}
	if _, err := srv.verifyActToken(mintRawActToken(srv, p)); err == nil {
		t.Fatal("verify: expected scope rejection for /mcp/, got nil")
	}
}

func TestActTokenMalformed(t *testing.T) {
	srv := newTestServer(t)
	for _, bad := range []string{"", "no-separator", "too.many.dots.dots"} {
		if _, err := srv.verifyActToken(bad); err == nil {
			t.Fatalf("verify(%q): expected malformed error, got nil", bad)
		}
	}
}

// ── Dispatch through the mux ──
//
// These hit /api/act/{token} via srv.ServeHTTP so the mount, origin bypass,
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

func TestActTokenDispatchTampered(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok := tamperedSigToken(srv, ada, http.MethodGet, "/api/me")
	rr := testRequest(t, srv, http.MethodGet, "/api/act/"+tok, nil, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for tampered token", rr.Code)
	}
}

func TestActTokenDispatchInactiveUser(t *testing.T) {
	srv := newTestServer(t)
	ada := createActUser(t, srv)
	tok, _ := srv.mintActToken(ada, http.MethodGet, "/api/me", 5*time.Minute)
	// Demote Ada after minting; the token is still well-signed for her email,
	// but the fresh re-resolve must reject her now-Inactive account.
	if err := srv.store.UpdateUser(ada.ID, "Ada", "ada@example.com", "Admin", "Inactive", nil); err != nil {
		t.Fatalf("demote: %v", err)
	}
	rr := testRequest(t, srv, http.MethodGet, "/api/act/"+tok, nil, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for inactive user", rr.Code)
	}
}
