package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMCPRoutingDispatch locks down the ServeMux routing contract between the
// central MCP server (/mcp) and the per-virtual-service MCP server (/mcp/{slug}).
//
// Go 1.22+'s enhanced ServeMux treats "/mcp" (no trailing slash) as an exact
// match — it matches ONLY the literal path "/mcp" — while "/mcp/{name}" matches
// "/mcp/<non-empty-segment>". Their matching sets are disjoint, so they neither
// conflict (registration would panic) nor shadow each other. This test proves
// the dispatch with the real Server.mux after SetupRoutes, guarding against
// accidental changes (e.g. turning "/mcp" into a "/mcp/" subtree, which would
// greedily swallow /mcp/{name} requests).
func TestMCPRoutingDispatch(t *testing.T) {
	srv := newTestServer(t)

	cases := []struct {
		method, path, wantPattern string
	}{
		// Central MCP server — bare /mcp must route to the central handler,
		// never to the per-service /mcp/{name} handler.
		{http.MethodPost, "/mcp", "/mcp"},
		{http.MethodGet, "/mcp", "/mcp"},

		// Per-virtual-service MCP server — /mcp/<slug> must NOT be swallowed by
		// the exact /mcp pattern.
		{http.MethodPost, "/mcp/sharepoint", "/mcp/{name}"},
		{http.MethodPost, "/mcp/my-svc", "/mcp/{name}"},

		// Protected-resource-metadata endpoints mirror the same split.
		{http.MethodGet, "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource/mcp"},
		{http.MethodGet, "/.well-known/oauth-protected-resource/mcp/sharepoint", "/.well-known/oauth-protected-resource/mcp/{name}"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		_, pattern := srv.mux.Handler(req)
		if pattern != c.wantPattern {
			t.Errorf("%s %s: matched pattern %q, want %q", c.method, c.path, pattern, c.wantPattern)
		}
	}
}

// TestMCPRoutingBareTrailingSlash documents the behavior of /mcp/ (trailing
// slash, empty slug). Neither "/mcp" (exact) nor "/mcp/{name}" (non-empty
// segment) matches it, so it falls through to the catch-all "/" index handler
// rather than the central MCP server. This is acceptable because the central
// MCP PRM advertises the canonical "/mcp" URL (no trailing slash) and MCP
// clients POST to the advertised resource. This test exists to surface a
// behavior change if routing is ever reworked to handle the trailing-slash
// case (e.g. via a redirect).
func TestMCPRoutingBareTrailingSlash(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp/", nil)
	_, pattern := srv.mux.Handler(req)
	if pattern == "/mcp" || pattern == "/mcp/{name}" {
		t.Fatalf("/mcp/ unexpectedly matched MCP pattern %q; expected fall-through to /", pattern)
	}
}

// TestMCPHostGuard covers the DNS-rebinding check on loopback connections.
// A tunnel (cloudflared → localhost) arrives on loopback carrying the
// public Host; a signed-in client there must get through, not a 403.
func TestMCPHostGuard(t *testing.T) {
	srv := newTestServer(t)
	srv.config.PublicBaseURL = "https://Tunnel.Example.dev"
	srv.SetupRoutes()
	rec := fakeGitHubIdP(t, srv)
	makeAdminAuth(t, srv, rec)
	user := knownUser(t, srv)
	tok, err := srv.mintFreshbreathToken(fmt.Sprintf("frbr:%d", user.ID), user.Email, "", user.Name, rec.ID, rec.ID, nil, nil, time.Now().Unix())
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	loopback := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9009}
	lan := &net.TCPAddr{IP: net.ParseIP("192.168.1.20"), Port: 9009}
	cases := []struct {
		name  string
		local net.Addr
		host  string
		want  int
	}{
		{"public host via tunnel", loopback, "tunnel.example.dev", http.StatusOK},
		{"public host with port", loopback, "tunnel.example.dev:443", http.StatusOK},
		{"localhost", loopback, "localhost:9009", http.StatusOK},
		{"ipv6 loopback", loopback, "[::1]:9009", http.StatusOK},
		{"rebinding domain", loopback, "evil.example.com", http.StatusForbidden},
		{"lan connection, any host", lan, "evil.example.com", http.StatusOK},
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Host = c.host
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, c.local))
		rr := httptest.NewRecorder()
		srv.centralMCPHandler.ServeHTTP(rr, req)
		if rr.Code != c.want {
			t.Errorf("%s: got %d, want %d (%s)", c.name, rr.Code, c.want, strings.TrimSpace(rr.Body.String()))
		}
	}
}
