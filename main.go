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

  "github.com/coreos/go-oidc/v3/oidc"
  _ "github.com/mattn/go-sqlite3"
  "github.com/joho/godotenv"
)

type Config struct {
  Dir           string
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

func main() {
  // Load .env file if present; ignore error — env vars may be set directly.
  _ = godotenv.Load()

  _, callerFile, _, _ := runtime.Caller(0)
  binDir, _ := filepath.EvalSymlinks(filepath.Dir(callerFile))

  cfg := Config{
    Dir:           getEnv("FRBR_DIR", binDir),
    DBPath:        getEnv("FRBR_DB_PATH", "./freshbreath.db"),
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
    httpClient:    &http.Client{Timeout: 300 * time.Second},
    oidcProviders: make(map[int64]*oidc.Provider),
    localKey:      localKey,
    adminNonce:    genNonce(),
    agentMgr:     agentMgr,
    sessionMgr:   sessionMgr,
  }
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

  log.Printf("*:・ﾟ✧ freshbreath %s server on %s [db: %s]", version, cfg.PublicBaseURL, cfg.DBPath)
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
