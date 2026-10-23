# Fresh Breath API & MCP Map

A complete reference to every HTTP route and MCP endpoint the server mounts.

Code lives in `cmd/freshbreath` (entrypoint) and `internal/server` (all handlers).
Routes are registered in `internal/server/server.go` → `SetupRoutes()`
(`internal/server/handler.go` lines ~112–241).

**Conventions**

- All JSON request/response bodies unless noted. Errors are plain-text (`http.Error`)
  or bearer-challenge JSON on 401s (RFC 6750 / RFC 9470 fields, mirrored in body).
- `X-App-Nonce` header (or a bare `?NONCE` query — the form a script tag
  can carry) identifies which app is calling; CORS
  is origin-gated: same-origin always, and any other origin — `null`
  (file://) included — only for an app that runs off this server (its URL
  names a location, or it has no routable slot — a blank URL that isn't
  hosted is a page opened from disk), named by nonce. Hosted apps get no
  cross-origin allowance at all.
- Role gate: `anyRole` = Superuser/Admin/Member/Read-only · `admin+` = Superuser/Admin ·
  `superuser` = Superuser only. Writes on services/auth records are additionally
  gated by the resource's member list (`core*` functions).

---

## 1. Pages & assets

| Route | Method | Description |
|---|---|---|
| `/` | GET | Redirects to default app (setting `default_app`) or `/control` |
| `/{slug}/…` | GET | Hosted static app. Gate-checked (app's `protected_by`), SPA fallback to the nearest index.html up the tree, `no-cache`+ETag headers for revalidation |
| `/{slug}@{slot}/…` | GET | Same, explicit deployment slot: `@dev`, `@staging`, `@prod` |
| `/control` | GET/… | Admin control panel (single-page app); gated by the admin auth record |
| `/control/*` | GET | Control panel static assets (no auth; `vendor/` immutable, rest `no-cache`) |
| `/env.js` | GET | `window.FRESHBREATH_ENV` config for client apps |
| `/frbr.js` | GET | Client helper library for non-hosted apps |

## 2. Browser login flows (`auth_flows.go`)

These implement Fresh Breath's own login: apps call `login` → server returns a
provider redirect → provider calls `callback` → "legs" are completed in sequence
(Multi-legs: OAuth + acts-as records + passphrase/API-key gates).

| Route | Method | Description |
|---|---|---|
| `/service/login` | GET | Start/resolve a login. Query: `state` (app state), optional `url` (act-as-service), `resolve` (dry run → legs), `return` (redirect back into app); the app is named by `X-App-Nonce` (a fetch — the query always carries `state`, so there is no nonce-in-the-URL form). Bearer may satisfy legs (`type: ok`). Returns JSON `{type: anonymous\|ok\|legs\|redirect, url, legs, service}` |
| `/service/callback` | GET | Provider redirect target; exchanges code, advances/finishes the pending login |
| `/service/ssh-auth` | GET/POST | Passphrase-SSH login form (GET HTML form) & POST `{state, email, passphrase}`. Adds key to in-process SSH agent for `sshAgentTTL` |
| `/service/apikey-auth` | GET/POST | API-key gate form & POST `{state, api_key}` |
| `/service/logout` | GET/POST | Clears browser login state |
| `/service/passphrase` | GET/POST | Invite/reset link page: token in link is credential; POST sets passphrase |

## 3. Service proxy & direct calls

| Route | Method | Description |
|---|---|---|
| `/service/{id}/…` | ANY | Proxy to an HTTP/Azure service's URL. Requires `X-App-Nonce`; service must be approved for the app. Consumes gate credential, applies resolved outbound credential (sealed in browser token → unsealed here) |
| `/service/call/{name}` | GET/POST | Non-proxy REST-like surface for `tasks` & `virtual` services. GET lists tools; POST `{task, args}` (JSON or multipart for file args) executes one. Elicitation steps rejected (MCP-only) |

## 4. MCP

### 4a. Central MCP — `/mcp` (+ `/.well-known/oauth-protected-resource/mcp`)

Fresh Breath's own MCP server: the admin API as ~50 tools (role-gated subsets).
stateless. Auth = `RequireBearerToken` against its OAuth server (ASCII PRM),
tool calls verify against central MCP JWTs or the admin auth service's OIDC.
A per-role variant of the server is picked per request (role read from the
token): Superuser sees everything; Member/Read-only get the all-role app/file
tools, scoped to their own apps; user & settings tools appear only for
admin+/superuser. Setup mode (no admin auth record yet) serves a synthetic
superuser so an agent can bootstrap the server.

**App tools** (`registerAppTools`)
| Tool | Role | Does |
|---|---|---|
| `list_apps` | all | List apps (admin+ all / member's own) |
| `get_app` | all | App detail |
| `create_app` | admin+ | `{name, environment, url?, owner_id?, protected_by?}` |
| `update_app` | admin+ | Patch (same fields) |
| `delete_app` | admin+ | Delete |
| `get_app_members` / `set_app_members` | admin+ / all (read) | Member list |
| `get_app_services` / `set_app_services` | admin+ / all (read) | Which services an app may use |
| `list_app_files` | all | `{nonce, area: web\|data\|dev\|staging\|production}` |
| `search_app_files` | all | Grep app files (regex, `ignore_case`, `area`) |
| `read_app_file` | all | Read one file; `transport: inline` or `http` (act-token URL) |
| `write_app_file` | admin+ (or web dir member) | `transport: inline` or `http`; also `{mode, paths}` for chmod/rm-style ops |
| `delete_app_file` | admin+ | Delete |

**Service tools** (`registerServiceTools`): `list_services`, `get_service`,
`create_service`, `update_service`, `delete_service`, `get_service_apps`,
`set_service_members`, `list_service_files`, `read_service_file`,
`write_service_file`, `delete_service_file` — services = OAuth/MCP/HTTP
integrations with descriptors (`github`, `tasks`, `virtual`, …). Writes
admin+ or service members — except task services, which only a Superuser
may create, change, read or delete.

**Auth-record tools** (`registerAuthTools`): `list_auth`, `create_auth`,
`update_auth`, `delete_auth` — the login methods/credential records apps and
services point at (`protected_by` on apps, `acts_as` on services). Delete is
refused while referenced.

**User & self tools** (`registerUserTools`, admin+): `list_users`, `get_user`,
`create_user`, `update_user`, `delete_user`, `get_user_apps`, `set_user_apps`,
`get_user_ssh_key`, `generate_user_ssh_key`, `delete_user_ssh_key`,
`create_user_passphrase_link`; and self-serve: `get_me`, `update_me`,
`get_my_ssh_key`, `generate_my_ssh_key`, `delete_my_ssh_key`.

**Other tools**: `list_roles`, `list_audit`, `get_guide`
(guides: `publishing`, `services`, `ssh`, `tasks`, `virtuals`),
`get_settings`/`update_settings` (superuser; incl. `mcp_database_mode`).

**Database tools** (`registerDatabaseTools`): `db_list_databases`, `db_schema`,
`db_query` (target: `''` per-app / `global` / `app:<nonce>`; `db_execute` needs
`mcp_database_mode: full-access`).

### 4b. Virtual/task service MCP — `/mcp/{name}` (+ PRM)

One Streamable HTTP handler dispatches by slug:

- `/mcp/<slug>` — *virtual* service: each tool step in the virtual definition
  becomes an MCP tool (`frbr-<name>`, server instructions "Virtual service").
  Supports SEP-2322 elicitation suspend/resume (`RequestState` retry,
  `/api/elicitation/{id}` completion callback); `!`-app-only tools carry MCP Apps
  `ui.visibility: [app]` metadata.
- `/mcp/app:<nonce>` (or bare `/mcp/app:` with `X-App-Nonce`) — the *app's own*
  virtual service, without registering one.
- Task services: one tool per task; args from `TASK_<NAME>` script vars.

Auth per gate (protected_resource_metadata): anonymous mounts advertise
nothing; otherwise PRM points at Fresh Breath's own authorization server.
Caller credentials are unwrapped at the server (browser never touches them).

### 4c. Elicitation

| Route | Method | Description |
|---|---|---|
| `/api/elicitation/{id}` | GET/POST | Public completion callback for URL/form elicitation steps; rides the SSE session back to the MCP caller |

## 5. OAuth authorization server (`oauth_server.go`)

Fresh Breath acts as the auth server for MCP clients (CLI tools, harnesses)
and for the browser control panel.

| Route | Method | Description |
|---|---|---|
| `/.well-known/oauth-authorization-server` | GET | Server metadata (`authorization_code` + `refresh_token` grants, PKCE, DCR) |
| `/.well-known/oauth-authorization-server/…` | GET | Resource-specific path variant |
| `/oauth/register` | POST | Dynamic client registration |
| `/oauth/authorize` | GET | Authorization endpoint (starts the leg-driven login flow) |
| `/oauth/token` | POST | Token exchange. Grants: `authorization_code` (code + PKCE verifier, sends refresh in body), `refresh_token` (form body **or** HttpOnly `refresh_token` cookie) |
| `/oauth/token/{authID}` | POST | Same handler; cookie refresh is scoped per auth record so multiple browser records don't share one cookie slot |
| `/oauth/jwks` | GET | Signing keys for Fresh Breath tokens |

Refresh kinds (dispatch on refresh-data `Kind`): `"wrapped"` — refresh the
upstream provider and re-wrap; `"identity"` — re-mint the identity token.
Refresh tokens may arrive in the form body (CLI/MCP) or the HttpOnly
`refresh_token` cookie (browser).

## 6. Admin REST API (`authWrap` + role pipeline)

### Apps

| Route | Method | Role | Description |
|---|---|---|---|
| `/api/apps` | POST | any | Create app `{name, environment, url?, owner_id?, protected_by?}` → `{nonce, name}` |
| `/api/apps` | GET | any | List apps (member/read-only: their apps) |
| `/api/apps/{nonce}` | GET/PUT/DELETE | any | Detail / update (204) / delete (204) |
| `/api/apps/{nonce}/members` | GET/PUT | any | App member list |
| `/api/apps/{nonce}/services` | GET/PUT | any | Approved services for the app |
| `/api/apps/{nonce}/web` | GET/POST/DELETE, ±`?file=path` | any canAccessApp | Web files. Bare GET = zip download; POST = multipart upload (100 MB cap); `?file=` = single-file GET/PUT/DELETE |
| `/api/apps/{nonce}/data` | GET/POST/DELETE, ±`?file=path` | any canAccessApp | Private (unhosted) files; GET served with `CSP: sandbox`, `nosniff` |
| `/api/apps/{nonce}/service/files` or `/tools` | GET etc. | any canAccessApp | The app's own virtual service (files = definition; tools = summaries) |
| `/api/apps/{nonce}/db` | GET/POST | any-auth (gate is `gateDBTarget`) | List / query the app's databases |
| `/api/apps/{nonce}/deploy` | POST | any canAccessApp | `{source: dev\|staging, target: staging\|prod}` → copy slot, returns route |
| `/api/apps/search` | GET | any | `?pattern=<regex>&nonce=&area=web\|data&ignore_case=` |

### Services

| Route | Method | Description |
|---|---|---|
| `/api/services` | GET/POST | List / create (descriptors per kind; core gates writes) |
| `/api/services/{id}` | GET/PUT/DELETE | Detail / update / delete |
| `/api/services/{id}/apps` | GET | Apps using this service |
| `/api/services/{id}/members` | GET/PUT | Users trusted with the service file |
| `/api/services/{id}/tools` | GET | Tool summaries |
| `/api/services/{id}/files` | GET/POST/PUT/DELETE | GET = txt download; POST = upload (50MB per file); PUT = full replace; DELETE = clear |

### Auth records / users / etc.

| Route | Method | Role | Description |
|---|---|---|---|
| `/api/auth` | GET/POST | any (write core-gated) | Auth records (login methods & credentials) |
| `/api/auth/{id}` | GET/PUT/DELETE | any (write core-gated) | Detail / update / delete |
| `/api/users` | GET/POST | admin+ | List (with app links) / create |
| `/api/users/{id}` | GET/PUT/DELETE | admin+ | Detail / update / delete |
| `/api/users/{id}/apps` | GET/PUT | admin+ | App access links |
| `/api/users/{id}/passphrase-link` | POST | admin+ | Mint invite/reset link `{url, expires_at}` |
| `/api/users/{id}/ssh-key` | GET/POST/DELETE | admin+ | Manage another user's SSH key |
| `/api/roles` | GET | any | List roles |
| `/api/audit` | GET | any | Last 100 audit entries (member/read-only: own) |
| `/api/me` | GET/PUT | any | Current user / update profile |
| `/api/me/ssh-key` | GET/POST/DELETE | any | Own SSH key |
| `/api/me/sessions` | GET/DELETE | any | Refresh-token families; DELETE revokes all |
| `/api/me/sessions/{id}` | DELETE | any | Revoke one session |
| `/api/settings` | GET/PUT | superuser | `admin_auth_service`, `default_app`, `mcp_database_mode` |
| `/api/onboarding` | GET/POST | superuser (open in setup mode) | Needed? / `POST {name, email, passphrase}` = first superuser |
| `/api/onboarding/skip` | POST | superuser | Skip onboarding |
| `/api/tls` | POST | superuser | Local HTTPS setup |
| `/api/tls/ca.pem` | GET | superuser | Local CA cert |

### Global databases (alias mount)

| Route | Method | Description |
|---|---|---|
| `/api/db` | GET | List databases |
| `/api/db/query` | POST | Statement or batch (`{sql?, params?, batch?}`) |
| `/api/db/watch?db=name` | GET | SSE change feed (forward-only, no replay) |
| `/api/db/{name}` | DELETE | Drop database (admin+ via `gateDBTarget`) |

## 7. Act tokens — `/api/act/{ticket}`

Anonymous mount that dispatches to another route after verifying a
capability ticket (MCP `transport: "http"` file transfers use this). Ticket
is single-tense: bound to one method, path, and user; `authWrap` steps aside
once the handler stashes the verified user in context.

## 8. SSH & Git-over-SSH (gate: `ssh` service access)

| Route | Method | Description |
|---|---|---|
| `/api/ssh/sessions` | POST | Open session `{host, port=22, username=email}` → `{sessionId, expiresAt}` |
| `/api/ssh/sessions/{id}` | GET/DELETE | Inspect / close; 401 step-up when agent key lapsed |
| `/api/ssh/known-hosts` | GET | TOFU host-key list |
| `/api/ssh/known-hosts/{host}:{port}` | DELETE | Forget a host key (for changed keys) |
| `/api/ssh/git/sign` | POST | SSH-sign commits `{commits: [...]}` → armored SIGNATURE + GitHub format |
| `/api/ssh/git/branches` | POST | `{url}` → ls-remote refs |
| `/api/ssh/git/pull` | POST | `{url, branch?, paths?}` → snapshot (no paths = tree listing) |
| `/api/ssh/git/commit` | POST | Fused sign + commit + push |

## 9. File sync over SFTP (gate: `ssh` service access)

| Route | Method | Description |
|---|---|---|
| `/api/sync/files?sessionId=…&path=…&recursive=1` | GET | Remote directory listing (hidden files skipped) |
| `/api/sync/files/diff` | POST | `{sessionId, basePath?, files: [{path, hash}]}` → `{upload: [...], delete: [...]}` (client compares hashes) |
| `/api/sync/files/{path…}?sessionId=…` | GET/PUT/DELETE | Download / upload (`X-Hash` SHA-256 verified after write) / delete remote file |

## 10. Remote update feeds (`handler_updates.go`)

| Route | Method | Auth | Description |
|---|---|---|---|
| `/api/updates` | GET/POST | admin+ | List feeds / create |
| `/api/updates/{id}` | PUT/DELETE | admin+ | Update / delete a feed |
| `/api/updates/{id}/build` | POST | admin+ | Publish mode |
| `/api/updates/{id}/archive` | GET | admin+ | Download update archive |
| `/api/updates/check` | GET | **anonymous** (rate-limited) | `coreCheckAllUpdateFeeds` — apps pull their own updates |
| `/api/updates/apply` | POST | **anonymous** (rate-limited) | `{ids?}`; SSE progress while applying |

`/api/updates/check` and `/api/updates/apply` are deliberately mounted bare —
the point is that a hosted app can check/apply without holding a role token.

## 11. Related HTTP-facing machinery

- **`rebuildHostedRoutes()`** — re-registers app slugs on create/update/delete;
  routes are maps, not static mux entries.
- **Rate limiting** — fixed-window counters for anonymous update endpoints.
- **Uploads** — global max 100 MB (`uploadMaxBytes`) per app-file request;
  service files 50 MB per form; sync downloads 44 MB.
- **Audit** — `store.LogAudit` is called for most mutating actions; events
  visible at `/api/audit`.

## 12. SDK / library versions

- MCP Go SDK: `github.com/modelcontextprotocol/go-sdk/mcp`
  (`mcp.NewStreamableHTTPHandler`, OAuth-protected-resource handlers, MOBN
  apps middleware; Streamable HTTP, stateless central / session-tracked
  virtual mounts).
- OIDC: `github.com/coreos/go-oidc/v3/oidc` for provider handshakes.

## 13. Where to look

| Concern | File |
|---|---|
| Route definitions | `internal/server/handler.go` (SetupRoutes) |
| CORS/origin gate | `handler.go` (ServeHTTP, originAllowed) |
| Login/leg flows + browser pages | `auth_flows.go` |
| OAuth server (RFC endpoints) | `oauth_server.go` |
| Central MCP tool definitions & role registration | `mcp_central.go` |
| Database tools | `mcp_db.go` |
| Virtual/task MCP mounts & elicitation | `mcp_endpoint.go`, `elicitation.go` |
| App/service/user core (`core*` functions, role gates) | `admin_core.go`, `updates_core.go`, `appdb.go` |
| Act tokens | `act_token.go` |
| SSH/git/sync handlers | `git.go`, `sync.go`, `sshkit/` |
| Token formats & sealing | `internal/db/*`, `internal/formats/*` |
