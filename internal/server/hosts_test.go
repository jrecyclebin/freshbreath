package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
)

// ── Hosts ──

func addHost(t *testing.T, srv *Server, as *db.User, body string) *db.Host {
	t.Helper()
	rr := requestAs(t, srv, as, "POST", "/api/hosts", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add host: %d %s", rr.Code, rr.Body.String())
	}
	var h db.Host
	json.Unmarshal(rr.Body.Bytes(), &h)
	return &h
}

// Admins keep the list; Members can't change it, and see only the hosts
// they're members of (none, here).
func TestHostsAreForAdmins(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	member := mustUser(t, srv, "Mo", "Member", "Active")
	h := addHost(t, srv, admin, fmt.Sprintf(`{"name":"blog.example.com","owner_id":%d}`, admin.ID))

	if rr := requestAs(t, srv, member, "GET", "/api/hosts", ""); rr.Code != 200 || strings.Contains(rr.Body.String(), "blog.example.com") {
		t.Errorf("Member lists hosts: %d %s, want 200 with none of them", rr.Code, rr.Body.String())
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/hosts", fmt.Sprintf(`{"name":"x.example.com","owner_id":%d}`, admin.ID)},
		{"PUT", "/api/hosts/" + h.ID, fmt.Sprintf(`{"name":"y.example.com","owner_id":%d}`, admin.ID)},
		{"DELETE", "/api/hosts/" + h.ID, ""},
	} {
		if rr := requestAs(t, srv, member, c.method, c.path, c.body); rr.Code != http.StatusForbidden {
			t.Errorf("Member %s %s: %d, want 403", c.method, c.path, rr.Code)
		}
	}
	if got, _ := srv.store.ListHosts(); len(got) != 1 || got[0].Name != "blog.example.com" {
		t.Errorf("hosts after Member attempts: %+v", got)
	}
}

// A host round-trips whole: name cleaned up, owner, gate, members.
func TestHostRoundTrips(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	mo := mustUser(t, srv, "Mo", "Member", "Active")
	ro := mustUser(t, srv, "Rita", "Read-only", "Active")
	gate := builtinAuth(t, srv, db.AuthAnonymous)

	h := addHost(t, srv, admin, fmt.Sprintf(
		`{"name":"  Blog.Example.COM. ","owner_id":%d,"protected_by":%d,"members":[%d,%d,%d]}`,
		admin.ID, gate.ID, mo.ID, ro.ID, admin.ID))
	if h.Name != "blog.example.com" || h.OwnerID == nil || *h.OwnerID != admin.ID ||
		h.ProtectedBy == nil || *h.ProtectedBy != gate.ID {
		t.Errorf("created host: %+v", h)
	}
	// Only Member accounts stay: Read-only can't create, Admins already can.
	if len(h.Members) != 1 || h.Members[0] != mo.ID {
		t.Errorf("members = %v, want just Mo (%d)", h.Members, mo.ID)
	}

	rr := requestAs(t, srv, admin, "PUT", "/api/hosts/"+h.ID, fmt.Sprintf(`{"name":"notes.example.com","owner_id":%d,"members":[]}`, admin.ID))
	if rr.Code != 200 {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := srv.store.GetHost(h.ID)
	if got.Name != "notes.example.com" || got.ProtectedBy != nil || len(got.Members) != 0 {
		t.Errorf("updated host: %+v", got)
	}

	rr = requestAs(t, srv, admin, "GET", "/api/hosts", "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "notes.example.com") {
		t.Errorf("list: %d %s", rr.Code, rr.Body.String())
	}
}

func TestHostIsRefused(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	mo := mustUser(t, srv, "Mo", "Member", "Active")
	addHost(t, srv, admin, fmt.Sprintf(`{"name":"taken.example.com","owner_id":%d}`, admin.ID))

	for _, c := range []struct {
		why, body string
		want      int
	}{
		{"no owner", `{"name":"a.example.com"}`, 400},
		{"Member owner", fmt.Sprintf(`{"name":"a.example.com","owner_id":%d}`, mo.ID), 400},
		{"scheme", fmt.Sprintf(`{"name":"https://a.example.com","owner_id":%d}`, admin.ID), 400},
		{"port", fmt.Sprintf(`{"name":"a.example.com:8080","owner_id":%d}`, admin.ID), 400},
		{"path", fmt.Sprintf(`{"name":"a.example.com/blog","owner_id":%d}`, admin.ID), 400},
		{"bad label", fmt.Sprintf(`{"name":"-a.example.com","owner_id":%d}`, admin.ID), 400},
		{"IP", fmt.Sprintf(`{"name":"10.0.0.1","owner_id":%d}`, admin.ID), 400},
		{"localhost", fmt.Sprintf(`{"name":"app.localhost","owner_id":%d}`, admin.ID), 400},
		{"our own name", fmt.Sprintf(`{"name":"example.com","owner_id":%d}`, admin.ID), 400},
		{"unknown gate", fmt.Sprintf(`{"name":"a.example.com","owner_id":%d,"protected_by":99999}`, admin.ID), 400},
		{"default app not on it", fmt.Sprintf(`{"name":"a.example.com","owner_id":%d,"default_app":"nope"}`, admin.ID), 400},
		{"duplicate, any case", fmt.Sprintf(`{"name":"TAKEN.example.com","owner_id":%d}`, admin.ID), 409},
	} {
		if rr := requestAs(t, srv, admin, "POST", "/api/hosts", c.body); rr.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.why, rr.Code, rr.Body.String(), c.want)
		}
	}
}

// A host apps are on can't be deleted or renamed out from under them, and
// its gate's auth record can't be deleted either.
func TestHostInUseStays(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	gate := newAuthRecord(t, srv, "Readers", db.AuthAPIKey, db.AuthDescriptor{})
	h := addHost(t, srv, admin, fmt.Sprintf(`{"name":"blog.example.com","owner_id":%d,"protected_by":%d}`, admin.ID, gate.ID))
	if _, err := srv.store.CreateApp("Notes", "Development", "https://blog.example.com/notes", nil, nil); err != nil {
		t.Fatal(err)
	}

	if rr := requestAs(t, srv, admin, "DELETE", "/api/hosts/"+h.ID, ""); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "Notes") {
		t.Errorf("delete in use: %d %s, want 409 naming Notes", rr.Code, rr.Body.String())
	}
	rr := requestAs(t, srv, admin, "PUT", "/api/hosts/"+h.ID, fmt.Sprintf(`{"name":"other.example.com","owner_id":%d}`, admin.ID))
	if rr.Code != http.StatusConflict {
		t.Errorf("rename in use: %d %s, want 409", rr.Code, rr.Body.String())
	}
	if err := srv.store.DeleteAuthRecord(gate.ID); err == nil {
		t.Error("deleted an auth record a host gates with")
	}
}

// Deleting a user takes them off host member lists and clears ownership.
func TestHostForgetsDeletedUsers(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	owner := mustUser(t, srv, "Olga", "Admin", "Active")
	mo := mustUser(t, srv, "Mo", "Member", "Active")
	h := addHost(t, srv, admin, fmt.Sprintf(`{"name":"blog.example.com","owner_id":%d,"members":[%d]}`, owner.ID, mo.ID))
	srv.store.DeleteUser(mo.ID)
	srv.store.DeleteUser(owner.ID)
	got, _ := srv.store.GetHost(h.ID)
	if len(got.Members) != 0 || got.OwnerID != nil {
		t.Errorf("host after deletes: %+v", got)
	}
}

// The list is the whole truth about other names: unlisted gets a 421,
// listed is answered as an app host (never the panel), and deleting puts
// it back to 421.
func TestOnlyListedHostsAreAnswered(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	if rr := hostRequest(srv, "GET", "https://blog.example.com/", nil); rr.Code != http.StatusMisdirectedRequest {
		t.Fatalf("unlisted: %d, want 421", rr.Code)
	}

	h := addHost(t, srv, admin, fmt.Sprintf(`{"name":"blog.example.com","owner_id":%d}`, admin.ID))
	if rr := hostRequest(srv, "GET", "https://blog.example.com/", nil); rr.Code != http.StatusNotFound {
		t.Errorf("listed, no apps: %d, want 404", rr.Code)
	}
	if rr := hostRequest(srv, "GET", "https://blog.example.com/control/", nil); rr.Code != http.StatusNotFound {
		t.Errorf("panel on a listed host: %d, want 404", rr.Code)
	}

	if rr := requestAs(t, srv, admin, "DELETE", "/api/hosts/"+h.ID, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	if rr := hostRequest(srv, "GET", "https://blog.example.com/", nil); rr.Code != http.StatusMisdirectedRequest {
		t.Errorf("after delete: %d, want 421", rr.Code)
	}
}

// ── Members creating apps on their hosts ──

// memberHostSetup lists blog.example.org, owned by Olga (Admin), gated by
// a key, with Mo as its member; shop.example.org has no members.
func memberHostSetup(t *testing.T, srv *Server) (owner, mo *db.User, gate *db.AuthRecord) {
	t.Helper()
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	owner = mustUser(t, srv, "Olga", "Admin", "Active")
	mo = mustUser(t, srv, "Mo", "Member", "Active")
	gate = newAuthRecord(t, srv, "Readers", db.AuthAPIKey, db.AuthDescriptor{Key: "k"})
	addHost(t, srv, admin, fmt.Sprintf(`{"name":"blog.example.org","owner_id":%d,"protected_by":%d,"members":[%d]}`, owner.ID, gate.ID, mo.ID))
	addHost(t, srv, admin, fmt.Sprintf(`{"name":"shop.example.org","owner_id":%d}`, admin.ID))
	return owner, mo, gate
}

// A Member creates an app one level down on their host. It takes the
// host's owner and inherits the host's gate, whatever they asked for, and
// they become its member.
func TestMemberCreatesAppOnTheirHost(t *testing.T) {
	srv := newTestServer(t)
	owner, mo, gate := memberHostSetup(t, srv)
	anon := builtinAuth(t, srv, db.AuthAnonymous)

	body := fmt.Sprintf(`{"name":"Notes","url":"https://blog.example.org/notes","owner_id":%d,"protected_by":%d}`, mo.ID, anon.ID)
	rr := requestAs(t, srv, mo, "POST", "/api/apps", body)
	if rr.Code != 200 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]string
	json.Unmarshal(rr.Body.Bytes(), &res)
	app, err := srv.store.GetApp(res["nonce"])
	if err != nil {
		t.Fatal(err)
	}
	if app.OwnerID == nil || *app.OwnerID != owner.ID {
		t.Errorf("owner = %v, want the host's owner %d", app.OwnerID, owner.ID)
	}
	if app.ProtectedBy != nil {
		t.Errorf("protected_by = %v, want nil (inherit)", *app.ProtectedBy)
	}
	if rec, _ := srv.resolveAppGate(app); rec == nil || rec.ID != gate.ID {
		t.Errorf("gate in force = %+v, want the host's %q", rec, gate.Name)
	}
	if ok, _ := srv.store.IsAppMember(app.Nonce, mo.ID); !ok {
		t.Error("the Member isn't a member of the app they created")
	}
	if rr := requestAs(t, srv, mo, "GET", "/api/apps", ""); !strings.Contains(rr.Body.String(), app.Nonce) {
		t.Error("the Member can't see the app they created")
	}
}

func TestMemberCreatesAppsOnlyOnTheirHosts(t *testing.T) {
	srv := newTestServer(t)
	_, mo, _ := memberHostSetup(t, srv)
	stranger := mustUser(t, srv, "Sam", "Member", "Active")
	ro := mustUser(t, srv, "Rita", "Read-only", "Active")

	for _, c := range []struct {
		why  string
		as   *db.User
		url  string
		want int
	}{
		{"someone else's host", mo, "https://shop.example.org/notes", 403},
		{"a slug here", mo, "notes", 403},
		{"blank", mo, "", 403},
		{"runs elsewhere", mo, "https://myapp.netlify.app", 403},
		{"the host's root", mo, "https://blog.example.org", 400},
		{"on no host at all", stranger, "https://blog.example.org/notes", 403},
		{"Read-only", ro, "https://blog.example.org/notes", 403},
	} {
		rr := requestAs(t, srv, c.as, "POST", "/api/apps", `{"name":"X","url":"`+c.url+`"}`)
		if rr.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.why, rr.Code, rr.Body.String(), c.want)
		}
	}
	if apps, _ := srv.store.ListHostedApps(); len(apps) != 0 {
		t.Errorf("refused creates left %d apps behind", len(apps))
	}
}

// A Member sees just their own hosts, and can't add one.
func TestMemberSeesTheirHosts(t *testing.T) {
	srv := newTestServer(t)
	_, mo, _ := memberHostSetup(t, srv)
	rr := requestAs(t, srv, mo, "GET", "/api/hosts", "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "blog.example.org") || strings.Contains(rr.Body.String(), "shop.example.org") {
		t.Errorf("Member's hosts: %d %s, want blog only", rr.Code, rr.Body.String())
	}
	ro := mustUser(t, srv, "Rita", "Read-only", "Active")
	if rr := requestAs(t, srv, ro, "GET", "/api/hosts", ""); rr.Code != 403 {
		t.Errorf("Read-only hosts: %d, want 403", rr.Code)
	}
	if rr := requestAs(t, srv, mo, "POST", "/api/hosts", fmt.Sprintf(`{"name":"mine.example.org","owner_id":%d}`, mo.ID)); rr.Code != 403 {
		t.Errorf("Member adds a host: %d, want 403", rr.Code)
	}
}
