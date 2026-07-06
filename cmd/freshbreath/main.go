package main

import (
	"database/sql"
	"fmt"
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

	sqlDB, err := sql.Open("sqlite3", cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer sqlDB.Close()

	store := db.NewStore(sqlDB)
	if err := store.Migrate(); err != nil {
		log.Fatal(err)
	}

	// Seed built-in SSH service
	if _, err := store.EnsureSSHService(); err != nil {
		log.Fatal(err)
	}

	localKey, err := store.GetOrCreateLocalSigningKey()
	if err != nil {
		log.Fatal(err)
	}

	// When TLS is enabled, derive JWT signing key from the TLS private key.
	// This makes the secret stable across restarts and ties it to the server's
	// TLS identity. Rotating the TLS key invalidates all sessions.
	if tlsEnabled {
		tlsKey, err := os.ReadFile(cfg.TLSKeyFile)
		if err != nil {
			log.Fatalf("read TLS key for JWT derivation: %v", err)
		}
		localKey = sshkit.DeriveJWTSecretFromTLSKey(tlsKey)
	}

	agentMgr := sshkit.NewAgentManager()
	sessionMgr := sshkit.NewSessionManager(agentMgr, store, 8*time.Hour)

	srv := server.New(cfg, store, localKey, agentMgr, sessionMgr, version, commit)

	log.Printf("*:・ﾟ✧ freshbreath %s server on %s [dir: %s, data: %s, db: %s]", version, cfg.PublicBaseURL, cfg.Dir, cfg.DataDir, cfg.DBPath)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
	sessionMgr.Stop()
}
