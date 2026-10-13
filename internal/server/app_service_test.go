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

// The MCP initialize request every /mcp/ POST starts with.
const mcpInitReq = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

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
	// The service is named for the nonce, not the route.
	name := "app:" + nonce
	other := openApp(t, srv, "other")

	// Mounted over HTTP behind the app's (here open) gate. The MCP server
	// answers as frbr-app-<slugified nonce>.
	rr := testRequest(t, srv, http.MethodPost, "/mcp/"+name, strings.NewReader(mcpInitReq),
		map[string]string{"Accept": "application/json, text/event-stream"})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "frbr-"+slugify(name)) {
		t.Fatalf("initialize /mcp/%s = %d %s", name, rr.Code, rr.Body.String())
	}

	// Every app starts with a blank, mounted service.
	if names, _ := appServiceToolNames(t, srv, name); len(names) != 0 {
		t.Fatalf("blank service tools = %v, want none", names)
	}

	// Defined through the same MCP tool as any virtual service.
	res := callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name": name, "new_text": appServiceToolFile, "transport": "inline",
	})
	if res.IsError {
		t.Fatalf("write_service_file: %s", toolResultText(t, res))
	}
	if _, err := os.Stat(filepath.Join(srv.config.DataDir, "apps", nonce, "service.txt")); err != nil {
		t.Fatalf("definition not stored with the app: %v", err)
	}
	res = callCentralTool(t, srv, "read_service_file", map[string]interface{}{"name": name, "transport": "inline"})
	if !strings.Contains(toolResultText(t, res), "[hello]") {
		t.Errorf("read_service_file = %s", toolResultText(t, res))
	}

	// MCP picks the tool up.
	names, cs := appServiceToolNames(t, srv, name)
	if len(names) != 1 || names[0] != "hello" {
		t.Fatalf("tools = %v, want [hello]", names)
	}
	call, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "hello", Arguments: map[string]any{"name": "mcp"}})
	if err != nil || !strings.Contains(toolResultText(t, call), `"name":"mcp"`) {
		t.Fatalf("call over MCP = %s, %v", toolResultText(t, call), err)
	}

	// HTTP: the app itself may call it; another app may not.
	body := `{"task":"hello","args":{"name":"http"}}`
	rr = testRequest(t, srv, http.MethodPost, "/service/call/"+name, strings.NewReader(body), map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"name":"http"`) {
		t.Fatalf("own app call = %d %s", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, http.MethodPost, "/service/call/"+name, strings.NewReader(body), map[string]string{"X-App-Nonce": other})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("other app call = %d, want 403", rr.Code)
	}

	// A page logs in to it by URL, as to any service (frbr.js login).
	rr = testRequest(t, srv, http.MethodGet, "/service/login?resolve=1&url=/mcp/"+name, nil, map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"url":"/mcp/`+name+`"`) {
		t.Fatalf("login resolve = %d %s", rr.Code, rr.Body.String())
	}
	rr = testRequest(t, srv, http.MethodGet, "/service/login?resolve=1&url=/mcp/"+name, nil, map[string]string{"X-App-Nonce": other})
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

	// The nonce is the name, so renaming the app moves nothing — the same
	// URL keeps answering, tools and all.
	if err := srv.coreUpdateApp(admin, nonce, "Pin Board", "Development", "", nil, &anon); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if names, _ := appServiceToolNames(t, srv, name); len(names) != 1 {
		t.Errorf("renamed app's tools = %v, want [hello] at the same mount", names)
	}
	rr = testRequest(t, srv, http.MethodPost, "/mcp/"+name, strings.NewReader(mcpInitReq),
		map[string]string{"Accept": "application/json, text/event-stream"})
	if rr.Code != http.StatusOK {
		t.Errorf("initialize after rename = %d, want 200 (the nonce outlives names)", rr.Code)
	}

	// Deleting the definition blanks it; the mount stays.
	rr = testRequest(t, srv, http.MethodDelete, "/api/apps/"+nonce+"/service/files", nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE service/files = %d %s", rr.Code, rr.Body.String())
	}
	if names, _ := appServiceToolNames(t, srv, name); len(names) != 0 {
		t.Errorf("after delete tools = %v, want none (still mounted)", names)
	}

	// Deleting the app takes the service with it.
	if err := srv.coreDeleteApp(admin, nonce); err != nil {
		t.Fatalf("delete app: %v", err)
	}
	if rr := testRequest(t, srv, http.MethodPost, "/mcp/"+name, strings.NewReader(`{}`), nil); rr.Code != http.StatusNotFound {
		t.Errorf("deleted app's service = %d, want 404", rr.Code)
	}
}

// The bare "app:" slug stands for the current app's own service: over MCP
// and /service/call, resolved from the request's app context (X-App-Nonce),
// with no app context a clear 400, and never another app's service.
func TestAppServiceBareSlug(t *testing.T) {
	srv := newTestServer(t)
	admin := &db.User{ID: 1, Role: "Superuser"}
	anon := builtinAuth(t, srv, db.AuthAnonymous).ID
	nonce, err := srv.coreCreateApp(admin, "Notes Board", "Development", "", nil, &anon)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	other := openApp(t, srv, "other")
	if res := callCentralTool(t, srv, "write_service_file", map[string]interface{}{
		"name": "app:" + nonce, "new_text": appServiceToolFile, "transport": "inline",
	}); res.IsError {
		t.Fatalf("write_service_file: %s", toolResultText(t, res))
	}

	// Over MCP, from the app's own pages: the header names the app.
	rr := testRequest(t, srv, http.MethodPost, "/mcp/app:", strings.NewReader(mcpInitReq),
		map[string]string{"Accept": "application/json, text/event-stream", "X-App-Nonce": nonce})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "frbr-app-") {
		t.Fatalf("initialize /mcp/app: = %d %s", rr.Code, rr.Body.String())
	}
	// No app context to resolve: a clear 400, not a mystery 404.
	rr = testRequest(t, srv, http.MethodPost, "/mcp/app:", strings.NewReader(mcpInitReq),
		map[string]string{"Accept": "application/json, text/event-stream"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("initialize /mcp/app: without context = %d, want 400", rr.Code)
	}
	// The admin nonce is no app either.
	rr = testRequest(t, srv, http.MethodPost, "/mcp/app:", strings.NewReader(mcpInitReq),
		map[string]string{"Accept": "application/json, text/event-stream", "X-App-Nonce": srv.adminNonce})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("initialize /mcp/app: as admin = %d, want 400", rr.Code)
	}
	// Another app's context resolves to that app's own (blank) service —
	// the bare slug is whoever's asking, never a foreign one.
	rr = testRequest(t, srv, http.MethodPost, "/mcp/app:", strings.NewReader(mcpInitReq),
		map[string]string{"Accept": "application/json, text/event-stream", "X-App-Nonce": other})
	if rr.Code != http.StatusOK {
		t.Errorf("initialize /mcp/app: as other app = %d %s", rr.Code, rr.Body.String())
	}

	// Over /service/call, the same bare form with the header nonce.
	body := `{"task":"hello","args":{"name":"bare"}}`
	rr = testRequest(t, srv, http.MethodPost, "/service/call/app:", strings.NewReader(body), map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"name":"bare"`) {
		t.Fatalf("bare slug call = %d %s", rr.Code, rr.Body.String())
	}
	// The other app's bare form calls its own (blank) service — the tool is
	// missing there, not forbidden: the app: part was never theirs to reach.
	rr = testRequest(t, srv, http.MethodPost, "/service/call/app:", strings.NewReader(body), map[string]string{"X-App-Nonce": other})
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("other app's bare slug call = %d, want 500 (its own blank service, no such tool)", rr.Code)
	}

	// A page logs in by the bare URL too; the resolve answers with the
	// canonical nonce URL, so the page learns its fixed address.
	rr = testRequest(t, srv, http.MethodGet, "/service/login?resolve=1&url=/mcp/app:", nil, map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"url":"/mcp/app:`+nonce+`"`) {
		t.Fatalf("bare login resolve = %d %s", rr.Code, rr.Body.String())
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
	name := "app:" + nonce
	rr := testRequest(t, srv, http.MethodPost, "/mcp/"+name, strings.NewReader(`{}`), nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("MCP without a token = %d, want 401", rr.Code)
	}
	rr = testRequest(t, srv, http.MethodPost, "/service/call/"+name, strings.NewReader(`{"task":"x"}`), map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("HTTP call without a token = %d, want 401", rr.Code)
	}
	// The bare form too: resolving the app is free, its gate still demands
	// a token.
	rr = testRequest(t, srv, http.MethodPost, "/mcp/app:", strings.NewReader(`{}`), map[string]string{"X-App-Nonce": nonce})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("bare MCP without a token = %d, want 401", rr.Code)
	}
}

// Only the service-file tools know app: names; get_service would otherwise
// hand any member another app's nonce and gate.
func TestAppServiceHiddenFromServiceTools(t *testing.T) {
	srv := newTestServer(t)
	nonce, err := srv.coreCreateApp(&db.User{ID: 1, Role: "Superuser"}, "private", "", "", nil, nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	name := "app:" + nonce
	for _, tool := range []string{"get_service", "get_service_apps", "delete_service"} {
		if res := callCentralTool(t, srv, tool, map[string]interface{}{"name": name}); !res.IsError {
			t.Errorf("%s(%s) succeeded: %s", tool, name, toolResultText(t, res))
		}
	}
	if res := callCentralTool(t, srv, "list_service_files", map[string]interface{}{"name": name}); res.IsError {
		t.Errorf("list_service_files(%s): %s", name, toolResultText(t, res))
	}
}
