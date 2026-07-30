package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/sshkit"
)

// Config holds the resolved runtime configuration for a freshbreath server.
// It is built by cmd/freshbreath (path/env discovery) and handed to New.
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

// Server is the freshbreath application server hub. All HTTP handlers, MCP
// endpoints, OAuth machinery, and admin logic are methods on *Server and
// share this one package (Go requires methods on *Server to share a package).
type Server struct {
	config            Config
	store             *db.Store
	mux               *http.ServeMux
	pending           map[string]*pendingAuth
	pendingMu         sync.Mutex
	httpClient        *http.Client
	oidcProviders     map[int64]*oidc.Provider
	oidcProvidersMu   sync.RWMutex
	localKey          []byte
	version           string                 // build version, threaded from cmd via -X main.version
	commit            string                 // build commit, threaded from cmd via -X main.commit
	adminNonce        string                 // ephemeral nonce for the admin panel (same-origin)
	agentMgr          *sshkit.AgentManager   // per-user SSH key signers
	sessionMgr        *sshkit.SessionManager // SSH + SFTP sessions
	gitGw             *sshkit.GitGateway     // stateless git-over-SSH gateway
	lastSeenAt        map[int64]time.Time
	lastSeenMu        sync.Mutex
	hostedRoutes      map[string]hostedApp // slug → hosted app route
	hostedMu          sync.RWMutex
	virtualMCPs       *virtualMCPRegistry                // slug → MCP server entries
	mcpAuthPending    *sync.Map                          // key → *mcpPendingAuth (MCP OAuth flow state)
	oauthSrv          *oauthServer                       // Freshbreath OAuth authorization server
	centralMCPHandler http.Handler                       // central MCP at /mcp
	centralMCPPRMVal  *oauthex.ProtectedResourceMetadata // PRM for central MCP
	centralMCPServers map[string]*mcp.Server             // role → lazily-built central MCP server
	centralMCPSrvMu   sync.Mutex                         // guards centralMCPServers
}

// hostedApp is one routable app: the nonce resolves on-disk slot dirs, and
// environment picks which slot the bare /<slug> path serves.
type hostedApp struct {
	nonce       string
	environment string
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

// New constructs a freshbreath server from its externally-wired dependencies
// (all of which cmd/freshbreath builds and owns), wires the internal state,
// registers routes, and starts the background expiry goroutine. The returned
// *Server is an http.Handler ready to Serve.
func New(cfg Config, store *db.Store, localKey []byte, agentMgr *sshkit.AgentManager, sessionMgr *sshkit.SessionManager, version, commit string) *Server {
	s := &Server{
		config:         cfg,
		store:          store,
		pending:        make(map[string]*pendingAuth),
		lastSeenAt:     make(map[int64]time.Time),
		hostedRoutes:   make(map[string]hostedApp),
		httpClient:     &http.Client{Timeout: 300 * time.Second},
		oidcProviders:  make(map[int64]*oidc.Provider),
		localKey:       localKey,
		version:        version,
		commit:         commit,
		adminNonce:     db.GenNonce(),
		agentMgr:       agentMgr,
		sessionMgr:     sessionMgr,
		gitGw:          sshkit.NewGitGateway(agentMgr, store),
		virtualMCPs:    newVirtualMCPRegistry(),
		mcpAuthPending: &sync.Map{},
	}
	s.oauthSrv = newOAuthServer(s)
	s.SetupRoutes()

	// Periodically expire stale SSH agent keys and sessions.
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for range t.C {
			s.agentMgr.ExpireKeys()
			s.sessionMgr.ExpireSessions()
			s.store.DeleteExpiredRefreshFamilies(time.Now())
		}
	}()

	return s
}

// ListenAndServe serves the freshbreath HTTP API over HTTP or HTTPS depending
// on the TLS config. When both TLSCertFile and TLSKeyFile are set it serves
// TLS; otherwise plain HTTP. The *Server itself is the http.Handler.
func (s *Server) ListenAndServe() error {
	if s.config.TLSCertFile != "" && s.config.TLSKeyFile != "" {
		return http.ListenAndServeTLS(s.config.ListenAddr, s.config.TLSCertFile, s.config.TLSKeyFile, s)
	}
	return http.ListenAndServe(s.config.ListenAddr, s)
}
