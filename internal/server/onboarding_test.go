package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
)

// ── Onboarding ──
//
// A fresh install has no users and no admin gate. Onboarding creates the
// first Superuser with an SSH passphrase and puts the panel behind the
// built-in SSH record, in one step.

func onboardingNeeded(t *testing.T, srv *Server) bool {
	t.Helper()
	rr := testRequest(t, srv, "GET", "/api/onboarding", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("GET /api/onboarding: %d %s", rr.Code, rr.Body.String())
	}
	var res struct{ Needed bool }
	json.Unmarshal(rr.Body.Bytes(), &res)
	return res.Needed
}

func onboard(t *testing.T, srv *Server, name, email, passphrase string) *httpResult {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "email": email, "passphrase": passphrase})
	rr := testRequest(t, srv, "POST", "/api/onboarding", strings.NewReader(string(body)), nil)
	return &httpResult{rr.Code, rr.Body.String()}
}

// setupActor is the synthetic Superuser authWrap hands out in setup mode.
var setupActor = &db.User{ID: -1, Name: "Setup Account", Role: "Superuser", Status: "Active"}

type httpResult struct {
	code int
	body string
}

func TestOnboardingNeededOnFreshInstall(t *testing.T) {
	srv := newTestServer(t)
	if !onboardingNeeded(t, srv) {
		t.Error("a fresh install should need onboarding")
	}
}

func TestOnboardingCreatesSuperuserBehindSSHGate(t *testing.T) {
	srv := newTestServer(t)

	if res := onboard(t, srv, "Ada", "ada@example.com", "correct horse"); res.code != http.StatusCreated {
		t.Fatalf("onboard: %d %s", res.code, res.body)
	}

	user, err := srv.store.GetUserByEmail("ada@example.com")
	if err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if user.Role != "Superuser" || user.Status != "Active" {
		t.Errorf("user = %s/%s, want Superuser/Active", user.Role, user.Status)
	}
	if user.Metadata == nil || user.Metadata.SSHKey == nil {
		t.Fatal("user has no SSH key")
	}
	if !sshkit.VerifyPassphrase(user.Metadata.SSHKey, "correct horse") {
		t.Error("SSH key doesn't open with the onboarding passphrase")
	}

	sshID, err := srv.store.BuiltinAuthID(db.AuthSSHKey)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := srv.store.GetSetting("admin_auth_service")
	if got != strconv.FormatInt(sshID, 10) {
		t.Errorf("admin_auth_service = %q, want the SSH record %d", got, sshID)
	}

	// The panel is gated now; an anonymous caller can't even ask.
	if rr := testRequest(t, srv, "GET", "/api/onboarding", nil, nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("GET after onboarding = %d, want 401", rr.Code)
	}
}

func TestOnboardingRefusedOnceAUserExists(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.store.CreateUser("Existing", "existing@example.com", "Member", "Active"); err != nil {
		t.Fatal(err)
	}
	if onboardingNeeded(t, srv) {
		t.Error("onboarding should not be needed once a user exists")
	}
	if res := onboard(t, srv, "Ada", "ada@example.com", "correct horse"); res.code != http.StatusConflict {
		t.Errorf("onboard = %d %s, want 409", res.code, res.body)
	}
	if got, _ := srv.store.GetSetting("admin_auth_service"); got != "" {
		t.Errorf("admin_auth_service = %q, want it left alone", got)
	}
}

// An install already behind an admin gate (OIDC, say) but with no user rows
// yet is configured, not fresh.
func TestOnboardingRefusedBehindAnAdminGate(t *testing.T) {
	srv := newTestServer(t)
	rec := oidcRecord(t, srv, "IdP")
	if err := srv.store.SetSetting("admin_auth_service", strconv.FormatInt(rec.ID, 10)); err != nil {
		t.Fatal(err)
	}
	needed, err := srv.onboardingNeeded()
	if err != nil || needed {
		t.Errorf("onboardingNeeded = %v, %v; want false", needed, err)
	}
	if _, err := srv.coreOnboard(setupActor, "Ada", "ada@example.com", "correct horse"); err == nil {
		t.Error("coreOnboard succeeded behind an admin gate")
	}
}

// A bad request leaves the install exactly as it was: no user, no gate.
func TestOnboardingRejectsBadInputWithoutSideEffects(t *testing.T) {
	cases := []struct{ name, email, passphrase string }{
		{"", "ada@example.com", "correct horse"},
		{"Ada", "", "correct horse"},
		{"Ada", "ada@example.com", "short"},
	}
	for _, tc := range cases {
		srv := newTestServer(t)
		if res := onboard(t, srv, tc.name, tc.email, tc.passphrase); res.code != http.StatusBadRequest {
			t.Errorf("%+v: onboard = %d %s, want 400", tc, res.code, res.body)
		}
		if users, _ := srv.store.ListUsers(); len(users) != 0 {
			t.Errorf("%+v: left %d users behind", tc, len(users))
		}
		if got, _ := srv.store.GetSetting("admin_auth_service"); got != "" {
			t.Errorf("%+v: admin_auth_service = %q", tc, got)
		}
	}
}

func TestOnboardingSkipHidesThePrompt(t *testing.T) {
	srv := newTestServer(t)
	if rr := testRequest(t, srv, "POST", "/api/onboarding/skip", nil, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("skip: %d %s", rr.Code, rr.Body.String())
	}
	if onboardingNeeded(t, srv) {
		t.Error("onboarding still needed after skipping")
	}
	// Skipping hides the prompt; it doesn't lock the door. Someone who
	// changes their mind can still onboard while the install is fresh.
	if res := onboard(t, srv, "Ada", "ada@example.com", "correct horse"); res.code != http.StatusCreated {
		t.Errorf("onboard after skip = %d %s, want 201", res.code, res.body)
	}
}
