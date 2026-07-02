package main

import (
	"errors"
	"net/http"
	"testing"
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
	member := &User{ID: 1, Role: "Member"}
	other := &User{ID: 99, Role: "Member"}

	// Admin+ operations: a Member must be forbidden.
	adminPlusOps := map[string]func() error{
		"coreCreateApp":      func() error { _, e := srv.coreCreateApp(member, "x", "", "", nil); return e },
		"coreUpdateApp":      func() error { return srv.coreUpdateApp(member, "n", "x", "", "", nil) },
		"coreDeleteApp":      func() error { return srv.coreDeleteApp(member, "n") },
		"coreSetAppMembers":  func() error { return srv.coreSetAppMembers(member, "n", nil) },
		"coreSetAppServices": func() error { return srv.coreSetAppServices(member, "n", nil) },
		"coreCreateService":  func() error { _, e := srv.coreCreateService(member, "x", "http://x", ServiceDescriptor{}); return e },
		"coreUpdateService":  func() error { return srv.coreUpdateService(member, 1, "x", "http://x", ServiceDescriptor{}) },
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
	if err := srv.coreUpdateSettings(member, nil, nil); !forbidden(err) {
		t.Errorf("coreUpdateSettings as Member: got %v, want 403", err)
	}
	if err := srv.coreUpdateSettings(&User{ID: 2, Role: "Admin"}, nil, nil); !forbidden(err) {
		t.Errorf("coreUpdateSettings as Admin: got %v, want 403", err)
	}
	if err := srv.coreUpdateSettings(&User{ID: 3, Role: "Superuser"}, nil, nil); err != nil {
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
	member := &User{ID: 1, Role: "Member"}
	other := &User{ID: 2, Role: "Member"}
	admin := &User{ID: 3, Role: "Admin"}

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

// TestGateApp verifies that app-level web ops allow Admin+ and app members,
// but reject non-members.
func TestGateApp(t *testing.T) {
	srv := newTestServer(t)

	// Create an app and two users: one member, one not.
	admin := &User{ID: 1, Role: "Admin"}
	member := &User{ID: 2, Role: "Member"}
	outsider := &User{ID: 3, Role: "Member"}

	nonce, err := srv.coreCreateApp(admin, "gate-test", "", "", nil)
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
		actor  *User
		op     func() error
		wantOK bool
	}{
		{"list: admin", admin, func() error { _, e := srv.coreListAppWeb(admin, nonce); return e }, true},
		{"list: member", member, func() error { _, e := srv.coreListAppWeb(member, nonce); return e }, true},
		{"list: outsider", outsider, func() error { _, e := srv.coreListAppWeb(outsider, nonce); return e }, false},
		{"upload: admin", admin, func() error { _, e := srv.coreUploadAppWeb(admin, nonce, []byte("<h1>x</h1>"), "index.html"); return e }, true},
		{"upload: member", member, func() error { _, e := srv.coreUploadAppWeb(member, nonce, []byte("<h1>x</h1>"), "index.html"); return e }, true},
		{"upload: outsider", outsider, func() error { _, e := srv.coreUploadAppWeb(outsider, nonce, []byte("<h1>x</h1>"), "index.html"); return e }, false},
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
