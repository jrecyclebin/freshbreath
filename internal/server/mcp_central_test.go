package server

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

func TestMCPCreateAppWithOwnerEmail(t *testing.T) {
	srv := newTestServer(t)
	owner, err := srv.store.CreateUser("App Owner", "appowner@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	res := callCentralTool(t, srv, "create_app", map[string]interface{}{
		"name":        "email-owned",
		"environment": "Development",
		"url":         "http://example.com",
		"owner_email": owner.Email,
	})
	if res.IsError {
		t.Fatalf("create_app failed: %s", toolResultText(t, res))
	}

	var createRes struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &createRes); err != nil {
		t.Fatalf("parse create result: %v", err)
	}

	app, err := srv.store.GetApp(createRes.Nonce)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.OwnerID == nil || *app.OwnerID != owner.ID {
		t.Fatalf("owner = %v, want %d", app.OwnerID, owner.ID)
	}
}

func TestMCPUpdateAppWithOwnerEmail(t *testing.T) {
	srv := newTestServer(t)
	admin := &db.User{ID: 1, Role: "Superuser"}
	nonce, err := srv.coreCreateApp(admin, "update-owner", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	newOwner, err := srv.store.CreateUser("New Owner", "newowner@example.com", "Member", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	res := callCentralTool(t, srv, "update_app", map[string]interface{}{
		"nonce":       nonce,
		"name":        "update-owner",
		"owner_email": newOwner.Email,
	})
	if res.IsError {
		t.Fatalf("update_app failed: %s", toolResultText(t, res))
	}

	app, err := srv.store.GetApp(nonce)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.OwnerID == nil || *app.OwnerID != newOwner.ID {
		t.Fatalf("owner = %v, want %d", app.OwnerID, newOwner.ID)
	}
}

func TestMCPServiceToolsByName(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "named-svc", "http://example.com", db.ServiceDescriptor{Type: "api"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	res := callCentralTool(t, srv, "get_service", map[string]interface{}{"name": svc.Name})
	if res.IsError {
		t.Fatalf("get_service failed: %s", toolResultText(t, res))
	}
	if !strings.Contains(toolResultText(t, res), "named-svc") {
		t.Errorf("get_service result missing service name: %s", toolResultText(t, res))
	}

	res = callCentralTool(t, srv, "update_service", map[string]interface{}{
		"name":     svc.Name,
		"new_name": "renamed-svc",
		"url":      "http://renamed.example.com",
	})
	if res.IsError {
		t.Fatalf("update_service failed: %s", toolResultText(t, res))
	}

	renamed, err := srv.store.GetService(svc.ID)
	if err != nil {
		t.Fatalf("get renamed service: %v", err)
	}
	if renamed.Name != "renamed-svc" || renamed.URL != "http://renamed.example.com" {
		t.Errorf("service = %+v, want renamed-svc at http://renamed.example.com", renamed)
	}

	res = callCentralTool(t, srv, "get_service_apps", map[string]interface{}{"name": "renamed-svc"})
	if res.IsError {
		t.Fatalf("get_service_apps failed: %s", toolResultText(t, res))
	}

	res = callCentralTool(t, srv, "delete_service", map[string]interface{}{"name": "renamed-svc"})
	if res.IsError {
		t.Fatalf("delete_service failed: %s", toolResultText(t, res))
	}
	if _, err := srv.store.GetService(svc.ID); err == nil {
		t.Fatal("expected service to be deleted")
	}
}

func TestMCPServiceByNameMissing(t *testing.T) {
	srv := newTestServer(t)
	res := callCentralTool(t, srv, "get_service", map[string]interface{}{"name": "no-such-svc"})
	if !res.IsError {
		t.Fatal("expected error for missing service name")
	}
	if !strings.Contains(toolResultText(t, res), "service not found") {
		t.Errorf("error = %q, want service not found", toolResultText(t, res))
	}
}

func TestMCPServiceByNameMissingRequired(t *testing.T) {
	srv := newTestServer(t)
	res := callCentralTool(t, srv, "get_service", map[string]interface{}{})
	if !res.IsError {
		t.Fatal("expected error for missing name argument")
	}
}

func TestMCPCreateAppUnknownOwnerEmail(t *testing.T) {
	srv := newTestServer(t)
	res := callCentralTool(t, srv, "create_app", map[string]interface{}{
		"name":        "orphan-app",
		"owner_email": "nobody@example.com",
	})
	if !res.IsError {
		t.Fatal("expected error for unknown owner email")
	}
	if !strings.Contains(toolResultText(t, res), "owner not found") {
		t.Errorf("error = %q, want owner not found", toolResultText(t, res))
	}
}

// ── Auth record tools ──
//
// Slots are only useful if an agent can also create what they point at.

func TestMCPAuthRecordLifecycle(t *testing.T) {
	srv := newTestServer(t)
	admin := &db.User{ID: 1, Email: "admin@example.com", Role: "Superuser"}

	rec, err := srv.coreCreateAuth(admin, "GitHub App", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		ClientID:     "cid", ClientSecret: "shh", Provider: "github",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	found, err := srv.authRecordByName("github app")
	if err != nil {
		t.Fatalf("lookup by name is case-insensitive: %v", err)
	}
	if found.ID != rec.ID {
		t.Errorf("found id %d, want %d", found.ID, rec.ID)
	}
	if _, err := srv.authRecordByName("nope"); err == nil {
		t.Error("expected an error for an unknown name")
	}
	if _, err := srv.authRecordByName(""); err == nil {
		t.Error("expected an error for an empty name")
	}

	// The patch shape update_auth relies on: an omitted secret keeps the
	// stored one rather than blanking it.
	desc := found.Descriptor
	desc.Scopes = "repo"
	desc.ClientSecret = ""
	if err := srv.coreUpdateAuth(admin, rec.ID, "GitHub App", db.AuthOAuth2, desc); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := srv.store.GetAuthRecord(rec.ID)
	if after.Descriptor.ClientSecret != "shh" {
		t.Errorf("client secret = %q, want the stored one kept", after.Descriptor.ClientSecret)
	}
	if after.Descriptor.Scopes != "repo" {
		t.Errorf("scopes = %q, want repo", after.Descriptor.Scopes)
	}

	// A record in use cannot be deleted out from under the thing using it.
	id := registerService(t, srv, "gh", "https://api.github.com", db.ServiceDescriptor{Type: "api"})
	setServiceActsAs(t, srv, id, rec.ID)
	if err := srv.coreDeleteAuth(admin, rec.ID); err == nil {
		t.Error("expected delete to be refused while a service points at the record")
	}

	// Unassign it (a pointer to 0 is still a reference — the slot has to
	// go back to nil) and the delete goes through.
	sid, _ := strconv.ParseInt(id, 10, 64)
	svc, _ := srv.store.GetService(sid)
	if err := srv.store.UpdateService(sid, svc.Name, svc.URL, svc.Descriptor, nil, nil); err != nil {
		t.Fatalf("clear slots: %v", err)
	}
	if err := srv.coreDeleteAuth(admin, rec.ID); err != nil {
		t.Fatalf("delete after unassigning: %v", err)
	}
}

func TestMCPAuthToolsGatedToAdmins(t *testing.T) {
	srv := newTestServer(t)
	member := &db.User{ID: 2, Email: "member@example.com", Role: "Member"}
	if _, err := srv.coreCreateAuth(member, "Sneaky", db.AuthAPIKey, db.AuthDescriptor{Key: "k"}); err == nil {
		t.Error("a Member must not be able to create auth records")
	}
}

// The descriptor schema is what an agent reads to decide what to send, so
// a stale property there is a lie that costs a round trip.
func TestMCPServiceDescriptorSchemaHasNoAuthFields(t *testing.T) {
	for _, dead := range []string{"auth", "api_key", "header", "client_id", "client_secret", "oauth_url", "scopes"} {
		if _, ok := serviceDescriptorSchema[dead]; ok {
			t.Errorf("service descriptor schema still advertises %q — auth lives in the slots now", dead)
		}
	}
	for _, live := range []string{"type", "proxied", "database_target", "database_name"} {
		if _, ok := serviceDescriptorSchema[live]; !ok {
			t.Errorf("service descriptor schema is missing %q", live)
		}
	}
}
