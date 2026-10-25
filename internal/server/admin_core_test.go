package server

import (
	"archive/zip"
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
		"coreCreateService": func() error {
			_, e := srv.coreCreateService(member, "x", "http://x", db.ServiceDescriptor{}, nil, nil)
			return e
		},
		"coreUpdateService": func() error {
			return srv.coreUpdateService(member, 1, "x", "http://x", db.ServiceDescriptor{}, nil, nil)
		},
		"coreDeleteService": func() error { return srv.coreDeleteService(member, 1) },
		"coreCreateUser":    func() error { _, e := srv.coreCreateUser(member, "n", "e@x", "Member", "Active"); return e },
		"coreUpdateUser":    func() error { return srv.coreUpdateUser(member, 1, "n", "e@x", "Member", "Active", nil) },
		"coreDeleteUser":    func() error { return srv.coreDeleteUser(member, other) },
		"coreSetUserApps":   func() error { return srv.coreSetUserApps(member, 1, nil) },
	}
	for name, op := range adminPlusOps {
		if err := op(); !forbidden(err) {
			t.Errorf("%s as Member: got %v, want 403 forbidden", name, err)
		}
	}

	// Settings is Superuser-only: Member and Admin both forbidden, Superuser passes.
	if err := srv.coreUpdateSettings(member, nil, nil, nil, nil); !forbidden(err) {
		t.Errorf("coreUpdateSettings as Member: got %v, want 403", err)
	}
	if err := srv.coreUpdateSettings(&db.User{ID: 2, Role: "Admin"}, nil, nil, nil, nil); !forbidden(err) {
		t.Errorf("coreUpdateSettings as Admin: got %v, want 403", err)
	}
	if err := srv.coreUpdateSettings(&db.User{ID: 3, Role: "Superuser"}, nil, nil, nil, nil); err != nil {
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

// TestCoreServiceFilesAuthority verifies that task service files are
// Superuser only: neither a Member nor an Admin may read or change them.
func TestCoreServiceFilesAuthority(t *testing.T) {
	srv := newTestServer(t)
	member := &db.User{ID: 1, Role: "Member"}
	admin := &db.User{ID: 2, Role: "Admin"}
	super := &db.User{ID: 3, Role: "Superuser"}

	if _, err := srv.coreCreateService(admin, "tasksvc", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil); !forbidden(err) {
		t.Fatalf("create tasks service as Admin: got %v, want 403", err)
	}
	svc, err := srv.coreCreateService(super, "tasksvc", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatalf("create tasks service: %v", err)
	}

	for _, actor := range []*db.User{member, admin} {
		ops := map[string]func() error{
			"coreDownloadServiceFiles": func() error { _, _, e := srv.coreDownloadServiceFiles(actor, svc.ID); return e },
			"coreUploadServiceFiles":   func() error { _, e := srv.coreUploadServiceFiles(actor, svc.ID, []byte("x"), "x.txt"); return e },
			"coreDeleteServiceFiles":   func() error { return srv.coreDeleteServiceFiles(actor, svc.ID) },
			"coreUpdateService": func() error {
				return srv.coreUpdateService(actor, svc.ID, svc.Name, svc.URL, svc.Descriptor, nil, nil)
			},
			"coreDeleteService": func() error { return srv.coreDeleteService(actor, svc.ID) },
		}
		for name, op := range ops {
			if err := op(); !forbidden(err) {
				t.Errorf("%s as %s: got %v, want 403 forbidden", name, actor.Role, err)
			}
		}
	}

	// An Admin can't turn a virtual service into a task service either.
	v, err := srv.coreCreateService(admin, "virt", "", db.ServiceDescriptor{Type: "virtual"}, nil, nil)
	if err != nil {
		t.Fatalf("create virtual service: %v", err)
	}
	if err := srv.coreUpdateService(admin, v.ID, v.Name, "", db.ServiceDescriptor{Type: "tasks"}, nil, nil); !forbidden(err) {
		t.Errorf("virtual → tasks as Admin: got %v, want 403", err)
	}
}

// TestServiceFileTasksCRUD exercises upload/download/delete for a tasks service,
// verifying the file lands at tasks/<name>.txt and is served as plain text.
func TestServiceFileTasksCRUD(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Superuser"} // task services are Superuser-only

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
	if srv.mcpMounts.get(slug) == nil {
		t.Errorf("virtual MCP registry should contain %q after upload", slug)
	}

	if err := srv.coreDeleteServiceFiles(admin, svc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if srv.mcpMounts.get(slug) != nil {
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
	admin := &db.User{ID: 1, Role: "Superuser"} // task services are Superuser-only

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
		{"list: admin", admin, func() error { _, e := srv.coreListAppFiles(admin, nonce, "", ""); return e }, true},
		{"list: member", member, func() error { _, e := srv.coreListAppFiles(member, nonce, "", ""); return e }, true},
		{"list: outsider", outsider, func() error { _, e := srv.coreListAppFiles(outsider, nonce, "", ""); return e }, false},
		{"upload: admin", admin, func() error {
			_, e := srv.coreUploadAppWeb(admin, nonce, []uploadFile{{Name: "index.html", Data: []byte("<h1>x</h1>")}})
			return e
		}, true},
		{"upload: member", member, func() error {
			_, e := srv.coreUploadAppWeb(member, nonce, []uploadFile{{Name: "index.html", Data: []byte("<h1>x</h1>")}})
			return e
		}, true},
		{"upload: outsider", outsider, func() error {
			_, e := srv.coreUploadAppWeb(outsider, nonce, []uploadFile{{Name: "index.html", Data: []byte("<h1>x</h1>")}})
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

// A zip inside an app upload is capped on its uncompressed total — the
// 100MB request body says nothing about what lands on disk. The cap is
// checked against the central directory before the old web dir is
// cleared, so a refused upload leaves the app as it was.
func TestUploadZipExtractCap(t *testing.T) {
	srv := newTestServer(t)
	su := &db.User{ID: 1, Role: "Superuser"}
	nonce := createApp(t, srv, "zipcap")
	cleanupSlotApp(t, srv, nonce)

	// Establish the current web dir.
	if _, err := srv.coreUploadAppWeb(su, nonce, []uploadFile{{Name: "index.html", Data: []byte("<h1>old</h1>")}}); err != nil {
		t.Fatalf("initial upload: %v", err)
	}

	// A zip whose central directory declares past-cap sizes (raw entries,
	// so no bomb has to actually be built to prove the check).
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fh := &zip.FileHeader{Name: "huge.bin", Method: zip.Store}
	fh.SetMode(0644)
	fh.UncompressedSize64 = uploadExtractMaxBytes + 1
	fh.CompressedSize64 = fh.UncompressedSize64
	fh.CRC32 = 0
	if w, err := zw.CreateRaw(fh); err != nil {
		t.Fatalf("CreateRaw: %v", err)
	} else if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("raw write: %v", err)
	}
	zw.Close()

	_, err := srv.coreUploadAppWeb(su, nonce, []uploadFile{{Name: "bomb.zip", Data: buf.Bytes()}})
	if err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("bomb upload err = %v, want the cap refusal", err)
	}

	// Refused before the clear: the old files are still there.
	if got, err := os.ReadFile(filepath.Join(srv.config.DataDir, "apps", nonce, "web", "index.html")); err != nil || string(got) != "<h1>old</h1>" {
		t.Errorf("old web dir disturbed: %q (err %v)", got, err)
	}
}

// F1 (audit 202610): a tasks/virtual definition lives at <dir>/<name>.txt,
// so a name must never walk out of its directory. An Admin naming a
// virtual service "../tasks/backup" used to overwrite a Superuser-only
// task script.
func TestServiceNameStaysInDefinitionDir(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	super := &db.User{ID: 1, Role: "Superuser"}
	admin := &db.User{ID: 2, Role: "Admin"}

	task, err := srv.coreCreateService(super, "backup", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	script := []byte("[run] Run the backup\necho backing-up\n")
	if err := srv.coreWriteServiceFile(super, task.ID, script, ""); err != nil {
		t.Fatalf("write task: %v", err)
	}

	for _, name := range []string{"../tasks/backup", "a/b", `a\b`, "..", ".hidden", "nul\x00byte"} {
		_, err := srv.coreCreateService(admin, name, "", db.ServiceDescriptor{Type: "virtual"}, nil, nil)
		if ce, ok := err.(*coreErr); !ok || ce.status != http.StatusBadRequest {
			t.Errorf("create %q: err = %v, want 400", name, err)
		}
	}
	for _, name := range []string{"My Service", "deploy-2", "a..b"} {
		if _, err := srv.coreCreateService(admin, name, "", db.ServiceDescriptor{Type: "virtual"}, nil, nil); err != nil {
			t.Errorf("create %q: %v", name, err)
		}
	}

	ok, err := srv.coreCreateService(admin, "innocent", "", db.ServiceDescriptor{Type: "virtual"}, nil, nil)
	if err != nil {
		t.Fatalf("create innocent: %v", err)
	}
	err = srv.coreUpdateService(admin, ok.ID, "../tasks/backup", ok.URL, ok.Descriptor, nil, nil)
	if ce, isCore := err.(*coreErr); !isCore || ce.status != http.StatusBadRequest {
		t.Errorf("rename to traversal: err = %v, want 400", err)
	}

	// A row that predates the check still can't write outside virtual/.
	id, err := srv.store.RegisterService("../tasks/backup", "/mcp/old", db.ServiceDescriptor{Type: "virtual"}, nil, nil)
	if err != nil {
		t.Fatalf("register legacy row: %v", err)
	}
	if err := srv.coreWriteServiceFile(admin, id, []byte("[run] x\nid\n"), ""); err == nil {
		t.Error("write through a legacy traversal name succeeded")
	}
	got, err := os.ReadFile(filepath.Join(srv.config.DataDir, "tasks", "backup.txt"))
	if err != nil || !bytes.Equal(got, script) {
		t.Fatalf("task script changed: %q, %v", got, err)
	}
}

// Audit 202610-r3 #3: deleting a user revoked nothing, and its id went to
// the next user created — so the deleted user's token and refresh family
// came back as the newcomer, role and all.
func TestDeletedUserSessionsDieWithIt(t *testing.T) {
	srv := newTestServer(t)
	gate := builtinAuth(t, srv, db.AuthSSHKey)
	makeAdminAuth(t, srv, gate)
	root, _ := srv.store.CreateUser("Root", "root@x.com", "Superuser", "Active")
	leaver, _ := srv.store.CreateUser("Leaver", "leaver@x.com", "Member", "Active")
	tok, err := srv.mintFreshbreathToken(subjectForUser(leaver), leaver.Email, "Member", "", gate.ID, nil, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	fam, _, err := srv.newRefreshFamily(subjectForUser(leaver), gate.ID, "")
	if err != nil {
		t.Fatalf("family: %v", err)
	}

	if err := srv.coreDeleteUser(root, leaver); err != nil {
		t.Fatalf("delete: %v", err)
	}
	newcomer, _ := srv.store.CreateUser("Newcomer", "new@x.com", "Admin", "Active")
	if newcomer.ID == leaver.ID {
		t.Errorf("newcomer reused the deleted user's id %d", leaver.ID)
	}
	if rr := testRequest(t, srv, "GET", "/api/me", nil, map[string]string{"Authorization": "Bearer " + tok}); rr.Code != http.StatusUnauthorized {
		t.Errorf("deleted user's token: status %d, want 401", rr.Code)
	}
	if f, ok, _ := srv.store.GetRefreshFamily(fam); !ok || !f.Revoked {
		t.Errorf("deleted user's refresh family survived: %+v", f)
	}
}

// Audit 202610-r3 #5: an Admin could point the admin gate record at a
// provider they control, which then vouches for a Superuser's email — or
// delete it and lock everyone out.
func TestAdminGateRecordIsSuperusers(t *testing.T) {
	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "Corp SSO", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://login.corp.example", ClientID: "c", ClientSecret: "s"})
	other := newAuthRecord(t, srv, "Partner SSO", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://partner.example", ClientID: "c", ClientSecret: "s"})
	makeAdminAuth(t, srv, rec)
	admin, _ := srv.store.CreateUser("Adm", "adm@x.com", "Admin", "Active")
	root, _ := srv.store.CreateUser("Root", "root@x.com", "Superuser", "Active")
	moved := db.AuthDescriptor{Issuer: "https://attacker-idp.example", ClientID: "mine", ClientSecret: "mine"}

	if err := srv.coreUpdateAuth(admin, rec.ID, rec.Name, db.AuthOIDC, moved); err == nil {
		t.Error("Admin re-pointed the admin gate record")
	}
	if err := srv.coreDeleteAuth(admin, rec.ID); err == nil {
		t.Error("Admin deleted the admin gate record")
	}
	if err := srv.coreDeleteAuth(root, rec.ID); err == nil {
		t.Error("Superuser deleted the admin gate record while it is in use")
	}
	if err := srv.coreUpdateAuth(root, rec.ID, rec.Name, db.AuthOIDC, moved); err != nil {
		t.Errorf("Superuser editing the admin gate: %v", err)
	}
	if err := srv.coreUpdateAuth(admin, other.ID, other.Name, db.AuthOIDC, db.AuthDescriptor{Issuer: "https://partner.example", ClientID: "c2"}); err != nil {
		t.Errorf("Admin editing another record: %v", err)
	}
}
