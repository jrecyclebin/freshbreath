# Authentication Guide for Fresh Breath Apps

Fresh Breath provides simple JavaScript calls for authenticating users and
calling third-party APIs from your HTML/JS apps. This guide covers the main
authentication flows and API calls you'll use in a Fresh Breath app.

---

## 1. Load frbr.js

Every Fresh Breath app starts with a module import:

```html
<script type="module">
  import { login, ServiceProxy } from "[Fresh Breath URL]/frbr.js?[Your App Nonce]";
  // ...
</script>
```

This can also be done as a script tag - for example, if your Fresh Breath server
is running HTTPS locally on port 9009:

```html
  <script type="module" src="https://localhost:9009/frbr.js?[Your App Nonce]"></script>
```

You can then access `login` and `ServiceProxy` from the global `window.FreshBreath` object:

```js
const { login, ServiceProxy } = window.FreshBreath;
```

For `file://` apps, use the full server URL (`http://localhost:9009/...`).
For hosted apps on the same origin as the server, `/frbr.js` works fine.

---

## 2. login()

Opens an OAuth popup (or skips it for API key access) and returns a `ServiceProxy`.

```js
const service = await login("https://mcp.example.com/mcp"); // registered service URL
```

If you need more control, you can pass an options object instead of a string:

```js
const service = await login({
  serviceURL: "https://mcp.example.com/mcp",  // required — registered service URL
  state: "optional-custom-state",  // optional — defaults to crypto.randomUUID()
  apiKey: "sk-...",             // optional — only for key-auth services
});
```

**Returns:** `Promise<ServiceProxy>`

**Flow:**
- For OAuth/OIDC/SSH services: opens a popup, waits for `postMessage` with `type: "auth-complete"`
- For key-auth services: no popup, resolves immediately
- Throws `"Popup closed"` if the user closes the window early

**After login**, persist the session:
```js
localStorage.setItem("my-auth-key", service.toJSON());
```

If you are storing several services, you can key by service URL.

---

## 3. Restore a Session (ServiceProxy from localStorage)

On page load, restore a prior session without re-prompting:

```js
const saved = localStorage.getItem("my-auth-key");
service = ServiceProxy.fromJSON(saved);
```

For more custom needs, here's the full `ServiceProxy` constructor:
`new ServiceProxy({ serviceURL, serviceID, data, apiKey, apiHeader, proxied })`

---

## 4. Auto-Refresh Tokens

Generally, you just want all of your services to auto-refresh and persist new
tokens when they do. To do that, listen for the `refresh` event on the
`ServiceProxy`:

```js
ServiceProxy.on('refresh', svc => {
  console.log("Access token refreshed.");
  localStorage.setItem("my-auth-key", svc.toJSON()); // or key by service URL,
                                                     // if you have several
});
```

You can set this up right after ServiceProxy is imported, so that it'll apply
to all the services you create.

---

## 5. listTools() and callTool()

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

Non-MCP ServiceProxy objects won't work with these methods.

---

## 6. fetch()

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

## 7. Accessing Tokens Directly

The full token payload lives on `service.data`:

```js
service.data.access_token   // Bearer token string
service.data.refresh_token  // Refresh token string
service.data.token_type     // "Bearer" or similar
service.data.token_endpoint // Token refresh URL
service.data.expires_at     // Expiration date for this token
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

## 8. Full Minimal Example

```html
<!DOCTYPE html>
<html>
<body>
  <button id="login">Connect</button>
  <pre id="out"></pre>

  <script type="module">
    import { login, ServiceProxy } from "https://localhost:9009/frbr.js?your-app-nonce-here";

    const SERVICE_URL = "https://mcp.example.com/mcp";
    const AUTH_KEY   = "my-app-auth";

    let service = null;
    ServiceProxy.on('refresh', svc => {
      console.log("Token refreshed.");
      localStorage.setItem(AUTH_KEY, svc.toJSON());
    });

    async function startLogin() {
      service = await login(SERVICE_URL);
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
        service = new ServiceProxy(JSON.parse(saved));
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
  it's a 48-char hex string. Use it in the URL for frbr.js.
- **Service URL** must match a service registered in that app on the server.
  Exact URL match — trailing slashes matter.
- **`file://` apps**: the server must be running and CORS must allow `null` origin
  (Fresh Breath echoes back the `Origin` header, so `file://` origin works fine).
  If a service gives you CORS errors, you can turn the `proxies` setting on in
  the Fresh Breath admin panel to route all calls through the server.
- **Token storage**: all tokens can live in `localStorage`. Nothing is stored server-side
  after the OAuth exchange. The server's `client_secret` is never sent to the browser.
- **Sign out**: `localStorage.removeItem("my-auth-key"); service = null;`
