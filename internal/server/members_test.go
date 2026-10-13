package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
)

// requestAs sends a request with user already authenticated, the way
// handleAct does: authWrap trusts a user it finds in the context.
func requestAs(t *testing.T, srv *Server, user *db.User, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), userKey, user))
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func mustUser(t *testing.T, srv *Server, name, role, status string) *db.User {
	t.Helper()
	u, err := srv.store.CreateUser(name, strings.ToLower(name)+"@example.com", role, status)
	if err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

func TestMemberAccessToControlPanelAPIs(t *testing.T) {
	srv := newTestServer(t)
	member := mustUser(t, srv, "Mel", "Member", "Active")

	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/api/services", "", http.StatusOK},
		{"GET", "/api/auth", "", http.StatusOK},
		{"GET", "/api/roles", "", http.StatusOK},
		{"GET", "/api/audit", "", http.StatusOK},
		{"POST", "/api/services", `{"name":"x","url":"http://x","descriptor":{"type":"api"}}`, http.StatusForbidden},
		{"POST", "/api/auth", `{"name":"x","kind":"api_key"}`, http.StatusForbidden},
		{"GET", "/api/users", "", http.StatusForbidden},
		{"GET", "/api/settings", "", http.StatusForbidden},
	} {
		if rr := requestAs(t, srv, member, c.method, c.path, c.body); rr.Code != c.want {
			t.Errorf("%s %s as Member: got %d, want %d (%s)", c.method, c.path, rr.Code, c.want, rr.Body.String())
		}
	}
}

func TestServiceMemberEditsDefinitionFile(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	member := mustUser(t, srv, "Mel", "Member", "Active")
	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	filePath := "/api/services/" + strconv.FormatInt(svc.ID, 10) + "/files"

	if rr := requestAs(t, srv, member, "PUT", filePath, "# tasks\n"); rr.Code != http.StatusForbidden {
		t.Fatalf("unassigned member write: got %d, want 403", rr.Code)
	}
	if rr := requestAs(t, srv, member, "PUT", "/api/services/"+strconv.FormatInt(svc.ID, 10)+"/members", `{"members":[`+strconv.FormatInt(member.ID, 10)+`]}`); rr.Code != http.StatusForbidden {
		t.Fatalf("member assigning themselves: got %d, want 403", rr.Code)
	}
	if rr := requestAs(t, srv, admin, "PUT", "/api/services/"+strconv.FormatInt(svc.ID, 10)+"/members", `{"members":[`+strconv.FormatInt(member.ID, 10)+`]}`); rr.Code != http.StatusNoContent {
		t.Fatalf("admin assigning member: got %d (%s)", rr.Code, rr.Body.String())
	}

	if rr := requestAs(t, srv, member, "PUT", filePath, "# tasks\n"); rr.Code != http.StatusNoContent {
		t.Fatalf("assigned member write: got %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := requestAs(t, srv, member, "GET", filePath, ""); rr.Code != http.StatusOK || rr.Body.String() != "# tasks\n" {
		t.Fatalf("assigned member read: got %d %q", rr.Code, rr.Body.String())
	}
	// Membership covers the file only, not the service record.
	if rr := requestAs(t, srv, member, "PUT", "/api/services/"+strconv.FormatInt(svc.ID, 10), `{"name":"renamed","descriptor":{"type":"tasks"}}`); rr.Code != http.StatusForbidden {
		t.Fatalf("assigned member updating service: got %d, want 403", rr.Code)
	}

	rr := requestAs(t, srv, member, "GET", "/api/services", "")
	var list struct {
		Services []db.Service `json:"services"`
	}
	json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.Services) == 0 {
		t.Fatalf("no services listed: %s", rr.Body.String())
	}
	for _, s := range list.Services {
		if s.ID == svc.ID && (len(s.Members) != 1 || s.Members[0] != member.ID) {
			t.Errorf("service members = %v, want [%d]", s.Members, member.ID)
		}
	}

	// Deleting the user drops the membership.
	if err := srv.store.DeleteUser(member.ID); err != nil {
		t.Fatal(err)
	}
	if ids, _ := srv.store.ListServiceMembers(svc.ID); len(ids) != 0 {
		t.Errorf("members after user delete = %v, want none", ids)
	}
}

func TestAuditScopedToOwnActivity(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	member := mustUser(t, srv, "Mel", "Member", "Active")
	srv.audit(admin, "did admin things", "x")
	srv.audit(member, "did member things", "y")

	actions := func(u *db.User) []string {
		rr := requestAs(t, srv, u, "GET", "/api/audit", "")
		var out struct {
			Audit []db.AuditEntry `json:"audit"`
		}
		json.Unmarshal(rr.Body.Bytes(), &out)
		var a []string
		for _, e := range out.Audit {
			a = append(a, e.Action)
		}
		return a
	}
	if got := actions(member); len(got) != 1 || got[0] != "did member things" {
		t.Errorf("member audit = %v, want only their own entry", got)
	}
	if got := actions(admin); len(got) != 2 {
		t.Errorf("admin audit = %v, want both entries", got)
	}
}

func TestUpdateOwnProfile(t *testing.T) {
	srv := newTestServer(t)
	member := mustUser(t, srv, "Mel", "Member", "Active")
	mustUser(t, srv, "Other", "Member", "Active")

	rr := requestAs(t, srv, member, "PUT", "/api/me", `{"name":"Melody","email":"melody@example.com","role":"Superuser"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update profile: got %d (%s)", rr.Code, rr.Body.String())
	}
	u, _ := srv.store.GetUser(member.ID)
	if u.Name != "Melody" || u.Email != "melody@example.com" || u.Role != "Member" {
		t.Errorf("after update: %+v, want new name/email and role unchanged", u)
	}

	if rr := requestAs(t, srv, member, "PUT", "/api/me", `{"name":"Melody","email":"other@example.com"}`); rr.Code != http.StatusConflict {
		t.Errorf("taking another user's email: got %d, want 409", rr.Code)
	}
	if rr := requestAs(t, srv, member, "PUT", "/api/me", `{"name":"","email":"x@example.com"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("blank name: got %d, want 400", rr.Code)
	}
}

func TestPassphraseLink(t *testing.T) {
	srv := newTestServer(t)
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	member := mustUser(t, srv, "Mel", "Member", "Active")
	invitee := mustUser(t, srv, "Ivy", "Member", "Invited")

	if rr := requestAs(t, srv, member, "POST", "/api/users/"+strconv.FormatInt(invitee.ID, 10)+"/passphrase-link", ""); rr.Code != http.StatusForbidden {
		t.Fatalf("member minting link: got %d, want 403", rr.Code)
	}
	rr := requestAs(t, srv, admin, "POST", "/api/users/"+strconv.FormatInt(invitee.ID, 10)+"/passphrase-link", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("mint link: got %d (%s)", rr.Code, rr.Body.String())
	}
	var minted struct {
		URL string `json:"url"`
	}
	json.Unmarshal(rr.Body.Bytes(), &minted)
	link, err := url.Parse(minted.URL)
	if err != nil || link.Path != "/service/passphrase" {
		t.Fatalf("link = %q", minted.URL)
	}
	token := link.Query().Get("token")

	// The stored hash never reaches the API.
	if rr := requestAs(t, srv, admin, "GET", "/api/users/"+strconv.FormatInt(invitee.ID, 10), ""); strings.Contains(rr.Body.String(), "token_hash") {
		t.Errorf("user JSON leaks the link hash: %s", rr.Body.String())
	}

	page := testRequest(t, srv, "GET", "/service/passphrase?token="+url.QueryEscape(token), nil, nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "ivy@example.com") {
		t.Fatalf("link page: got %d", page.Code)
	}
	if bad := testRequest(t, srv, "GET", "/service/passphrase?token="+strconv.FormatInt(invitee.ID, 10)+".nope", nil, nil); bad.Code != http.StatusBadRequest {
		t.Errorf("forged token page: got %d, want 400", bad.Code)
	}

	post := func(pass string) int {
		body, _ := json.Marshal(map[string]string{"token": token, "passphrase": pass})
		return testRequest(t, srv, "POST", "/service/passphrase", strings.NewReader(string(body)), nil).Code
	}
	if code := post("short"); code != http.StatusBadRequest {
		t.Errorf("short passphrase: got %d, want 400", code)
	}
	if code := post("correct horse battery"); code != http.StatusNoContent {
		t.Fatalf("set passphrase: got %d", code)
	}
	u, _ := srv.store.GetUser(invitee.ID)
	if u.Status != "Active" {
		t.Errorf("status after invite = %q, want Active", u.Status)
	}
	if u.Metadata == nil || u.Metadata.SSHKey == nil || !sshkit.VerifyPassphrase(u.Metadata.SSHKey, "correct horse battery") {
		t.Fatalf("SSH key not sealed under the new passphrase")
	}
	if code := post("another passphrase"); code != http.StatusBadRequest {
		t.Errorf("reusing a spent link: got %d, want 400", code)
	}

	// An expired link fails like an unknown one.
	link2, _, err := srv.coreCreatePassphraseLink(admin, u)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = srv.store.GetUser(invitee.ID)
	u.Metadata.PassphraseReset.ExpiresAt = time.Now().Add(-time.Minute)
	srv.store.UpdateUser(u.ID, u.Name, u.Email, u.Role, u.Status, u.Metadata)
	parsed, _ := url.Parse(link2)
	if _, err := srv.passphraseLinkUser(parsed.Query().Get("token")); err == nil {
		t.Errorf("expired link accepted")
	}
}

func TestOnlyActiveUsersSignIn(t *testing.T) {
	srv := newTestServer(t)
	active := mustUser(t, srv, "Ann", "Member", "Active")
	invited := mustUser(t, srv, "Ivo", "Member", "Invited")
	suspended := mustUser(t, srv, "Sue", "Member", "Suspended")

	// Every token, gate pass and refresh resolves its user here.
	if u, err := srv.userFromSubject(subjectForUser(active)); err != nil || u == nil {
		t.Errorf("active user: got %v, %v", u, err)
	}
	for _, u := range []*db.User{invited, suspended} {
		if _, err := srv.userFromSubject(subjectForUser(u)); err == nil {
			t.Errorf("%s user resolved from a token; want refused", u.Status)
		}
		// A fresh login (any kind) can't mint a token for them either.
		leg := &completedLeg{rec: builtinAuth(t, srv, db.AuthSSHKey), user: u}
		if _, _, err := srv.mintForLegs([]*completedLeg{leg}, leg.rec.ID); err == nil {
			t.Errorf("%s user got a token minted", u.Status)
		}
	}
}

func TestAdminsCannotManageSuperusers(t *testing.T) {
	srv := newTestServer(t)
	root := mustUser(t, srv, "Root", "Superuser", "Active")
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	member := mustUser(t, srv, "Mel", "Member", "Active")
	srv.coreGenerateSSHKey(root, root, "rootpassword")
	root, _ = srv.store.GetUser(root.ID)

	for name, op := range map[string]func() error{
		"create a Superuser":        func() error { _, e := srv.coreCreateUser(admin, "New", "new@example.com", "Superuser", "Active"); return e },
		"promote self to Superuser": func() error { return srv.coreUpdateUser(admin, admin.ID, admin.Name, admin.Email, "Superuser", "Active", nil) },
		"edit a Superuser":          func() error { return srv.coreUpdateUser(admin, root.ID, root.Name, root.Email, "Admin", "Active", root.Metadata) },
		"delete a Superuser":        func() error { return srv.coreDeleteUser(admin, root) },
		"set a Superuser's apps":    func() error { return srv.coreSetUserApps(admin, root.ID, nil) },
		"delete a Superuser's key":  func() error { return srv.coreDeleteSSHKey(admin, root) },
		"reset a Superuser":         func() error { _, _, e := srv.coreCreatePassphraseLink(admin, root); return e },
	} {
		if err := op(); !forbidden(err) {
			t.Errorf("Admin may %s: got %v, want 403", name, err)
		}
	}

	// Admins still manage everyone else; Superusers manage Superusers.
	if err := srv.coreUpdateUser(admin, member.ID, member.Name, member.Email, "Admin", "Active", nil); err != nil {
		t.Errorf("Admin promoting a Member to Admin: %v", err)
	}
	if _, _, err := srv.coreCreatePassphraseLink(root, root); err != nil {
		t.Errorf("Superuser resetting a Superuser: %v", err)
	}
	if _, err := srv.coreCreateUser(root, "Two", "two@example.com", "Superuser", "Active"); err != nil {
		t.Errorf("Superuser creating a Superuser: %v", err)
	}
	if _, err := srv.coreCreateUser(root, "Odd", "odd@example.com", "Overlord", "Active"); err == nil {
		t.Errorf("unknown role accepted")
	}
}

// An upload URL for a service file, minted for someone who isn't one of its
// members, must not write — the act dispatch re-runs the membership gate.
func TestServiceFileActURLNeedsMembership(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := mustUser(t, srv, "Ada", "Admin", "Active")
	member := mustUser(t, srv, "Mel", "Member", "Active")
	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := srv.mintActToken(member, http.MethodPut, serviceFileActPath(svc), actTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	rr := testRequest(t, srv, "PUT", "/api/act/"+ticket, strings.NewReader("[pwned] nope\necho\n"), map[string]string{"Content-Type": "text/plain"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("PUT through a non-member's act URL: got %d, want 403", rr.Code)
	}
	if _, _, err := srv.coreReadServiceFile(admin, svc.ID, 0, 0); err == nil {
		t.Errorf("file was written")
	}
}
