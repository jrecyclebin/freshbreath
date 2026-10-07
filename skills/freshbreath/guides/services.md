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

## How auth works

A service has two auth slots that point at an **auth record** — a credential or
login method standing on its own, shared by everything that names it:

- **Protected by** — who may call in. Empty means *inherit the admin record*,
  not *open*; only an explicit Anonymous record means open.
- **Acts as** — what credential goes upstream. Empty means *the caller's own*.
  **mcp** services have no acts-as: the MCP server decides. An open server
  needs nothing; an OAuth one (Notion, say) is signed in to as a step of
  `login()` for that service's URL, and works only when the service is
  proxied. Behind an open app that login is optional — the app runs without
  it and connects when the user asks.

Two consequences worth stopping at, because they're what make `login()` so
quiet in practice:

1. **The door owns the gate.** A service reached through your app answers to
   *your app's* gate, not its own. So clearing the app's gate is most of what
   any login does.
2. **Credentials are shared by gate.** Two apps behind the same record find the
   same stored credential, and the second one prompts zero times.

---

## 1. Load frbr.js

Every Fresh Breath app starts with a module import of `frbr.js`.

```html
<script type="module" src="/frbr.js"></script>
```

frbr.js figures out which app it is from where the page is hosted (the browser
sends a `Referer`, or frbr.js tells the server its location on load).

If you are writing a static HTML app (`file://` URL), then you'll need to
manually include your app nonce in the URL. (Or on pages that set
`referrerpolicy="no-referrer"`.)

```html
<script type="module" src="/frbr.js?[Your App Nonce]"></script>
```

Either way puts everything on `window.FreshBreath` (aliased `window.FrBr`):

```js
const { api, login, currentSession, signOut } = window.FreshBreath;
```

For `file://` apps use the full server URL with the nonce; the server can't
infer the app from a `file://` page. For apps hosted on the server's own
origin, `/frbr.js` is enough.

### The page gate

If your app is hosted on the server and has a gate, the server enforces it
before serving anything — HTML and assets alike. A browser that hasn't
cleared the gate is sent through the login and back to the page it asked for.

This is the gate pass: an HttpOnly cookie listing active logins, active
tokens. It is not a credential — API calls still carry the token from the
store — and your app never touches it.

### Auto-login at load

The page gate normally fills the store on its way in, but the store can still
come up empty (cleared site data, say). So loading frbr.js is itself a login
event: when the store holds no credential for the gate, the
page navigates into the login and comes back with one — before your first
frame draws.

A lapsed credential still counts as holding one (the refresh family can
revive it silently), so only a first visit redirects — and a login that comes
back empty-handed trips a once-per-session guard instead of a redirect loop.
`file://` apps never auto-login: the hand-back runs through localStorage, which
they cannot reach.

To keep the decision in app code, set the flag before the script loads:

```html
<script>window.__FRBR_AUTO_LOGIN = false</script>
<script type="module" src="/frbr.js"></script>
```

---

## 2. login()

One verb, every service.

```js
const svc = await login("https://mcp.example.com/mcp");  // a registered service URL
```

Before it prompts for anything it asks the
server what this door actually requires, then looks in the store for a
credential that satisfies it. So:

- An anonymous service resolves instantly, with no popup and no session.
- A service behind a gate you already cleared — in this app or another one —
  resolves with no popup either.
- Only a genuinely missing credential opens a window.

**Call it from a click.** A popup with no user gesture behind it is a popup the
browser blocks.

A second argument says how hard to try:

- `"normal"` (default, can also use `true`): Used a stored cred or pop a log-in.
- `"silent"` (or `false` here works): Spends a stored credential or returns `null`. Never prompts — quick boot-time check.
- `"fresh"`: Force a log-in.

**When a login dies mid-session**, the underlying `api()` and `ServiceProxy`
infrastructure handle it. No problem if the gate has changed, that code will
check for that as well.

Called with **no argument**, it clears your app's own gate and returns the
`AuthSession` rather than a proxy — the "sign in" verb for a gated app:

```js
const session = await login();   // the app's gate, nothing else
```

You rarely need it. Logging in to any service clears the app's gate on the way
past, because the app's gate is the first leg of that login.

**Throws:** `"Login window closed"` if the user gives up, `"The login window was
blocked"` if there was no gesture behind the call.

### resolveDoor()

To ask what a login would cost without starting one:

```js
const door = await resolveDoor();                 // this app's gate
const door = await resolveDoor("https://mcp.example.com/mcp");  // a service door
// { type: "anonymous" } or { type: "legs", legs: [...] }
```

A pure query: no flow begins, no window opens, no state is stored. Useful for
deciding whether to draw a sign-in affordance at all.

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

The session object also has `user_name` and `email` properties for displaying
the name and email of the logged-in user - obviously very important.

`subject` is useful as a unique ID that also expresses whether the user's
account is native to Fresh Breath or an unmanaged log-in.

**Persistence is not your problem.** Access tokens and refresh tokens are
saved and refreshed as needed.

To ask whether you're already signed in - at boot, before any network call:

```js
const session = currentSession();   // null when the store holds nothing live
```

To sign out:

```js
signOut();   // forgets this app's gate, and ends that login on the server
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
    import { login, currentSession, SessionExpired } from "/frbr.js";

    const SERVICE_URL = "https://mcp.example.com/mcp";
    let svc = null;

    async function showTools() {
      const tools = await svc.listTools();
      document.getElementById("out").textContent =
        tools.map(t => `${t.name}: ${t.description}`).join("\n");
    }

    async function connect(prompt = true) {
      svc = await login(SERVICE_URL, prompt);
      await showTools();
    }

    document.getElementById("connect").onclick = () =>
      connect().catch(e => { document.getElementById("out").textContent = e.message; });

    // Already signed in from an earlier visit — or from a sibling app behind
    // the same gate? Let's check silently.
    connect(false);
  </script>
</body>
</html>
```

---

## Notes

- **App nonce** comes from `/api/apps` — a 10-character random string.
- **Service URL** must match a service registered *and linked to your app*.
- **`file://` apps**: the server must be running, and assigned services must
  be proxied.
- **Sharing the store**: apps served from the Fresh Breath origin share one
  store, which is what lets a second app skip the prompt. Apps served from their
  own origins don't, and will prompt separately.
- **Client secrets** never reach the browser. Token exchange and refresh run on
  the server, and refresh tokens live in HttpOnly cookies your code can't read.
