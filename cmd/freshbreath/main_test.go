package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"poggers.institute/freshbreath/internal/db"
)

func unsetSigningEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FRBR_SIGNING_KEY", "")
}

func testStore(t *testing.T) *db.Store {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	store := db.NewStore(sqlDB)
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

func TestDecodeKeyMaterial(t *testing.T) {
	raw := make([]byte, 32)
	rand.Read(raw)

	cases := []struct {
		name  string
		give  string
		want  []byte
		fails bool
	}{
		{"hex", hex.EncodeToString(raw), raw, false},
		{"base64 std", base64.StdEncoding.EncodeToString(raw), raw, false},
		{"base64 raw std", base64.RawStdEncoding.EncodeToString(raw), raw, false},
		{"base64 url", base64.URLEncoding.EncodeToString(raw), raw, false},
		{"base64 raw url", base64.RawURLEncoding.EncodeToString(raw), raw, false},
		{"hex padded with whitespace", "  " + hex.EncodeToString(raw) + "\n", raw, false},
		{"empty", "", nil, true},
		{"garbage", "!!!not a key!!!", nil, true},
		{"too short", "abcd", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decodeKeyMaterial(c.give)
			if c.fails {
				if err == nil {
					t.Fatalf("expected error, got %x", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if hex.EncodeToString(got) != hex.EncodeToString(c.want) {
				t.Fatalf("got %x, want %x", got, c.want)
			}
		})
	}
}

func TestLoadSigningKeyEnvWins(t *testing.T) {
	unsetSigningEnv(t)
	raw := make([]byte, 32)
	rand.Read(raw)
	t.Setenv("FRBR_SIGNING_KEY", hex.EncodeToString(raw))

	dir := t.TempDir()
	key, err := loadSigningKey(dir, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if hex.EncodeToString(key) != hex.EncodeToString(raw) {
		t.Fatalf("env key not used")
	}
	if _, err := os.Stat(filepath.Join(dir, "signing.key")); !os.IsNotExist(err) {
		t.Fatalf("env branch must not write a file")
	}
}

func TestLoadSigningKeyBadEnvFails(t *testing.T) {
	t.Setenv("FRBR_SIGNING_KEY", "zzz-not-decodable")
	if _, err := loadSigningKey(t.TempDir(), nil); err == nil {
		t.Fatal("expected error for undecodable env key")
	}
}

func TestLoadSigningKeyFileBeatsDB(t *testing.T) {
	unsetSigningEnv(t)
	store := testStore(t)
	raw := make([]byte, 32)
	rand.Read(raw)
	store.SetSetting("local_signing_key", hex.EncodeToString(make([]byte, 32)))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "signing.key"), []byte(hex.EncodeToString(raw)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := loadSigningKey(dir, store)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if hex.EncodeToString(key) != hex.EncodeToString(raw) {
		t.Fatal("file key not used")
	}
	if val, _ := store.GetSetting("local_signing_key"); val != "" {
		t.Fatal("legacy settings row must be retired")
	}
}

func TestLoadSigningKeyAdoptsLegacyRow(t *testing.T) {
	unsetSigningEnv(t)
	store := testStore(t)
	raw := make([]byte, 32)
	rand.Read(raw)
	if err := store.SetSetting("local_signing_key", hex.EncodeToString(raw)); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	key, err := loadSigningKey(dir, store)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if hex.EncodeToString(key) != hex.EncodeToString(raw) {
		t.Fatal("legacy key not adopted")
	}
	// The adopted key must land in the file — the settings row is gone.
	onDisk, err := os.ReadFile(filepath.Join(dir, "signing.key"))
	if err != nil {
		t.Fatalf("key file: %v", err)
	}
	if got, _ := decodeKeyMaterial(string(onDisk)); hex.EncodeToString(got) != hex.EncodeToString(raw) {
		t.Fatal("file does not hold the adopted key")
	}
	if val, _ := store.GetSetting("local_signing_key"); val != "" {
		t.Fatal("legacy settings row must be retired")
	}
	info, err := os.Stat(filepath.Join(dir, "signing.key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file perms: %v (%o)", err, info.Mode().Perm())
	}
}

func TestLoadSigningKeyMintsFresh(t *testing.T) {
	unsetSigningEnv(t)
	store := testStore(t) // settings table exists, but no legacy row

	dir := t.TempDir()
	key, err := loadSigningKey(dir, store)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("minted key is %d bytes, want 32", len(key))
	}
	// A second load must return the same key — it came from the file.
	again, err := loadSigningKey(dir, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if hex.EncodeToString(again) != hex.EncodeToString(key) {
		t.Fatal("minted key not stable across loads")
	}
}

func TestLoadSigningKeyFreshInstallNoTable(t *testing.T) {
	// A brand-new DB has no settings table until Migrate; the mint path
	// must not care (HasTable guard).
	unsetSigningEnv(t)
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	store := db.NewStore(sqlDB)

	dir := t.TempDir()
	key, err := loadSigningKey(dir, store)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("minted key is %d bytes, want 32", len(key))
	}
}
