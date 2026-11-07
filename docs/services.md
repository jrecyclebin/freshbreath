# Services

<div class="short">

A service is anything your app talks to. Link it to the app, then call `login()` with its URL to get a proxy: `callTool()` for tools and MCP servers, `fetch()` for web APIs, and `watch()` to hear about calls as they finish.

</div>

## The five kinds

<div class="facts">

- 🌐 **API** An HTTP API. Call it with `svc.fetch()`. Proxied, if you allow it, so CORS stops mattering.
- 🧩 **MCP** Someone else's MCP server — Notion, Linear, your own. Call it with `listTools()` and `callTool()`.
- ✍️ **Virtual** Tools you write that wrap HTTP requests or SQL queries. Also an MCP server at `/mcp/its-name`. [The format.](/tools)
- ⚙️ **Tasks** Tools that run bash or PowerShell scripts on the server. Superusers only, since they run as the server's own account.
- 🗝️ **SSH** One per server, at `ssh://`. Sessions, file sync and git with a key Fresh Breath keeps for you.

</div>

And every app gets one more for free: its **app service**, `app:<nonce>`, a blank virtual service for that app's own database tools. No setup, no link to make.

## From your app's side

<div class="steps">

1. **Load frbr.js.** Hosted apps use `/frbr.js`. A page opened from disk names its app: `https://your-server/frbr.js?YOUR-NONCE`.
2. **Log in to the service by its URL.** It must be registered and linked to your app.
3. **Call it.** The proxy refreshes tokens near expiry and retries once on a 401.

</div>

```html
<script type="module">
  import { login } from "/frbr.js";

  const tix = await login("/mcp/app:");                        // this app's own tools
  const issues = await tix.callTool("list-issues", { project: 3 });

  const gh = await login("https://api.github.com");            // a proxied API
  const me = await (await gh.fetch("/user")).json();
</script>
```

`callTool` parses a JSON result and throws on a tool error, so you get the value, not an envelope. Pass a `File` as any argument and the call goes out as multipart — that's how a task script receives an upload.

::: tip Proxied or not
A proxied service's calls go through `/service/{id}/…`, where the server attaches the right credential. A page opened from `file://` always goes through the proxy, so the services it uses must be proxied.
:::

## Watching calls

`svc.watch()` streams a service's finished calls as they happen — *anyone's* calls, from any page, any agent, any scheduled task. That's how two people looking at the same list stay in sync without polling.

<WatchSim />

```js
const stop = new AbortController()
svc.watch((ev) => {
  if (ev.tool === "add-item" && ev.key === currentList) refresh()
}, { signal: stop.signal, onOpen: refresh })
```

<div class="facts">

- ✅ **Successes only** A call that fails is never reported.
- 🙈 **No arguments** Just the tool name (or method and path for a proxied API), plus the `WATCH` key if there is one.
- 🔁 **Re-read on open** Missed events aren't replayed, so refresh in `onOpen`.
- 🔇 **Opt-in once you start** Until a file has a `WATCH`, every successful call is reported. After that, only tools with one.

</div>

::: warning A WATCH key is broadcast
Everyone who can open the app can watch, and they all see the key. `WATCH $list_id` is fine. `WATCH $token_email` tells every watcher who just made the call. Key on ids, not contents.
:::

::: warning Mind feedback loops
A page that refreshes by calling a tool, on a service that reports every call, hears its own refresh and refreshes again. Put `WATCH` on the tools that change things.
:::

## Task services

A task is a script with a name. Arguments arrive as environment variables — `notes` becomes `$TASK_NOTES` — and whatever the script prints is the result, parsed as JSON when it is JSON.

```
[import] Move an uploaded image into the app's data folder.

mv "$TASK_IMAGE" "$FRBR_APP_DIR/data/"
printf '{"saved":"%s"}' "$(basename "$TASK_IMAGE")"
```

Each run gets a fresh temporary home that's deleted afterward, so keep anything worth keeping in `$FRBR_APP_DIR/data`. Over the MCP, each task is a tool whose arguments are read off the `$TASK_…` names it mentions.

## SSH

Fresh Breath generates a key for you and keeps it locked behind your passphrase. Sign in once and the key is ready for an hour; open sessions last up to eight. Your app can then open sessions, sync files, or sign git commits through the SSH service — the browser never sees the key.

The full how-to is the `ssh` guide. Ask your agent:

```say
Read the Fresh Breath ssh guide. Show me my SSH key details, and if I don't have one yet, tell me what I need to do to make one.
```
