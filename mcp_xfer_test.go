package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callCentralTool invokes a tool on the central MCP server via an in-memory
// transport. With no admin auth service configured, the call runs as the
// synthetic Superuser.
func callCentralTool(t *testing.T, srv *Server, name string, args map[string]interface{}) *mcp.CallToolResult {
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

func createZipWithFile(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.Create(name)
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestPublishAppFilesReturnsURL(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&User{ID: 1, Role: "Superuser"}, "app-url", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	pub := callCentralTool(t, srv, "publish_app_files", map[string]interface{}{
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
	if pubRes.URL == "" {
		t.Fatal("expected upload URL")
	}
}

func TestPublishAppFilesLegacyDataHappy(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&User{ID: 1, Role: "Superuser"}, "legacy-html", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	res := callCentralTool(t, srv, "publish_app_files", map[string]interface{}{
		"nonce":       nonce,
		"filename":    "index.html",
		"legacy_data": "<h1>hello</h1>",
	})
	if res.IsError {
		t.Fatalf("publish_app_files legacy_data failed: %s", toolResultText(t, res))
	}
	var upRes struct {
		Route string `json:"route"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &upRes); err != nil {
		t.Fatalf("parse upload result: %v", err)
	}
	if upRes.Route != "/legacy-html" {
		t.Errorf("route = %q, want /legacy-html", upRes.Route)
	}

	files, err := srv.coreListAppWeb(&User{ID: 1, Role: "Superuser"}, nonce)
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	if len(files) != 1 || files[0].Path != "index.html" {
		t.Errorf("files = %v, want [index.html]", files)
	}
}

func TestPublishAppFilesLegacyDataZip(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&User{ID: 1, Role: "Superuser"}, "legacy-zip", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	zipData := createZipWithFile(t, "index.html", "<h1>zip</h1>")
	res := callCentralTool(t, srv, "publish_app_files", map[string]interface{}{
		"nonce":       nonce,
		"filename":    "site.zip",
		"legacy_data": base64.StdEncoding.EncodeToString(zipData),
	})
	if res.IsError {
		t.Fatalf("publish_app_files legacy_data zip failed: %s", toolResultText(t, res))
	}
	var upRes struct {
		Route string `json:"route"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &upRes); err != nil {
		t.Fatalf("parse upload result: %v", err)
	}
	if upRes.Route != "/legacy-zip" {
		t.Errorf("route = %q, want /legacy-zip", upRes.Route)
	}

	files, err := srv.coreListAppWeb(&User{ID: 1, Role: "Superuser"}, nonce)
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	if len(files) != 1 || files[0].Path != "index.html" {
		t.Errorf("files = %v, want [index.html]", files)
	}
}

func TestPublishAppFilesLegacyDataBadBase64(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&User{ID: 1, Role: "Superuser"}, "legacy-b64", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	res := callCentralTool(t, srv, "publish_app_files", map[string]interface{}{
		"nonce":       nonce,
		"filename":    "site.zip",
		"legacy_data": "!!!not-base64!!!",
	})
	if !res.IsError {
		t.Fatal("expected error for bad base64")
	}
	if !strings.Contains(toolResultText(t, res), "invalid base64") {
		t.Errorf("error = %q, want invalid base64", toolResultText(t, res))
	}
}

func TestPublishAppFilesLegacyDataTooLarge(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&User{ID: 1, Role: "Superuser"}, "legacy-large", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	res := callCentralTool(t, srv, "publish_app_files", map[string]interface{}{
		"nonce":       nonce,
		"filename":    "index.html",
		"legacy_data": string(make([]byte, xferMaxUpload+1)),
	})
	if !res.IsError {
		t.Fatal("expected error for too large")
	}
	if !strings.Contains(toolResultText(t, res), "too large") {
		t.Errorf("error = %q, want too large", toolResultText(t, res))
	}
}

func TestDownloadAppFilesLegacyMode(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&User{ID: 1, Role: "Superuser"}, "legacy-dl", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, err := srv.coreUploadAppWeb(&User{ID: 1, Role: "Superuser"}, nonce, []byte("<h1>hello</h1>"), "index.html"); err != nil {
		t.Fatalf("upload app web: %v", err)
	}

	dl := callCentralTool(t, srv, "download_app_files", map[string]interface{}{
		"nonce":       nonce,
		"legacy_mode": true,
	})
	if dl.IsError {
		t.Fatalf("download_app_files legacy_mode failed: %s", toolResultText(t, dl))
	}
	var dlRes struct {
		Data     string `json:"data"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, dl)), &dlRes); err != nil {
		t.Fatalf("parse download result: %v", err)
	}
	if dlRes.Filename != "legacy-dl.zip" {
		t.Errorf("filename = %q, want legacy-dl.zip", dlRes.Filename)
	}
	data, err := base64.StdEncoding.DecodeString(dlRes.Data)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "index.html" {
		t.Errorf("zip contents = %v, want [index.html]", zr.File)
	}
}

func TestServiceFileMCPLegacyDataUpload(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &User{ID: 1, Role: "Superuser"}

	svc, err := srv.coreCreateService(admin, "deploy", "", ServiceDescriptor{Type: "tasks"})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	content := []byte("[build]\nmake all\n")
	res := callCentralTool(t, srv, "publish_service_files", map[string]interface{}{
		"id":          float64(svc.ID),
		"filename":    "deploy.txt",
		"legacy_data": string(content),
	})
	if res.IsError {
		t.Fatalf("publish_service_files legacy_data failed: %s", toolResultText(t, res))
	}
	var upRes struct {
		Route string `json:"route"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &upRes); err != nil {
		t.Fatalf("parse upload result: %v", err)
	}
	if upRes.Route != svc.URL {
		t.Errorf("route = %q, want %q", upRes.Route, svc.URL)
	}

	path := filepath.Join(srv.config.DataDir, "tasks", svc.Name+".txt")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read legacy file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("legacy file = %q, want %q", got, content)
	}
}

func TestServiceFileMCPLegacyModeDownload(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &User{ID: 1, Role: "Superuser"}

	svc, err := srv.coreCreateService(admin, "deploy-dl", "", ServiceDescriptor{Type: "tasks"})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	content := []byte("[build]\nmake all\n")
	path := filepath.Join(srv.config.DataDir, "tasks", svc.Name+".txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	dl := callCentralTool(t, srv, "download_service_files", map[string]interface{}{
		"id":          float64(svc.ID),
		"legacy_mode": true,
	})
	if dl.IsError {
		t.Fatalf("download_service_files legacy_mode failed: %s", toolResultText(t, dl))
	}
	var dlRes struct {
		Data     string `json:"data"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, dl)), &dlRes); err != nil {
		t.Fatalf("parse download result: %v", err)
	}
	if dlRes.Filename != svc.Name+".txt" {
		t.Errorf("filename = %q, want %q", dlRes.Filename, svc.Name+".txt")
	}
	if dlRes.Data != string(content) {
		t.Errorf("data = %q, want %q", dlRes.Data, content)
	}
}

func TestServiceFileMCPZipRejected(t *testing.T) {
	srv := newTestServer(t)
	admin := &User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "deploy", "", ServiceDescriptor{Type: "tasks"})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	res := callCentralTool(t, srv, "publish_service_files", map[string]interface{}{
		"id":       float64(svc.ID),
		"filename": "site.zip",
	})
	if !res.IsError {
		t.Fatal("expected error for zip filename")
	}
	if !strings.Contains(toolResultText(t, res), "zip") {
		t.Errorf("error = %q, want zip rejection", toolResultText(t, res))
	}
}

func TestServiceFileMCPUnsupportedType(t *testing.T) {
	srv := newTestServer(t)
	admin := &User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "api-svc", "http://example.com", ServiceDescriptor{Type: "api"})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	res := callCentralTool(t, srv, "publish_service_files", map[string]interface{}{
		"id":       float64(svc.ID),
		"filename": "x.txt",
	})
	if !res.IsError {
		t.Fatal("expected error for unsupported service type")
	}
	if !strings.Contains(toolResultText(t, res), "does not support file publishing") {
		t.Errorf("error = %q, want unsupported type", toolResultText(t, res))
	}
}
