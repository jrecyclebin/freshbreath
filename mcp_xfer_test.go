package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"poggers.institute/freshbreath/internal/db"
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

func createAppFile(t *testing.T, srv *Server, nonce, path string, content []byte) {
	t.Helper()
	webDir := filepath.Join(srv.config.DataDir, "apps", nonce, "web")
	fullPath := filepath.Join(webDir, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		t.Fatalf("write app file: %v", err)
	}
}

func TestMCPListAppFiles(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "list-app", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Empty initially.
	res := callCentralTool(t, srv, "list_app_files", map[string]interface{}{"nonce": nonce})
	if res.IsError {
		t.Fatalf("list_app_files failed: %s", toolResultText(t, res))
	}
	if !strings.Contains(toolResultText(t, res), `"files":[]`) {
		t.Errorf("expected empty files, got %s", toolResultText(t, res))
	}

	createAppFile(t, srv, nonce, "index.html", []byte("<h1>hello</h1>"))
	createAppFile(t, srv, nonce, "styles.css", []byte("body { color: blue; }"))

	res = callCentralTool(t, srv, "list_app_files", map[string]interface{}{"nonce": nonce})
	if res.IsError {
		t.Fatalf("list_app_files failed: %s", toolResultText(t, res))
	}
	var listRes struct {
		Files []appFile `json:"files"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &listRes); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(listRes.Files) != 2 {
		t.Errorf("files = %v, want 2", listRes.Files)
	}

	// Search by content.
	res = callCentralTool(t, srv, "list_app_files", map[string]interface{}{"nonce": nonce, "search": "blue"})
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &listRes); err != nil {
		t.Fatalf("parse search: %v", err)
	}
	if len(listRes.Files) != 1 || listRes.Files[0].Path != "styles.css" {
		t.Errorf("search result = %v, want [styles.css]", listRes.Files)
	}
}

func TestMCPReadAppFile(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "read-app", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "index.html", []byte("<h1>hello world</h1>"))

	res := callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce": nonce,
		"path":  "index.html",
	})
	if res.IsError {
		t.Fatalf("read_app_file failed: %s", toolResultText(t, res))
	}
	var readRes struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &readRes); err != nil {
		t.Fatalf("parse read: %v", err)
	}
	if readRes.Content != "<h1>hello world</h1>" {
		t.Errorf("content = %q, want <h1>hello world</h1>", readRes.Content)
	}

	// Chunk read.
	res = callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce":  nonce,
		"path":   "index.html",
		"offset": 4,
		"limit":  5,
	})
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &readRes); err != nil {
		t.Fatalf("parse chunk: %v", err)
	}
	if readRes.Content != "hello" {
		t.Errorf("chunk = %q, want hello", readRes.Content)
	}
}

func TestMCPReadAppFileBinary(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "read-bin", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "data.bin", []byte{0xff, 0xfe, 0xfd})

	res := callCentralTool(t, srv, "read_app_file", map[string]interface{}{
		"nonce": nonce,
		"path":  "data.bin",
	})
	if res.IsError {
		t.Fatalf("read_app_file failed: %s", toolResultText(t, res))
	}
	var readRes struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &readRes); err != nil {
		t.Fatalf("parse read: %v", err)
	}
	if readRes.Encoding != "base64" {
		t.Errorf("encoding = %q, want base64", readRes.Encoding)
	}
	want := base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe, 0xfd})
	if readRes.Content != want {
		t.Errorf("content = %q, want %q", readRes.Content, want)
	}
}

func TestMCPWriteAppFileWholeAndPatch(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "write-app", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Write whole file.
	res := callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce":   nonce,
		"path":    "index.html",
		"content": "<h1>hello</h1>",
	})
	if res.IsError {
		t.Fatalf("write_app_file failed: %s", toolResultText(t, res))
	}

	// Patch via old_text.
	res = callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce":    nonce,
		"path":     "index.html",
		"content":  "goodbye",
		"old_text": "hello",
	})
	if res.IsError {
		t.Fatalf("write_app_file patch failed: %s", toolResultText(t, res))
	}

	data, err := srv.coreReadAppFile(&db.User{ID: 1, Role: "Superuser"}, nonce, "index.html", 0, 0)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "<h1>goodbye</h1>" {
		t.Errorf("content = %q, want <h1>goodbye</h1>", data)
	}
}

func TestMCPWriteAppFileOldTextNotFound(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "write-missing", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "index.html", []byte("abc"))

	res := callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce":    nonce,
		"path":     "index.html",
		"content":  "x",
		"old_text": "notfound",
	})
	if !res.IsError {
		t.Fatal("expected error for missing old_text")
	}
	if !strings.Contains(toolResultText(t, res), "not found") {
		t.Errorf("error = %q, want not found", toolResultText(t, res))
	}
}

func TestMCPWriteAppFileOldTextNotUnique(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "write-dup", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "index.html", []byte("abc abc"))

	res := callCentralTool(t, srv, "write_app_file", map[string]interface{}{
		"nonce":    nonce,
		"path":     "index.html",
		"content":  "x",
		"old_text": "abc",
	})
	if !res.IsError {
		t.Fatal("expected error for non-unique old_text")
	}
	if !strings.Contains(toolResultText(t, res), "not unique") {
		t.Errorf("error = %q, want not unique", toolResultText(t, res))
	}
}

func TestMCPDeleteAppFile(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "delete-app", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	createAppFile(t, srv, nonce, "index.html", []byte("bye"))

	res := callCentralTool(t, srv, "delete_app_file", map[string]interface{}{
		"nonce": nonce,
		"path":  "index.html",
	})
	if res.IsError {
		t.Fatalf("delete_app_file failed: %s", toolResultText(t, res))
	}

	_, err = srv.coreReadAppFile(&db.User{ID: 1, Role: "Superuser"}, nonce, "index.html", 0, 0)
	if err == nil {
		t.Fatal("expected file to be deleted")
	}
}

func TestMCPListServiceFiles(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	// Empty initially.
	res := callCentralTool(t, srv, "list_service_files", map[string]interface{}{"name": svc.Name})
	if res.IsError {
		t.Fatalf("list_service_files failed: %s", toolResultText(t, res))
	}
	if !strings.Contains(toolResultText(t, res), `"files":[]`) {
		t.Errorf("expected empty files, got %s", toolResultText(t, res))
	}

	content := []byte("[build]\nmake all\n")
	if err := srv.coreWriteServiceFile(admin, svc.ID, content, ""); err != nil {
		t.Fatalf("write service file: %v", err)
	}

	res = callCentralTool(t, srv, "list_service_files", map[string]interface{}{"name": svc.Name})
	var listRes struct {
		Files []appFile `json:"files"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &listRes); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(listRes.Files) != 1 || listRes.Files[0].Path != "deploy.txt" {
		t.Errorf("files = %v, want [deploy.txt]", listRes.Files)
	}

	// Search by content.
	res = callCentralTool(t, srv, "list_service_files", map[string]interface{}{"name": svc.Name, "search": "make all"})
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &listRes); err != nil {
		t.Fatalf("parse search: %v", err)
	}
	if len(listRes.Files) != 1 {
		t.Errorf("search result = %v, want 1 file", listRes.Files)
	}

	res = callCentralTool(t, srv, "list_service_files", map[string]interface{}{"name": svc.Name, "search": "missing"})
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &listRes); err != nil {
		t.Fatalf("parse search missing: %v", err)
	}
	if len(listRes.Files) != 0 {
		t.Errorf("search result = %v, want empty", listRes.Files)
	}
}

func TestMCPReadServiceFile(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	content := []byte("[build]\nmake all\n")
	if err := srv.coreWriteServiceFile(admin, svc.ID, content, ""); err != nil {
		t.Fatalf("write service file: %v", err)
	}

	res := callCentralTool(t, srv, "read_service_file", map[string]interface{}{"name": svc.Name})
	if res.IsError {
		t.Fatalf("read_service_file failed: %s", toolResultText(t, res))
	}
	var readRes struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &readRes); err != nil {
		t.Fatalf("parse read: %v", err)
	}
	if readRes.Content != string(content) {
		t.Errorf("content = %q, want %q", readRes.Content, content)
	}

	// Chunk read.
	res = callCentralTool(t, srv, "read_service_file", map[string]interface{}{
		"name":   svc.Name,
		"offset": 1,
		"limit":  5,
	})
	if err := json.Unmarshal([]byte(toolResultText(t, res)), &readRes); err != nil {
		t.Fatalf("parse chunk: %v", err)
	}
	if readRes.Content != "build" {
		t.Errorf("chunk = %q, want build", readRes.Content)
	}
}

func TestMCPWriteServiceFile(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	res := callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name":    svc.Name,
		"content": "[build]\nmake all\n",
	})
	if res.IsError {
		t.Fatalf("write_service_file failed: %s", toolResultText(t, res))
	}

	res = callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name":     svc.Name,
		"content":  "make install",
		"old_text": "make all",
	})
	if res.IsError {
		t.Fatalf("write_service_file patch failed: %s", toolResultText(t, res))
	}

	data, _, err := srv.coreReadServiceFile(admin, svc.ID, 0, 0)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "[build]\nmake install\n" {
		t.Errorf("content = %q, want [build]\\nmake install\\n", data)
	}
}

func TestMCPDeleteServiceFile(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "deploy", "", db.ServiceDescriptor{Type: "tasks"})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := srv.coreWriteServiceFile(admin, svc.ID, []byte("x"), ""); err != nil {
		t.Fatalf("write service file: %v", err)
	}

	res := callCentralTool(t, srv, "delete_service_file", map[string]interface{}{"name": svc.Name})
	if res.IsError {
		t.Fatalf("delete_service_file failed: %s", toolResultText(t, res))
	}

	_, _, err = srv.coreReadServiceFile(admin, svc.ID, 0, 0)
	if err == nil {
		t.Fatal("expected file to be deleted")
	}
}

func TestMCPServiceFileUnsupportedType(t *testing.T) {
	srv := newTestServer(t)
	admin := &db.User{ID: 1, Role: "Superuser"}
	svc, err := srv.coreCreateService(admin, "api-svc", "http://example.com", db.ServiceDescriptor{Type: "api"})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	res := callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name":    svc.Name,
		"content": "x",
	})
	if !res.IsError {
		t.Fatal("expected error for unsupported service type")
	}
	if !strings.Contains(toolResultText(t, res), "does not support file publishing") {
		t.Errorf("error = %q, want unsupported type", toolResultText(t, res))
	}
}
