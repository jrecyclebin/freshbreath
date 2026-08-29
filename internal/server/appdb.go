package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3"

	"poggers.institute/freshbreath/internal/db"
)

// App databases — SQLite for every app, per design/app-databases.md.
//
// One hardened engine (the "sqlite3_app" driver), one gate (gateDBTarget),
// and four faces over it (two HTTP mounts, central MCP tools, virtual-service
// SQL steps). The engine hardening lives here; the faces are thin.

// ── Constants ───────────────────────────────────────────────────────

const (
	appDBStatementTimeout = 5 * time.Second // per statement, any path
	appDBRowCapDefault    = 10000           // reported honestly as "truncated"
	appDBIdleEviction     = 5 * time.Minute // a thousand apps shouldn't pin file handles
)

var appDBNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ── Engine: the hardened connector ──────────────────────────────────

// Each pool entry builds its own driver value and opens through sql.OpenDB
// instead of sql.Register. Registration is process-global and permanent: a
// ConnectHook parked in Go's driver registry outlives the Server that wrote
// it, so in a process that builds a second Server the first one would keep
// receiving everyone's change events. A connector belongs to the handle that
// made it and dies with it.
type appDBConnector struct {
	dsn string
	drv *sqlite3.SQLiteDriver
}

func (c *appDBConnector) Connect(context.Context) (driver.Conn, error) {
	return c.drv.Open(c.dsn)
}

func (c *appDBConnector) Driver() driver.Driver { return c.drv }

// appDBConnectHook is the per-connection hardening for one database: the
// ATTACH limit, the authorizer, and — on writable handles only — an update
// hook wired straight to that database's hub. Because the hub is captured
// here, the connection never has to ask SQLite which file it just opened.
func appDBConnectHook(hub *appDBWatchHub, ro bool) func(*sqlite3.SQLiteConn) error {
	return func(c *sqlite3.SQLiteConn) error {
		c.SetLimit(sqlite3.SQLITE_LIMIT_ATTACHED, 0)
		if !ro {
			c.RegisterUpdateHook(func(op int, dbName, table string, rowid int64) {
				hub.broadcast(dbChangeEvent{
					DB:    dbName,
					Table: table,
					Op:    appDBOpName(op),
					RowID: rowid,
				})
			})
		}
		c.RegisterAuthorizer(appDBAuthorizer)
		return nil
	}
}

// appDBOpName maps a sqlite3 update-hook operation code to a readable name.
func appDBOpName(op int) string {
	switch op {
	case sqlite3.SQLITE_INSERT:
		return "insert"
	case sqlite3.SQLITE_DELETE:
		return "delete"
	case sqlite3.SQLITE_UPDATE:
		return "update"
	default:
		return fmt.Sprintf("op%d", op)
	}
}

// ── Authorizer ──────────────────────────────────────────────────────

// appDBAuthorizer is the engine-level guard against ATTACH, extension
// loading, and arbitrary PRAGMAs. Statement-string blocklists lose to
// comments and `/**/ATTACH` on day two; this is told what the statement
// *actually intends* by SQLite itself.
//
// The PRAGMA allowlist is exactly what db_schema needs and not one pragma
// wider — table_info, table_xinfo, index_list, index_info, index_xinfo, and
// foreign_key_list. Everything else (journal_mode, writable_schema, …) is
// denied. db_schema runs through the same hardened pool, so there is no side
// channel; the allowlist is how it gets through.
var appDBPragmaAllow = map[string]bool{
	"table_info":       true,
	"table_xinfo":      true,
	"index_list":       true,
	"index_info":       true,
	"index_xinfo":      true,
	"foreign_key_list": true,
}

func appDBAuthorizer(param int, arg1, arg2, arg3 string) int {
	switch param {
	case sqlite3.SQLITE_ATTACH, sqlite3.SQLITE_DETACH:
		return sqlite3.SQLITE_DENY
	case sqlite3.SQLITE_PRAGMA:
		if appDBPragmaAllow[strings.ToLower(arg1)] {
			return sqlite3.SQLITE_OK
		}
		return sqlite3.SQLITE_DENY
	case sqlite3.SQLITE_FUNCTION:
		// Deny load_extension() by name. Other functions (date, count, …) pass.
		if strings.EqualFold(arg1, "load_extension") {
			return sqlite3.SQLITE_DENY
		}
		return sqlite3.SQLITE_OK
	}
	return sqlite3.SQLITE_OK
}

// ── Path resolution + the gate ───────────────────────────────────────

// dbPath resolves a (target, name) to a database file path. Targets:
//
//	app:<nonce>   apps/<nonce>/db/<name>.db
//	global        db/<name>.db
//
// The @slot suffix (e.g. app:<nonce>@staging) is reserved and rejected — an
// unsupported target should say so, not quietly hand back production. The
// name is validated against a conservative charset so an app can't ask for
// ../../freshbreath.db.
func (s *Server) dbPath(target, name string) (string, error) {
	if strings.Contains(target, "@") {
		return "", cerr(http.StatusBadRequest, "database target %q: per-slot (@) databases are reserved and not yet supported", target)
	}
	if !appDBNameRe.MatchString(name) {
		return "", cerr(http.StatusBadRequest, "invalid database name %q", name)
	}
	switch {
	case strings.HasPrefix(target, "app:"):
		nonce := strings.TrimPrefix(target, "app:")
		return filepath.Join(s.config.DataDir, "apps", nonce, "db", name+".db"), nil
	case target == "global":
		return filepath.Join(s.config.DataDir, "db", name+".db"), nil
	}
	return "", cerr(http.StatusBadRequest, "unknown database target %q", target)
}

// gateDBTarget is the single place data access is decided. Every face —
// both HTTP mounts, the central MCP tools, and virtual-service SQL steps —
// resolves a target string and hands it here. There is exactly one function
// to read when someone asks "who can touch this?" and one to fix.
func (s *Server) gateDBTarget(actor *db.User, target string) error {
	switch {
	case target == "global":
		// No app to be a member of, so this is the operator door — matching
		// how /api/services is gated. Apps reach shared data by having a
		// virtual service linked to them, not by naming a file.
		if actor == nil || !roleIn(actor.Role, rolesAdminPlus) {
			return cerr(http.StatusForbidden, "forbidden: global databases are admin+")
		}
		return nil
	case strings.HasPrefix(target, "app:"):
		return s.gateApp(actor, strings.TrimPrefix(target, "app:"))
	}
	return cerr(http.StatusBadRequest, "unknown database target %q", target)
}

// ── Change broadcaster (forward-only) ────────────────────────────────

type dbChangeEvent struct {
	DB    string `json:"db"`
	Table string `json:"table"`
	Op    string `json:"op"`
	RowID int64  `json:"rowid"`
}

// appDBWatchHub fans change events to subscribers for one database file. v1 is
// forward-only: the server sends no id: fields and keeps no replay buffer, so
// Last-Event-ID is accepted and politely ignored (see /db/watch).
type appDBWatchHub struct {
	mu   sync.Mutex
	subs map[chan dbChangeEvent]struct{}
	name string // database name, to relabel SQLite's "main" on the wire
}

func newAppDBWatchHub(name string) *appDBWatchHub {
	return &appDBWatchHub{subs: make(map[chan dbChangeEvent]struct{}), name: name}
}

func (h *appDBWatchHub) subscribe() (chan dbChangeEvent, func()) {
	ch := make(chan dbChangeEvent, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *appDBWatchHub) broadcast(ev dbChangeEvent) {
	if h.name != "" && ev.DB == "main" {
		ev.DB = h.name
	}
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			// Buffer full: drop rather than block the writer's hook callback.
			// A watcher behind a slow reader re-runs its queries on reconnect.
		}
	}
	h.mu.Unlock()
}

// appDBHub returns the watch hub for one database file, creating it on first
// need. Hubs outlive pool entries — a watcher may subscribe to a database
// that is currently closed, and the handle that reopens it must find the same
// hub the watcher is sitting on.
func (s *Server) appDBHub(absPath, name string) *appDBWatchHub {
	s.appDBWatchMu.Lock()
	defer s.appDBWatchMu.Unlock()
	if s.appDBWatch == nil {
		s.appDBWatch = make(map[string]*appDBWatchHub)
	}
	hub := s.appDBWatch[absPath]
	if hub == nil {
		hub = newAppDBWatchHub(name)
		s.appDBWatch[absPath] = hub
	}
	return hub
}

// ── Connection pool ──────────────────────────────────────────────────

type dbPoolKey struct {
	target string
	name   string
	ro     bool
}

type dbPoolEntry struct {
	db       *sql.DB
	lastUsed time.Time
}

// appDB returns a *sql.DB for (target, name), opening lazily. The directory
// is created on demand (databases are created on first touch).
func (s *Server) appDB(target, name string, ro bool) (*sql.DB, error) {
	s.appDBPoolInit()

	path, err := s.dbPath(target, name)
	if err != nil {
		return nil, err
	}

	key := dbPoolKey{target, name, ro}
	s.appDBPoolMu.Lock()
	if e, ok := s.appDBPool[key]; ok {
		e.lastUsed = time.Now()
		db := e.db
		s.appDBPoolMu.Unlock()
		return db, nil
	}
	s.appDBPoolMu.Unlock()

	// The hub is resolved before the open, so the connect hook can close over
	// it. Every handle on this file — read-only or not, now or after an idle
	// eviction — shares the one hub keyed by its absolute path.
	abs, _ := filepath.Abs(path)
	hub := s.appDBHub(abs, name)

	// Create the directory (databases materialize on first touch).
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	dsn := path
	if q := strings.IndexByte(dsn, '?'); q >= 0 {
		dsn = dsn[:q]
	}
	pragmas := []string{"_busy_timeout=5000", "_foreign_keys=on"}
	if ro {
		// Read-only at the engine level via PRAGMA query_only (set as a DSN
		// pragma, before the authorizer goes up). Writes fail with
		// SQLITE_READONLY — the engine refuses them, not a SQL blocklist.
		pragmas = append(pragmas, "_query_only=1")
	} else {
		pragmas = append(pragmas, "_journal_mode=WAL", "_synchronous=NORMAL")
	}
	dsn = dsn + "?" + strings.Join(pragmas, "&")

	handle := sql.OpenDB(&appDBConnector{
		dsn: dsn,
		drv: &sqlite3.SQLiteDriver{ConnectHook: appDBConnectHook(hub, ro)},
	})
	handle.SetMaxOpenConns(1) // no SQLITE_BUSY, ever — a personal app never notices the trade

	// Verify the connection
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, err
	}

	s.appDBPoolMu.Lock()
	// Re-check: another goroutine may have opened the same key.
	if e, ok := s.appDBPool[key]; ok {
		s.appDBPoolMu.Unlock()
		handle.Close()
		e.lastUsed = time.Now()
		return e.db, nil
	}
	s.appDBPool[key] = &dbPoolEntry{db: handle, lastUsed: time.Now()}
	s.appDBPoolMu.Unlock()
	return handle, nil
}

// appDBPoolInit lazily creates the pool map. Tests construct *Server directly
// (see newTestServer), so every accessor self-initializes; appDBHub does the
// same for the watch map.
func (s *Server) appDBPoolInit() {
	s.appDBPoolMu.Lock()
	if s.appDBPool == nil {
		s.appDBPool = make(map[dbPoolKey]*dbPoolEntry)
	}
	s.appDBPoolMu.Unlock()
}

// appDBSweep closes idle pool entries. Run on the existing 60s tick.
func (s *Server) appDBSweep() {
	cutoff := time.Now().Add(-appDBIdleEviction)
	s.appDBPoolMu.Lock()
	defer s.appDBPoolMu.Unlock()
	for key, e := range s.appDBPool {
		if e.lastUsed.Before(cutoff) {
			e.db.Close()
			delete(s.appDBPool, key)
		}
	}
}

// appDBClose drops and closes a pool entry, used before deleting a database
// file so the OS-level unlink takes effect (and WAL sidecars clear).
func (s *Server) appDBClose(target, name string) {
	s.appDBPoolMu.Lock()
	for _, ro := range []bool{false, true} {
		if e, ok := s.appDBPool[dbPoolKey{target, name, ro}]; ok {
			e.db.Close()
			delete(s.appDBPool, dbPoolKey{target, name, ro})
		}
	}
	s.appDBPoolMu.Unlock()
}

// appDBWatchCh subscribes to change events for a database. The returned stop
// function unsubscribes and drains; callers MUST defer it.
func (s *Server) appDBWatchCh(target, name string) (chan dbChangeEvent, func(), error) {
	path, err := s.dbPath(target, name)
	if err != nil {
		return nil, nil, err
	}
	abs, _ := filepath.Abs(path)
	ch, stop := s.appDBHub(abs, name).subscribe()
	return ch, stop, nil
}

// ── Core: query/list/delete/schema ───────────────────────────────────

type dbStmtRequest struct {
	SQL    string      `json:"sql"`
	Params interface{} `json:"params"`
}

type dbQueryRequest struct {
	DB     string          `json:"db"`     // optional, defaults to "app"
	SQL    string          `json:"sql"`    // single statement
	Params interface{}     `json:"params"` // positional array | named object
	Batch  []dbStmtRequest `json:"batch"`  // all-or-nothing transaction
}

type dbQueryResult struct {
	Columns      []string        `json:"columns"`
	Rows         [][]interface{} `json:"rows"`
	RowsAffected int64           `json:"rowsAffected"`
	LastInsertID int64           `json:"lastInsertId"`
	Truncated    bool            `json:"truncated"`
}

func (s *Server) rowCap() int {
	if s.dbRowCap > 0 {
		return s.dbRowCap
	}
	return appDBRowCapDefault
}

// coreDBQuery runs a statement or batch against the resolved database. The
// `ro` flag opens the connection through the read-only pool — the engine
// refuses writes (PRAGMA query_only, set as a DSN pragma before the
// authorizer goes up), so the read-only guarantee is not a SQL blocklist.
//
// The MCP db_query/db_execute faces call this with ro=true; the HTTP faces
// and virtual-service SQL steps call it with ro=false.
func (s *Server) coreDBQuery(actor *db.User, target string, req dbQueryRequest, ro bool) (*dbQueryResult, error) {
	if err := s.gateDBTarget(actor, target); err != nil {
		return nil, err
	}
	name := req.DB
	if name == "" {
		name = "app"
	}
	handle, err := s.appDB(target, name, ro)
	if err != nil {
		return nil, err
	}
	if len(req.Batch) > 0 {
		return s.coreDBBatch(actor, target, handle, req.Batch)
	}
	return s.coreDBOneStmt(handle, req.SQL, req.Params)
}

// errMCPDatabaseReadOnly is the friendly message both blocked MCP write paths
// surface — a write sent through the always-read-only db_query, and any write
// sent while mcp_database_mode is read-only. SQLite's raw "attempt to write a
// readonly database" tells a model nothing about what to do next; this names
// the exact setting and the value to set.
var errMCPDatabaseReadOnly = cerr(http.StatusForbidden,
	`Update Settings > MCP database mode to "full access" in the control panel to allow queries like this.`)

// isReadonlyWriteErr reports whether err is SQLite refusing a write because the
// connection is read-only (query_only). Used by the MCP faces to remap.
func isReadonlyWriteErr(err error) bool {
	var s3 sqlite3.Error
	if errors.As(err, &s3) {
		if s3.Code == sqlite3.ErrNo(8) { // SQLITE_READONLY
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "attempt to write a readonly database") ||
		strings.Contains(msg, "readonly database") ||
		strings.Contains(msg, "query_only")
}

func (s *Server) coreDBOneStmt(handle *sql.DB, sqlText string, rawParams interface{}) (*dbQueryResult, error) {
	if sqlText = strings.TrimSpace(sqlText); sqlText == "" {
		return nil, cerr(http.StatusBadRequest, "empty sql")
	}
	ctx, cancel := context.WithTimeout(context.Background(), appDBStatementTimeout)
	defer cancel()

	args, err := decodeDBParams(rawParams)
	if err != nil {
		return nil, err
	}

	return runDBStmt(ctx, s.rowCap(), handle, sqlText, args)
}

// coreDBBatch runs a batch inside one transaction, all-or-nothing. A failing
// statement rolls back the whole thing — "migrations mostly work" becomes
// "migrations work."
func (s *Server) coreDBBatch(actor *db.User, target string, handle *sql.DB, batch []dbStmtRequest) (*dbQueryResult, error) {
	tx, err := handle.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	var last *dbQueryResult
	for i, stmt := range batch {
		ctx, cancel := context.WithTimeout(context.Background(), appDBStatementTimeout)
		args, perr := decodeDBParams(stmt.Params)
		if perr != nil {
			cancel()
			return nil, fmt.Errorf("batch %d: %w", i, perr)
		}
		res, err := runDBStmt(ctx, s.rowCap(), tx, stmt.SQL, args)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("batch %d: %w", i, err)
		}
		last = res
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	if last == nil {
		return &dbQueryResult{}, nil
	}
	return last, nil
}

// dbExecQuerier is the slice of *sql.DB / *sql.Tx both implement: enough to
// run a statement as either an Exec or a Query from one body.
type dbExecQuerier interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
}

// runDBStmt runs a single statement. The write/read split is a routing hint for
// result shaping only — it is NOT a security boundary. The authorizer and
// read-only mode are the real gates; a misrouted read errors harmlessly and a
// misrouted write still returns rowsAffected 0.
func runDBStmt(ctx context.Context, cap int, q dbExecQuerier, sqlText string, args []interface{}) (*dbQueryResult, error) {
	sqlText = strings.TrimSpace(sqlText)
	if looksLikeWrite(sqlText) {
		res, err := q.ExecContext(ctx, sqlText, args...)
		if err != nil {
			return nil, err
		}
		aff, _ := res.RowsAffected()
		lid, _ := res.LastInsertId()
		return &dbQueryResult{RowsAffected: aff, LastInsertID: lid}, nil
	}
	rows, err := q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	scaners := make([]interface{}, len(cols))
	holders := make([]interface{}, len(cols))
	for i := range holders {
		scaners[i] = &holders[i]
	}
	out := dbQueryResult{Columns: cols}
	for rows.Next() {
		if err := rows.Scan(scaners...); err != nil {
			return nil, err
		}
		row := make([]interface{}, len(holders))
		for i, v := range holders {
			row[i] = encodeDBValue(v)
		}
		out.Rows = append(out.Rows, row)
		if len(out.Rows) >= cap {
			out.Truncated = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &out, nil
}

// looksLikeWrite is a coarse verb check used only to pick the Exec vs Query
// path for result shaping. It is NOT a security boundary — the authorizer
// and ro mode are the real gates — so a `WITH … SELECT` reading statement
// misrouted to Exec would still work (Exec returns rowsAffected 0), and a
// write misrouted to Query simply errors, which is also fine.
func looksLikeWrite(sqlText string) bool {
	s := strings.ToUpper(strings.TrimSpace(sqlText))
	for _, v := range []string{"INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "DROP", "ALTER"} {
		if strings.HasPrefix(s, v) {
			return true
		}
	}
	return false
}

// ── Parameter decode ─────────────────────────────────────────────────

// decodeDBParams turns the inbound `params` (positional array, named object,
// or nil) into bind args. Positional → []interface{}; named → []sql.NamedArg.
func decodeDBParams(v interface{}) (args []interface{}, err error) {
	if v == nil {
		return nil, nil
	}
	switch t := v.(type) {
	case []interface{}:
		args = make([]interface{}, 0, len(t))
		for _, x := range t {
			args = append(args, decodeDBValue(x))
		}
		return args, nil
	case map[string]interface{}:
		args = make([]interface{}, 0, len(t))
		names := make([]string, 0, len(t))
		for k := range t {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			args = append(args, sql.Named(k, decodeDBValue(t[k])))
		}
		return args, nil
	}
	return nil, cerr(http.StatusBadRequest, "params must be an array (positional) or an object (named), got %T", v)
}

// decodeDBValue normalizes a single inbound param value. JSON numbers come in
// as float64; an integral float is rebound as int64 so an id of 1 doesn't
// silently become REAL 1.0 in SQLite. The {"$blob": "<base64>"} wrapper
// decodes to []byte.
func decodeDBValue(v interface{}) interface{} {
	switch t := v.(type) {
	case float64:
		// Integral floats become int64 to preserve integer semantics.
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case map[string]interface{}:
		if b, ok := t["$blob"].(string); ok {
			if data, derr := base64.StdEncoding.DecodeString(b); derr == nil {
				return data
			}
		}
		return t
	default:
		return v
	}
}

// encodeDBValue converts a scanned column value for the wire. []byte becomes
// the {"$blob": "<base64>"} wrapper; everything else passes through (int64,
// float64, string, bool, nil).
func encodeDBValue(v interface{}) interface{} {
	switch t := v.(type) {
	case []byte:
		return map[string]interface{}{"$blob": base64.StdEncoding.EncodeToString(t)}
	default:
		return t
	}
}

// ── List / Delete / Schema ──────────────────────────────────────────

type dbDBInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
}

func (s *Server) coreDBList(actor *db.User, target string) ([]dbDBInfo, error) {
	if err := s.gateDBTarget(actor, target); err != nil {
		return nil, err
	}
	var dir string
	switch {
	case strings.HasPrefix(target, "app:"):
		dir = filepath.Join(s.config.DataDir, "apps", strings.TrimPrefix(target, "app:"), "db")
	case target == "global":
		dir = filepath.Join(s.config.DataDir, "db")
	default:
		return nil, cerr(http.StatusBadRequest, "unknown database target %q", target)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []dbDBInfo{}, nil
		}
		return nil, err
	}
	var out []dbDBInfo
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".db") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, dbDBInfo{
			Name:    strings.TrimSuffix(name, ".db"),
			Size:    fi.Size(),
			ModTime: fi.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Server) coreDBDelete(actor *db.User, target, name string) error {
	if err := s.gateDBTarget(actor, target); err != nil {
		return err
	}
	if !appDBNameRe.MatchString(name) {
		return cerr(http.StatusBadRequest, "invalid database name %q", name)
	}
	path, err := s.dbPath(target, name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return cerr(http.StatusNotFound, "database %q not found", name)
		}
		return err
	}
	// Close the pool entries before unlinking; on Linux an open handle keeps
	// the inode alive and the WAL sidecars would linger.
	s.appDBClose(target, name)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// coreDBSchema returns tables, columns, indexes, and DDL. Built from
// sqlite_master plus the read-only PRAGMAs the authorizer allowlists — the
// one tool that actually matters for an MCP model writing queries blind.
func (s *Server) coreDBSchema(actor *db.User, target, name string) (interface{}, error) {
	if err := s.gateDBTarget(actor, target); err != nil {
		return nil, err
	}
	if name == "" {
		name = "app"
	}
	handle, err := s.appDB(target, name, true)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), appDBStatementTimeout)
	defer cancel()

	rows, err := handle.QueryContext(ctx,
		"SELECT name, sql FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	type tableMeta struct{ name, ddl string }
	var tables []tableMeta
	for rows.Next() {
		var n, ddl sql.NullString
		if err := rows.Scan(&n, &ddl); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, tableMeta{n.String, ddl.String})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]map[string]interface{}, 0, len(tables))
	for _, t := range tables {
		entry := map[string]interface{}{
			"name": t.name,
			"ddl":  t.ddl,
		}
		entry["columns"] = appDBPragmaRows(handle, ctx, "PRAGMA table_info("+quoteIdent(t.name)+")")
		entry["indexes"] = appDBIndexInfo(handle, ctx, t.name)
		out = append(out, entry)
	}
	return map[string]interface{}{"tables": out}, nil
}

func appDBPragmaRows(handle *sql.DB, ctx context.Context, q string) []map[string]interface{} {
	rows, err := handle.QueryContext(ctx, q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]interface{}
	for rows.Next() {
		scan := make([]interface{}, len(cols))
		for i := range scan {
			scan[i] = new(interface{})
		}
		if err := rows.Scan(scan...); err != nil {
			return out
		}
		m := map[string]interface{}{}
		for i, c := range cols {
			m[c] = *scan[i].(*interface{})
		}
		out = append(out, m)
	}
	return out
}

func appDBIndexInfo(handle *sql.DB, ctx context.Context, table string) []map[string]interface{} {
	idxRows := appDBPragmaRows(handle, ctx, "PRAGMA index_list("+quoteIdent(table)+")")
	for _, idx := range idxRows {
		if name, ok := idx["name"].(string); ok {
			idx["columns"] = appDBPragmaRows(handle, ctx, "PRAGMA index_info("+quoteIdent(name)+")")
		}
	}
	return idxRows
}

func quoteIdent(name string) string {
	// Identifiers here come from sqlite_master (table names) or our own
	// allowlisted pragmas, never user input. Quote defensively anyway.
	return "'" + strings.ReplaceAll(name, "'", "''") + "'"
}
