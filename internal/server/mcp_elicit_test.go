package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Elicitation e2e: FORM (blocking form elicitation), URL hand-off and URL
// suspend-resume through the real MCP handler with an elicitation-capable
// client over in-memory transports. The negotiated protocol is the SDK's
// latest, so the multi-round-trip path runs end to end: input-required
// results reach the client middleware, which fulfills them via the
// ElicitationHandler and retries — the full suspend/resume loop, no
// hand-rolling.
const elicitToolFile = `[approve] Ask the user to approve.

FORM "Approve the pending payout?"
    $approved is boolean
    $note is string?

{
  "approved": $approved,
  "note": $note
}
---
[link] Hand a link to the user and finish.

URL https://idp.example.com/authorize "Open this to connect"
---
[callback] Out-of-band flow gated on the completion callback.

URL https://idp.example.com/authorize?redirect=$elicitation_url "Authorize, then come back"
GET https://example.com/verify/$code

HTTP 200
`

func newElicitTest(t *testing.T) *Server {
	t.Helper()
	srv, _, _ := newVirtualSvcServer(t, elicitToolFile, db.ServiceDescriptor{Type: "virtual"})
	return srv
}

// connectElicitClient attaches an elicitation-capable client to the virtual
// service's MCP server and returns the client session (CallTool lives there).
func connectElicitClient(t *testing.T, srv *Server, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error), complete func(context.Context, *mcp.ElicitationCompleteNotificationRequest), srvOpts ...func(*mcp.ServerOptions)) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	mcps, err := srv.newVirtualMCPServer(mustVirtualSvc(t, srv), srvOpts...)
	if err != nil {
		t.Fatalf("virtual mcp server: %v", err)
	}
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := mcps.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })

	opts := &mcp.ClientOptions{ElicitationHandler: handler, ElicitationCompleteHandler: complete}
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, opts)
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// mustVirtualSvc fetches the Keeper virtual service created by
// newVirtualSvcServer. newVirtualMCPServer loads its tools fresh from the
// tool file on every connect, so callers that need to repoint step URLs
// (repointVerifyStep) must do so before dialing.
func mustVirtualSvc(t *testing.T, srv *Server) *db.Service {
	t.Helper()
	svc, err := srv.store.GetServiceByName("Keeper")
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	return svc
}

// repointVerifyStep rewrites the tool file so the callback tool's verify GET
// hits the test server instead of the const's placeholder host.
func repointVerifyStep(t *testing.T, srv *Server, url string) error {
	t.Helper()
	path := filepath.Join(srv.config.DataDir, "virtual", "Keeper.txt")
	file, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	const placeholder = "https://example.com/verify/$code"
	return os.WriteFile(path, bytes.Replace(file, []byte(placeholder), []byte(url), 1), 0o644)
}

func TestVirtualFormElicitationAccept(t *testing.T) {
	srv := newElicitTest(t)
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approved": true, "note": "looks good"}}, nil
	}
	c := connectElicitClient(t, srv, handler, nil)

	res, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "approve"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	text := toolResultText(t, res)
	if !strings.Contains(text, `"approved":true`) || !strings.Contains(text, `looks good`) {
		t.Fatalf("result = %s", text)
	}
}

func TestVirtualFormElicitationDecline(t *testing.T) {
	srv := newElicitTest(t)
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	c := connectElicitClient(t, srv, handler, nil)

	res, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "approve"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError || !strings.Contains(toolResultText(t, res), "declined") {
		t.Fatalf("want declined error, got isError=%v text=%s", res.IsError, toolResultText(t, res))
	}
}

func TestVirtualURLHandoff(t *testing.T) {
	srv := newElicitTest(t)
	called := false
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		called = true
		return &mcp.ElicitResult{Action: "accept"}, nil
	}
	c := connectElicitClient(t, srv, handler, nil)

	res, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "link"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if called {
		t.Fatal("hand-off URL step must not elicit")
	}
	if !strings.Contains(toolResultText(t, res), "https://idp.example.com/authorize") {
		t.Fatalf("result = %s", toolResultText(t, res))
	}
}

func TestVirtualURLSuspendResumeViaCallback(t *testing.T) {
	srv := newElicitTest(t)

	// Verify GET must hit a local server, not the real example.com.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/verify/abc123" {
			t.Errorf("verify got path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"verified": true}`)
	}))
	defer ts.Close()
	if err := repointVerifyStep(t, srv, ts.URL+"/verify/$code"); err != nil {
		t.Fatal(err)
	}

	// The host's URL handler waits for the completion notification
	// (spec-conscious behavior) before answering. The test completes the
	// out-of-band flow by hitting the public callback route, which fires
	// the notification AND carries query params that become the resumed
	// scope ($code for the verify GET).
	unblock := make(chan struct{})
	handler := func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		if req.Params.Mode != "url" {
			return nil, fmt.Errorf("unexpected mode %q", req.Params.Mode)
		}
		// $elicitation_url is query-escaped when interpolated, so decode the
		// redirect param before checking for the callback path.
		u, err := url.Parse(req.Params.URL)
		if err != nil {
			return nil, fmt.Errorf("bad elicitation URL %q: %w", req.Params.URL, err)
		}
		if !strings.Contains(u.Query().Get("redirect"), "/elicitation/") {
			return nil, fmt.Errorf("URL %q lacks the callback", req.Params.URL)
		}
		<-unblock
		return &mcp.ElicitResult{Action: "accept"}, nil
	}
	completeSeen := make(chan string, 1)
	complete := func(_ context.Context, n *mcp.ElicitationCompleteNotificationRequest) {
		select {
		case completeSeen <- n.Params.ElicitationID:
		default:
		}
	}
	c := connectElicitClient(t, srv, handler, complete)

	type callResult struct {
		res *mcp.CallToolResult
		err error
	}
	done := make(chan callResult, 1)
	go func() {
		res, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "callback"})
		done <- callResult{res, err}
	}()

	// Wait for the suspended URL elicitation to appear.
	var id string
	for i := 0; i < 100; i++ {
		srv.pendingElicits.mu.Lock()
		for _, e := range srv.pendingElicits.entries {
			if e.mode == "url" {
				id = e.id
			}
		}
		srv.pendingElicits.mu.Unlock()
		if id != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("no pending URL elicitation appeared")
	}

	// The out-of-band target redirects to the callback with data.
	req := httptest.NewRequest(http.MethodGet, "/elicitation/"+id+"?code=abc123", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("callback status = %d", rec.Code)
	}

	// The client learns completion; release its handler.
	select {
	case seen := <-completeSeen:
		if seen != id {
			t.Fatalf("complete notification for %q, want %q", seen, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("elicitation/complete notification never reached the client")
	}
	close(unblock)

	cr := <-done
	if cr.err != nil {
		t.Fatalf("call: %v", cr.err)
	}
	text := toolResultText(t, cr.res)
	if !strings.Contains(text, `"verified":true`) {
		t.Fatalf("resumed result = %s", text)
	}
}

// TestVirtualFormElicitationOldProtocol pins the pre-SEP-2322 path: a client
// negotiating an old protocol version gets the input request fulfilled by the
// SDK's server middleware via a blocking ss.Elicit — no input-required result
// ever reaches the host, and the handler is re-invoked with the response.
func TestVirtualFormElicitationOldProtocol(t *testing.T) {
	srv := newElicitTest(t)
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approved": true, "note": "ok"}}, nil
	}
	// 2025-06-18 predates multi-round-trip (2026-07-28), so the negotiated
	// version forces the middleware's blocking-fulfillment path.
	oldOnly := func(o *mcp.ServerOptions) {
		o.SupportedProtocolVersions = []string{"2025-06-18"}
	}
	cs := connectElicitClient(t, srv, handler, nil, oldOnly)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "approve"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("old-protocol elicitation failed: %s", toolResultText(t, res))
	}
	text := toolResultText(t, res)
	if !strings.Contains(text, `"approved":true`) {
		t.Fatalf("result = %s", text)
	}
}
