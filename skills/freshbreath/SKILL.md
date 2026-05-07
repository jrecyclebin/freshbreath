# Fresh Breath

*Adding Integrations to Static HTML Apps*

Fresh Breath is a lightweight auth + MCP proxy server. It handles
OAuth flows, token exchange, and MCP proxying so that static HTML apps — even
`file://` apps — can connect to third-party services without exposing secrets.

This skill covers adding service integrations to apps that use Fresh Breath.

---

## 1. Load env.js and setup.js

Every Fresh Breath app starts with two script tags (or module imports):

```html
<!-- Injects window.__HOMESLICE_CONFIG with { apiBase, authRequired, authServiceName } -->
<script src="http://your-freshbreath-server/env.js"></script>

<!-- Then import the SDK as an ES module -->
<script type="module">
  import { login, ServiceProxy } from "http://your-freshbreath-server/setup.js";
  // ...
</script>
```

For `file://` apps, use the full server URL (`http://localhost:8080/...`).
For hosted apps on the same origin as the server, `/env.js` and `/setup.js` work fine.

`env.js` must be loaded **before** `setup.js` — the SDK reads `window.__HOMESLICE_CONFIG`
at module evaluation time to find the API base URL.

---

## 2. login()

Opens an OAuth popup (or skips it for API key access) and returns a `ServiceProxy`.

```js
const service = await login({
  appNonce: "your-app-nonce",   // required — app identifier from /api/apps
  serviceURL: "https://mcp.example.com/mcp",  // required — registered service URL
  state: "optional-custom-state",  // optional — defaults to crypto.randomUUID()
  apiKey: "sk-...",             // optional — only for key-auth services
});
```

**Returns:** `Promise<ServiceProxy>`

**Flow:**
- For OAuth/OIDC services: opens a popup, waits for `postMessage` with `type: "auth-complete"`
- For key-auth services: no popup, resolves immediately
- Throws `"Popup closed"` if the user closes the window early

**After login**, persist the session:
```js
localStorage.setItem("my-auth-key", service.toJSON());
```

---

## 3. Restore a Session (ServiceProxy from localStorage)

On page load, restore a prior session without re-prompting:

```js
const saved = localStorage.getItem("my-auth-key");
if (saved) {
  const parsed = JSON.parse(saved);
  const service = new ServiceProxy({ appNonce: APP_NONCE, ...parsed });
  // Use service normally — token refresh happens automatically
}
```

`ServiceProxy` constructor: `new ServiceProxy({ appNonce, serviceURL, serviceID, data, apiKey, proxied })`

---

## 4. listTools() and callTool()

For MCP services, after login:

```js
// List all available tools
const tools = await service.listTools();
// tools: Array<{ name: string, description: string, inputSchema: object }>

// Call a tool
const result = await service.callTool("tool_name", { arg1: "value", arg2: 42 });
// result: { content: [...], isError?: boolean }
```

Both methods:
- Auto-connect on first call (no need to call `connect()` manually)
- Auto-refresh the token if it's expired or near expiry (5-minute buffer)
- Auto-retry once on 401

`callTool` throws if the MCP server returns an error — check `result.isError` for
soft tool-level failures vs. thrown errors for transport/auth failures.

Do **not** call `listTools()` or `callTool()` on an OIDC identity proxy — use
`.data.claims` instead (see §6).

---

## 5. fetch()

For REST/HTTP services, `service.fetch()` wraps the standard `fetch` API:

```js
// GET
const res = await service.fetch("/v1/users");
const data = await res.json();

// POST with body
const res = await service.fetch("/v1/items", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ name: "thing" }),
});
```

**What it does automatically:**
- Sets `X-App-Nonce` header
- Sets `Authorization: Bearer <apiKey>` for key-auth services
- For proxied services (or `file://` apps): routes through `/service/{serviceID}/{path}`
- For non-proxied same-origin calls: hits the service URL directly

The `path` parameter is relative to the service's registered URL (leading slash optional).

---

## 6. Accessing Tokens Directly

The full token payload lives on `service.data`:

```js
service.data.access_token   // Bearer token string
service.data.refresh_token  // Refresh token string
service.data.token_type     // "Bearer" or similar
service.data.token_endpoint // Token refresh URL
service.data.expires_in     // Seconds until expiry (from last refresh)
service.data.scopes         // Space-separated scopes string
service.data.client_id      // OAuth client ID
```

For OIDC identity services (login via an identity provider like Google):

```js
service.isIdentity          // true if this is an IdP login
service.data.claims         // { sub, email, name, ... } — OIDC user claims
service.data.id_token       // Raw JWT id_token
```

Identity proxies cannot use `listTools()` / `callTool()` / `connect()` — they're
for authentication only, not MCP. Use `.data.claims` to get user info and
`.data.id_token` to pass to your own backend.

---

## 7. Full Minimal Example

```html
<!DOCTYPE html>
<html>
<head>
  <script src="http://localhost:8080/env.js"></script>
</head>
<body>
  <button id="login">Connect</button>
  <pre id="out"></pre>

  <script type="module">
    import { login, ServiceProxy } from "http://localhost:8080/setup.js";

    const APP_NONCE  = "your-app-nonce-here";
    const SERVICE_URL = "https://mcp.example.com/mcp";
    const AUTH_KEY   = "my-app-auth";

    let service = null;

    async function startLogin() {
      service = await login({ appNonce: APP_NONCE, serviceURL: SERVICE_URL });
      localStorage.setItem(AUTH_KEY, service.toJSON());
      showTools();
    }

    async function showTools() {
      const tools = await service.listTools();
      document.getElementById("out").textContent =
        tools.map(t => `${t.name}: ${t.description}`).join("\n");
    }

    // Restore on load
    const saved = localStorage.getItem(AUTH_KEY);
    if (saved) {
      try {
        service = new ServiceProxy({ appNonce: APP_NONCE, ...JSON.parse(saved) });
        showTools().catch(() => {
          localStorage.removeItem(AUTH_KEY);
          service = null;
        });
      } catch { localStorage.removeItem(AUTH_KEY); }
    }

    document.getElementById("login").onclick = startLogin;
  </script>
</body>
</html>
```

---

## Notes

- **App nonce** comes from the `/api/apps` endpoint on the Fresh Breath server —
  it's a 48-char hex string. Set it in a constant at the top of your app.
- **Service URL** must match a service registered in that app on the server.
  Exact URL match — trailing slashes matter.
- **`file://` apps**: the server must be running and CORS must allow `null` origin
  (Fresh Breath echoes back the `Origin` header, so `file://` origin works fine).
  If a service gives you CORS errors, you can turn the `proxies` setting on in
  the Fresh Breath admin panel to route all calls through the server.
- **Hosted apps**: load `/env.js` and `/setup.js` from the same origin as the server
  to avoid CORS preflight on every call.
- **Token storage**: all tokens live in `localStorage`. Nothing is stored server-side
  after the OAuth exchange. The server's `client_secret` is never sent to the browser.
- **Sign out**: `localStorage.removeItem("my-auth-key"); service = null;`
