package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/server"
	"poggers.institute/freshbreath/internal/sshkit"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// resolveConfigDir returns the XDG config directory for freshbreath
// (or "" if none exists). It does not load any config file.
func resolveConfigDir() string {
	for _, dir := range append([]string{xdg.ConfigHome}, xdg.ConfigDirs...) {
		p := filepath.Join(dir, "freshbreath")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// resolveConfigPath resolves a (possibly relative) path against configDir.
// Empty paths pass through unchanged; absolute paths are returned as-is; a
// relative path is joined to configDir, but only when configDir is set —
// otherwise it stays relative to the current working directory.
func resolveConfigPath(configDir, p string) string {
	if p == "" || filepath.IsAbs(p) || configDir == "" {
		return p
	}
	return filepath.Join(configDir, p)
}

// decodeKeyMaterial decodes operator-provided key material: hex first
// (the `openssl rand -hex 32` shape), then base64 in its std and URL
// variants, padded or raw. Material that decodes to fewer than 16 bytes
// is rejected — a master key shorter than that isn't one.
func decodeKeyMaterial(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty key material")
	}
	if b, err := hex.DecodeString(s); err == nil {
		if len(b) < 16 {
			return nil, fmt.Errorf("key material is %d bytes; need at least 16", len(b))
		}
		return b, nil
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) < 16 {
				return nil, fmt.Errorf("key material is %d bytes; need at least 16", len(b))
			}
			return b, nil
		}
	}
	return nil, errors.New("not valid hex or base64")
}

// loadSigningKey resolves the master signing key, by precedence:
//
//  1. FRBR_SIGNING_KEY env var (hex or base64)
//  2. signing.key file in the config dir (0600)
//  3. adoption of the legacy settings-table key, preserving sessions
//  4. a fresh 32-byte mint
//
// The key never lives in the DB: the JWT-sign and at-rest-seal subkeys
// both derive from it (HKDF), so the DB file alone can neither forge
// tokens nor open sealed secrets. Branches 3 and 4 write the key to the
// file so later boots find it there; the legacy settings row is retired
// whichever branch wins. store may be nil (tests) — legacy adoption and
// retirement are skipped then.
func loadSigningKey(configDir string, store *db.Store) ([]byte, error) {
	if env := os.Getenv("FRBR_SIGNING_KEY"); env != "" {
		key, err := decodeKeyMaterial(env)
		if err != nil {
			return nil, fmt.Errorf("FRBR_SIGNING_KEY: %w", err)
		}
		retireLegacySigningRow(store)
		return key, nil
	}

	if configDir == "" {
		// No config dir exists yet; give the key (and future config) a home.
		configDir = filepath.Join(xdg.ConfigHome, "freshbreath")
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			return nil, fmt.Errorf("create config dir: %w", err)
		}
	}
	keyPath := filepath.Join(configDir, "signing.key")

	if raw, err := os.ReadFile(keyPath); err == nil {
		key, err := decodeKeyMaterial(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", keyPath, err)
		}
		if info, err := os.Stat(keyPath); err == nil && info.Mode().Perm()&0o077 != 0 {
			log.Printf("warning: %s is readable by group/others (chmod 600 it)", keyPath)
		}
		retireLegacySigningRow(store)
		return key, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read signing key: %w", err)
	}

	// No env, no file: adopt the legacy settings-table key when one
	// exists, else mint fresh.
	var key []byte
	if store != nil && store.HasTable("settings") {
		if val, err := store.GetSetting("local_signing_key"); err == nil && val != "" {
			key, _ = decodeKeyMaterial(val)
		}
	}
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate signing key: %w", err)
		}
		log.Printf("generated new signing key: %s", keyPath)
	}
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write signing key: %w", err)
	}
	retireLegacySigningRow(store)
	return key, nil
}

// retireLegacySigningRow deletes the settings-table copy of the signing
// key (FRBR-5). Missing rows and a missing table are both quiet; the key
// has a better home now.
func retireLegacySigningRow(store *db.Store) {
	if store == nil || !store.HasTable("settings") {
		return
	}
	if err := store.DeleteSetting("local_signing_key"); err != nil {
		log.Printf("retire legacy signing key row: %v", err)
	}
}

// resolveDir searches for a freshbreath install directory containing a
// web/ subdirectory. Search order: env var, binary's own directory,
// XDG_DATA_HOME/freshbreath, then each entry in XDG_DATA_DIRS.
func resolveDir(binDir string) string {
	if v := os.Getenv("FRBR_DIR"); v != "" {
		return v
	}
	// Current working directory
	if _, err := os.Stat("web"); err == nil {
		if _, err := os.Stat("skills"); err == nil {
			return "."
		}
	}
	// Binary's own directory
	if _, err := os.Stat(filepath.Join(binDir, "web")); err == nil {
		return binDir
	}
	// XDG data directories
	for _, dir := range append([]string{xdg.DataHome}, xdg.DataDirs...) {
		p := filepath.Join(dir, "freshbreath")
		if _, err := os.Stat(filepath.Join(p, "web")); err == nil {
			return p
		}
	}
	return ""
}

// resolveDataDir determines where mutable state (apps/, virtual/, tasks/,
// and the database) lives. Returns (dataDir, dbPath).
func resolveDataDir(binDir string) (string, string) {
	// Explicit env vars win
	if v := os.Getenv("FRBR_DATA_DIR"); v != "" {
		dbPath := os.Getenv("FRBR_DB_PATH")
		if dbPath == "" {
			dbPath = filepath.Join(v, "freshbreath.db")
		}
		return v, dbPath
	}
	if v := os.Getenv("FRBR_DB_PATH"); v != "" {
		return filepath.Dir(v), v
	}
	// Search for an existing database
	candidates := []string{
		"./freshbreath.db",
		filepath.Join(binDir, "freshbreath.db"),
		filepath.Join(xdg.DataHome, "freshbreath", "freshbreath.db"),
	}
	for _, dir := range xdg.DataDirs {
		candidates = append(candidates, filepath.Join(dir, "freshbreath", "freshbreath.db"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return filepath.Dir(c), c
		}
	}
	// Default: current working directory
	return ".", "./freshbreath.db"
}

func main() {
	// Config loading: .env in CWD wins; otherwise try XDG config.
	cwdEnv := ".env"
	if _, err := os.Stat(cwdEnv); err == nil {
		_ = godotenv.Load(cwdEnv)
	} else {
		xdgCfgDir := resolveConfigDir()
		if xdgCfgDir != "" {
			_ = godotenv.Load(filepath.Join(xdgCfgDir, "config.env"))
		}
	}

	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("freshbreath: can't locate own executable: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	binDir := filepath.Dir(exePath)

	dir := resolveDir(binDir)
	if dir == "" {
		log.Fatal("freshbreath: can't find control panel directory (web/). Set FRBR_DIR to the install directory.")
	}

	dataDir, dbPath := resolveDataDir(binDir)

	cfg := server.Config{
		Dir:           dir,
		DataDir:       dataDir,
		ConfigDir:     resolveConfigDir(),
		DBPath:        dbPath,
		PublicBaseURL: getEnv("FRBR_BASE_URL", ""),
		ListenAddr:    getEnv("FRBR_LISTEN_ADDR", ":9009"),
		TLSCertFile:   getEnv("FRBR_TLS_CERT", ""),
		TLSKeyFile:    getEnv("FRBR_TLS_KEY", ""),
	}

	// TLS cert/key paths resolve relative to ConfigDir; absolute paths are
	// used as-is. (If no ConfigDir was found, relative paths stay relative
	// to the current working directory.)
	cfg.TLSCertFile = resolveConfigPath(cfg.ConfigDir, cfg.TLSCertFile)
	cfg.TLSKeyFile = resolveConfigPath(cfg.ConfigDir, cfg.TLSKeyFile)

	tlsEnabled := cfg.TLSCertFile != "" && cfg.TLSKeyFile != ""
	if cfg.PublicBaseURL == "" {
		proto := "http"
		if tlsEnabled {
			proto = "https"
		}
		host := cfg.ListenAddr
		if strings.HasPrefix(host, ":") {
			host = "localhost" + host
		}
		cfg.PublicBaseURL = fmt.Sprintf("%s://%s", proto, host)
	}

	// SQLite tuning for the main store. Opened bare for a long time, which
	// meant rollback-journal mode (writers block readers) and instant
	// SQLITE_BUSY on any write contention — fine single-user, unkind under a
	// couple of concurrent requests.
	//
	//	_journal_mode=WAL    readers run alongside the writer. Persistent:
	//	                     this is a property of the file, set once.
	//	_busy_timeout=5000   wait for a lock instead of failing instantly.
	//	_foreign_keys=on     inert against today's schema (no FK declarations
	//	                     anywhere in db.go) — here so the day someone adds
	//	                     one, it's actually enforced.
	//	_synchronous=NORMAL  the standard companion to WAL: durable across
	//	                     process crashes, trades only power-loss fsyncs.
	//
	// Appended with a separator that respects a DBPath already carrying a
	// query string (":memory:" and plain paths both land on "?").
	sep := "?"
	if strings.Contains(cfg.DBPath, "?") {
		sep = "&"
	}
	dsn := cfg.DBPath + sep + "_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&_synchronous=NORMAL"
	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer sqlDB.Close()

	store := db.NewStore(sqlDB)

	// Master signing key: env var, 0600 file, legacy adoption, or fresh
	// mint (see loadSigningKey). Resolved before Migrate so the DB needs
	// no key to open, and Migrate can seal secrets against it (FRBR-4).
	localKey, err := loadSigningKey(cfg.ConfigDir, store)
	if err != nil {
		log.Fatal(err)
	}

	if err := store.Migrate(); err != nil {
		log.Fatal(err)
	}

	// Seed built-in SSH service
	if _, err := store.EnsureSSHService(); err != nil {
		log.Fatal(err)
	}

	agentMgr := sshkit.NewAgentManager()
	sessionMgr := sshkit.NewSessionManager(agentMgr, store, 8*time.Hour)

	srv := server.New(cfg, store, localKey, agentMgr, sessionMgr, version, commit)

	log.Printf("*:・ﾟ✧ freshbreath %s server on %s [dir: %s, data: %s, db: %s]", version, cfg.PublicBaseURL, cfg.Dir, cfg.DataDir, cfg.DBPath)
	if !server.FTS5Enabled {
		log.Printf("   note: no FTS5 in this build — rebuild with -tags sqlite_fts5 for full-text search")
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
	sessionMgr.Stop()
}
