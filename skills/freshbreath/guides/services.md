# Services Guide for Fresh Breath Apps

Fresh Breath gives your HTML/JS app one JavaScript verb for authentication and a
small proxy object for everything after it. There is no session plumbing to
write: the library owns the store, the refresh, and the sharing between apps.

To use any of it you need an app registered on the server:

- If the app isn't in your list, create one — an admin will need to do this.
  Hang on to the *nonce*; it's used throughout this guide.

- Add the services the app needs, from the control panel or over MCP. The thing
  to hang on to here is the *service URL*.

If you aren't an admin you must also be the owner or a member of the app. (You
don't have to be a member to *use* the app, just to maintain it.)

## Service types

- **api** — an HTTP API. Call it with `fetch` (§5).
- **mcp** — an MCP server. Call it with `listTools` / `callTool` (§4).
- **tasks** — custom tools wrapping shell scripts, for system work: upload a
  file to a tool that moves it onto a network share, that sort of thing. Called
  with `listTools` / `callTool` like any MCP service. (See the 'tasks' guide for
  writing the scripts.)
- **virtual** — custom MCP endpoints you define, wrapping API calls or SQL
  queries. Also `listTools` / `callTool`. (See the 'virtuals' guide.)
- **ssh** — every server has exactly one SSH service (URL `ssh://`) for
  connecting to remote machines.

## How auth works now

A service no longer carries its own login configuration. Two slots do that work,
and both point at an **auth record** — a credential or login method standing on
its own, shared by everything that names it:

- **Protected by** — who may call in. Empty means *inherit the admin record*,
  not *open*; only an explicit Anonymous record means open.
- **Acts as** — what credential goes upstream. Empty means *the caller's own*.

Two consequences worth internalizing, because they're what make `login()` so
quiet in practice:

1. **The door owns the gate.** A service reached through your app answers to
   *your app's* gate, not its own. So clearing the app's gate is most of what
   any login does.
2. **Credentials are shared by gate.** Two apps behind the same record find the
   same stored credential, and the second one prompts zero times.

---

## 1. Load frbr.js

Every Fresh Breath app starts with a module import:

```html
<script type="module">
  import { login } from "[Fresh Breath URL]/frbr.js?[Your App Nonce]";
  // ...
</script>
```

Or as a plain script tag — say your server is on HTTPS port 9009:

```html
<script type="module" src="https://localhost:9009/frbr.js?[Your App Nonce]"></script>
```

which puts everything on `window.FreshBreath` (aliased `window.FrBr`):

```js
const { login, currentSession, signOut } = window.FreshBreath;
```

For `file://` apps use the full server URL. For apps hosted on the server's own
origin, `/frbr.js` is enough.

---

## 2. login()

One verb, every service.

```js
const svc = await login("https://mcp.example.com/mcp");  // a registered service URL
```

It takes a string and nothing else. Before it prompts for anything it asks the
server what this door actually requires, then looks in the store for a
credential that satisfies it. So:

- An anonymous service resolves instantly, with no popup and no session.
- A service behind a gate you already cleared — in this app or another one —
  resolves with no popup either.
- Only a genuinely missing credential opens a window.

**Call it from a click.** A popup with no user gesture behind it is a popup the
browser blocks. `login` never opens one on its own for this reason: when a
session lapses beyond recovery you get a `SessionExpired`, and offering the
re-login is your call to make at a moment the user is looking.

Called with **no argument**, it clears your app's own gate and returns the
`AuthSession` rather than a proxy — the "sign in" verb for a gated app:

```js
await login();   // the app's gate, nothing else
```

You rarely need it. Logging in to any service clears the app's gate on the way
past, because the app's gate is the first leg of that login.

**Throws:** `"Login window closed"` if the user gives up, `"The login window was
blocked"` if there was no gesture behind the call.

---

## 3. Sessions

`svc.session` is the `AuthSession` behind a proxy — `null` for an anonymous
service. One session exists per auth record, shared by every proxy riding it, so
a refresh on one heals them all.

```js
svc.session.subject     // "frbr:3" for a known user, "ext:github:12345" otherwise
svc.session.kind        // "oidc" | "oauth2" | "api_key" | "ssh_key"
svc.session.provider    // "github" — the upstream slug, when there is one
svc.session.expiresAt   // Date
```

`subject` is the honest answer to "who is this?" — display it, or send it to
your own backend.

**Persistence is not your problem.** The library stores one entry per cleared
record under `localStorage["frbr:auth:<id>"]`, writes on login, rewrites on every
refresh, and evicts when the server refuses one. There is nothing to serialize,
no event to subscribe to, and no key for your app to choose. Don't write to
`frbr:auth:*` yourself.

To ask whether you're already signed in — at boot, before any network call:

```js
const session = currentSession();   // null when the store holds nothing live
```

To sign out:

```js
signOut();   // forgets this app's gate
```

What's stored is always a Fresh Breath token, never a provider's. Upstream
credentials ride sealed inside it and are unsealed by the server on the way out,
so the worst thing readable out of localStorage is a token that expires.

---

## 4. listTools() and callTool()

For MCP, tasks, and virtual services:

```js
const tools = await svc.listTools();
// Array<{ name, description, inputSchema }>

const result = await svc.callTool("tool_name", { arg1: "value", arg2: 42 });
```

Both connect on first use, refresh a credential that's near expiry, and retry
once on a 401. `callTool` parses a JSON result for you and throws on a tool
error, so what you get back is the value, not an envelope.

Pass a `File` or `Blob` as any argument and the call goes out as multipart:

```js
await svc.callTool("ingest", { file: fileInput.files[0], label: "invoices" });
```

---

## 5. fetch()

For HTTP services, `svc.fetch()` wraps the standard `fetch`:

```js
const res  = await svc.fetch("/v1/users");
const data = await res.json();

await svc.fetch("/v1/items", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ name: "thing" }),
});
```

It stamps `X-App-Nonce`, attaches the session's credential, and retries once
after a refresh on a 401. For proxied services (and any `file://` app) it routes
through `/service/{id}/{path}`, where the server decides what actually goes
upstream — your stored key, your sealed provider token, or nothing. The path is
relative to the service's registered URL; the leading slash is optional.

---

## 6. Full minimal example

```html
<!DOCTYPE html>
<html>
<body>
  <button id="connect">Connect</button>
  <pre id="out"></pre>

  <script type="module">
    import { login, currentSession, SessionExpired }
      from "https://localhost:9009/frbr.js?your-app-nonce-here";

    const SERVICE_URL = "https://mcp.example.com/mcp";
    let svc = null;

    async function showTools() {
      const tools = await svc.listTools();
      document.getElementById("out").textContent =
        tools.map(t => `${t.name}: ${t.description}`).join("\n");
    }

    async function connect() {
      svc = await login(SERVICE_URL);
      await showTools();
    }

    document.getElementById("connect").onclick = () =>
      connect().catch(e => { document.getElementById("out").textContent = e.message; });

    // Already signed in from an earlier visit — or from a sibling app behind
    // the same gate? Then this needs no popup and no click.
    if (currentSession()) {
      connect().catch(e => {
        // A lapsed session is not an error to shout about: the button is
        // still there, and clicking it is the fix.
        if (!(e instanceof SessionExpired)) console.error(e);
      });
    }
  </script>
</body>
</html>
```

---

## Notes

- **App nonce** comes from `/api/apps` — a 48-char hex string. Put it in the
  frbr.js URL.
- **Service URL** must match a service registered *and linked to your app*.
  Exact match; trailing slashes matter.
- **`file://` apps**: the server must be running, and CORS must allow the `null`
  origin (Fresh Breath echoes the `Origin` header back, so it does). If a
  service gives you CORS errors, switch it to proxied in the control panel and
  every call routes through the server instead.
- **Sharing the store**: apps served from the Fresh Breath origin share one
  store, which is what lets a second app skip the prompt. Apps served from their
  own origins don't, and will prompt separately.
- **Client secrets** never reach the browser. Token exchange and refresh run on
  the server, and refresh tokens live in HttpOnly cookies your code can't read.
