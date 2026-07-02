package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callCentralTool invokes a tool on the central MCP server via an in-memory
// transport. With no admin auth service configured, the call runs as the
// synthetic Superuser.
func callCentralTool(t *testing.T, srv *Server, name string, args map[string]string) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	mcps := srv.buildCentralMCPServerForRole("Superuser")
	ss, err := mcps.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })

	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func toolResultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		return ""
	}
	txt, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	return txt.Text
}

func TestXferFallbackHappy(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&User{ID: 1, Role: "Superuser"}, "xfer-fallback", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	pub := callCentralTool(t, srv, "publish_app_files", map[string]string{
		"nonce":    nonce,
		"filename": "index.html",
	})
	if pub.IsError {
		t.Fatalf("publish_app_files failed: %s", toolResultText(t, pub))
	}
	var pubRes struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, pub)), &pubRes); err != nil {
		t.Fatalf("parse publish result: %v", err)
	}

	data := base64.StdEncoding.EncodeToString([]byte("<h1>hello</h1>"))
	res := callCentralTool(t, srv, "xfer_fallback", map[string]string{
		"url":  pubRes.URL,
		"data": data,
	})
	if res.IsError {
		t.Fatalf("xfer_fallback failed: %s", toolResultText(t, res))
	}
	var upRes struct {
		Route string `json:"route"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &upRes); err != nil {
		t.Fatalf("parse upload result: %v", err)
	}
	if upRes.Route != "/xfer-fallback" {
		t.Errorf("route = %q, want /xfer-fallback", upRes.Route)
	}

	files, err := srv.coreListAppWeb(&User{ID: 1, Role: "Superuser"}, nonce)
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	if len(files) != 1 || files[0].Path != "index.html" {
		t.Errorf("files = %v, want [index.html]", files)
	}
}

func TestXferFallbackInvalidURL(t *testing.T) {
	srv := newTestServer(t)
	res := callCentralTool(t, srv, "xfer_fallback", map[string]string{
		"url":  "not-a-url",
		"data": base64.StdEncoding.EncodeToString([]byte("x")),
	})
	if !res.IsError {
		t.Fatal("expected error for invalid url")
	}
	if !strings.Contains(toolResultText(t, res), "invalid transfer url") {
		t.Errorf("error = %q, want invalid transfer url", toolResultText(t, res))
	}
}

func TestXferFallbackExpiredToken(t *testing.T) {
	srv := newTestServer(t)
	tok := "expired-token"
	srv.xfers[tok] = &transferEntry{
		Action:    "upload",
		Nonce:     "n",
		Filename:  "index.html",
		Actor:     &User{ID: -1, Role: "Superuser"},
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	url := srv.config.PublicBaseURL + "/api/xfer/" + tok

	res := callCentralTool(t, srv, "xfer_fallback", map[string]string{
		"url":  url,
		"data": base64.StdEncoding.EncodeToString([]byte("x")),
	})
	if !res.IsError {
		t.Fatal("expected error for expired token")
	}
	if !strings.Contains(toolResultText(t, res), "invalid or expired") {
		t.Errorf("error = %q, want invalid or expired", toolResultText(t, res))
	}
}

func TestXferFallbackDownloadURL(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&User{ID: 1, Role: "Superuser"}, "xfer-dl", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	dl := callCentralTool(t, srv, "download_app_files", map[string]string{"nonce": nonce})
	if dl.IsError {
		t.Fatalf("download_app_files failed: %s", toolResultText(t, dl))
	}
	var dlRes struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, dl)), &dlRes); err != nil {
		t.Fatalf("parse download result: %v", err)
	}

	res := callCentralTool(t, srv, "xfer_fallback", map[string]string{
		"url":  dlRes.URL,
		"data": base64.StdEncoding.EncodeToString([]byte("x")),
	})
	if !res.IsError {
		t.Fatal("expected error for download url")
	}
	if !strings.Contains(toolResultText(t, res), "not an upload") {
		t.Errorf("error = %q, want not an upload", toolResultText(t, res))
	}
}

func TestXferFallbackWrongUser(t *testing.T) {
	srv := newTestServer(t)
	tok := "wrong-user-token"
	srv.xfers[tok] = &transferEntry{
		Action:    "upload",
		Nonce:     "n",
		Filename:  "index.html",
		Actor:     &User{ID: 999, Role: "Member"},
		ExpiresAt: time.Now().Add(time.Minute),
	}
	url := srv.config.PublicBaseURL + "/api/xfer/" + tok

	res := callCentralTool(t, srv, "xfer_fallback", map[string]string{
		"url":  url,
		"data": base64.StdEncoding.EncodeToString([]byte("x")),
	})
	if !res.IsError {
		t.Fatal("expected error for wrong user")
	}
	if !strings.Contains(toolResultText(t, res), "different user") {
		t.Errorf("error = %q, want different user", toolResultText(t, res))
	}
}

func TestXferFallbackBadBase64(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&User{ID: 1, Role: "Superuser"}, "xfer-b64", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	pub := callCentralTool(t, srv, "publish_app_files", map[string]string{
		"nonce":    nonce,
		"filename": "index.html",
	})
	var pubRes struct {
		URL string `json:"url"`
	}
	json.Unmarshal([]byte(toolResultText(t, pub)), &pubRes)

	res := callCentralTool(t, srv, "xfer_fallback", map[string]string{
		"url":  pubRes.URL,
		"data": "!!!not-base64!!!",
	})
	if !res.IsError {
		t.Fatal("expected error for bad base64")
	}
	if !strings.Contains(toolResultText(t, res), "invalid base64") {
		t.Errorf("error = %q, want invalid base64", toolResultText(t, res))
	}
}

func TestXferFallbackTooLarge(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&User{ID: 1, Role: "Superuser"}, "xfer-large", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	pub := callCentralTool(t, srv, "publish_app_files", map[string]string{
		"nonce":    nonce,
		"filename": "index.html",
	})
	var pubRes struct {
		URL string `json:"url"`
	}
	json.Unmarshal([]byte(toolResultText(t, pub)), &pubRes)

	data := base64.StdEncoding.EncodeToString(make([]byte, xferMaxUpload+1))
	res := callCentralTool(t, srv, "xfer_fallback", map[string]string{
		"url":  pubRes.URL,
		"data": data,
	})
	if !res.IsError {
		t.Fatal("expected error for too large")
	}
	if !strings.Contains(toolResultText(t, res), "too large") {
		t.Errorf("error = %q, want too large", toolResultText(t, res))
	}
}
