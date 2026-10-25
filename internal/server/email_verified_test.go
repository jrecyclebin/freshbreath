package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"golang.org/x/oauth2"

	"poggers.institute/freshbreath/internal/db"
)

// An email the provider marks unverified names no account; one it marks
// verified, or says nothing about, still does.
func TestUnverifiedEmailIsDropped(t *testing.T) {
	srv := newTestServer(t)
	for _, c := range []struct {
		verified any
		want     string
	}{
		{false, ""},
		{"false", ""},
		{true, "boss@example.com"},
		{"true", "boss@example.com"},
		{nil, "boss@example.com"},
	} {
		ui := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := map[string]any{"sub": "42", "email": "boss@example.com"}
			if c.verified != nil {
				body["email_verified"] = c.verified
			}
			json.NewEncoder(w).Encode(body)
		}))
		rec := &db.AuthRecord{Name: "idp", Kind: db.AuthOAuth2, Descriptor: db.AuthDescriptor{UserInfoURL: ui.URL}}
		claims, err := srv.identityFromUserInfo(context.Background(), rec, nil, &oauth2.Token{AccessToken: "t"})
		ui.Close()
		if err != nil {
			t.Fatal(err)
		}
		if claims.Email != c.want {
			t.Errorf("email_verified=%v: email = %q, want %q", c.verified, claims.Email, c.want)
		}
	}
}

// F11 (audit 202610): the admin gate links a login to a user by email.
// With "require verified email" on, only an email the provider explicitly
// marked verified links; a missing claim no longer counts. Off (the
// default), a missing claim still links, as before.
func TestAdminGateCanRequireVerifiedEmail(t *testing.T) {
	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "IdP", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://idp.example", Provider: "idp"})
	super := &db.User{ID: 99, Role: "Superuser"}
	id := strconv.FormatInt(rec.ID, 10)
	if err := srv.coreUpdateSettings(super, &id, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	user, _ := srv.store.CreateUser("Known", "known@example.com", "Member", "Active")

	linked := func(verified bool) bool {
		subject, _ := srv.legIdentity(&completedLeg{rec: rec, email: "known@example.com", sub: "s1", emailVerified: verified})
		return subject == subjectForUser(user)
	}
	if !linked(false) || !linked(true) {
		t.Error("with the setting off, both should link")
	}

	on := true
	if err := srv.coreUpdateSettings(super, nil, nil, nil, &on); err != nil {
		t.Fatal(err)
	}
	if srv.settingsView()["admin_require_verified_email"] != true {
		t.Error("settings don't report the setting on")
	}
	if linked(false) {
		t.Error("unverified email linked with the setting on")
	}
	if !linked(true) {
		t.Error("verified email didn't link with the setting on")
	}
}

// identityFromUserInfo says whether the email came verified: an explicit
// claim, or GitHub-style primary-email lookup (which only returns
// verified addresses). A missing claim is not verified.
func TestUserInfoReportsVerifiedEmail(t *testing.T) {
	srv := newTestServer(t)
	for _, c := range []struct {
		verified any
		want     bool
	}{{true, true}, {"true", true}, {nil, false}} {
		ui := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := map[string]any{"sub": "42", "email": "boss@example.com"}
			if c.verified != nil {
				body["email_verified"] = c.verified
			}
			json.NewEncoder(w).Encode(body)
		}))
		rec := &db.AuthRecord{Name: "idp", Kind: db.AuthOAuth2, Descriptor: db.AuthDescriptor{UserInfoURL: ui.URL}}
		claims, err := srv.identityFromUserInfo(context.Background(), rec, nil, &oauth2.Token{AccessToken: "t"})
		ui.Close()
		if err != nil {
			t.Fatal(err)
		}
		if emailMarkedVerified(claims.Verified) != c.want {
			t.Errorf("email_verified=%v: verified = %v, want %v", c.verified, claims.Verified, c.want)
		}
	}

	emails := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"email": "boss@example.com", "primary": true, "verified": true}})
	}))
	defer emails.Close()
	profile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "login": "boss"})
	}))
	defer profile.Close()
	rec := &db.AuthRecord{Name: "gh", Kind: db.AuthOAuth2, Descriptor: db.AuthDescriptor{UserInfoURL: profile.URL, UserEmailsURL: emails.URL}}
	claims, err := srv.identityFromUserInfo(context.Background(), rec, nil, &oauth2.Token{AccessToken: "t"})
	if err != nil || claims.Email != "boss@example.com" || !emailMarkedVerified(claims.Verified) {
		t.Errorf("primary-email lookup: %+v, %v", claims, err)
	}
}
