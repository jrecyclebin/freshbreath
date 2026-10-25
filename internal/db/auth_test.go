package db

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAuthRecordSeeds(t *testing.T) {
	store := newTestStore(t)

	sshID, err := store.BuiltinAuthID(AuthSSHKey)
	if err != nil {
		t.Fatalf("builtin ssh_key: %v", err)
	}
	anonID, err := store.BuiltinAuthID(AuthAnonymous)
	if err != nil {
		t.Fatalf("builtin anonymous: %v", err)
	}
	if sshID == anonID {
		t.Fatal("expected distinct builtin ids")
	}

	ssh, _ := store.GetAuthRecord(sshID)
	if ssh.Name != "Built-in" || !ssh.Builtin {
		t.Errorf("ssh seed = %q builtin=%v", ssh.Name, ssh.Builtin)
	}

	// Migrate must be idempotent — no duplicate seeds.
	if err := store.Migrate(); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	recs, _ := store.ListAuthRecords()
	if len(recs) != 2 {
		t.Errorf("len(records) = %d, want 2", len(recs))
	}
}

func TestAuthRecordCRUD(t *testing.T) {
	store := newTestStore(t)

	rec, err := store.CreateAuthRecord("GitHub (staff)", AuthOAuth2, AuthDescriptor{
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		ClientID:     "cid",
		ClientSecret: "shh",
		Provider:     "github",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.Kind != AuthOAuth2 || rec.Descriptor.Provider != "github" {
		t.Errorf("round trip: kind=%q provider=%q", rec.Kind, rec.Descriptor.Provider)
	}
	if rec.Descriptor.ClientSecret != "shh" {
		t.Errorf("secret should survive in-process: %q", rec.Descriptor.ClientSecret)
	}

	// Duplicate name refused.
	if _, err := store.CreateAuthRecord("GitHub (staff)", AuthOIDC, AuthDescriptor{}); err == nil {
		t.Error("expected duplicate-name error")
	}

	// Update with empty secret keeps the stored one.
	if err := store.UpdateAuthRecord(rec.ID, "GitHub (public)", AuthOAuth2, AuthDescriptor{
		ClientID: "cid2", Provider: "github",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := store.GetAuthRecord(rec.ID)
	if got.Name != "GitHub (public)" || got.Descriptor.ClientID != "cid2" {
		t.Errorf("update round trip: %q %q", got.Name, got.Descriptor.ClientID)
	}
	if got.Descriptor.ClientSecret != "shh" {
		t.Errorf("empty secret on update should keep stored: %q", got.Descriptor.ClientSecret)
	}

	if err := store.DeleteAuthRecord(rec.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetAuthRecord(rec.ID); err == nil {
		t.Error("expected deleted record to be gone")
	}
}

func TestAuthRecordKindValidation(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.CreateAuthRecord("Bogus", "carrier_pigeon", AuthDescriptor{}); err == nil {
		t.Error("expected unknown-kind error on create")
	}
	rec, _ := store.CreateAuthRecord("Key", AuthAPIKey, AuthDescriptor{Key: "k"})
	if err := store.UpdateAuthRecord(rec.ID, "Key", "smoke_signal", AuthDescriptor{}); err == nil {
		t.Error("expected unknown-kind error on update")
	}
}

func TestAuthRecordBuiltinProtections(t *testing.T) {
	store := newTestStore(t)
	anonID, _ := store.BuiltinAuthID(AuthAnonymous)

	if err := store.DeleteAuthRecord(anonID); err == nil {
		t.Error("expected builtin delete to be refused")
	}

	// Update may touch the descriptor, but name/kind stay frozen.
	if err := store.UpdateAuthRecord(anonID, "Sneaky", AuthAPIKey, AuthDescriptor{}); err != nil {
		t.Fatalf("update builtin: %v", err)
	}
	got, _ := store.GetAuthRecord(anonID)
	if got.Name != "Anonymous" || got.Kind != AuthAnonymous {
		t.Errorf("builtin name/kind changed: %q %q", got.Name, got.Kind)
	}
}

func TestAuthRecordDeleteWhileReferenced(t *testing.T) {
	store := newTestStore(t)
	rec, _ := store.CreateAuthRecord("Gate", AuthAPIKey, AuthDescriptor{Key: "k"})
	if _, err := store.RegisterService("svc", "https://x.example", ServiceDescriptor{Type: "api"}, &rec.ID, nil); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := store.DeleteAuthRecord(rec.ID); err == nil {
		t.Error("expected referenced-record delete to be refused")
	}
}

func TestAuthRecordSecretMasking(t *testing.T) {
	store := newTestStore(t)
	rec, _ := store.CreateAuthRecord("Masked", AuthOAuth2, AuthDescriptor{
		ClientID: "cid", ClientSecret: "topsecret", Provider: "x",
	})
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if strings.Contains(s, "topsecret") {
		t.Errorf("secret leaked in serialization: %s", s)
	}
	if !strings.Contains(s, `"has_secret":true`) {
		t.Errorf("expected has_secret flag: %s", s)
	}

	key, _ := store.CreateAuthRecord("KeyRec", AuthAPIKey, AuthDescriptor{Key: "sk-42"})
	b, _ = json.Marshal(key)
	if strings.Contains(string(b), "sk-42") {
		t.Errorf("api key leaked in serialization: %s", b)
	}
}

// The exposure warning is a comparison, so what matters is the ordering and
// the ties — not the absolute numbers.
func TestAuthStrictnessOrdering(t *testing.T) {
	if AuthStrictness(AuthOIDC) != AuthStrictness(AuthOAuth2) {
		t.Errorf("oidc and oauth2 admit the same audience and must tie")
	}
	ascending := []string{AuthAnonymous, AuthAPIKey, AuthOAuth2, AuthSSHKey}
	for i := 1; i < len(ascending); i++ {
		lo, hi := ascending[i-1], ascending[i]
		if AuthStrictness(lo) >= AuthStrictness(hi) {
			t.Errorf("%s (%d) should be looser than %s (%d)",
				lo, AuthStrictness(lo), hi, AuthStrictness(hi))
		}
	}
	if AuthStrictness("kind-from-the-future") != AuthStrictness(AuthAnonymous) {
		t.Errorf("an unknown kind must rank widest, never strong by accident")
	}
}

func TestAuthStrictnessSerialized(t *testing.T) {
	store := newTestStore(t)
	rec, _ := store.CreateAuthRecord("Keys", AuthAPIKey, AuthDescriptor{Key: "sk-1"})
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"strictness":1`) {
		t.Errorf("expected strictness in serialization: %s", b)
	}
}

// F9 (audit 202610): a masked secret used to survive any edit, so an Admin
// who can't read it could repoint the record's token endpoint at a host of
// their own and receive it. Moving any endpoint (or changing the kind) now
// needs the secret entered again; other edits keep it.
func TestAuthRecordSecretStaysWithItsEndpoints(t *testing.T) {
	store := newTestStore(t)
	rec, err := store.CreateAuthRecord("Corp", AuthOAuth2, AuthDescriptor{
		AuthorizeURL: "https://idp.example/authorize", TokenURL: "https://idp.example/token",
		ClientID: "cid", ClientSecret: "shh",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	base := rec.Descriptor
	base.ClientSecret = ""

	for name, edit := range map[string]func(d *AuthDescriptor){
		"token url":     func(d *AuthDescriptor) { d.TokenURL = "https://evil.example/token" },
		"authorize url": func(d *AuthDescriptor) { d.AuthorizeURL = "https://evil.example/authorize" },
		"userinfo url":  func(d *AuthDescriptor) { d.UserInfoURL = "https://evil.example/me" },
		"emails url":    func(d *AuthDescriptor) { d.UserEmailsURL = "https://evil.example/emails" },
		"issuer":        func(d *AuthDescriptor) { d.Issuer = "https://evil.example" },
		"resource":      func(d *AuthDescriptor) { d.Resource = "https://evil.example/mcp" },
	} {
		d := base
		edit(&d)
		if err := store.UpdateAuthRecord(rec.ID, "Corp", AuthOAuth2, d); !errors.Is(err, ErrSecretNeeded) {
			t.Errorf("%s moved without the secret: err = %v", name, err)
		}
	}
	if err := store.UpdateAuthRecord(rec.ID, "Corp", AuthOIDC, base); !errors.Is(err, ErrSecretNeeded) {
		t.Errorf("kind changed without the secret: err = %v", err)
	}

	// Re-entering the secret lets the move through.
	moved := base
	moved.TokenURL, moved.ClientSecret = "https://idp2.example/token", "new-shh"
	if err := store.UpdateAuthRecord(rec.ID, "Corp", AuthOAuth2, moved); err != nil {
		t.Fatalf("move with secret: %v", err)
	}

	// Edits that don't move it keep the stored secret, as before.
	kept := moved
	kept.ClientSecret, kept.Scopes = "", "read"
	if err := store.UpdateAuthRecord(rec.ID, "Corp Renamed", AuthOAuth2, kept); err != nil {
		t.Fatalf("plain edit: %v", err)
	}
	got, _ := store.GetAuthRecord(rec.ID)
	if got.Descriptor.ClientSecret != "new-shh" || got.Descriptor.TokenURL != "https://idp2.example/token" {
		t.Errorf("got secret %q token %q", got.Descriptor.ClientSecret, got.Descriptor.TokenURL)
	}

	// A record with no secret on file moves freely.
	open, _ := store.CreateAuthRecord("Public", AuthOIDC, AuthDescriptor{Issuer: "https://a.example", ClientID: "c"})
	if err := store.UpdateAuthRecord(open.ID, "Public", AuthOIDC, AuthDescriptor{Issuer: "https://b.example", ClientID: "c"}); err != nil {
		t.Errorf("secretless move: %v", err)
	}
}
