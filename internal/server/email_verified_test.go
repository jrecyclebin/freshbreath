package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
