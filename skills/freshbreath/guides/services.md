# Services Guide for Fresh Breath Apps

Fresh Breath provides simple JavaScript calls for authenticating users and
calling third-party APIs from your HTML/JS apps.

In order to use these calls, you must have an app setup:

- If the app doesn't exist in your list, create one - an admin will need to do this.
  Hang on to the *nonce* - it's used throughout this guide.

- Add the necessary services to the app - if it needs storage providers, OIDC,
  SSH access, or any other third-party integrations (MCPs or APIs), they can
  be added from the control panel or MCP. The important thing to hang on to
  here is the *service URL*.

The following service types are available:

- api: URL to an API service, along with some hints about how to log-in.
  Use the `fetch` call (#6 below) to access these.

- mcp: URL to a public MCP server. Use `callTool` and `listTools` from JS.

- oidc: URL to a public OIDC server. Login only — it proves *who* the user is
  and nothing more. Fresh Breath verifies the provider login, then issues its
  own identity token (it does **not** hand back the provider's access token, so
  you can't call the provider's API through an oidc service — use an `api` or
  `mcp` service for that). Fresh Breath refreshes that identity token locally,
  so the user doesn't have to keep checking back in with the provider.

- tasks: A set of custom tools (use `callTool` and `listTools` from JavaScript)
  that can be called and are wrappers for shell scripts that perform system
  functions. (For instance, you could upload a file to a tool that can move
  the file onto a network share for processing.) One key note about tasks:
  they have no auth of their own, so the `login` function shouldn't be used -
  instead, the `authService` property to the `ServiceProxy` constructor can
  receive another `ServiceProxy` object for any service that it depends on. (See
  the 'tasks' guide for details on writing the file content - the tool scripts -
  and calling the tools from an app.)

- virtual: These are custom MCP endpoints that can be used to provide tools
  (also available through `callTool` and `listTools`) that wrap API calls -
  to give a nice MCP interface with discoverable auth. (See the 'virtuals'
  guide for more on how to write these tool scripts.)

- ssh: Every Fresh Breath server has a single SSH service (URL: `ssh://`)
  that can be used for general authentication or for connecting to remote
  machines. Honestly, this is a great option for managing passwords
  directly from Fresh Breath.

If you aren't an admin, you also must be listed as the owner or member of the
app. (You don't have to be a member to use the app, just to maintain it.)

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

For `file://` apps, use the full server URL (e.g. `https://localhost:9009/...`).
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
- For most services: opens a popup, returns when the user has logged in.
- If a default API key is set: no popup, resolves immediately
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
- Sets any auth headers
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
service.data.claims         // { sub, email, name, ... } — provider profile, for display
service.data.id_token       // Fresh Breath identity token (a JWT issued by Fresh Breath)
service.data.access_token   // same value as id_token
```

Unlike `api`/`mcp` services, an OIDC service does **not** expose the provider's
own access or refresh token — `access_token` and `id_token` are one and the
same Fresh Breath-issued identity JWT, and it auto-refreshes locally (no
`token_endpoint` / `scopes` to manage). So there's no provider token to call
the provider's API with; an oidc service is purely for login.

Identity proxies cannot use `listTools()` / `callTool()` / `connect()` — they're
for authentication only, not MCP. Use `.data.claims` to get user info, and pass
`.data.id_token` to your own backend (verify it against Fresh Breath, not the
provider — it's a Fresh Breath JWT).

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
