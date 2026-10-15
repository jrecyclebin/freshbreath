package server

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"poggers.institute/freshbreath/internal/db"
)

// authCodeOptions emits PKCE plus the descriptor's refresh/consent toggles.
// access_type=offline is Google's non-standard switch for "give me a refresh
// token"; prompt=consent makes Google return one on every login.
func TestAuthCodeOptions(t *testing.T) {
	verifier := "v-xyz"
	cases := []struct {
		name     string
		d        db.AuthDescriptor
		want     []string // "key=value" params that must be present
		dontWant []string
	}{
		{"neither", db.AuthDescriptor{}, nil, []string{"access_type", "prompt"}},
		{"offline only", db.AuthDescriptor{OfflineAccess: true}, []string{"access_type=offline"}, []string{"prompt"}},
		{"consent only", db.AuthDescriptor{ForceConsent: true}, []string{"prompt=consent"}, []string{"access_type"}},
		{"both", db.AuthDescriptor{OfflineAccess: true, ForceConsent: true}, []string{"access_type=offline", "prompt=consent"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &oauth2.Config{
				ClientID:    "c",
				Endpoint:    oauth2.Endpoint{AuthURL: "https://up.example/authorize"},
				RedirectURL: "http://localhost/cb",
			}
			authURL := cfg.AuthCodeURL("state", authCodeOptions(verifier, c.d)...)
			q, err := url.Parse(authURL)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			for _, w := range c.want {
				k, v, _ := strings.Cut(w, "=")
				if got := q.Query().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			for _, k := range c.dontWant {
				if _, ok := q.Query()[k]; ok {
					t.Errorf("%s should be absent, got %q", k, q.Query().Get(k))
				}
			}
			// PKCE is always present.
			if q.Query().Get("code_challenge") == "" {
				t.Error("missing code_challenge")
			}
		})
	}
}

// An OAuth2 record carrying the offline/consent toggles seeds the authorize
// URL with access_type=offline and prompt=consent — the regression that cost
// Google its refresh token, and thus its sealed credential once the first
// access token expired.
func TestOAuth2BeginAuthOfflineAccess(t *testing.T) {
	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "Google", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:      "https://oauth2.googleapis.com/token",
		ClientID:      "g-client",
		OfflineAccess: true,
		ForceConsent:  true,
	})
	authURL, _, _, _, _, _, err := srv.oauth2BeginAuth(context.Background(), rec, "http://localhost:9009/service/callback")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	q, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := q.Query()
	if got.Get("access_type") != "offline" {
		t.Errorf("access_type = %q, want offline", got.Get("access_type"))
	}
	if got.Get("prompt") != "consent" {
		t.Errorf("prompt = %q, want consent", got.Get("prompt"))
	}
	if got.Get("client_id") != "g-client" {
		t.Errorf("client_id = %q", got.Get("client_id"))
	}
}

// Without the toggles the authorize URL carries neither param, so existing
// non-Google setups keep their old behavior.
func TestOAuth2BeginAuthNoOfflineByDefault(t *testing.T) {
	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "GitHub-ish", db.AuthOAuth2, db.AuthDescriptor{
		AuthorizeURL: "https://up.example/authorize",
		TokenURL:     "https://up.example/token",
		ClientID:     "c",
	})
	authURL, _, _, _, _, _, err := srv.oauth2BeginAuth(context.Background(), rec, "http://localhost:9009/service/callback")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	q, _ := url.Parse(authURL)
	got := q.Query()
	if _, ok := got["access_type"]; ok {
		t.Errorf("access_type present by default: %q", got.Get("access_type"))
	}
	if _, ok := got["prompt"]; ok {
		t.Errorf("prompt present by default: %q", got.Get("prompt"))
	}
}

// The OIDC path honors the same toggles (Google can be configured as OIDC
// too): the discovery-driven authorize URL still carries access_type=offline
// and prompt=consent.
func TestOIDCBeginAuthOfflineAccess(t *testing.T) {
	iss := fakeOIDCIssuer(t)
	srv := newTestServer(t)
	rec := newAuthRecord(t, srv, "Google", db.AuthOIDC, db.AuthDescriptor{
		Issuer:        iss.URL,
		ClientID:      "g-client",
		OfflineAccess: true,
		ForceConsent:  true,
	})
	authURL, _, _, _, _, err := srv.oidcBeginAuth(context.Background(), rec, "http://localhost:9009/service/callback")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	q, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := q.Query()
	if got.Get("access_type") != "offline" {
		t.Errorf("access_type = %q, want offline", got.Get("access_type"))
	}
	if got.Get("prompt") != "consent" {
		t.Errorf("prompt = %q, want consent", got.Get("prompt"))
	}
}
