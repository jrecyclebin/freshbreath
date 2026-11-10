---
episode: 7
title: Logging in from your app
icon: 🔑
minutes: 5
blurb: frbr.js, login(), and why the second app never asks.
short: Load /frbr.js and call login() with a service’s URL. It only prompts when the browser has nothing for that door — so apps behind the same gate share one sign-in.
---

## Load frbr.js

Every Fresh Breath app starts by loading one small script from the server:

```html
<script type="module" src="/frbr.js"></script>
```

It works out which app it's running in from where the page is hosted, and
puts everything on `window.FreshBreath`. Or import what you need:

```js
import { login, currentSession, signOut } from "/frbr.js";
```

::: tip Running from your desktop?
A page opened from disk (`file://`) has no host to go by, so name the app in
the script URL with its nonce:
`<script type="module" src="https://your-server/frbr.js?aB3xK9mQ2p">`.
Nothing to set up on the server — an app with no deployment is treated as
running from disk.
:::

## One verb: login()

Call `login()` with the URL of a service linked to your app, and get back an
object to call it with:

```js
const github = await login("https://api.github.com");
const res = await github.fetch("/user/repos");

const tix = await login("/mcp/app:");   // the app's own tools
const issues = await tix.callTool("list-issues", { status: "open" });
```

Before it prompts for anything, `login()` asks the server what that door
needs, then checks the browser for a credential that fits. Only a genuinely
missing one opens a window.

The second argument says how hard to try:

1. `"normal"` (the default) — use what's stored, or open a sign-in window.
2. `"silent"` — use what's stored, or return `null`. Never prompts. Handy at
   startup.
3. `"fresh"` — always sign in again.

::: warning Call it from a click
A popup with no click behind it is a popup the browser blocks. Do your silent
check at startup, and the normal `login()` from a button.
:::

## Two apps, one browser

Here's the trick that makes Fresh Breath feel quiet. Sign in to one app,
then the other.

<LoginSim />

## Who's signed in

The session behind a service tells you who's there:

```js
const session = currentSession();   // null when nobody's signed in
session.user_name                   // "Dana Scully"
session.email                       // "dana@yourteam.dev"
session.subject                     // "frbr:3" or "ext:entra:4f2a…"
signOut();                          // forget this app's gate
```

`subject` starts with `frbr:` for a Fresh Breath user and `ext:` for
everyone else, so it doubles as a stable user ID.

<Say>
In my Fresh Breath app [app], read the Fresh Breath services guide, then check for a session silently when the page loads, show a Sign in button if there isn't one, and show the person's name and a Sign out button if there is.
</Say>

::: details Under the hood
Tokens refresh themselves inside `fetch()` and `callTool()`, which also retry
once on a 401. For apps on the Fresh Breath origin, refresh tokens live in
HttpOnly cookies your code can't read. What's in `localStorage` is always a
Fresh Breath token with the provider's credential sealed inside; the worst
thing anyone can read out of it is a token that expires.
:::
