package server

import (
	"context"
	"strconv"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// TestIdentityTokenServiceBinding is a regression test for the IdP-confusion
// exploit: an identity token minted by one service must NOT authenticate
// against a different service. Otherwise an attacker who can register an
// account at some secondary OIDC service under a victim's email could mint a
// token there and use it to impersonate the victim at the admin auth service
// (e.g. log into the central MCP).
func TestIdentityTokenServiceBinding(t *testing.T) {
	srv := newTestServer(t)
	srv.localKey = []byte("0123456789abcdef0123456789abcdef") // 32 bytes for HS256

	// adminSvc is the designated admin auth service; otherSvc is an unrelated
	// app-level OIDC service the attacker can register an account with.
	adminID, err := srv.store.RegisterService("admin-idp", "https://admin.example", db.ServiceDescriptor{Type: "oidc"})
	if err != nil {
		t.Fatalf("register admin service: %v", err)
	}
	otherID, err := srv.store.RegisterService("other-idp", "https://other.example", db.ServiceDescriptor{Type: "oidc"})
	if err != nil {
		t.Fatalf("register other service: %v", err)
	}
	adminSvc, _ := srv.store.GetService(adminID)

	if _, err := srv.store.CreateUser("Vic Tim", "victim@example.com", "Superuser", "Active"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	ctx := context.Background()

	// A token minted by adminSvc verifies against adminSvc.
	tokAdmin, err := srv.mintFreshbreathToken("identity", "victim@example.com", "Superuser", "Vic Tim", adminID, nil)
	if err != nil {
		t.Fatalf("mint admin-bound token: %v", err)
	}
	email, _, err := srv.verifyIDToken(ctx, adminSvc, tokAdmin)
	if err != nil {
		t.Fatalf("admin-bound token should verify against admin service: %v", err)
	}
	if email != "victim@example.com" {
		t.Fatalf("got email %q, want victim@example.com", email)
	}

	// THE EXPLOIT: a token minted by otherSvc (attacker-controlled) must be
	// rejected when presented against adminSvc.
	tokOther, err := srv.mintFreshbreathToken("identity", "victim@example.com", "Superuser", "Vic Tim", otherID, nil)
	if err != nil {
		t.Fatalf("mint other-bound token: %v", err)
	}
	if _, _, err := srv.verifyIDToken(ctx, adminSvc, tokOther); err == nil {
		t.Fatal("SECURITY: token minted by other service was accepted against admin service")
	}

	// And the same protection through the admin gate used by the central MCP.
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(adminID, 10)); err != nil {
		t.Fatalf("set admin_auth_service: %v", err)
	}
	if _, err := srv.verifyAdminTokenFromBearer(ctx, strconv.FormatInt(adminID, 10), tokOther); err == nil {
		t.Fatal("SECURITY: other-service token passed the admin gate")
	}
	user, err := srv.verifyAdminTokenFromBearer(ctx, strconv.FormatInt(adminID, 10), tokAdmin)
	if err != nil {
		t.Fatalf("admin-bound token should pass the admin gate: %v", err)
	}
	if user.Email != "victim@example.com" {
		t.Fatalf("got user %q, want victim@example.com", user.Email)
	}

	// A wrapped token (non-identity) must never authenticate a user, even if
	// it happens to be bound to the admin service.
	tokWrapped, err := srv.mintFreshbreathToken("wrapped", "victim@example.com", "", "", adminID, &sealedUpstreamData{UpstreamToken: "x"})
	if err != nil {
		t.Fatalf("mint wrapped token: %v", err)
	}
	if _, _, err := srv.verifyIDToken(ctx, adminSvc, tokWrapped); err == nil {
		t.Fatal("SECURITY: a wrapped token authenticated as an identity")
	}
}
