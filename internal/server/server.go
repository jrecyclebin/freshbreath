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

	// Remote update feeds (design/remote-updates.md).
	pendingArchives map[string]*pendingArchive // "feedID/ref" → built archive, one-shot
	updateMu        sync.Mutex                 // guards pendingArchives + updateRate
	updateRate      map[string]updateRateCount // per-IP limiter for anonymous check/apply

	// App databases (design/app-databases.md). The pool is lazy; the watch
	// registry fans change events from the per-connection update hook.
	appDBPoolMu  sync.Mutex
	appDBPool    map[dbPoolKey]*dbPoolEntry
	appDBWatchMu sync.Mutex
	appDBWatch   map[string]*appDBWatchHub
	dbRowCap     int // 0 → appDBRowCapDefault; lowered in tests
}

// updateRateCount is one per-IP fixed-window counter for the anonymous
// update endpoints. Blunt, but enough to stop an anonymous hammer turning
// apply into a download DoS.
type updateRateCount struct {
	windowStart time.Time
	count       int
}

// hostedApp is one routable app: the nonce resolves on-disk slot dirs, and
// environment picks which slot the bare /<slug> path serves.
type hostedApp struct {
	nonce       string
	environment string
}

// pendingAuth is one login in progress: the auth records still to clear
// (legs), a cursor, and the legs already done. One object travels the whole
// flow — each leg re-keys it under a fresh state, so retries stay possible
// until the TTL. Browser flows carry appNonce/appState for the postMessage
// hand-back; MCP flows carry mcpKey into the mcpAuthPending map instead.
type pendingAuth struct {
	appNonce string // requesting app (or adminNonce); "" for MCP flows
	appState string // opener's correlation state (browser flows)
	mcpKey   string // key into mcpAuthPending; "" for browser flows

	legs      []*db.AuthRecord // records still to clear, in order
	at        int              // cursor into legs
	done      []*completedLeg  // cleared legs (including any pre-covered by a presented token)
	primaryID int64            // record the finished token is minted under

	// Active-leg OAuth state (interactive kinds only).
	verifier      string
	clientID      string
	clientSecret  string
	tokenEndpoint string
	oidcNonce     string

	expiresAt time.Time
}

// current returns the leg the flow is waiting on.
func (p *pendingAuth) current() *db.AuthRecord { return p.legs[p.at] }

// putPending registers a login-in-progress state, stamped with pendingAuthTTL.
// State is only removed by expiry (or its own retrieval of an expired entry) —
// failed credentials and link replays must not kill it.
func (s *Server) putPending(state string, p *pendingAuth) {
	p.expiresAt = time.Now().Add(pendingAuthTTL)
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.sweepPendingLocked(time.Now())
	s.pending[state] = p
}

// getPending returns the in-flight login for state. Non-expired entries are
// never deleted here — the expired flag distinguishes "expired" from
// "unknown" for error messages, and sweeps the expired entry.
func (s *Server) getPending(state string) (p *pendingAuth, ok, expired bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if p, ok = s.pending[state]; ok && time.Now().After(p.expiresAt) {
		delete(s.pending, state)
		return nil, false, true
	}
	return p, ok, false
}

// sweepPendingLocked drops expired login states; caller holds pendingMu.
// Lazy by design — no background goroutine purely for GC.
func (s *Server) sweepPendingLocked(now time.Time) {
	for k, p := range s.pending {
		if now.After(p.expiresAt) {
			delete(s.pending, k)
		}
	}
}

func (s *Server) putMCPPending(key string, m *mcpPendingAuth) {
	m.expiresAt = time.Now().Add(pendingAuthTTL)
	s.sweepMCPPending(time.Now())
	s.mcpAuthPending.Store(key, m)
}

// getMCPPending mirrors getPending for the MCP OAuth flow state.
func (s *Server) getMCPPending(key string) (m *mcpPendingAuth, ok, expired bool) {
	if v, found := s.mcpAuthPending.Load(key); found {
		m = v.(*mcpPendingAuth)
		if time.Now().After(m.expiresAt) {
			s.mcpAuthPending.Delete(key)
			return nil, false, true
		}
		return m, true, false
	}
	return nil, false, false
}

func (s *Server) sweepMCPPending(now time.Time) {
	s.mcpAuthPending.Range(func(k, v any) bool {
		if now.After(v.(*mcpPendingAuth).expiresAt) {
			s.mcpAuthPending.Delete(k)
		}
		return true
	})
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
			s.appDBSweep()
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
