//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ── Shared OAuth plumbing ───────────────────────────────────────────

// authServer is the part of an OAuth2 provider both fakes share: an
// authorize endpoint that approves on sight (the "user" always clicks
// Authorize), a token endpoint that checks PKCE, and the set of access
// tokens it has handed out. Every check it makes is one a real provider
// makes, so a Fresh Breath bug that sends the wrong code, verifier, client
// or token fails here the way it would upstream.
type authServer struct {
	clientID     string // "" admits any client (dynamically registered ones)
	clientSecret string // "" skips the secret check (public clients)
	tokenPrefix  string

	mu      sync.Mutex
	codes   map[string]pendingCode
	tokens  map[string]bool
	refresh map[string]bool
	seq     int
	logins  int // authorize requests approved
}

type pendingCode struct {
	clientID    string
	redirectURI string
	challenge   string
}

func newAuthServer(clientID, clientSecret, tokenPrefix string) *authServer {
	return &authServer{
		clientID: clientID, clientSecret: clientSecret, tokenPrefix: tokenPrefix,
		codes: map[string]pendingCode{}, tokens: map[string]bool{}, refresh: map[string]bool{},
	}
}

func (a *authServer) next(kind string) string {
	a.seq++
	return fmt.Sprintf("%s%s-%d", a.tokenPrefix, kind, a.seq)
}

// authorize approves the request and bounces straight back with a code.
func (a *authServer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if a.clientID != "" && q.Get("client_id") != a.clientID {
		http.Error(w, "unknown client_id "+q.Get("client_id"), http.StatusBadRequest)
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "PKCE (S256) required", http.StatusBadRequest)
		return
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Scheme == "" {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	code := a.next("code")
	a.codes[code] = pendingCode{clientID: q.Get("client_id"), redirectURI: q.Get("redirect_uri"), challenge: q.Get("code_challenge")}
	a.logins++
	a.mu.Unlock()

	back := redirect.Query()
	back.Set("code", code)
	back.Set("state", q.Get("state"))
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// token handles authorization_code (with PKCE) and refresh_token grants.
func (a *authServer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request")
		return
	}
	clientID, clientSecret, ok := r.BasicAuth()
	if !ok {
		clientID, clientSecret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if a.clientID != "" && clientID != a.clientID {
		oauthError(w, "invalid_client")
		return
	}
	if a.clientSecret != "" && clientSecret != a.clientSecret {
		oauthError(w, "invalid_client")
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		code := r.PostForm.Get("code")
		p, found := a.codes[code]
		delete(a.codes, code)
		if !found || p.clientID != clientID || p.redirectURI != r.PostForm.Get("redirect_uri") {
			oauthError(w, "invalid_grant")
			return
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
			oauthError(w, "invalid_grant")
			return
		}
	case "refresh_token":
		if !a.refresh[r.PostForm.Get("refresh_token")] {
			oauthError(w, "invalid_grant")
			return
		}
	default:
		oauthError(w, "unsupported_grant_type")
		return
	}
	access, refresh := a.next("access"), a.next("refresh")
	a.tokens[access] = true
	a.refresh[refresh] = true
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "refresh_token": refresh,
		"token_type": "bearer", "expires_in": 3600,
	})
}

// Logins counts the authorize requests approved so far.
func (a *authServer) Logins() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.logins
}

// bearerOK reports whether the request carries a token this server issued.
func (a *authServer) bearerOK(r *http.Request) bool {
	tok, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found {
		// GitHub also takes "token <x>".
		tok, found = strings.CutPrefix(r.Header.Get("Authorization"), "token ")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return found && a.tokens[tok]
}

func oauthError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ── Fake GitHub ─────────────────────────────────────────────────────

// The data fake GitHub serves. Tests assert against these, so an app or
// MCP client that shows them got them through the whole chain.
var (
	githubUser = map[string]any{
		"login": "octo-fixture", "id": 4242, "name": "Octo Fixture",
		"email": "octo@fixture.test",
	}
	githubRepo = map[string]any{
		"id": 1296269, "name": "hello-fixture", "full_name": "octo-fixture/hello-fixture",
		"private": false, "stargazers_count": 1337, "default_branch": "main",
	}
	githubIssues = []map[string]any{
		{"number": 7, "title": "Fixture issue seven", "state": "open"},
		{"number": 3, "title": "Fixture issue three", "state": "open"},
	}
)

const (
	githubClientID     = "fake-github-client"
	githubClientSecret = "fake-github-secret"
)

// fakeGitHub is a GitHub OAuth App plus the few REST routes the tests
// call. OAuth lives under /login/oauth/ and the API at the root, the way
// github.com and api.github.com split them.
type fakeGitHub struct {
	*httptest.Server
	*authServer
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{authServer: newAuthServer(githubClientID, githubClientSecret, "gho_")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", f.authorize)
	mux.HandleFunc("POST /login/oauth/access_token", f.token)
	api := func(v any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !f.bearerOK(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
				return
			}
			writeJSON(w, http.StatusOK, v)
		}
	}
	mux.HandleFunc("GET /user", api(githubUser))
	mux.HandleFunc("GET /user/emails", api([]map[string]any{{"email": githubUser["email"], "primary": true, "verified": true}}))
	mux.HandleFunc("GET /repos/octo-fixture/hello-fixture", api(githubRepo))
	mux.HandleFunc("GET /repos/octo-fixture/hello-fixture/issues", api(githubIssues))
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// ── Fake Notion MCP ─────────────────────────────────────────────────

// The pages fake Notion's search returns.
var notionPages = []map[string]any{
	{"id": "page-fixture-1", "title": "Fixture Roadmap", "url": "https://notion.fixture/page-fixture-1"},
	{"id": "page-fixture-2", "title": "Fixture Meeting Notes", "url": "https://notion.fixture/page-fixture-2"},
}

// fakeNotion is an MCP server behind OAuth with its own authorization
// server on the same origin: protected-resource metadata, auth-server
// metadata, dynamic client registration, PKCE — the shape of
// mcp.notion.com.
type fakeNotion struct {
	*httptest.Server
	*authServer
}

func newFakeNotion(t *testing.T) *fakeNotion {
	t.Helper()
	f := &fakeNotion{authServer: newAuthServer("", "", "ntn_")}

	server := mcp.NewServer(&mcp.Implementation{Name: "fake-notion", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "notion-search", Description: "Search the workspace."},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct {
			Query string `json:"query"`
		}) (*mcp.CallToolResult, any, error) {
			var hits []map[string]any
			for _, p := range notionPages {
				if strings.Contains(strings.ToLower(p["title"].(string)), strings.ToLower(in.Query)) {
					hits = append(hits, p)
				}
			}
			return nil, map[string]any{"results": hits}, nil
		})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if !f.bearerOK(r) {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.URL+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"resource": f.URL + "/mcp", "authorization_servers": []string{f.URL}})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                f.URL,
			"authorization_endpoint":                f.URL + "/authorize",
			"token_endpoint":                        f.URL + "/token",
			"registration_endpoint":                 f.URL + "/register",
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var meta map[string]any
		json.NewDecoder(r.Body).Decode(&meta)
		meta["client_id"] = "notion-dcr-client"
		writeJSON(w, http.StatusCreated, meta)
	})
	mux.HandleFunc("GET /authorize", f.authorize)
	mux.HandleFunc("POST /token", f.token)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}
