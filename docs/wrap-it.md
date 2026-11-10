---
episode: 8
title: Wrap an API, run a script
icon: 🧰
minutes: 6
blurb: Virtual services over HTTP, Acts as, MCPs for free, and task services.
short: A virtual service turns HTTP calls into named tools, the same text format as your SQL tools. Gate it with your own login and let it act as the API's key outbound. Every one is an MCP server too. Task services do the same with shell scripts.
---

## Tools over HTTP

The tool scripts from last episode don't only do SQL. A step can be an
HTTP request instead: a method and URL, headers, a body, and the status you
expect back.

```txt
[open-prs] List a repository's open pull requests.

GET https://api.github.com/repos/$owner/$repo/pulls?state=open
Authorization: Bearer $token

HTTP 200
{prs: [.[] | {number, title, author: .user.login}]}
```

1. `$owner` and `$repo` become the tool's arguments, safely escaped into the
   URL.
2. `$token` is filled in by Fresh Breath with the credential the service
   *acts as* (more on that in a moment).
3. `HTTP 200` is the status that counts as success.
4. The `{…}` block after it is **jq**: it shapes GitHub's big response into
   just what callers need. Leave it out and the raw body comes back.

A service full of tools like these is a **virtual service**. Apps call it
with `callTool()`, like the app service, once it's linked to them.

## Watch one get written

<ChatSim title="Try it: Claude wraps the GitHub API" file="github-tools · tools">

- @you Make me a Fresh Breath service with tools for our GitHub repos: open pull requests, and filing an issue. Read the virtuals guide first.
- @claude Reading `get_guide("virtuals")`… I'll create a virtual service called **github-tools** at `/mcp/github-tools` with `create_service`, then write its tools. First, open PRs:

  ```txt
  [open-prs] List a repository's open pull requests.

  GET https://api.github.com/repos/$owner/$repo/pulls?state=open
  Authorization: Bearer $token

  HTTP 200
  ```

- @claude That returns GitHub's whole response, about 300 lines per PR. I'll trim it to what matters with a shaping block:

  ```txt
  [open-prs] List a repository's open pull requests.

  GET https://api.github.com/repos/$owner/$repo/pulls?state=open
  Authorization: Bearer $token

  HTTP 200
  {prs: [.[] | {number, title, author: .user.login}]}
  ```

- @claude Now filing an issue. GitHub answers `201` for a created one, so that's the status to expect:

  ```txt
  [open-prs] List a repository's open pull requests.

  GET https://api.github.com/repos/$owner/$repo/pulls?state=open
  Authorization: Bearer $token

  HTTP 200
  {prs: [.[] | {number, title, author: .user.login}]}
  ---
  [file-issue] Open an issue in a repository.

  POST https://api.github.com/repos/$owner/$repo/issues
  Authorization: Bearer $token
  Content-Type: application/json
  {
    "title": $title,
    "body": $body
  }

  HTTP 201
  {number: .number, url: .html_url}
  ```

- @you Who does it act as on GitHub?
- @claude Right now, the caller, but your gate is your Fresh Breath passphrase, which has no GitHub credential behind it. Set **Acts as** to a stored GitHub key and every call uses that. Make an api_key record in the control panel (keep the key out of our chat) and I'll point the service at it.

</ChatSim>

## In with your login, out with the API's key

Here's the setup most team tools want:

<div class="flow">
  <div class="node me"><b>You, or Claude</b><small>Calls a tool</small></div>
  <div class="arr" aria-hidden="true">→</div>
  <div class="node"><b>Protected by: empty</b><small>Inherits the Admin auth. Only your people get in.</small></div>
  <div class="arr" aria-hidden="true">→</div>
  <div class="node fb"><b>github-tools</b><small>Runs the tool</small></div>
  <div class="arr" aria-hidden="true">→</div>
  <div class="node cl"><b>Acts as: GitHub key</b><small>A stored api_key record goes out as $token</small></div>
</div>

**Protected by** decides who gets *in*. **Acts as** decides what goes
*out*. They don't have to match: the door can be your Fresh Breath login
while GitHub only ever sees one service account's key. Nobody using the tool
needs a GitHub account at all, and the key never reaches a browser.

A stored key is also the only Acts as that works with nobody around, so it's
what a scheduled task needs. (Episode 6 has the full picture of who sends
what.)

::: warning Keys go in the control panel
Never paste a secret into a chat. Make the api_key record yourself in the
control panel, then ask your agent to point the service at it by name.
:::

<Say>
Set my github-tools service to act as the [name of your GitHub key record] auth record, and leave its Protected by empty so it inherits the Admin auth.
</Say>

## It's an MCP server too

Every virtual service is also an MCP server, at `/mcp/` plus its name,
behind the same gate. So the tools you wrote for an app work in Claude as
well:

```bash
claude mcp add --transport http github-tools http://localhost:9009/mcp/github-tools
```

Sign in when asked, and Claude can list PRs and file issues, with exactly
the powers you gave it and no more. It's a neat way to give an agent
**limited** access to an API: a read-only tool set, say, instead of a
full-power token.

::: tip Tools just for the app
End a tool's name with `!` (`[delete-all!]`) and it's app-only: pages can
call it, MCP clients never see it.
:::

## Task services: scripts as tools

Some jobs don't fit an HTTP call: moving a file onto a network share,
running an image converter. A **task service** wraps shell scripts (bash, or
PowerShell on Windows) as tools, in the same file format:

```txt
[to-webp] Convert an uploaded image to WebP and keep it in the app's data.

cwebp -q 80 "$TASK_IMAGE" -o "$FRBR_APP_DIR/data/$TASK_LABEL.webp"
printf '{"saved":"%s.webp"}' "$TASK_LABEL"
```

1. Arguments arrive as environment variables: `image` becomes `$TASK_IMAGE`.
   A file from `callTool()` arrives as a path.
2. Whatever the script prints is the result. Print JSON and `callTool()`
   hands back an object.
3. A non-zero exit makes the call fail, with the script's stderr.
4. `$FRBR_APP_DIR` is the calling app's folder, and its `data/` is a safe
   place to keep things. Each run gets a fresh temporary home, deleted
   afterwards.

```js
const tasks = await login("tasks://imaging");
await tasks.callTool("to-webp", { image: fileInput.files[0], label: "hero" });
```

::: warning Superusers only
Task scripts run as the server's own OS account, so only a Superuser can
create a task service or edit its file. Treat one like giving someone a
terminal on the box.
:::

A task service is an MCP server too, at `/mcp/` plus its name. Over MCP
there's no file upload and no calling app, so `$FRBR_APP_DIR` is unset there.
