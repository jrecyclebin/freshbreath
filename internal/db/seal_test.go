package db

import (
	"encoding/json"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// rawDescriptor reads the descriptor JSON straight from the table,
// bypassing the unseal path — the "is it really sealed at rest?" probe.
func rawDescriptor(t *testing.T, store *Store, id int64) string {
	t.Helper()
	var desc string
	if err := store.DB().QueryRow("SELECT descriptor FROM auth_records WHERE id = ?", id).Scan(&desc); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	return desc
}

func rawClientSecret(t *testing.T, store *Store, clientID string) string {
	t.Helper()
	var secret string
	if err := store.DB().QueryRow("SELECT client_secret FROM oauth_clients WHERE client_id = ?", clientID).Scan(&secret); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	return secret
}

func TestSealedAtRestRoundtrip(t *testing.T) {
	store := newTestStore(t)
	store.SetSealKey([]byte("0123456789abcdef0123456789abcdef"))

	rec, err := store.CreateAuthRecord("sealed-oauth", AuthOAuth2, AuthDescriptor{
		ClientID:     "cid",
		ClientSecret: "s3cret-value",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	raw := rawDescriptor(t, store, rec.ID)
	if !strings.Contains(raw, "enc1:") {
		t.Fatalf("client_secret not sealed at rest: %s", raw)
	}
	if strings.Contains(raw, "s3cret-value") {
		t.Fatalf("plaintext secret in DB: %s", raw)
	}

	got, err := store.GetAuthRecord(rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Descriptor.ClientSecret != "s3cret-value" {
		t.Fatalf("unsealed value: %q", got.Descriptor.ClientSecret)
	}
}

func TestAPIKeySealedAtRest(t *testing.T) {
	store := newTestStore(t)
	store.SetSealKey([]byte("0123456789abcdef0123456789abcdef"))

	rec, err := store.CreateAuthRecord("sealed-apikey", AuthAPIKey, AuthDescriptor{
		Key:    "sk-live-abcdef",
		Header: "X-API-Key",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	raw := rawDescriptor(t, store, rec.ID)
	if strings.Contains(raw, "sk-live-abcdef") {
		t.Fatalf("plaintext key in DB: %s", raw)
	}
	if !strings.Contains(raw, "X-API-Key") {
		t.Fatalf("non-secret header must stay readable: %s", raw)
	}

	got, _ := store.GetAuthRecord(rec.ID)
	if got.Descriptor.Key != "sk-live-abcdef" {
		t.Fatalf("unsealed key: %q", got.Descriptor.Key)
	}
}

// A pre-FRBR-4 database holds plaintext secrets. The Migrate pass must
// seal them in place, and reads must work before and after.
func TestMigrateSealsPlaintextSecrets(t *testing.T) {
	store := newTestStore(t) // sealing off: writes store plaintext, like an old DB

	rec, err := store.CreateAuthRecord("legacy-oauth", AuthOAuth2, AuthDescriptor{
		ClientID:     "cid",
		ClientSecret: "legacy-secret",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.RegisterOAuthClient("legacy-client", "legacy-client-secret", nil); err != nil {
		t.Fatalf("register client: %v", err)
	}
	if raw := rawDescriptor(t, store, rec.ID); !strings.Contains(raw, "legacy-secret") {
		t.Fatalf("setup: expected plaintext at rest")
	}

	// Arm sealing and run the boot-time migration pass.
	store.SetSealKey([]byte("0123456789abcdef0123456789abcdef"))
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if raw := rawDescriptor(t, store, rec.ID); strings.Contains(raw, "legacy-secret") {
		t.Fatalf("secret still plaintext after migration: %s", raw)
	}
	if raw := rawClientSecret(t, store, "legacy-client"); strings.Contains(raw, "legacy-client-secret") {
		t.Fatalf("client secret still plaintext after migration")
	}

	got, err := store.GetAuthRecord(rec.ID)
	if err != nil {
		t.Fatalf("post-migration read: %v", err)
	}
	if got.Descriptor.ClientSecret != "legacy-secret" {
		t.Fatalf("migrated secret lost: %q", got.Descriptor.ClientSecret)
	}
	secret, _, ok, err := store.GetOAuthClient("legacy-client")
	if err != nil || !ok || secret != "legacy-client-secret" {
		t.Fatalf("post-migration client read: %q %v %v", secret, ok, err)
	}
}

// With a seal key armed, a plaintext secret is a hard error — the server
// is either fully migrated or it doesn't start.
func TestStrictReadRejectsPlaintext(t *testing.T) {
	store := newTestStore(t)
	store.SetSealKey([]byte("0123456789abcdef0123456789abcdef"))

	if _, err := store.DB().Exec(
		"INSERT INTO auth_records (name, kind, descriptor) VALUES ('rogue', 'api_key', ?)",
		`{"key":"plaintext-nope"}`,
	); err != nil {
		t.Fatal(err)
	}
	var id int64
	store.DB().QueryRow("SELECT id FROM auth_records WHERE name = 'rogue'").Scan(&id)

	if _, err := store.GetAuthRecord(id); err == nil {
		t.Fatal("expected strict error reading unsealed secret")
	}

	if err := store.RegisterOAuthClient("rogue-client", "plaintext-nope", nil); err != nil {
		t.Fatal(err)
	}
	// The register path seals on write, so this is fine — only
	// pre-existing plaintext is rejected.
	if _, _, _, err := store.GetOAuthClient("rogue-client"); err != nil {
		t.Fatalf("sealed client read: %v", err)
	}
}

// The masked marshaling (has_secret, empty updates keep stored) must hold
// with sealing armed.
func TestMaskedUpdateKeepsStoredSecret(t *testing.T) {
	store := newTestStore(t)
	store.SetSealKey([]byte("0123456789abcdef0123456789abcdef"))

	rec, err := store.CreateAuthRecord("masked-apikey", AuthAPIKey, AuthDescriptor{Key: "the-key"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Masked JSON never carries the secret.
	blob, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "the-key") {
		t.Fatalf("secret leaked through marshal: %s", blob)
	}

	// Empty update keeps the stored (sealed) secret.
	if err := store.UpdateAuthRecord(rec.ID, "masked-apikey", AuthAPIKey, AuthDescriptor{Header: "X-Key"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := store.GetAuthRecord(rec.ID)
	if got.Descriptor.Key != "the-key" {
		t.Fatalf("empty update lost the stored key: %q", got.Descriptor.Key)
	}
	if raw := rawDescriptor(t, store, rec.ID); strings.Contains(raw, "the-key") {
		t.Fatalf("kept secret stored unsealed: %s", raw)
	}

	// A new secret overwrites and stays sealed at rest.
	if err := store.UpdateAuthRecord(rec.ID, "masked-apikey", AuthAPIKey, AuthDescriptor{Key: "the-new-key"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = store.GetAuthRecord(rec.ID)
	if got.Descriptor.Key != "the-new-key" {
		t.Fatalf("update lost new key: %q", got.Descriptor.Key)
	}
	if raw := rawDescriptor(t, store, rec.ID); strings.Contains(raw, "the-new-key") {
		t.Fatalf("replaced secret stored unsealed: %s", raw)
	}
}

func TestSealingDisabledPassthrough(t *testing.T) {
	store := newTestStore(t) // no seal key

	rec, err := store.CreateAuthRecord("plain-apikey", AuthAPIKey, AuthDescriptor{Key: "plain-key"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if raw := rawDescriptor(t, store, rec.ID); !strings.Contains(raw, "plain-key") {
		t.Fatalf("sealing-off must store plaintext")
	}
	got, err := store.GetAuthRecord(rec.ID)
	if err != nil || got.Descriptor.Key != "plain-key" {
		t.Fatalf("passthrough read: %v %q", err, got.Descriptor.Key)
	}
}

func TestSealedWithWrongKeyFails(t *testing.T) {
	store := newTestStore(t)
	store.SetSealKey([]byte("0123456789abcdef0123456789abcdef"))

	rec, err := store.CreateAuthRecord("wrongkey-apikey", AuthAPIKey, AuthDescriptor{Key: "the-key"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	store.SetSealKey([]byte("fedcba9876543210fedcba9876543210"))
	if _, err := store.GetAuthRecord(rec.ID); err == nil {
		t.Fatal("expected unseal failure with a different seal key")
	}
}

// DeriveSubkey is the single HKDF authority: the same master+label must
// produce the same subkey here and in the server package's jwt/seal
// derivation.
func TestDeriveSubkeyStable(t *testing.T) {
	master := []byte("master-key-material-32-bytes-ok!!")
	a := DeriveSubkey(master, SealSubkeyLabel)
	b := DeriveSubkey(master, SealSubkeyLabel)
	if string(a) != string(b) {
		t.Fatal("subkey not stable")
	}
	jwt := DeriveSubkey(master, "freshbreath/jwt-sign")
	if string(a) == string(jwt) {
		t.Fatal("different labels must not produce the same subkey")
	}
	if len(a) != 32 {
		t.Fatalf("subkey len %d", len(a))
	}
}

// The Migrate pass must be idempotent: already-sealed rows untouched.
func TestMigrateSealPassIdempotent(t *testing.T) {
	store := newTestStore(t)
	store.SetSealKey([]byte("0123456789abcdef0123456789abcdef"))

	rec, err := store.CreateAuthRecord("idem-oauth", AuthOAuth2, AuthDescriptor{ClientSecret: "once-sealed"})
	if err != nil {
		t.Fatal(err)
	}
	first := rawDescriptor(t, store, rec.ID)

	if err := store.Migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if second := rawDescriptor(t, store, rec.ID); second != first {
		t.Fatalf("idempotence broken:\n first: %s\nsecond: %s", first, second)
	}
}
