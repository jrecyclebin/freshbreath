package main

import (
  "database/sql"
  "fmt"
  "log"
  "net/http"
  "os"
  "runtime"
  "strings"
  "path/filepath"
  "sync"
  "time"

  "github.com/adrg/xdg"
  "github.com/coreos/go-oidc/v3/oidc"
  _ "github.com/mattn/go-sqlite3"
  "github.com/joho/godotenv"
  "github.com/modelcontextprotocol/go-sdk/mcp"
  "github.com/modelcontextprotocol/go-sdk/oauthex"
)

type Config struct {
  Dir           string // install directory (web/ control panel)
  DataDir       string // mutable state directory (apps/, virtual/, tasks/)
  ConfigDir     string // XDG config directory, empty if none found
  DBPath        string
  PublicBaseURL string
  ListenAddr    string
  TLSCertFile   string
  TLSKeyFile    string
}

type Server struct {
  config           Config
  store            *Store
  mux              *http.ServeMux
  pending          map[string]*pendingAuth
  pendingMu        sync.Mutex
  httpClient       *http.Client
  oidcProviders    map[int64]*oidc.Provider
  oidcProvidersMu  sync.RWMutex
  localKey         []byte
  adminNonce       string          // ephemeral nonce for the admin panel (same-origin)
  agentMgr         *AgentManager   // per-user SSH key signers
  sessionMgr       *SessionManager // SSH + SFTP sessions
  lastSeenAt       map[int64]time.Time
  lastSeenMu       sync.Mutex
  hostedRoutes     map[string]string // slug → app nonce
  hostedMu         sync.RWMutex
  allowedOrigins   map[string]bool   // origins allowed for CORS
  allowedOriginsMu sync.RWMutex
  virtualMCPs      *virtualMCPRegistry // slug → MCP server entries
  mcpAuthPending   *sync.Map          // key → *mcpPendingAuth (MCP OAuth flow state)
  oauthSrv         *oauthServer        // Freshbreath OAuth authorization server
  centralMCPHandler  http.Handler                       // central MCP at /mcp
  centralMCPPRMVal   *oauthex.ProtectedResourceMetadata  // PRM for central MCP
  centralMCPServers  map[string]*mcp.Server              // role → lazily-built central MCP server
  centralMCPSrvMu    sync.Mutex                          // guards centralMCPServers
}

type pendingAuth struct {
  serviceID     int64
  serviceURL    string
  appNonce      string
  appState      string
  verifier      string
  clientID      string
  clientSecret  string
  tokenEndpoint string
  scopes        string
  proxied       bool
  serviceType   string // descriptor type: "oidc", "ssh", "mcp", "api"
  // OIDC fields
  oidcNonce  string
  oidcIssuer string
}

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

// resolveDir searches for a freshbreath install directory containing a
// web/ subdirectory. Search order: env var, binary's own directory,
// XDG_DATA_HOME/freshbreath, then each entry in XDG_DATA_DIRS.
func resolveDir(binDir string) string {
  if v := os.Getenv("FRBR_DIR"); v != "" {
    return v
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

  _, callerFile, _, _ := runtime.Caller(0)
  binDir, _ := filepath.EvalSymlinks(filepath.Dir(callerFile))

  dir := resolveDir(binDir)
  if dir == "" {
    log.Fatal("freshbreath: can't find control panel directory (web/). Set FRBR_DIR to the install directory.")
  }

  dataDir, dbPath := resolveDataDir(binDir)

  cfg := Config{
    Dir:           dir,
    DataDir:       dataDir,
    ConfigDir:     resolveConfigDir(),
    DBPath:        dbPath,
    PublicBaseURL: getEnv("FRBR_BASE_URL", ""),
    ListenAddr:    getEnv("FRBR_LISTEN_ADDR", ":9009"),
    TLSCertFile:   getEnv("FRBR_TLS_CERT", ""),
    TLSKeyFile:    getEnv("FRBR_TLS_KEY", ""),
  }

  tlsEnabled := cfg.TLSCertFile != "" && cfg.TLSKeyFile != ""
  if cfg.PublicBaseURL == "" {
    proto := "http"
    if tlsEnabled { proto = "https" }
    host := cfg.ListenAddr
    if strings.HasPrefix(host, ":") {
      host = "localhost" + host
    }
    cfg.PublicBaseURL = fmt.Sprintf("%s://%s", proto, host)
  }

  db, err := sql.Open("sqlite3", cfg.DBPath)
  if err != nil {
    log.Fatal(err)
  }
  defer db.Close()

  store := &Store{db: db}
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
    localKey = deriveJWTSecretFromTLSKey(tlsKey)
  }

  agentMgr := NewAgentManager()

  sessionMgr := NewSessionManager(agentMgr, store, 8*time.Hour)

  srv := &Server{
    config:        cfg,
    store:         store,
    pending:       make(map[string]*pendingAuth),
    lastSeenAt:    make(map[int64]time.Time),
    hostedRoutes:  make(map[string]string),
    allowedOrigins: make(map[string]bool),
    httpClient:    &http.Client{Timeout: 300 * time.Second},
    oidcProviders: make(map[int64]*oidc.Provider),
    localKey:      localKey,
    adminNonce:    genNonce(),
    agentMgr:     agentMgr,
    sessionMgr:   sessionMgr,
    virtualMCPs:  newVirtualMCPRegistry(),
    mcpAuthPending: &sync.Map{},
  }
  srv.oauthSrv = newOAuthServer(srv)
  srv.SetupRoutes()

  // Periodically expire stale SSH agent keys and sessions.
  go func() {
    t := time.NewTicker(60 * time.Second)
    defer t.Stop()
    for range t.C {
      agentMgr.ExpireKeys()
      sessionMgr.ExpireSessions()
    }
  }()

  log.Printf("*:・ﾟ✧ freshbreath %s server on %s [dir: %s, data: %s, db: %s]", version, cfg.PublicBaseURL, cfg.Dir, cfg.DataDir, cfg.DBPath)
  if tlsEnabled {
    if err := http.ListenAndServeTLS(cfg.ListenAddr, cfg.TLSCertFile, cfg.TLSKeyFile, srv); err != nil {
      log.Fatal(err)
    }
  } else {
    if err := http.ListenAndServe(cfg.ListenAddr, srv); err != nil {
      log.Fatal(err)
    }
  }
  sessionMgr.Stop()
}
