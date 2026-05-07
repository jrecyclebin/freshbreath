package main

import (
  "database/sql"
  "log"
  "net/http"
  "os"
  "sync"
  "time"

  "github.com/coreos/go-oidc/v3/oidc"
  _ "github.com/mattn/go-sqlite3"
  "github.com/joho/godotenv"
)

type Config struct {
  DBPath        string
  PublicBaseURL string
  ListenAddr    string
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
  // OIDC fields
  oidcNonce string
  oidcIssuer string
  isOIDC    bool
  isAdmin   bool
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

  cfg := Config{
    DBPath:        getEnv("FREBRE_DB_PATH", "./freshbreath.db"),
    PublicBaseURL: getEnv("FREBRE_BASE_URL", "http://localhost:9009"),
    ListenAddr:    getEnv("FREBRE_LISTEN_ADDR", ":9009"),
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

  localKey, err := store.GetOrCreateLocalSigningKey()
  if err != nil {
    log.Fatal(err)
  }

  srv := &Server{
    config:        cfg,
    store:         store,
    pending:       make(map[string]*pendingAuth),
    httpClient:    &http.Client{Timeout: 300 * time.Second},
    oidcProviders: make(map[int64]*oidc.Provider),
    localKey:      localKey,
  }
  srv.SetupRoutes()

  log.Printf("*:・ﾟ✧ freshbreath server on %s", cfg.ListenAddr)
  if err := http.ListenAndServe(cfg.ListenAddr, srv); err != nil {
    log.Fatal(err)
  }
}
