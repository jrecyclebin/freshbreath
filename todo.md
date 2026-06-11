# Virtual Service Type — Declarative HTTP-to-MCP Bridge

Add a `virtual` service type to Freshbreath that wraps API calls in an MCP tool interface. A virtual service reads a description file (like `Sharepoint.txt`) that defines tools with a declarative HTTP request script syntax — no shell scripts, no code execution. The server interprets the scripts, makes the HTTP calls, and returns MCP-format responses.

## Key Design Decisions

- **Go MCP SDK**: Use `github.com/modelcontextprotocol/go-sdk` for the MCP endpoint — handles JSON-RPC, sessions, StreamableHTTP transport, tool registration, and auth middleware
- **Auto-discovery via RFC 9728**: Unauthenticated requests to `/mcp/{name}` get a 401 with `WWW-Authenticate` header pointing to Protected Resource Metadata. The PRM's `authorization_servers` field points to the OIDC service's issuer. External MCP clients can discover auth requirements and complete the OAuth flow automatically — no manual configuration needed.
- **Two access patterns**: (1) Full MCP server at `/mcp/{name}` for external tools/agents, (2) Simple GET/POST at `/service/call/{name}` for frbr.js
- **Service URL**: `/mcp/{slug}` (absolute path, not `virtual://`) — resilient to domain changes
- **Generalized call route**: `/service/task/{name}` → `/service/call/{name}` (shared by tasks + virtual)
- **Auth via SDK middleware**: `auth.RequireBearerToken` wraps the MCP handler with a custom `TokenVerifier`; verified token available from context as `$token` in scripts
- **Inline OIDC config**: Virtual services carry their own OAuth fields (issuer URL, client ID, client secret, scopes) directly — no separate auth service reference needed. The PRM document is built from these fields, pointing external MCP clients at the issuer for auto-discovery.
- **frbr.js detection**: URL prefix `/mcp/` → route through `/service/call/` (like `tasks://` for tasks)

## Relevant Files

- `handler.go:626-860` — Task parsing/execution. Virtual follows same pattern with HTTP interpreter.
- `handler.go:87-97` — Route registration. Need `/mcp/{name}` and rename task route.
- `handler.go:687-746` — `handleTaskCall`. Generalize to `handleServiceCall` dispatching by type.
- `handler.go:1414-1435` — `handleCreateService`. Add `virtual` type with `/mcp/` URL scheme.
- `handler.go:1464-1510` — `handleServiceDetail`. Same URL auto-generation for virtual.
- `handler.go:283-327` — `handleLogin`. Virtual services don't login (like tasks).
- `types.go:89` — `ServiceDescriptor.Type`. Add "virtual" type.
- `web/frbr.js:251-295` — `listTools`/`callTool`. Add `/mcp/` routing.
- `web/frbr.js:313-350` — `#callTask`. Generalize for both tasks and virtual.
- `web/control/app.js:1263-1317` — ServiceDrawer with type dropdown + virtual-specific fields.
- `web/control/app.js:486-489` — `serviceInstructions` helper.
- `Sharepoint.txt` — Sample virtual service description file.

## Quality Check

- `go build ./...` compiles cleanly
- Manual test: create virtual service, list tools, call a tool
- Manual test: external MCP client connects to `/mcp/{name}`

## Phase 1: Backend — Parser & Data Model ✅

- [x] Design VirtualTool and VirtualScript data structures
  - [x] VirtualStep: method, URL template, headers, body template, response assertions, variable assignments, response shaping
  - [x] VirtualTool: name, description, list of steps
- [x] Implement the virtual file parser
  - [x] Reuse `parseTaskHeader` for `[tool-name]` headers
  - [x] Require `---` separators between tool sections
  - [x] Parse HTTP request lines: `GET/POST/PATCH/PUT/DELETE url`
  - [x] Parse header lines (e.g. `Authorization: Bearer $token`)
  - [x] Parse JSON body blocks (with variable interpolation points)
  - [x] Parse `HTTP nnn` response assertions
  - [x] Parse response shaping JSON blocks (after `HTTP nnn`)
  - [x] Parse variable assignments: `$var = expression`
  - [x] Parse comment lines (`#`)
  - [x] Parse `assert()` expressions
  - [x] Handle `$$` escaping (literal `$` for query params)
- [x] Implement built-in functions: `host()`, `path()`, `assert()`
- [x] Implement JSON path query evaluation: `$.field`, `$['key']['subkey']`
- [x] Implement variable resolution: plain variables ($name) and JSON path queries ($[...])
- [x] URL-encode variables in URL positions; JSON-encode variables in JSON body positions
  - Note: URL mode uses raw string substitution (not QueryEscape) to preserve path
    structure; JSON-encode in body mode works correctly via json.Marshal

## Phase 2: Backend — HTTP Executor & Route Generalization

- [ ] Implement the virtual HTTP executor
  - [ ] Execute variable assignments as they occur
  - [ ] Prior to requests, JSON path queries access the incoming arguments
     (i.e. `$.token` and `$.arg_name` both work and are useful for complex
     object arguments.)
  - [ ] Step execution: resolve variables in URL/headers/body, make HTTP request
  - [ ] Check response status against assertion (e.g. `HTTP 200`)
  - [ ] Support multiple response code blocks after a request (branching)
  - [ ] Bring response body into scope for JSON path queries
  - [ ] Apply response shaping (if present) to produce tool output
  - [ ] If no shaping, return raw response body
- [ ] Rename `/service/task/{name}` → `/service/call/{name}`
- [ ] Generalize `handleTaskCall` → `handleServiceCall` dispatching by service type
  - [ ] Tasks → shell executor (existing)
  - [ ] Virtual → HTTP executor (new)
- [ ] Update `handleCreateService` / `handleServiceDetail` for virtual type with `/mcp/` URLs
- [ ] Update `handleLogin` to reject virtual services (like tasks)

## Phase 3: Backend — MCP Server Endpoint (Go MCP SDK)

- [ ] Add `github.com/modelcontextprotocol/go-sdk` dependency
- [ ] Create MCP server factory for virtual services
  - [ ] `newVirtualMCPServer(svc *ServiceDescriptor) *mcp.Server` — creates an `mcp.Server` with tools from the parsed virtual file
  - [ ] For each VirtualTool: `mcp.AddTool(server, &mcp.Tool{...}, handler)` where handler executes the virtual HTTP script
  - [ ] Tool handler extracts `$token` from context via `auth.TokenInfoFromContext(ctx)`
  - [ ] Tool handler executes virtual script steps and returns `*mcp.CallToolResult`
- [ ] Mount MCP endpoint at `/mcp/{name}`
  - [ ] Use `mcp.StreamableHTTPHandler` to wrap the server into an `http.Handler`
  - [ ] Wrap with `auth.RequireBearerToken` middleware using a custom `TokenVerifier`
  - [ ] `TokenVerifier` validates Bearer token using the service's inline OIDC config (JWT validation against the issuer, or OIDC introspection)
  - [ ] When no OIDC fields are configured (public virtual service), skip the `RequireBearerToken` middleware
- [ ] Serve Protected Resource Metadata (RFC 9728)
  - [ ] Mount `auth.ProtectedResourceMetadataHandler` at `/.well-known/oauth-protected-resource/mcp/{name}`
  - [ ] Build `oauthex.ProtectedResourceMetadata` from the service's inline OIDC config:
    - `Resource`: the MCP endpoint URL (`/mcp/{slug}`)
    - `AuthorizationServers`: the issuer URL from the service's OIDC fields
    - `ScopesSupported`: scopes from the service's OIDC fields
    - `BearerMethodsSupported`: `["header"]`
  - [ ] `RequireBearerTokenOptions.ResourceMetadataURL` points to this PRM endpoint
  - [ ] When no OIDC fields are configured, no PRM endpoint is needed
- [ ] Wire it all together in route registration
  - [ ] `/mcp/{name}` → `RequireBearerToken(mcpHandler)` (or just `mcpHandler` if public)
  - [ ] `/.well-known/oauth-protected-resource/mcp/{name}` → PRM handler (only when auth is configured)

## Phase 4: Frontend — frbr.js & Control Panel

- [ ] Add `/mcp/` URL detection to `listTools` and `callTool` in frbr.js
  - [ ] Route to `/service/call/{slug}` (same as tasks but extracting slug from `/mcp/slug`)
- [ ] Generalize `#callTask` → works for both tasks and virtual
- [ ] Update `connect()` to throw for virtual services
- [ ] Add "Virtual" option to ServiceDrawer type dropdown
- [ ] Show inline OIDC fields for virtual services (issuer URL, client ID, client secret, scopes) — same pattern as `api` type services
- [ ] Hide URL field for virtual services (auto-generated as `/mcp/{slug}`)
- [ ] Update `serviceInstructions` for virtual type — mention auto-discovery URL (`/mcp/{slug}`) and that external tools can connect directly
- [ ] Show the PRM URL in service instructions when OIDC fields are configured

## Phase 5: Task File `---` Separator Requirement

- [ ] Update `parseTasksFile` to require `---` between task sections
- [ ] Update any existing task files to use `---` separators

## Phase 6: Testing & Polish

- [ ] Create sample virtual description file (e.g. `virtual/Sharepoint.txt`)
- [ ] End-to-end test: create virtual service, list tools, call a tool via frbr.js
- [ ] End-to-end test: external MCP client connects to `/mcp/{name}`
- [ ] End-to-end test: unauthenticated request to `/mcp/{name}` returns 401 with `WWW-Authenticate` header pointing to PRM
- [ ] End-to-end test: PRM endpoint returns valid metadata with `authorization_servers` pointing to OIDC issuer
- [ ] End-to-end test: full auto-discovery flow — external client discovers auth, completes OAuth, calls tools
- [ ] End-to-end test: public virtual service (no OIDC fields) — no 401, no PRM endpoint, tools work directly
- [ ] Error handling: non-matching HTTP status, missing tools, assertion failures

## Legend

- 🔧 Backend Go code
- 🌐 Frontend JS code
- 📡 MCP protocol
- ⚠️ Breaking change (task route rename, `---` requirement)

## Notes

- Virtual services are like tasks but with an **interpreted HTTP script** instead of shell execution
- **Go MCP SDK** (`github.com/modelcontextprotocol/go-sdk`) handles all MCP protocol concerns — JSON-RPC, sessions, StreamableHTTP transport, tool registration
- **Auth auto-discovery** via RFC 9728: the `auth.RequireBearerToken` middleware + `auth.ProtectedResourceMetadataHandler` make the MCP endpoint self-describing. An external MCP client hitting `/mcp/{name}` without a token gets a 401 with `WWW-Authenticate: Bearer resource_metadata="<PRM-URL>"`. The PRM document tells the client where to authenticate. Zero manual config for the client.
- `$token` is extracted from the auth middleware's context (`auth.TokenInfoFromContext`) and injected into the virtual script, so virtual scripts can use it for upstream API auth
- Variables in URLs → URL-escaped; variables in JSON bodies → JSON-encoded
- `$$` in URLs → literal `$` (for Graph API's `$select`, `$top`, etc.)
- Response shaping: JSON block after `HTTP nnn` maps raw response → clean output
- Multiple response code blocks allowed after a request (branching on status)
- Variable assignment (`$var = expr`) can happen at any point in the script
- `assert()` for safety checks (e.g. validating nextLink host)
- MCP endpoint makes virtual services first-class — any agent can discover and use them
- The `TokenVerifier` validates tokens using the virtual service's own OIDC config — no separate auth service reference needed. The virtual service is the protected resource and carries its auth coordinates inline.

## Online References

- MCP Specification: https://spec.modelcontextprotocol.io/
- Go MCP SDK: https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp
- Go MCP SDK Auth: https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/auth
- Go MCP SDK OAuth Extensions: https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/oauthex
- RFC 9728 (Protected Resource Metadata): https://www.rfc-editor.org/rfc/rfc9728.html
- SEP-985 (MCP alignment with RFC 9728): https://github.com/modelcontextprotocol/modelcontextprotocol/issues/985
- Microsoft Graph API: the primary use case driving the design
