package main

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

func (s *Store) Migrate() error {
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
      name           TEXT NOT NULL,
      url            TEXT NOT NULL UNIQUE,
      descriptor     TEXT NOT NULL DEFAULT '{}',
      created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
    );

    CREATE TABLE IF NOT EXISTS users (
      id         INTEGER PRIMARY KEY,
      name       TEXT NOT NULL,
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
    CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
    CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);
    CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
    CREATE INDEX IF NOT EXISTS idx_app_members_nonce ON app_members(app_nonce);
    CREATE INDEX IF NOT EXISTS idx_app_service_app ON app_service_links(app_nonce);
    CREATE INDEX IF NOT EXISTS idx_app_service_svc ON app_service_links(service_id);
    CREATE INDEX IF NOT EXISTS idx_oauth_clients_id ON oauth_clients(client_id);
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

func (s *Store) CreateApp(name, env string, url string, ownerID *int64) (string, error) {
  if env == "" { env = "Development" }
  nonce := genNonce()
  for {
    _, err := s.db.Exec("INSERT INTO apps (nonce, name, environment, url, owner_id) VALUES (?, ?, ?, ?, ?)", nonce, name, env, url, ownerID)
    if err == nil {
      return nonce, nil
    }
    if isUnique(err) {
      nonce = genNonce()
      continue
    }
    return "", err
  }
}

func (s *Store) ListApps() ([]map[string]interface{}, error) {
  rows, err := s.db.Query(`
    SELECT a.nonce, a.name, a.environment, a.url, a.created_at, a.details,
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
    var ownerID sql.NullInt64
    var ownerName sql.NullString
    var memberCount, serviceCount int
    if err := rows.Scan(&nonce, &name, &env, &url, &created, &detailsStr, &ownerID, &ownerName, &memberCount, &serviceCount); err != nil {
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
    SELECT a.nonce, a.name, a.environment, a.url, a.created_at, a.details,
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
    var ownerID sql.NullInt64
    var ownerName sql.NullString
    var memberCount, serviceCount int
    if err := rows.Scan(&nonce, &name, &env, &url, &created, &detailsStr, &ownerID, &ownerName, &memberCount, &serviceCount); err != nil {
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
  row := s.db.QueryRow("SELECT id, nonce, name, url, details FROM apps WHERE nonce = ?", nonce)
  a := &App{}
  var detailsStr string
  err := row.Scan(&a.ID, &a.Nonce, &a.Name, &a.URL, &detailsStr)
  if err == sql.ErrNoRows {
    return nil, errors.New("app not found")
  }
  if err != nil {
    return nil, err
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

func (s *Store) ListHostedApps() ([]*App, error) {
  rows, err := s.db.Query("SELECT nonce, name, url, details FROM apps WHERE details != '{}'")
  if err != nil {
    return nil, err
  }
  defer rows.Close()
  var apps []*App
  for rows.Next() {
    a := &App{}
    var detailsStr string
    if err := rows.Scan(&a.Nonce, &a.Name, &a.URL, &detailsStr); err != nil {
      return nil, err
    }
    var d AppDetails
    if json.Unmarshal([]byte(detailsStr), &d) != nil || d.LastUploaded == nil {
      continue
    }
    a.Details = &d
    apps = append(apps, a)
  }
  return apps, rows.Err()
}

func (s *Store) UpdateApp(nonce string, name, env string, url string, ownerID *int64) error {
  _, err := s.db.Exec(
    "UPDATE apps SET name = ?, environment = ?, url = ?, owner_id = ? WHERE nonce = ?",
    name, env, url, ownerID, nonce,
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
  if role == "" { role = "Member" }
  if status == "" { status = "Active" }
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
  if limit <= 0 { limit = 100 }
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

func (s *Store) RegisterService(name, serviceURL string, descriptor ServiceDescriptor) (int64, error) {
  descJSON, err := json.Marshal(descriptor)
  if err != nil {
    return 0, err
  }
  res, err := s.db.Exec(
    "INSERT INTO services (name, url, descriptor) VALUES (?, ?, ?)",
    name, serviceURL, string(descJSON),
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
  row := s.db.QueryRow(
    "SELECT id, name, url, descriptor FROM services WHERE id = ?",
    serviceID,
  )
  svc := &Service{}
  var descStr string
  err := row.Scan(&svc.ID, &svc.Name, &svc.URL, &descStr)
  if err == sql.ErrNoRows {
    return nil, errors.New("service not found")
  }
  if err != nil {
    return nil, err
  }
  if err := json.Unmarshal([]byte(descStr), &svc.Descriptor); err != nil {
    return nil, err
  }
  return svc, nil
}

func (s *Store) GetServiceByURL(serviceURL string) (*Service, error) {
  row := s.db.QueryRow(
    "SELECT id, name, url, descriptor FROM services WHERE rtrim(url, '/') = rtrim(?, '/')",
    serviceURL,
  )
  svc := &Service{}
  var descStr string
  err := row.Scan(&svc.ID, &svc.Name, &svc.URL, &descStr)
  if err == sql.ErrNoRows {
    return nil, errors.New("service not found")
  }
  if err != nil {
    return nil, err
  }
  if err := json.Unmarshal([]byte(descStr), &svc.Descriptor); err != nil {
    return nil, err
  }
  return svc, nil
}

func (s *Store) UpdateService(id int64, name, serviceURL string, descriptor ServiceDescriptor) error {
  descJSON, err := json.Marshal(descriptor)
  if err != nil {
    return err
  }
  _, err = s.db.Exec(
    "UPDATE services SET name = ?, url = ?, descriptor = ? WHERE id = ?",
    name, serviceURL, string(descJSON), id,
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
  if allowed { a = 1 }
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
  rows, err := s.db.Query(
    "SELECT id, name, url, descriptor FROM services ORDER BY name",
  )
  if err != nil {
    return nil, err
  }
  defer rows.Close()
  var out []*Service
  for rows.Next() {
    svc := &Service{}
    var descStr string
    if err := rows.Scan(&svc.ID, &svc.Name, &svc.URL, &descStr); err != nil {
      return nil, err
    }
    if err := json.Unmarshal([]byte(descStr), &svc.Descriptor); err != nil {
      return nil, err
    }
    out = append(out, svc)
  }
  return out, rows.Err()
}

func genNonce() string {
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
  id, err := s.RegisterService("SSH", "ssh://", ServiceDescriptor{Type: "ssh"})
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
