package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"

	"poggers.institute/freshbreath/internal/db"
)

// ── Remotes ──

// testTailcatAddr makes a fresh, valid tailcat address.
func testTailcatAddr(t *testing.T) string {
	t.Helper()
	nk := key.NewNode()
	ci := tailcat.ConnInfo{
		ServerPublic:      tailcat.NodePublic{NodePublic: nk.Public()},
		ServerDiscoPublic: tailcat.DiscoPublicForNode(nk),
		PresharedKey:      tailcat.NewPresharedKey(),
		RegionID:          1,
	}
	return string(ci.Addr())
}

func addRemote(t *testing.T, srv *Server, as *db.User, name, addr string) *db.Remote {
	t.Helper()
	rr := requestAs(t, srv, as, "POST", "/api/remotes", `{"name":"`+name+`","address":"`+addr+`"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add remote: %d %s", rr.Code, rr.Body.String())
	}
	var r db.Remote
	json.Unmarshal(rr.Body.Bytes(), &r)
	return &r
}

// Admins manage remotes; Members don't see them at all.
func TestRemotesAreForAdmins(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	member := mustUser(t, srv, "Mo", "Member", "Active")
	r := addRemote(t, srv, admin, "Client X", testTailcatAddr(t))

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/remotes", ""},
		{"POST", "/api/remotes", `{"name":"Y","address":"` + testTailcatAddr(t) + `"}`},
		{"PUT", "/api/remotes/" + r.ID, `{"name":"Z"}`},
		{"DELETE", "/api/remotes/" + r.ID, ""},
	} {
		if rr := requestAs(t, srv, member, c.method, c.path, c.body); rr.Code != http.StatusForbidden {
			t.Errorf("Member %s %s: %d, want 403", c.method, c.path, rr.Code)
		}
	}
}

// The address goes in and never comes back out; a fingerprint stands in
// for it.
func TestRemoteAddressIsNeverShown(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	addr := testTailcatAddr(t)
	r := addRemote(t, srv, admin, "Client X", addr)
	if r.ID == "" || r.Fingerprint == "" || !strings.HasPrefix(r.Fingerprint, "r1-") {
		t.Errorf("created remote: %+v", r)
	}

	rr := requestAs(t, srv, admin, "GET", "/api/remotes", "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Client X") {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), addr) || strings.Contains(rr.Body.String(), `"address"`) {
		t.Errorf("list shows the address: %s", rr.Body.String())
	}
	if got, _ := srv.store.GetRemote(r.ID); got == nil || got.Address != addr {
		t.Errorf("stored address didn't round-trip")
	}
}

func TestRemoteNeedsATailcatAddress(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	for _, body := range []string{
		`{"name":"X","address":"https://example.com"}`,
		`{"name":"X","address":""}`,
		`{"name":"","address":"` + testTailcatAddr(t) + `"}`,
	} {
		if rr := requestAs(t, srv, admin, "POST", "/api/remotes", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rr.Code)
		}
	}
}

// A remote can be renamed and deleted, but its address can't change: a
// new address is a new remote.
func TestRemoteAddressIsFixed(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	addr := testTailcatAddr(t)
	r := addRemote(t, srv, admin, "Client X", addr)

	if rr := requestAs(t, srv, admin, "PUT", "/api/remotes/"+r.ID, `{"name":"Client X","address":"`+testTailcatAddr(t)+`"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("changing the address: %d, want 400", rr.Code)
	}
	if rr := requestAs(t, srv, admin, "PUT", "/api/remotes/"+r.ID, `{"name":"Client Y"}`); rr.Code != http.StatusNoContent {
		t.Fatalf("rename: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := srv.store.GetRemote(r.ID)
	if got.Name != "Client Y" || got.Address != addr {
		t.Errorf("after rename: %q %v", got.Name, got.Address == addr)
	}

	if rr := requestAs(t, srv, admin, "DELETE", "/api/remotes/"+r.ID, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rr.Code)
	}
	if rr := requestAs(t, srv, admin, "DELETE", "/api/remotes/"+r.ID, ""); rr.Code != http.StatusNotFound {
		t.Errorf("delete again: %d, want 404", rr.Code)
	}
}
