package server

import (
	"strconv"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// TestTokenRecordBinding is a regression test for the IdP-confusion
// exploit: a token minted under one auth record must NOT authenticate
// against a different record's gate. Otherwise an attacker who can register
// an account at some secondary provider under a victim's email could mint a
// token there and use it to impersonate the victim at the admin gate.
func TestTokenRecordBinding(t *testing.T) {
	srv := newTestServer(t)
	srv.localKey = []byte("0123456789abcdef0123456789abcdef") // 32 bytes for HS256

	// adminRec backs the admin gate; otherRec is an unrelated provider the
	// attacker can register an account with.
	adminRec, err := srv.store.CreateAuthRecord("Admin IdP", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://admin.example", Provider: "admin-idp"})
	if err != nil {
		t.Fatalf("create admin record: %v", err)
	}
	otherRec, err := srv.store.CreateAuthRecord("Other IdP", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://other.example", Provider: "other-idp"})
	if err != nil {
		t.Fatalf("create other record: %v", err)
	}

	victim, err := srv.store.CreateUser("Vic Tim", "victim@example.com", "Superuser", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	subject := subjectForUser(victim)

	// A token minted under adminRec verifies against adminRec's gate.
	tokAdmin, err := srv.mintFreshbreathToken(subject, victim.Email, "Superuser", "Vic Tim", adminRec.ID, nil, nil)
	if err != nil {
		t.Fatalf("mint admin-bound token: %v", err)
	}
	_, user, err := srv.verifyGateToken(adminRec, tokAdmin)
	if err != nil {
		t.Fatalf("admin-bound token should verify against admin record: %v", err)
	}
	if user == nil || user.Email != "victim@example.com" {
		t.Fatalf("got user %+v, want victim@example.com", user)
	}

	// THE EXPLOIT: a token minted under otherRec (attacker-controlled) must
	// be rejected at adminRec's gate.
	tokOther, err := srv.mintFreshbreathToken(subject, victim.Email, "Superuser", "Vic Tim", otherRec.ID, nil, nil)
	if err != nil {
		t.Fatalf("mint other-bound token: %v", err)
	}
	if _, _, err := srv.verifyGateToken(adminRec, tokOther); err == nil {
		t.Fatal("SECURITY: token minted under another record was accepted at the admin gate")
	}

	// And the same protection through the admin gate used by the central MCP.
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(adminRec.ID, 10)); err != nil {
		t.Fatalf("set admin_auth_service: %v", err)
	}
	if _, err := srv.verifyAdminTokenFromBearer(tokOther); err == nil {
		t.Fatal("SECURITY: other-record token passed the admin gate")
	}
	got, err := srv.verifyAdminTokenFromBearer(tokAdmin)
	if err != nil {
		t.Fatalf("admin-bound token should pass the admin gate: %v", err)
	}
	if got.Email != "victim@example.com" {
		t.Fatalf("got user %q, want victim@example.com", got.Email)
	}

	// A token whose Legs include the admin record also passes — that's a
	// multi-leg login that genuinely cleared the admin gate.
	tokLegs, err := srv.mintFreshbreathToken(subject, victim.Email, "Superuser", "Vic Tim", otherRec.ID, []int64{adminRec.ID}, nil)
	if err != nil {
		t.Fatalf("mint legs token: %v", err)
	}
	if _, _, err := srv.verifyGateToken(adminRec, tokLegs); err != nil {
		t.Fatalf("legs-bound token should verify against admin record: %v", err)
	}
	// But foreign legs don't help at an unrelated gate.
	thirdRec, _ := srv.store.CreateAuthRecord("Third", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://third.example"})
	if _, _, err := srv.verifyGateToken(thirdRec, tokLegs); err == nil {
		t.Fatal("SECURITY: token with unrelated legs accepted at a foreign gate")
	}

	// An ext: subject from the right record still isn't a user — the admin
	// gate must refuse identities with no user row.
	tokExt, err := srv.mintFreshbreathToken(extSubject("admin-idp", "12345"), "stranger@example.com", "", "", adminRec.ID, nil, nil)
	if err != nil {
		t.Fatalf("mint ext token: %v", err)
	}
	if _, err := srv.verifyAdminTokenFromBearer(tokExt); err == nil {
		t.Fatal("SECURITY: ext: subject with no user row passed the admin gate")
	}
}
