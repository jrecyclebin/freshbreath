package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Store struct{ db *sql.DB }

// NewStore wraps an open *sql.DB so callers outside package db can construct
// a Store without touching its unexported db field.
func NewStore(database *sql.DB) *Store { return &Store{db: database} }

// DB exposes the underlying connection for callers that run ad-hoc SQL
// (e.g. the host-key listing in the hub). Prefer adding real Store methods.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Migrate() error {
	// Pre-auth-rework refresh_families used user_email/service_id columns.
	// The table only holds live sessions, so the cheap path is a drop:
	// everyone re-logs in once and the new shape is created below.
	var oldFamilies bool
	s.db.QueryRow("SELECT COUNT(*) > 0 FROM pragma_table_info('refresh_families') WHERE name='user_email'").Scan(&oldFamilies)
	if oldFamilies {
		if _, err := s.db.Exec("DROP TABLE refresh_families"); err != nil {
			return err
		}
	}

	_, err := s.db.Exec(`
    CREATE TABLE IF NOT EXISTS apps (
      id          INTEGER PRIMARY KEY,
      nonce       TEXT NOT NULL UNIQUE,
      name        TEXT NOT NULL,
      url         TEXT NOT NULL,
      owner_id    INTEGER,
      environment TEXT DEFAULT 'Development',
      created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
    );

    CREATE TABLE IF NOT EXISTS services (
      id             INTEGER PRIMARY KEY,
      name           TEXT NOT NULL UNIQUE,
      url            TEXT NOT NULL UNIQUE,
      descriptor     TEXT NOT NULL DEFAULT '{}',
      created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
    );

    CREATE TABLE IF NOT EXISTS users (
      id         INTEGER PRIMARY KEY,
      name       TEXT NOT NULL UNIQUE,
      email      TEXT NOT NULL UNIQUE,
      role       TEXT NOT NULL DEFAULT 'Member',
      status     TEXT NOT NULL DEFAULT 'Active',
      last_seen  TEXT,
      created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
    );

    CREATE TABLE IF NOT EXISTS roles (
      id          INTEGER PRIMARY KEY,
      name        TEXT NOT NULL UNIQUE,
      description TEXT NOT NULL,
      members     INTEGER NOT NULL DEFAULT 0
    );

    CREATE TABLE IF NOT EXISTS app_members (
      app_nonce TEXT NOT NULL,
      user_id   INTEGER NOT NULL,
      PRIMARY KEY (app_nonce, user_id)
    );

    CREATE TABLE IF NOT EXISTS app_service_links (
      app_nonce   TEXT NOT NULL,
      service_id  INTEGER NOT NULL,
      allowed     INTEGER NOT NULL DEFAULT 1,
      first_used  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
      PRIMARY KEY (app_nonce, service_id)
    );

    CREATE TABLE IF NOT EXISTS audit_log (
      id         INTEGER PRIMARY KEY,
      actor      TEXT NOT NULL,
      action     TEXT NOT NULL,
      target     TEXT NOT NULL,
      created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
    );

    CREATE TABLE IF NOT EXISTS settings (
      key   TEXT NOT NULL PRIMARY KEY,
      value TEXT NOT NULL DEFAULT ''
    );

    CREATE TABLE IF NOT EXISTS ssh_host_keys (
      host        TEXT NOT NULL,
      port        INTEGER NOT NULL DEFAULT 22,
      fingerprint TEXT NOT NULL,
      key_data    BLOB NOT NULL,
      trusted_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
      PRIMARY KEY (host, port)
    );

    CREATE TABLE IF NOT EXISTS oauth_clients (
      client_id      TEXT NOT NULL PRIMARY KEY,
      client_secret  TEXT NOT NULL,
      redirect_uris  TEXT NOT NULL DEFAULT '[]',
      created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
    );

    CREATE INDEX IF NOT EXISTS idx_services_url ON services(url);
    CREATE INDEX IF NOT EXISTS idx_services_name ON services(name);
    CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
    CREATE INDEX IF NOT EXISTS idx_users_name ON users(name);
    CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);
    CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
    CREATE INDEX IF NOT EXISTS idx_app_members_nonce ON app_members(app_nonce);
    CREATE INDEX IF NOT EXISTS idx_app_service_app ON app_service_links(app_nonce);
    CREATE INDEX IF NOT EXISTS idx_app_service_svc ON app_service_links(service_id);
    CREATE INDEX IF NOT EXISTS idx_oauth_clients_id ON oauth_clients(client_id);

    CREATE TABLE IF NOT EXISTS auth_records (
      id         INTEGER PRIMARY KEY,
      name       TEXT NOT NULL UNIQUE,
      kind       TEXT NOT NULL,
      descriptor TEXT NOT NULL DEFAULT '{}',
      builtin    INTEGER NOT NULL DEFAULT 0,
      created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
    );

    CREATE TABLE IF NOT EXISTS refresh_families (
      id           TEXT PRIMARY KEY,
      subject      TEXT NOT NULL,
      auth_id      INTEGER NOT NULL,
      device_label TEXT,
      current_jti  TEXT NOT NULL,
      prev_jti     TEXT,
      created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
      expires_at   TEXT NOT NULL,
      rotated_at   TEXT,
      last_used_at TEXT,
      revoked      INTEGER NOT NULL DEFAULT 0
    );
    CREATE INDEX IF NOT EXISTS idx_refresh_families_subject ON refresh_families(subject);
    CREATE INDEX IF NOT EXISTS idx_refresh_families_expires_at ON refresh_families(expires_at);

    CREATE TABLE IF NOT EXISTS update_feeds (
      id                   TEXT PRIMARY KEY,
      url                  TEXT NOT NULL DEFAULT '',
      mode                 TEXT NOT NULL,
      key_hex              TEXT NOT NULL,
      name                 TEXT NOT NULL DEFAULT '',
      created_by           INTEGER NOT NULL DEFAULT 0,
      created_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
      last_applied_version TEXT NOT NULL DEFAULT '',
      last_applied_at      TEXT,
      last_seen_version    TEXT NOT NULL DEFAULT '',
      last_etag            TEXT NOT NULL DEFAULT '',
      last_modified        TEXT NOT NULL DEFAULT '',
      last_error           TEXT NOT NULL DEFAULT '',
      last_error_at        TEXT
    );
    CREATE INDEX IF NOT EXISTS idx_update_feeds_mode ON update_feeds(mode);
  `)
	if err != nil {
		return err
	}

	// Add metadata column to users if missing
	var hasMetadata bool
	s.db.QueryRow("SELECT COUNT(*) > 0 FROM pragma_table_info('users') WHERE name='metadata'").Scan(&hasMetadata)
	if !hasMetadata {
		_, err = s.db.Exec("ALTER TABLE users ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'")
		if err != nil {
			return err
		}
	}

	// Add details column to apps if missing
	var hasDetails bool
	s.db.QueryRow("SELECT COUNT(*) > 0 FROM pragma_table_info('apps') WHERE name='details'").Scan(&hasDetails)
	if !hasDetails {
		_, err = s.db.Exec("ALTER TABLE apps ADD COLUMN details TEXT NOT NULL DEFAULT '{}'")
		if err != nil {
			return err
		}
	}

	// Add last_seen_version to update_feeds if missing. Cached validators are
	// dropped alongside: they predate the column, and a 304 decided without a
	// seen-version would be wrong for feeds pending at upgrade time. The next
	// check re-fetches (a 200) and stamps it.
	var hasSeenVersion bool
	s.db.QueryRow("SELECT COUNT(*) > 0 FROM pragma_table_info('update_feeds') WHERE name='last_seen_version'").Scan(&hasSeenVersion)
	if !hasSeenVersion {
		_, err = s.db.Exec("ALTER TABLE update_feeds ADD COLUMN last_seen_version TEXT NOT NULL DEFAULT ''")
		if err != nil {
			return err
		}
		if _, err = s.db.Exec("UPDATE update_feeds SET last_etag = '', last_modified = ''"); err != nil {
			return err
		}
	}

	// Auth slot columns (design/decoupled-auth.md): protected_by is the
	// inbound gate, acts_as the outbound credential. Real columns, not
	// descriptor keys — they're validated on write and joined on read.
	for _, col := range []struct{ table, name string }{
		{"services", "protected_by"},
		{"services", "acts_as"},
		{"apps", "protected_by"},
	} {
		var has bool
		s.db.QueryRow("SELECT COUNT(*) > 0 FROM pragma_table_info(?) WHERE name=?", col.table, col.name).Scan(&has)
		if !has {
			if _, err := s.db.Exec("ALTER TABLE " + col.table + " ADD COLUMN " + col.name + " INTEGER"); err != nil {
				return err
			}
		}
	}

	// Seed the built-in auth records: the passphrase login and the explicit
	// "open to anyone on the LAN" record. Undeletable; name/kind frozen.
	for _, rec := range []struct{ name, kind string }{
		{"Built-in", "ssh_key"},
		{"Anonymous", "anonymous"},
	} {
		_, err := s.db.Exec(`
      INSERT INTO auth_records (name, kind, builtin)
      SELECT ?, ?, 1
      WHERE NOT EXISTS (SELECT 1 FROM auth_records WHERE kind = ? AND builtin = 1)`,
			rec.name, rec.kind, rec.kind)
		if err != nil {
			return err
		}
	}

	// Seed built-in roles if empty
	var roleCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM roles").Scan(&roleCount); err != nil {
		return err
	}
	if roleCount == 0 {
		_, err = s.db.Exec(`
      INSERT INTO roles (name, description, members) VALUES
        ('Superuser', 'Full access across the system.', 0),
        ('Admin',     'Manage apps, services, and users.', 0),
        ('Member',    'Read & write within assigned apps.', 0),
        ('Read-only', 'View-only access.', 0)
    `)
		if err != nil {
			return err
		}
	}

	return nil
}

// CreateApp registers an app. A nil protectedBy defaults to the builtin
// Anonymous record — most apps are open on creation, and the gate is an
// explicit choice either way (empty would mean inherit-admin, which is the
// wrong default for a fresh dashboard).
func (s *Store) CreateApp(name, env string, url string, ownerID *int64, protectedBy *int64) (string, error) {
	if env == "" {
		env = "Development"
	}
	if protectedBy == nil {
		anonID, err := s.BuiltinAuthID(AuthAnonymous)
		if err != nil {
			return "", err
		}
		protectedBy = &anonID
	}
	nonce := GenNonce()
	for {
		_, err := s.db.Exec(
			"INSERT INTO apps (nonce, name, environment, url, owner_id, protected_by) VALUES (?, ?, ?, ?, ?, ?)",
			nonce, name, env, url, ownerID, nullableID(protectedBy))
		if err == nil {
			return nonce, nil
		}
		if isUnique(err) {
			nonce = GenNonce()
			continue
		}
		return "", err
	}
}

func (s *Store) ListApps() ([]map[string]interface{}, error) {
	rows, err := s.db.Query(`
    SELECT a.nonce, a.name, a.environment, a.url, a.created_at, a.details, a.protected_by,
           u.id, u.name,
           (SELECT COUNT(DISTINCT user_id) FROM app_members WHERE app_nonce = a.nonce) as member_count,
           (SELECT COUNT(DISTINCT service_id) FROM app_service_links WHERE app_nonce = a.nonce AND allowed = 1) as service_count
    FROM apps a
    LEFT JOIN users u ON a.owner_id = u.id
    ORDER BY a.name
  `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []map[string]interface{}
	for rows.Next() {
		var nonce, name, env, url, created, detailsStr string
		var ownerID, protectedBy sql.NullInt64
		var ownerName sql.NullString
		var memberCount, serviceCount int
		if err := rows.Scan(&nonce, &name, &env, &url, &created, &detailsStr, &protectedBy, &ownerID, &ownerName, &memberCount, &serviceCount); err != nil {
			return nil, err
		}
		app := map[string]interface{}{
			"nonce":         nonce,
			"name":          name,
			"environment":   env,
			"url":           url,
			"created_at":    created,
			"member_count":  memberCount,
			"service_count": serviceCount,
		}
		if protectedBy.Valid {
			app["protected_by"] = protectedBy.Int64
		}
		if ownerID.Valid {
			app["owner_id"] = ownerID.Int64
			app["owner_name"] = ownerName.String
		}
		if detailsStr != "" && detailsStr != "{}" {
			var d AppDetails
			if json.Unmarshal([]byte(detailsStr), &d) == nil {
				app["details"] = d
			}
		}
		apps = append(apps, app)
	}

	return apps, rows.Err()
}

func (s *Store) ListAppsForUser(userID int64) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(`
    SELECT a.nonce, a.name, a.environment, a.url, a.created_at, a.details, a.protected_by,
           u.id, u.name,
           (SELECT COUNT(DISTINCT user_id) FROM app_members WHERE app_nonce = a.nonce) as member_count,
           (SELECT COUNT(DISTINCT service_id) FROM app_service_links WHERE app_nonce = a.nonce AND allowed = 1) as service_count
    FROM apps a
    LEFT JOIN users u ON a.owner_id = u.id
    WHERE a.owner_id = ? OR a.nonce IN (SELECT app_nonce FROM app_members WHERE user_id = ?)
    ORDER BY a.name
  `, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []map[string]interface{}
	for rows.Next() {
		var nonce, name, env, url, created, detailsStr string
		var ownerID, protectedBy sql.NullInt64
		var ownerName sql.NullString
		var memberCount, serviceCount int
		if err := rows.Scan(&nonce, &name, &env, &url, &created, &detailsStr, &protectedBy, &ownerID, &ownerName, &memberCount, &serviceCount); err != nil {
			return nil, err
		}
		app := map[string]interface{}{
			"nonce":         nonce,
			"name":          name,
			"environment":   env,
			"url":           url,
			"created_at":    created,
			"member_count":  memberCount,
			"service_count": serviceCount,
		}
		if protectedBy.Valid {
			app["protected_by"] = protectedBy.Int64
		}
		if ownerID.Valid {
			app["owner_id"] = ownerID.Int64
			app["owner_name"] = ownerName.String
		}
		if detailsStr != "" && detailsStr != "{}" {
			var d AppDetails
			if json.Unmarshal([]byte(detailsStr), &d) == nil {
				app["details"] = d
			}
		}
		apps = append(apps, app)
	}

	return apps, rows.Err()
}

func (s *Store) GetApp(nonce string) (*App, error) {
	row := s.db.QueryRow(
		"SELECT id, nonce, name, url, environment, owner_id, details, protected_by FROM apps WHERE nonce = ?",
		nonce,
	)
	a := &App{}
	var detailsStr string
	var ownerID, protectedBy sql.NullInt64
	err := row.Scan(&a.ID, &a.Nonce, &a.Name, &a.URL, &a.Environment, &ownerID, &detailsStr, &protectedBy)
	if err == sql.ErrNoRows {
		return nil, errors.New("app not found")
	}
	if err != nil {
		return nil, err
	}
	if ownerID.Valid {
		a.OwnerID = &ownerID.Int64
	}
	if protectedBy.Valid {
		a.ProtectedBy = &protectedBy.Int64
	}
	if detailsStr != "" && detailsStr != "{}" {
		a.Details = &AppDetails{}
		json.Unmarshal([]byte(detailsStr), a.Details)
	}
	return a, nil
}

func (s *Store) UpdateAppDetails(nonce string, details *AppDetails) error {
	data, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE apps SET details = ? WHERE nonce = ?", string(data), nonce)
	return err
}

// ListHostedApps returns every app with its environment and details blob.
// Routability is decided by the caller: a slot is served iff its directory
// exists on disk, so this no longer gates on LastUploaded.
func (s *Store) ListHostedApps() ([]*App, error) {
	rows, err := s.db.Query("SELECT nonce, name, url, environment, details FROM apps")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []*App
	for rows.Next() {
		a := &App{}
		var detailsStr string
		if err := rows.Scan(&a.Nonce, &a.Name, &a.URL, &a.Environment, &detailsStr); err != nil {
			return nil, err
		}
		if detailsStr != "" && detailsStr != "{}" {
			d := AppDetails{}
			if json.Unmarshal([]byte(detailsStr), &d) == nil {
				a.Details = &d
			}
		}
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

func (s *Store) UpdateApp(nonce string, name, env string, url string, ownerID *int64, protectedBy *int64) error {
	_, err := s.db.Exec(
		"UPDATE apps SET name = ?, environment = ?, url = ?, owner_id = ?, protected_by = ? WHERE nonce = ?",
		name, env, url, ownerID, nullableID(protectedBy), nonce,
	)
	return err
}

func (s *Store) DeleteApp(nonce string) error {
	_, err := s.db.Exec("DELETE FROM app_members WHERE app_nonce = ?", nonce)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM app_service_links WHERE app_nonce = ?", nonce)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM apps WHERE nonce = ?", nonce)
	return err
}

// ── Users ──

func (s *Store) CreateUser(name, email, role, status string) (*User, error) {
	if role == "" {
		role = "Member"
	}
	if status == "" {
		status = "Active"
	}
	res, err := s.db.Exec(
		"INSERT INTO users (name, email, role, status, metadata) VALUES (?, ?, ?, ?, '{}')",
		name, email, role, status,
	)
	if err != nil {
		if isUnique(err) {
			return nil, errors.New("user with that email already exists")
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetUser(id)
}

func (s *Store) GetUser(id int64) (*User, error) {
	row := s.db.QueryRow(
		"SELECT id, name, email, role, status, last_seen, created_at, metadata FROM users WHERE id = ?", id,
	)
	u := &User{}
	var lastSeen sql.NullString
	var createdAt string
	var metadataStr string
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Status, &lastSeen, &createdAt, &metadataStr)
	if err == sql.ErrNoRows {
		return nil, errors.New("user not found")
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if lastSeen.Valid {
		if t, err := time.Parse(time.RFC3339, lastSeen.String); err == nil {
			u.LastSeen = &t
		}
	}
	if metadataStr != "" && metadataStr != "{}" {
		u.Metadata = &UserMetadata{}
		json.Unmarshal([]byte(metadataStr), u.Metadata)
	}
	return u, nil
}

func (s *Store) GetUserByEmail(email string) (*User, error) {
	row := s.db.QueryRow(
		"SELECT id, name, email, role, status, last_seen, created_at, metadata FROM users WHERE email = ?", email,
	)
	u := &User{}
	var lastSeen sql.NullString
	var createdAt string
	var metadataStr string
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Status, &lastSeen, &createdAt, &metadataStr)
	if err == sql.ErrNoRows {
		return nil, errors.New("user not found")
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if lastSeen.Valid {
		if t, err := time.Parse(time.RFC3339, lastSeen.String); err == nil {
			u.LastSeen = &t
		}
	}
	if metadataStr != "" && metadataStr != "{}" {
		u.Metadata = &UserMetadata{}
		json.Unmarshal([]byte(metadataStr), u.Metadata)
	}
	return u, nil
}

func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query("SELECT id, name, email, role, status, last_seen, created_at, metadata FROM users ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u := &User{}
		var lastSeen sql.NullString
		var createdAt string
		var metadataStr string
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Status, &lastSeen, &createdAt, &metadataStr); err != nil {
			return nil, err
		}
		u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if lastSeen.Valid {
			if t, err := time.Parse(time.RFC3339, lastSeen.String); err == nil {
				u.LastSeen = &t
			}
		}
		if metadataStr != "" && metadataStr != "{}" {
			u.Metadata = &UserMetadata{}
			json.Unmarshal([]byte(metadataStr), u.Metadata)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) TouchLastSeen(userID int64) error {
	_, err := s.db.Exec(
		"UPDATE users SET last_seen = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?", userID,
	)
	return err
}

func (s *Store) UpdateUser(id int64, name, email, role, status string, metadata *UserMetadata) error {
	var metadataStr string
	if metadata != nil {
		data, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		metadataStr = string(data)
	} else {
		metadataStr = "{}"
	}
	_, err := s.db.Exec(
		"UPDATE users SET name = ?, email = ?, role = ?, status = ?, metadata = ? WHERE id = ?",
		name, email, role, status, metadataStr, id,
	)
	return err
}

func (s *Store) DeleteUser(id int64) error {
	_, err := s.db.Exec("DELETE FROM app_members WHERE user_id = ?", id)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return err
	}
	// Clear owner references
	_, err = s.db.Exec("UPDATE apps SET owner_id = NULL WHERE owner_id = ?", id)
	return err
}

// ── Roles ──

func (s *Store) ListRoles() ([]*Role, error) {
	rows, err := s.db.Query(`
    SELECT r.id, r.name, r.description,
      (SELECT COUNT(*) FROM users WHERE role = r.name) as members
    FROM roles r
    ORDER BY r.id
  `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []*Role
	for rows.Next() {
		r := &Role{}
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.Members); err != nil {
			return nil, err
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

func (s *Store) UpdateRole(id int64, description string) error {
	_, err := s.db.Exec("UPDATE roles SET description = ? WHERE id = ?", description, id)
	return err
}

// ── Audit ──

func (s *Store) LogAudit(actor, action, target string) error {
	_, err := s.db.Exec(
		"INSERT INTO audit_log (actor, action, target) VALUES (?, ?, ?)",
		actor, action, target,
	)
	return err
}

func (s *Store) ListAudit(limit int) ([]*AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		"SELECT id, created_at, actor, action, target FROM audit_log ORDER BY created_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*AuditEntry
	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.When, &e.Actor, &e.Action, &e.Target); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ── Services ──

const serviceCols = "id, name, url, descriptor, protected_by, acts_as"

// nullableID converts an optional record reference for binding: nil stays
// NULL in the database rather than 0.
func nullableID(id *int64) interface{} {
	if id == nil {
		return nil
	}
	return *id
}

func scanService(row interface{ Scan(...any) error }) (*Service, error) {
	svc := &Service{}
	var descStr string
	var protectedBy, actsAs sql.NullInt64
	err := row.Scan(&svc.ID, &svc.Name, &svc.URL, &descStr, &protectedBy, &actsAs)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(descStr), &svc.Descriptor); err != nil {
		return nil, err
	}
	if protectedBy.Valid {
		svc.ProtectedBy = &protectedBy.Int64
	}
	if actsAs.Valid {
		svc.ActsAs = &actsAs.Int64
	}
	return svc, nil
}

func (s *Store) RegisterService(name, serviceURL string, descriptor ServiceDescriptor, protectedBy, actsAs *int64) (int64, error) {
	descJSON, err := json.Marshal(descriptor)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		"INSERT INTO services (name, url, descriptor, protected_by, acts_as) VALUES (?, ?, ?, ?, ?)",
		name, serviceURL, string(descJSON), nullableID(protectedBy), nullableID(actsAs),
	)
	if err != nil {
		if isUnique(err) {
			return 0, errors.New("that url is already registered as a service")
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetService(serviceID int64) (*Service, error) {
	svc, err := scanService(s.db.QueryRow(
		"SELECT "+serviceCols+" FROM services WHERE id = ?", serviceID))
	if err == sql.ErrNoRows {
		return nil, errors.New("service not found")
	}
	return svc, err
}

func (s *Store) GetServiceByURL(serviceURL string) (*Service, error) {
	svc, err := scanService(s.db.QueryRow(
		"SELECT "+serviceCols+" FROM services WHERE rtrim(url, '/') = rtrim(?, '/')", serviceURL))
	if err == sql.ErrNoRows {
		return nil, errors.New("service not found")
	}
	return svc, err
}

// GetServiceByName looks up a service by name. It returns an error if no
// service has the name or if more than one shares it.
func (s *Store) GetServiceByName(name string) (*Service, error) {
	svc, err := scanService(s.db.QueryRow(
		"SELECT "+serviceCols+" FROM services WHERE name = ?", name))
	if err == sql.ErrNoRows {
		return nil, errors.New("service not found")
	}
	return svc, err
}

func (s *Store) UpdateService(id int64, name, serviceURL string, descriptor ServiceDescriptor, protectedBy, actsAs *int64) error {
	descJSON, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		"UPDATE services SET name = ?, url = ?, descriptor = ?, protected_by = ?, acts_as = ? WHERE id = ?",
		name, serviceURL, string(descJSON), nullableID(protectedBy), nullableID(actsAs), id,
	)
	return err
}

func (s *Store) DeleteService(id int64) error {
	_, err := s.db.Exec("DELETE FROM services WHERE id = ?", id)
	return err
}

func (s *Store) AddAppMember(appNonce string, userID int64) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO app_members (app_nonce, user_id) VALUES (?, ?)",
		appNonce, userID,
	)
	return err
}

func (s *Store) RemoveAppMember(appNonce string, userID int64) error {
	_, err := s.db.Exec(
		"DELETE FROM app_members WHERE app_nonce = ? AND user_id = ?",
		appNonce, userID,
	)
	return err
}

func (s *Store) ListAppMembers(appNonce string) ([]int64, error) {
	rows, err := s.db.Query("SELECT user_id FROM app_members WHERE app_nonce = ?", appNonce)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) SetAppMembers(appNonce string, userIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("DELETE FROM app_members WHERE app_nonce = ?", appNonce)
	if err != nil {
		return err
	}
	for _, uid := range userIDs {
		_, err = tx.Exec("INSERT INTO app_members (app_nonce, user_id) VALUES (?, ?)", appNonce, uid)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetUserApps(userID int64) ([]string, error) {
	rows, err := s.db.Query("SELECT app_nonce FROM app_members WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nonces []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		nonces = append(nonces, n)
	}
	return nonces, rows.Err()
}

func (s *Store) SetUserApps(userID int64, appNonces []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("DELETE FROM app_members WHERE user_id = ?", userID)
	if err != nil {
		return err
	}
	for _, nonce := range appNonces {
		_, err = tx.Exec("INSERT INTO app_members (app_nonce, user_id) VALUES (?, ?)", nonce, userID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetAppsUsingService(serviceID int64) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(`
    SELECT DISTINCT a.nonce, a.name
    FROM apps a
    JOIN app_service_links l ON a.nonce = l.app_nonce
    WHERE l.service_id = ?
  `, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []map[string]interface{}
	for rows.Next() {
		var nonce, name string
		if err := rows.Scan(&nonce, &name); err != nil {
			return nil, err
		}
		apps = append(apps, map[string]interface{}{"nonce": nonce, "name": name})
	}
	return apps, rows.Err()
}

func (s *Store) LinkAppService(appNonce string, serviceID int64) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO app_service_links (app_nonce, service_id) VALUES (?, ?)",
		appNonce, serviceID,
	)
	return err
}

func (s *Store) IsAppMember(appNonce string, userID int64) (bool, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM app_members WHERE app_nonce = ? AND user_id = ?", appNonce, userID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

type AppServiceLink struct {
	ServiceID int64  `json:"service_id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Allowed   bool   `json:"allowed"`
}

func (s *Store) SetAppServiceLinks(appNonce string, serviceIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM app_service_links WHERE app_nonce = ?", appNonce); err != nil {
		tx.Rollback()
		return err
	}
	for _, sid := range serviceIDs {
		if _, err := tx.Exec("INSERT INTO app_service_links (app_nonce, service_id, allowed) VALUES (?, ?, 1)", appNonce, sid); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetAppServiceLinks(appNonce string) ([]*AppServiceLink, error) {
	rows, err := s.db.Query(`
    SELECT s.id, s.name, s.url, COALESCE(gsl.allowed, 0)
    FROM services s
    LEFT JOIN app_service_links gsl ON s.id = gsl.service_id AND gsl.app_nonce = ?
    ORDER BY s.name
  `, appNonce)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []*AppServiceLink
	for rows.Next() {
		link := &AppServiceLink{}
		var allowed int
		if err := rows.Scan(&link.ServiceID, &link.Name, &link.URL, &allowed); err != nil {
			return nil, err
		}
		link.Allowed = allowed != 0
		links = append(links, link)
	}
	return links, rows.Err()
}

func (s *Store) SetAppServiceAllowed(appNonce string, serviceID int64, allowed bool) error {
	// Ensure row exists first
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO app_service_links (app_nonce, service_id) VALUES (?, ?)",
		appNonce, serviceID,
	)
	if err != nil {
		return err
	}
	a := 0
	if allowed {
		a = 1
	}
	_, err = s.db.Exec(
		"UPDATE app_service_links SET allowed = ? WHERE app_nonce = ? AND service_id = ?",
		a, appNonce, serviceID,
	)
	return err
}

func (s *Store) IsServiceAllowedForApp(appNonce string, serviceID int64) (bool, error) {
	var allowed int
	err := s.db.QueryRow(
		"SELECT COALESCE((SELECT allowed FROM app_service_links WHERE app_nonce = ? AND service_id = ?), 0)",
		appNonce, serviceID,
	).Scan(&allowed)
	return allowed != 0, err
}

func (s *Store) ListServices() ([]*Service, error) {
	rows, err := s.db.Query("SELECT " + serviceCols + " FROM services ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Service
	for rows.Next() {
		svc, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, rows.Err()
}

func GenNonce() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return (len(msg) > 6 && msg[:6] == "UNIQUE") ||
		(len(msg) > 13 && msg[:13] == "UNIQUE const")
}

// ── SSH Service ──

func (s *Store) EnsureSSHService() (int64, error) {
	svc, err := s.GetServiceByURL("ssh://")
	if err == nil {
		return svc.ID, nil
	}
	id, err := s.RegisterService("SSH", "ssh://", ServiceDescriptor{Type: "ssh"}, nil, nil)
	if err != nil {
		return 0, fmt.Errorf("seed SSH service: %w", err)
	}
	return id, nil
}

func (s *Store) IsSSHService(id int64) bool {
	svc, err := s.GetService(id)
	if err != nil {
		return false
	}
	return svc.Descriptor.Type == "ssh"
}

// ── Settings ──

func (s *Store) GetSetting(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
	return err
}

func (s *Store) GetOrCreateLocalSigningKey() ([]byte, error) {
	val, err := s.GetSetting("local_signing_key")
	if err != nil {
		return nil, err
	}
	if val != "" {
		return hex.DecodeString(val)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := s.SetSetting("local_signing_key", hex.EncodeToString(key)); err != nil {
		return nil, err
	}
	return key, nil
}

// GetSSHHostKey returns the stored host key data and fingerprint for a host:port.
// Returns nil if no key is on record (first connection).
func (s *Store) GetSSHHostKey(host string, port int) (keyData []byte, fingerprint string, err error) {
	row := s.db.QueryRow(
		"SELECT key_data, fingerprint FROM ssh_host_keys WHERE host = ? AND port = ?",
		host, port,
	)
	var data []byte
	var fp string
	if err := row.Scan(&data, &fp); err != nil {
		if err == sql.ErrNoRows {
			return nil, "", nil
		}
		return nil, "", err
	}
	return data, fp, nil
}

// StoreSSHHostKey records a trusted host key (TOFU — trust on first use).
func (s *Store) StoreSSHHostKey(host string, port int, keyData []byte, fingerprint string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO ssh_host_keys (host, port, fingerprint, key_data, trusted_at) VALUES (?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))",
		host, port, fingerprint, keyData,
	)
	return err
}

// DeleteSSHHostKey removes a stored host key. Used when an admin wants to
// accept a changed key.
func (s *Store) DeleteSSHHostKey(host string, port int) error {
	_, err := s.db.Exec("DELETE FROM ssh_host_keys WHERE host = ? AND port = ?", host, port)
	return err
}

// ── OAuth Clients (DCR persistence) ──

// RegisterOAuthClient persists a dynamically-registered OAuth client.
func (s *Store) RegisterOAuthClient(clientID, clientSecret string, redirectURIs []string) error {
	urisJSON, err := json.Marshal(redirectURIs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		"INSERT INTO oauth_clients (client_id, client_secret, redirect_uris) VALUES (?, ?, ?)",
		clientID, clientSecret, string(urisJSON),
	)
	return err
}

// GetOAuthClient looks up a registered OAuth client by ID.
func (s *Store) GetOAuthClient(clientID string) (clientSecret string, redirectURIs []string, ok bool, err error) {
	var secretStr, urisStr string
	err = s.db.QueryRow(
		"SELECT client_secret, redirect_uris FROM oauth_clients WHERE client_id = ?", clientID,
	).Scan(&secretStr, &urisStr)
	if err == sql.ErrNoRows {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	var uris []string
	if err := json.Unmarshal([]byte(urisStr), &uris); err != nil {
		return "", nil, false, err
	}
	return secretStr, uris, true, nil
}

// ── Refresh Token Families ──

type RefreshFamily struct {
	ID          string
	Subject     string // "frbr:<user_id>" or "ext:<provider>:<sub>"
	AuthID      int64  // auth record the session is bound to
	DeviceLabel string
	CurrentJTI  string
	PrevJTI     string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	RotatedAt   time.Time
	LastUsedAt  time.Time
	Revoked     bool
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func (s *Store) CreateRefreshFamily(fam *RefreshFamily) error {
	_, err := s.db.Exec(
		`INSERT INTO refresh_families
      (id, subject, auth_id, device_label, current_jti, expires_at)
      VALUES (?, ?, ?, ?, ?, ?)`,
		fam.ID, fam.Subject, fam.AuthID, fam.DeviceLabel, fam.CurrentJTI, fam.ExpiresAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetRefreshFamily(id string) (*RefreshFamily, bool, error) {
	row := s.db.QueryRow(
		`SELECT id, subject, auth_id, device_label, current_jti, prev_jti,
            created_at, expires_at, rotated_at, last_used_at, revoked
     FROM refresh_families WHERE id = ?`, id,
	)
	var f RefreshFamily
	var r int
	var c, e string
	var prev sql.NullString
	var rot, lu sql.NullString
	err := row.Scan(&f.ID, &f.Subject, &f.AuthID, &f.DeviceLabel, &f.CurrentJTI, &prev, &c, &e, &rot, &lu, &r)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if prev.Valid {
		f.PrevJTI = prev.String
	}
	if rot.Valid {
		f.RotatedAt = parseTime(rot.String)
	}
	if lu.Valid {
		f.LastUsedAt = parseTime(lu.String)
	}
	f.CreatedAt = parseTime(c)
	f.ExpiresAt = parseTime(e)
	f.Revoked = r != 0
	return &f, true, nil
}

func (s *Store) RotateRefreshFamily(id, fromJTI, toJTI string, now time.Time) (ok bool, err error) {
	res, err := s.db.Exec(
		`UPDATE refresh_families
     SET prev_jti = current_jti, current_jti = ?, rotated_at = ?, last_used_at = ?
     WHERE id = ? AND current_jti = ? AND revoked = 0`,
		toJTI, now.Format(time.RFC3339), now.Format(time.RFC3339), id, fromJTI,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) RevokeRefreshFamily(id string) error {
	_, err := s.db.Exec(
		"UPDATE refresh_families SET revoked = 1 WHERE id = ?", id,
	)
	return err
}

func (s *Store) RevokeUserRefreshFamilies(subject string) error {
	_, err := s.db.Exec(
		"UPDATE refresh_families SET revoked = 1 WHERE subject = ?", subject,
	)
	return err
}

func (s *Store) ListRefreshFamilies(subject string) ([]RefreshFamily, error) {
	rows, err := s.db.Query(
		`SELECT id, subject, auth_id, device_label, current_jti, prev_jti,
            created_at, expires_at, rotated_at, last_used_at, revoked
     FROM refresh_families
     WHERE subject = ? AND revoked = 0 AND expires_at > strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
     ORDER BY created_at DESC`,
		subject,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RefreshFamily
	for rows.Next() {
		var f RefreshFamily
		var r int
		var c, e string
		var prev sql.NullString
		var rot, lu sql.NullString
		if err := rows.Scan(&f.ID, &f.Subject, &f.AuthID, &f.DeviceLabel, &f.CurrentJTI, &prev, &c, &e, &rot, &lu, &r); err != nil {
			return nil, err
		}
		if prev.Valid {
			f.PrevJTI = prev.String
		}
		if rot.Valid {
			f.RotatedAt = parseTime(rot.String)
		}
		if lu.Valid {
			f.LastUsedAt = parseTime(lu.String)
		}
		f.CreatedAt = parseTime(c)
		f.ExpiresAt = parseTime(e)
		f.Revoked = r != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) DeleteExpiredRefreshFamilies(now time.Time) (int64, error) {
	res, err := s.db.Exec(
		"DELETE FROM refresh_families WHERE expires_at < ?",
		now.Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ── Update Feeds ──

const updateFeedCols = `id, url, mode, key_hex, name, created_by, created_at,
    last_applied_version, last_applied_at, last_seen_version, last_etag, last_modified, last_error, last_error_at`

func scanUpdateFeed(row interface{ Scan(...any) error }) (*UpdateFeed, error) {
	f := &UpdateFeed{}
	var createdAt string
	var appliedAt, errAt sql.NullString
	err := row.Scan(&f.ID, &f.URL, &f.Mode, &f.KeyHex, &f.Name, &f.CreatedBy, &createdAt,
		&f.LastAppliedVersion, &appliedAt, &f.LastSeenVersion, &f.LastETag, &f.LastModified, &f.LastError, &errAt)
	if err != nil {
		return nil, err
	}
	f.CreatedAt = parseTime(createdAt)
	if appliedAt.Valid && appliedAt.String != "" {
		t := parseTime(appliedAt.String)
		f.LastAppliedAt = &t
	}
	if errAt.Valid && errAt.String != "" {
		t := parseTime(errAt.String)
		f.LastErrorAt = &t
	}
	return f, nil
}

// CreateUpdateFeed inserts a feed row with a caller-supplied key. The caller
// generates both the id and the key so the core layer can return the key in
// the same breath it first persists it.
func (s *Store) CreateUpdateFeed(id, url, mode, name, keyHex string, createdBy int64) error {
	_, err := s.db.Exec(
		`INSERT INTO update_feeds (id, url, mode, key_hex, name, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
		id, url, mode, keyHex, name, createdBy,
	)
	return err
}

func (s *Store) ListUpdateFeeds() ([]*UpdateFeed, error) {
	rows, err := s.db.Query(`SELECT ` + updateFeedCols + ` FROM update_feeds ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UpdateFeed
	for rows.Next() {
		f, err := scanUpdateFeed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) ListUpdateFeedsByMode(mode string) ([]*UpdateFeed, error) {
	rows, err := s.db.Query(`SELECT `+updateFeedCols+` FROM update_feeds WHERE mode = ? ORDER BY created_at`, mode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UpdateFeed
	for rows.Next() {
		f, err := scanUpdateFeed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) GetUpdateFeed(id string) (*UpdateFeed, error) {
	f, err := scanUpdateFeed(s.db.QueryRow(`SELECT `+updateFeedCols+` FROM update_feeds WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, errors.New("update feed not found")
	}
	return f, err
}

func (s *Store) DeleteUpdateFeed(id string) error {
	_, err := s.db.Exec("DELETE FROM update_feeds WHERE id = ?", id)
	return err
}

// UpdateUpdateFeed patches the mutable fields. Editing the URL drops the
// cached etag/last-modified (they describe the old remote) and any recorded
// error (it described the old fetch).
func (s *Store) UpdateUpdateFeed(id string, url, name *string) error {
	if url != nil {
		// The cached validators, seen-version, and recorded error all describe
		// the old remote.
		_, err := s.db.Exec(`UPDATE update_feeds SET url = ?, last_etag = '', last_modified = '', last_seen_version = '', last_error = '', last_error_at = NULL WHERE id = ?`, *url, id)
		if err != nil {
			return err
		}
	}
	if name != nil {
		if _, err := s.db.Exec("UPDATE update_feeds SET name = ? WHERE id = ?", *name, id); err != nil {
			return err
		}
	}
	return nil
}

// StampUpdateFeedApplied records a fully-applied manifest version and clears
// any recorded error (a successful apply is self-healing). It also records
// the version as seen: an applied version is by definition one we saw, which
// keeps the 304 short-circuit (seen vs applied) interpretable even for feeds
// whose pre-apply check took the 304 path.
func (s *Store) StampUpdateFeedApplied(id, version string) error {
	_, err := s.db.Exec(
		`UPDATE update_feeds SET last_applied_version = ?, last_applied_at = ?, last_seen_version = ?, last_error = '', last_error_at = NULL WHERE id = ?`,
		version, time.Now().UTC().Format(time.RFC3339), version, id,
	)
	return err
}

// UpdateFeedCache refreshes the conditional-GET cache (etag/last-modified)
// without touching the version stamps. Used by both check (any 200, before
// the manifest is parsed) and apply.
func (s *Store) UpdateFeedCache(id, etag, lastModified string) error {
	_, err := s.db.Exec(
		"UPDATE update_feeds SET last_etag = ?, last_modified = ? WHERE id = ?",
		etag, lastModified, id,
	)
	return err
}

// UpdateFeedSeen records the version a check just saw (the manifest version
// from the same 200 whose etag/last-modified went into the cache).
func (s *Store) UpdateFeedSeen(id, version string) error {
	_, err := s.db.Exec(
		"UPDATE update_feeds SET last_seen_version = ? WHERE id = ?",
		version, id,
	)
	return err
}

// SetUpdateFeedError records a check/apply failure for admin visibility. An
// empty message clears it.
func (s *Store) SetUpdateFeedError(id, msg string) error {
	if msg == "" {
		_, err := s.db.Exec("UPDATE update_feeds SET last_error = '', last_error_at = NULL WHERE id = ?", id)
		return err
	}
	_, err := s.db.Exec(
		"UPDATE update_feeds SET last_error = ?, last_error_at = ? WHERE id = ?",
		msg, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}
