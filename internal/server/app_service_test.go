package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"poggers.institute/freshbreath/internal/db"
)

// A tool with no request step hands its arguments back.
const appServiceToolFile = `[hello] Echo a name.

{
  "name": $name
}
`

// appServiceToolNames connects a client to the MCP server mounted at
// /mcp/<slug> and returns its tool names.
func appServiceToolNames(t *testing.T, srv *Server, slug string) ([]string, *mcp.ClientSession) {
	t.Helper()
	entry := srv.mcpMounts.get(slug)
	if entry == nil {
		t.Fatalf("nothing mounted at /mcp/%s", slug)
	}
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := entry.mcps.Connect(context.Background(), t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	cs, err := c.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("connect /mcp/%s: %v", slug, err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names, cs
}

func TestAppServiceLifecycle(t *testing.T) {
	srv := newTestServer(t)
	admin := &db.User{ID: 1, Role: "Superuser"}
	anon := builtinAuth(t, srv, db.AuthAnonymous).ID
	nonce, err := srv.coreCreateApp(admin, "Notes Board", "Development", "", nil, &anon)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	other := openApp(t, srv, "other")

	// Mounted over HTTP behind the app's (here open) gate.
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	rr := testRequest(t, srv, http.MethodPost, "/mcp/app:notes-board", strings.NewReader(initReq),
		map[string]string{"Accept": "application/json, text/event-stream"})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "frbr-app-notes-board") {
		t.Fatalf("initialize /mcp/app:notes-board = %d %s", rr.Code, rr.Body.String())
	}

	// Every app starts with a blank, mounted service.
	if names, _ := appServiceToolNames(t, srv, "app:notes-board"); len(names) != 0 {
		t.Fatalf("blank service tools = %v, want none", names)
	}

	// Defined through the same MCP tool as any virtual service.
	res := callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name": "app:notes-board", "new_text": appServiceToolFile, "transport": "inline",
	})
	if res.IsError {
		t.Fatalf("write_service_file: %s", toolResultText(t, res))
	}
	if _, err := os.Stat(filepath.Join(srv.config.DataDir, "apps", nonce, "service.txt")); err != nil {
		t.Fatalf("definition not stored with the app: %v", err)
	}
	res = callCentralTool(t, srv, "read_service_file", map[string]interface{}{"name": "app:notes-board", "transport": "inline"})
	if !strings.Contains(toolResultText(t, res), "[hello]") {
		t.Errorf("read_service_file = %s", toolResultText(t, res))
	}

	// MCP picks the tool up.
	names, cs := appServiceToolNames(t, srv, "app:notes-board")
	if len(names) != 1 || names[0] != "hello" {
		t.Fatalf("tools = %v, want [hello]", names)
	}
	call, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "hello", Arguments: map[string]any{"name": "mcp"}})
	if err != nil || !strings.Contains(toolResultText(t, call), `"name":"mcp"`) {
		t.Fatalf("call over MCP = %s, %v", toolResultText(t, call), err)
	}

	// HTTP: the app itself may call it; another app may not.
	body := `{"task":"hello","args":{"name":"http"}}`
	rr = testRequest(t, srv, http.MethodPost, "/service/call/app:notes-board", strings.NewReader(body), map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"name":"http"`) {
		t.Fatalf("own app call = %d %s", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, http.MethodPost, "/service/call/app:notes-board", strings.NewReader(body), map[string]string{"X-App-Nonce": other})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("other app call = %d, want 403", rr.Code)
	}

	// A page logs in to it by URL, as to any service (frbr.js login).
	rr = testRequest(t, srv, http.MethodGet, "/service/login?resolve=1&url=/mcp/app:notes-board", nil, map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"url":"/mcp/app:notes-board"`) {
		t.Fatalf("login resolve = %d %s", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, http.MethodGet, "/service/login?resolve=1&url=/mcp/app:notes-board", nil, map[string]string{"X-App-Nonce": other})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("other app's login resolve = %d, want 403", rr.Code)
	}

	// The control panel's routes.
	rr = testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/service/files", nil, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "[hello]") {
		t.Fatalf("GET service/files = %d %s", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, http.MethodGet, "/api/apps/"+nonce+"/service/tools", nil, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"name":"hello"`) {
		t.Fatalf("GET service/tools = %d %s", rr.Code, rr.Body.String())
	}

	// Renaming the app moves the mount; deleting the definition blanks it.
	if err := srv.coreUpdateApp(admin, nonce, "Pin Board", "Development", "", nil, &anon); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if rr := testRequest(t, srv, http.MethodPost, "/mcp/app:notes-board", strings.NewReader(`{}`), nil); rr.Code != http.StatusNotFound {
		t.Errorf("old slug = %d, want 404", rr.Code)
	}
	if names, _ := appServiceToolNames(t, srv, "app:pin-board"); len(names) != 1 {
		t.Errorf("renamed service tools = %v, want [hello]", names)
	}
	rr = testRequest(t, srv, http.MethodDelete, "/api/apps/"+nonce+"/service/files", nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE service/files = %d %s", rr.Code, rr.Body.String())
	}
	if names, _ := appServiceToolNames(t, srv, "app:pin-board"); len(names) != 0 {
		t.Errorf("after delete tools = %v, want none (still mounted)", names)
	}

	// Deleting the app takes the service with it.
	if err := srv.coreDeleteApp(admin, nonce); err != nil {
		t.Fatalf("delete app: %v", err)
	}
	if rr := testRequest(t, srv, http.MethodPost, "/mcp/app:pin-board", strings.NewReader(`{}`), nil); rr.Code != http.StatusNotFound {
		t.Errorf("deleted app's service = %d, want 404", rr.Code)
	}
}

func TestAppServicePermissions(t *testing.T) {
	srv := newTestServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}
	nonce, _ := srv.coreCreateApp(admin, "team", "", "", nil, nil)
	member, _ := srv.coreCreateUser(admin, "member", "member@x", "Member", "Active")
	outsider, _ := srv.coreCreateUser(admin, "outsider", "outsider@x", "Member", "Active")
	if err := srv.coreSetAppMembers(admin, nonce, []int64{member.ID}); err != nil {
		t.Fatalf("set members: %v", err)
	}
	app, _ := srv.store.GetApp(nonce)

	if err := srv.coreWriteServiceFile(member, -app.ID, []byte(appServiceToolFile), ""); err != nil {
		t.Errorf("app member write: %v", err)
	}
	if err := srv.coreWriteServiceFile(outsider, -app.ID, []byte("x"), ""); err == nil {
		t.Errorf("outsider write succeeded, want forbidden")
	}

	// The app: prefix belongs to app services.
	if _, err := srv.coreCreateService(admin, "app:team", "", db.ServiceDescriptor{Type: "virtual"}, nil, nil); err == nil {
		t.Errorf("created a service named app:team, want reserved")
	}
}

// The app's gate guards its service: no token, no MCP and no HTTP call.
func TestAppServiceUsesAppGate(t *testing.T) {
	srv := newTestServer(t)
	idp := newAuthRecord(t, srv, "IdP", db.AuthOIDC, db.AuthDescriptor{Issuer: "https://idp.example", Provider: "idp"})
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "locked", "", "", nil, &idp.ID)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	rr := testRequest(t, srv, http.MethodPost, "/mcp/app:locked", strings.NewReader(`{}`), nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("MCP without a token = %d, want 401", rr.Code)
	}
	rr = testRequest(t, srv, http.MethodPost, "/service/call/app:locked", strings.NewReader(`{"task":"x"}`), map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("HTTP call without a token = %d, want 401", rr.Code)
	}
}

// Only the service-file tools know app: names; get_service would otherwise
// hand any member another app's nonce and gate.
func TestAppServiceHiddenFromServiceTools(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "private", "", "", nil, nil); err != nil {
		t.Fatalf("create app: %v", err)
	}
	for _, tool := range []string{"get_service", "get_service_apps", "delete_service"} {
		if res := callCentralTool(t, srv, tool, map[string]interface{}{"name": "app:private"}); !res.IsError {
			t.Errorf("%s(app:private) succeeded: %s", tool, toolResultText(t, res))
		}
	}
	if res := callCentralTool(t, srv, "list_service_files", map[string]interface{}{"name": "app:private"}); res.IsError {
		t.Errorf("list_service_files(app:private): %s", toolResultText(t, res))
	}
}
