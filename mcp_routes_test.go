package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
