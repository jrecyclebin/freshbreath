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
		"coreListAppWeb":     func() error { _, e := srv.coreListAppWeb(member, "n"); return e },
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
