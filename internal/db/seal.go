package db

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ── Secret sealing at rest ──────────────────────────────────────────
//
// Stored secrets (auth descriptor client secrets and keys, OAuth client
// secrets) are sealed with AES-256-GCM before they touch the DB, under
// the seal subkey derived from the master signing key (FRBR-4/FRBR-5).
// Sealed values carry an "enc1:" prefix so the migration pass can tell
// plaintext from sealed, and so a read can never mistake one for the
// other.
//
// With no seal key set (tests, and anything that never called
// SetSealKey) sealing is simply off: writes store plaintext, reads
// return it. Production always sets the key before Migrate, so a
// plaintext secret behind a set key is a hard error — the server is
// either fully migrated or it doesn't start.

// SealSubkeyLabel is the HKDF label for the at-rest seal subkey. The
// same subkey seals upstream credentials inside tokens (server package)
// — one key, two doors, deliberately.
const SealSubkeyLabel = "freshbreath/seal"

const sealedPrefix = "enc1:"

// DeriveSubkey splits a master key into an independent 32-byte subkey
// per purpose (HKDF-SHA256). Subkeys can't interact even though they
// share a master.
func DeriveSubkey(master []byte, label string) []byte {
	k, err := hkdf.Key(sha256.New, master, nil, label, 32)
	if err != nil {
		// HKDF-SHA256 only errors on absurd output lengths; 32 never does.
		panic(fmt.Sprintf("hkdf derive %q: %v", label, err))
	}
	return k
}

// SetSealKey arms at-rest sealing. Call it before Migrate: the migration
// pass seals any still-plaintext secrets, and strict reads depend on it.
// A nil or short key disables sealing (dev/tests).
func (s *Store) SetSealKey(key []byte) {
	if len(key) < 16 {
		s.sealKey = nil
		return
	}
	s.sealKey = key
}

// sealField seals a secret value for storage. Empty values and sealing-
// disabled stores pass through; already-sealed values are returned as-is
// so write paths are idempotent.
func (s *Store) sealField(plain string) string {
	if s.sealKey == nil || plain == "" || strings.HasPrefix(plain, sealedPrefix) {
		return plain
	}
	block, err := aes.NewCipher(s.sealKey)
	if err != nil {
		panic("seal key: " + err.Error())
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		panic("gcm: " + err.Error())
	}
	nonce := make([]byte, aesgcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		panic("nonce: " + err.Error())
	}
	ct := aesgcm.Seal(nonce, nonce, []byte(plain), nil) // nonce prepended
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(ct)
}

// openField unseals a stored secret. Sealed values open with the seal
// key; a non-empty plaintext behind an armed key is a hard error (the
// migration pass should have sealed it); with sealing off, values pass
// through untouched.
func (s *Store) openField(val string) (string, error) {
	if val == "" {
		return "", nil
	}
	if !strings.HasPrefix(val, sealedPrefix) {
		if s.sealKey != nil {
			return "", fmt.Errorf("secret is not sealed (migration missing?)")
		}
		return val, nil
	}
	if s.sealKey == nil {
		return "", fmt.Errorf("sealed secret but no seal key is set")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(val, sealedPrefix))
	if err != nil {
		return "", fmt.Errorf("decode sealed secret: %w", err)
	}
	block, err := aes.NewCipher(s.sealKey)
	if err != nil {
		return "", err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := aesgcm.NonceSize()
	if len(raw) < ns+aesgcm.Overhead() {
		return "", fmt.Errorf("sealed secret too short")
	}
	plain, err := aesgcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("unseal: %w", err)
	}
	return string(plain), nil
}

// sealDescriptor seals a descriptor's secret fields in place, before the
// descriptor JSON is stored.
func (s *Store) sealDescriptor(d *AuthDescriptor) {
	d.ClientSecret = s.sealField(d.ClientSecret)
	d.Key = s.sealField(d.Key)
}

// unsealDescriptor opens a descriptor's secret fields in place, after
// the descriptor JSON is read.
func (s *Store) unsealDescriptor(d *AuthDescriptor) error {
	secret, err := s.openField(d.ClientSecret)
	if err != nil {
		return fmt.Errorf("client_secret: %w", err)
	}
	d.ClientSecret = secret
	key, err := s.openField(d.Key)
	if err != nil {
		return fmt.Errorf("key: %w", err)
	}
	d.Key = key
	return nil
}

// sealSecretsAtRest is the boot-time one-time pass: any secret still
// stored plaintext (a pre-FRBR-4 database) is sealed in place.
// Idempotent — already-sealed values are skipped — so it runs on every
// boot cheaply.
func (s *Store) sealSecretsAtRest() error {
	if s.sealKey == nil {
		return nil
	}

	rows, err := s.db.Query("SELECT id, descriptor FROM auth_records")
	if err != nil {
		return err
	}
	type update struct {
		id   int64
		desc string
	}
	var updates []update
	for rows.Next() {
		var id int64
		var descStr string
		if err := rows.Scan(&id, &descStr); err != nil {
			rows.Close()
			return err
		}
		var d AuthDescriptor
		if err := json.Unmarshal([]byte(descStr), &d); err != nil {
			rows.Close()
			return fmt.Errorf("auth record %d descriptor: %w", id, err)
		}
		if d.ClientSecret == "" && d.Key == "" {
			continue
		}
		s.sealDescriptor(&d)
		descJSON, err := json.Marshal(d)
		if err != nil {
			rows.Close()
			return err
		}
		if string(descJSON) == descStr {
			continue
		}
		updates = append(updates, update{id, string(descJSON)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, u := range updates {
		if _, err := s.db.Exec("UPDATE auth_records SET descriptor = ? WHERE id = ?", u.desc, u.id); err != nil {
			return err
		}
	}

	rows, err = s.db.Query("SELECT client_id, client_secret FROM oauth_clients")
	if err != nil {
		return err
	}
	type clientUpdate struct {
		id     string
		secret string
	}
	var clientUpdates []clientUpdate
	for rows.Next() {
		var id, secret string
		if err := rows.Scan(&id, &secret); err != nil {
			rows.Close()
			return err
		}
		if secret == "" || strings.HasPrefix(secret, sealedPrefix) {
			continue
		}
		clientUpdates = append(clientUpdates, clientUpdate{id, s.sealField(secret)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, u := range clientUpdates {
		if _, err := s.db.Exec("UPDATE oauth_clients SET client_secret = ? WHERE client_id = ?", u.secret, u.id); err != nil {
			return err
		}
	}
	return nil
}
