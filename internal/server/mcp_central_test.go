package server

import (
	"encoding/json"
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
	nonce, err := srv.coreCreateApp(admin, "update-owner", "", "", nil)
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
	svc, err := srv.coreCreateService(admin, "named-svc", "http://example.com", db.ServiceDescriptor{Type: "api"})
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
