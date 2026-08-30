package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ── Auth records ────────────────────────────────────────────────────
//
// An auth record is a credential or login method that services and apps
// reference by id, in one of two slots: "protected_by" (who may call —
// inbound) and "acts_as" (what credential goes upstream — outbound).
// One record can serve many services in either slot.

// Auth record kinds. Every kind is eligible in both slots; what a kind
// *means* differs by slot (see design/decoupled-auth.md).
const (
	AuthAnonymous = "anonymous" // explicit "open to anyone on the LAN"
	AuthSSHKey    = "ssh_key"   // passphrase login against users.ssh_key (or a stored key)
	AuthOIDC      = "oidc"      // discovery-based OpenID Connect provider
	AuthOAuth2    = "oauth2"    // explicit-endpoint OAuth2 (GitHub-shaped, no id_token)
	AuthAPIKey    = "api_key"   // stored key, injected under a header
)

var authKinds = map[string]bool{
	AuthAnonymous: true,
	AuthSSHKey:    true,
	AuthOIDC:      true,
	AuthOAuth2:    true,
	AuthAPIKey:    true,
}

// ValidAuthKind reports whether kind is one of the five auth kinds.
func ValidAuthKind(kind string) bool { return authKinds[kind] }

// AuthStrictness ranks a kind by how narrow an audience it admits, from 0
// (anyone on the LAN) upward. It answers exactly one question — "would
// moving something behind this gate widen who can reach it?" — which is
// what the admin panel's link-time exposure warning compares.
//
// oidc and oauth2 tie deliberately: both admit whoever holds an account at
// the upstream provider, and the protocol difference between them says
// nothing about who gets in. An unknown kind ranks widest, so a gate nobody
// understands is never mistaken for a strong one.
func AuthStrictness(kind string) int {
	switch kind {
	case AuthSSHKey:
		return 3 // a registered user, holding a passphrase-protected key
	case AuthOIDC, AuthOAuth2:
		return 2 // anyone with an account at the provider
	case AuthAPIKey:
		return 1 // anyone holding the key
	default: // anonymous, and anything unrecognized
		return 0
	}
}

// AuthDescriptor is the kind-specific configuration of an auth record.
// Stored as JSON like services.descriptor — new fields need no DDL.
type AuthDescriptor struct {
	// oidc: issuer URL for discovery.
	Issuer string `json:"issuer,omitempty"`

	// oauth2: explicit endpoints (no discovery, no id_token).
	AuthorizeURL  string `json:"authorize_url,omitempty"`
	TokenURL      string `json:"token_url,omitempty"`
	UserInfoURL   string `json:"userinfo_url,omitempty"`
	UserEmailsURL string `json:"userinfo_emails_url,omitempty"`

	// oidc + oauth2 shared.
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	Scopes       string `json:"scopes,omitempty"`

	// Provider is a slug distinct from the record name: it keys ext:
	// subjects and the sealed-credential map, so two records over the same
	// upstream app share one provider and one user identity.
	Provider string `json:"provider,omitempty"`

	// api_key: the stored key and its header (empty ⇒ Authorization: Bearer).
	// ssh_key: Key optionally holds a stored private key; empty means the
	// passphrase is checked against the ssh_key in users.
	Key    string `json:"key,omitempty"`
	Header string `json:"header,omitempty"`
}

type AuthRecord struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Descriptor AuthDescriptor `json:"descriptor"`
	Builtin    bool           `json:"builtin"`
	CreatedAt  time.Time      `json:"created_at"`
}

// Secret reports the record's stored secret (client secret or key),
// whichever its kind uses.
func (a *AuthRecord) Secret() string {
	if a.Descriptor.ClientSecret != "" {
		return a.Descriptor.ClientSecret
	}
	return a.Descriptor.Key
}

// MarshalJSON masks stored secrets: client_secret and key never leave the
// server. HasSecret tells the UI a secret is on file; updates with an empty
// secret keep the stored one (see UpdateAuthRecord).
func (a AuthRecord) MarshalJSON() ([]byte, error) {
	type Alias AuthRecord
	out := Alias(a)
	hasSecret := out.Descriptor.ClientSecret != "" || out.Descriptor.Key != ""
	out.Descriptor.ClientSecret = ""
	out.Descriptor.Key = ""
	return json.Marshal(struct {
		Alias
		HasSecret  bool `json:"has_secret"`
		Strictness int  `json:"strictness"`
	}{out, hasSecret, AuthStrictness(a.Kind)})
}

func (s *Store) CreateAuthRecord(name, kind string, d AuthDescriptor) (*AuthRecord, error) {
	if !ValidAuthKind(kind) {
		return nil, fmt.Errorf("unknown auth kind %q", kind)
	}
	descJSON, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(
		"INSERT INTO auth_records (name, kind, descriptor) VALUES (?, ?, ?)",
		name, kind, string(descJSON),
	)
	if err != nil {
		if isUnique(err) {
			return nil, errors.New("an auth record with that name already exists")
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetAuthRecord(id)
}

func scanAuthRecord(row interface{ Scan(...any) error }) (*AuthRecord, error) {
	a := &AuthRecord{}
	var descStr, createdAt string
	var builtin int
	err := row.Scan(&a.ID, &a.Name, &a.Kind, &descStr, &builtin, &createdAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(descStr), &a.Descriptor); err != nil {
		return nil, err
	}
	a.Builtin = builtin != 0
	a.CreatedAt = parseTime(createdAt)
	return a, nil
}

const authRecordCols = "id, name, kind, descriptor, builtin, created_at"

func (s *Store) GetAuthRecord(id int64) (*AuthRecord, error) {
	a, err := scanAuthRecord(s.db.QueryRow(
		"SELECT "+authRecordCols+" FROM auth_records WHERE id = ?", id))
	if err == sql.ErrNoRows {
		return nil, errors.New("auth record not found")
	}
	return a, err
}

func (s *Store) GetAuthRecordByName(name string) (*AuthRecord, error) {
	a, err := scanAuthRecord(s.db.QueryRow(
		"SELECT "+authRecordCols+" FROM auth_records WHERE name = ?", name))
	if err == sql.ErrNoRows {
		return nil, errors.New("auth record not found")
	}
	return a, err
}

// BuiltinAuthID returns the id of the seeded builtin record of the given
// kind (AuthSSHKey or AuthAnonymous).
func (s *Store) BuiltinAuthID(kind string) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		"SELECT id FROM auth_records WHERE kind = ? AND builtin = 1", kind).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("builtin %s auth record missing", kind)
	}
	return id, err
}

func (s *Store) ListAuthRecords() ([]*AuthRecord, error) {
	rows, err := s.db.Query("SELECT " + authRecordCols + " FROM auth_records ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuthRecord
	for rows.Next() {
		a, err := scanAuthRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAuthRecord replaces a record's fields. A builtin record keeps its
// name and kind. An empty incoming secret keeps the stored one, so the
// masked serialization can round-trip through an edit form.
func (s *Store) UpdateAuthRecord(id int64, name, kind string, d AuthDescriptor) error {
	existing, err := s.GetAuthRecord(id)
	if err != nil {
		return err
	}
	if !ValidAuthKind(kind) {
		return fmt.Errorf("unknown auth kind %q", kind)
	}
	if existing.Builtin {
		name, kind = existing.Name, existing.Kind
	}
	if d.ClientSecret == "" {
		d.ClientSecret = existing.Descriptor.ClientSecret
	}
	if d.Key == "" {
		d.Key = existing.Descriptor.Key
	}
	descJSON, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		"UPDATE auth_records SET name = ?, kind = ?, descriptor = ? WHERE id = ?",
		name, kind, string(descJSON), id,
	)
	if isUnique(err) {
		return errors.New("an auth record with that name already exists")
	}
	return err
}

// DeleteAuthRecord removes a record. Builtins are undeletable, and a record
// still referenced by a service or app slot must be unlinked first.
func (s *Store) DeleteAuthRecord(id int64) error {
	existing, err := s.GetAuthRecord(id)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return errors.New("cannot delete a built-in auth record")
	}
	var refs int
	err = s.db.QueryRow(`
    SELECT (SELECT COUNT(*) FROM services WHERE protected_by = ?1 OR acts_as = ?1)
         + (SELECT COUNT(*) FROM apps WHERE protected_by = ?1)`, id).Scan(&refs)
	if err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("auth record is used by %d service(s) or app(s)", refs)
	}
	_, err = s.db.Exec("DELETE FROM auth_records WHERE id = ?", id)
	return err
}
