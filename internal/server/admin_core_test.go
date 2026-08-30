package server

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// forbidden reports whether err is a *coreErr carrying a 403 status.
func forbidden(err error) bool {
	var ce *coreErr
	return errors.As(err, &ce) && ce.status == http.StatusForbidden
}

// TestCoreAuthority verifies that the core mutating operations self-enforce
// their role tier — the single source of truth now lives on the first line of
// each function (s.gate / s.gateSelfOrAdmin), not in the transports. A
// too-weak actor must get a 403 *before* any store work happens.
func TestCoreAuthority(t *testing.T) {
	srv := newTestServer(t)
	member := &db.User{ID: 1, Role: "Member"}
	other := &db.User{ID: 99, Role: "Member"}

	// Admin+ operations: a Member must be forbidden.
	adminPlusOps := map[string]func() error{
		"coreCreateApp":      func() error { _, e := srv.coreCreateApp(member, "x", "", "", nil, nil); return e },
		"coreUpdateApp":      func() error { return srv.coreUpdateApp(member, "n", "x", "", "", nil, nil) },
		"coreDeleteApp":      func() error { return srv.coreDeleteApp(member, "n") },
		"coreSetAppMembers":  func() error { return srv.coreSetAppMembers(member, "n", nil) },
		"coreSetAppServices": func() error { return srv.coreSetAppServices(member, "n", nil) },
		"coreCreateService":  func() error { _, e := srv.coreCreateService(member, "x", "http://x", db.ServiceDescriptor{}, nil, nil); return e },
		"coreUpdateService":  func() error { return srv.coreUpdateService(member, 1, "x", "http://x", db.ServiceDescriptor{}, nil, nil) },
		"coreDeleteService":  func() error { return srv.coreDeleteService(member, 1) },
		"coreCreateUser":     func() error { _, e := srv.coreCreateUser(member, "n", "e@x", "Member", "Active"); return e },
		"coreUpdateUser":     func() error { return srv.coreUpdateUser(member, 1, "n", "e@x", "Member", "Active", nil) },
		"coreDeleteUser":     func() error { return srv.coreDeleteUser(member, other) },
		"coreSetUserApps":    func() error { return srv.coreSetUserApps(member, 1, nil) },
	}
	for name, op := range adminPlusOps {
		if err := op(); !forbidden(err) {
			t.Errorf("%s as Member: got %v, want 403 forbidden", name, err)
		}
	}

	// Settings is Superuser-only: Member and Admin both forbidden, Superuser passes.
	if err := srv.coreUpdateSettings(member, nil, nil, nil); !forbidden(err) {
		t.Errorf("coreUpdateSettings as Member: got %v, want 403", err)
	}
	if err := srv.coreUpdateSettings(&db.User{ID: 2, Role: "Admin"}, nil, nil, nil); !forbidden(err) {
		t.Errorf("coreUpdateSettings as Admin: got %v, want 403", err)
	}
	if err := srv.coreUpdateSettings(&db.User{ID: 3, Role: "Superuser"}, nil, nil, nil); err != nil {
		t.Errorf("coreUpdateSettings as Superuser: got %v, want pass", err)
	}
}

// TestCoreAuthoritySelfOrAdmin covers the dual-tier SSH-key ops: a user may
// act on their own account regardless of role, but acting on someone else's
// requires Admin+. We assert via the gate outcome — operating on self/admin
// gets past the 403 (and fails later for a benign reason like "no SSH key"),
// while a Member targeting another user is forbidden outright.
func TestCoreAuthoritySelfOrAdmin(t *testing.T) {
	srv := newTestServer(t)
	member := &db.User{ID: 1, Role: "Member"}
	other := &db.User{ID: 2, Role: "Member"}
	admin := &db.User{ID: 3, Role: "Admin"}

	// Self-service: gate passes (then 404 "no SSH key to delete", not 403).
	if err := srv.coreDeleteSSHKey(member, member); forbidden(err) {
		t.Errorf("coreDeleteSSHKey(self): got 403, want gate to pass")
	}
	// Admin acting on another user: gate passes.
	if err := srv.coreDeleteSSHKey(admin, other); forbidden(err) {
		t.Errorf("coreDeleteSSHKey(admin→other): got 403, want gate to pass")
	}
	// Member acting on another user: forbidden.
	if err := srv.coreDeleteSSHKey(member, other); !forbidden(err) {
		t.Errorf("coreDeleteSSHKey(member→other): got %v, want 403", err)
	}
}

// TestCoreServiceFilesAuthority verifies that service file ops are Admin+ only.
func TestCoreServiceFilesAuthority(t *testing.T) {
	srv := newTestServer(t)
	member := &db.User{ID: 1, Role: "Member"}
	admin := &db.User{ID: 2, Role: "Admin"}

	svc, err := srv.coreCreateService(admin, "tasksvc", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatalf("create tasks service: %v", err)
	}

	adminPlusOps := map[string]func() error{
		"coreDownloadServiceFiles": func() error { _, _, e := srv.coreDownloadServiceFiles(member, svc.ID); return e },
		"coreUploadServiceFiles":   func() error { _, e := srv.coreUploadServiceFiles(member, svc.ID, []byte("x"), "x.txt"); return e },
		"coreDeleteServiceFiles":   func() error { return srv.coreDeleteServiceFiles(member, svc.ID) },
	}
	for name, op := range adminPlusOps {
		if err := op(); !forbidden(err) {
			t.Errorf("%s as Member: got %v, want 403 forbidden", name, err)
		}
	}
}

// TestServiceFileTasksCRUD exercises upload/download/delete for a tasks service,
// verifying the file lands at tasks/<name>.txt and is served as plain text.
func TestServiceFileTasksCRUD(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Admin"}

	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	content := []byte("[build]\nmake all\n")

	// Upload.
	route, err := srv.coreUploadServiceFiles(admin, svc.ID, content, "deploy.txt")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if route != svc.URL {
		t.Errorf("route = %q, want %q", route, svc.URL)
	}

	// File exists at legacy path.
	path := filepath.Join(srv.config.DataDir, "tasks", svc.Name+".txt")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read legacy file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("legacy file = %q, want %q", got, content)
	}

	// Download returns raw content.
	data, filename, err := srv.coreDownloadServiceFiles(admin, svc.ID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Errorf("download data = %q, want %q", data, content)
	}
	if filename != svc.Name+".txt" {
		t.Errorf("filename = %q, want %q", filename, svc.Name+".txt")
	}

	// Delete removes the file.
	if err := srv.coreDeleteServiceFiles(admin, svc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed")
	}

	// Download after delete is 404.
	if _, _, err := srv.coreDownloadServiceFiles(admin, svc.ID); err == nil {
		t.Errorf("download after delete should fail")
	}
}

// TestServiceFileVirtualReload verifies that uploading to a virtual service
// reloads its in-memory MCP registry, and deleting removes it.
func TestServiceFileVirtualReload(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Admin"}

	name := "greeter"
	svc, err := srv.coreCreateService(admin, name, "", db.ServiceDescriptor{Type: "virtual"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	content := []byte("[hello] Say hello\necho hi $name\n---\n")
	if _, err := srv.coreUploadServiceFiles(admin, svc.ID, content, "greeter.txt"); err != nil {
		t.Fatalf("upload: %v", err)
	}

	slug := strings.TrimPrefix(svc.URL, "/mcp/")
	if srv.virtualMCPs.get(slug) == nil {
		t.Errorf("virtual MCP registry should contain %q after upload", slug)
	}

	if err := srv.coreDeleteServiceFiles(admin, svc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if srv.virtualMCPs.get(slug) != nil {
		t.Errorf("virtual MCP registry should not contain %q after delete", slug)
	}
}

// TestServiceFileUnsupportedType verifies that non-tasks/virtual services reject
// file operations.
func TestServiceFileUnsupportedType(t *testing.T) {
	srv := newTestServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}

	svc, err := srv.coreCreateService(admin, "api-svc", "http://example.com", db.ServiceDescriptor{Type: "api"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	if _, _, err := srv.coreDownloadServiceFiles(admin, svc.ID); err == nil {
		t.Errorf("download for api service should fail")
	}
	if _, err := srv.coreUploadServiceFiles(admin, svc.ID, []byte("x"), "x.txt"); err == nil {
		t.Errorf("upload for api service should fail")
	}
	if err := srv.coreDeleteServiceFiles(admin, svc.ID); err == nil {
		t.Errorf("delete for api service should fail")
	}
}

// TestServiceFileNoZip verifies that tasks/virtual services reject zip uploads.
func TestServiceFileNoZip(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Admin"}

	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	if _, err := srv.coreUploadServiceFiles(admin, svc.ID, []byte("PK"), "site.zip"); err == nil {
		t.Errorf("zip upload for tasks service should fail")
	}
}

// TestGateApp verifies that app-level web ops allow Admin+ and app members,
// but reject non-members.
func TestGateApp(t *testing.T) {
	srv := newTestServer(t)

	// Create an app and two users: one member, one not.
	admin := &db.User{ID: 1, Role: "Admin"}
	member := &db.User{ID: 2, Role: "Member"}
	outsider := &db.User{ID: 3, Role: "Member"}

	nonce, err := srv.coreCreateApp(admin, "gate-test", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	member, err = srv.coreCreateUser(admin, "member", "member@x", "Member", "Active")
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	outsider, err = srv.coreCreateUser(admin, "outsider", "outsider@x", "Member", "Active")
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	if err := srv.coreSetAppMembers(admin, nonce, []int64{member.ID}); err != nil {
		t.Fatalf("set members: %v", err)
	}

	cases := []struct {
		name   string
		actor  *db.User
		op     func() error
		wantOK bool
	}{
		{"list: admin", admin, func() error { _, e := srv.coreListAppWeb(admin, nonce, ""); return e }, true},
		{"list: member", member, func() error { _, e := srv.coreListAppWeb(member, nonce, ""); return e }, true},
		{"list: outsider", outsider, func() error { _, e := srv.coreListAppWeb(outsider, nonce, ""); return e }, false},
		{"upload: admin", admin, func() error { _, e := srv.coreUploadAppWeb(admin, nonce, []byte("<h1>x</h1>"), "index.html"); return e }, true},
		{"upload: member", member, func() error {
			_, e := srv.coreUploadAppWeb(member, nonce, []byte("<h1>x</h1>"), "index.html")
			return e
		}, true},
		{"upload: outsider", outsider, func() error {
			_, e := srv.coreUploadAppWeb(outsider, nonce, []byte("<h1>x</h1>"), "index.html")
			return e
		}, false},
		{"download: admin", admin, func() error { _, _, e := srv.coreDownloadAppWeb(admin, nonce); return e }, true},
		{"download: member", member, func() error { _, _, e := srv.coreDownloadAppWeb(member, nonce); return e }, true},
		{"download: outsider", outsider, func() error { _, _, e := srv.coreDownloadAppWeb(outsider, nonce); return e }, false},
		{"delete: admin", admin, func() error { return srv.coreDeleteAppWeb(admin, nonce) }, true},
		{"delete: member", member, func() error { return srv.coreDeleteAppWeb(member, nonce) }, true},
		{"delete: outsider", outsider, func() error { return srv.coreDeleteAppWeb(outsider, nonce) }, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.op()
			if tc.wantOK && forbidden(err) {
				t.Errorf("got 403, want allowed")
			}
			if !tc.wantOK && !forbidden(err) {
				t.Errorf("got %v, want 403 forbidden", err)
			}
		})
	}
}
