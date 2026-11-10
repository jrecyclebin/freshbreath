---
episode: 3
title: Connect your prompt
icon: 🤝
minutes: 3
blurb: Plug your agent into Fresh Breath, and give an app its first service.
short: Fresh Breath is an MCP server at /mcp. Point Claude Code (or Pi, OpenCode, Claude Desktop…) at it and sign in. From then on your prompts can build apps and hook up services — an API or MCP server, linked to the app that needs it.
---

## Plug it in

Your agent talks to Fresh Breath through MCP — the standard way agents plug
into tools. The address is your server plus `/mcp`:

```txt
http://localhost:9009/mcp
```

1. @you Add that address as an MCP server in your agent.
   In Claude Code that's `claude mcp add --transport http freshbreath http://localhost:9009/mcp`.
2. @you Sign in when it asks — with the Fresh Breath user and passphrase from
   the last episode.
3. @claude Gets a token and can now use Fresh Breath's tools.

::: tip Claude Desktop and Cowork
Use [LMFriend](https://github.com/jrecyclebin/lmfriend) to add Fresh Breath
to Claude Desktop's config: `lmfriend setup freshbreath=https://localhost:9009`.
It keeps the connection alive even if you get logged out, so no pile of
popups.
:::

## Say hello

The first thing to ask is whether it can see anything. Here's how that goes.

<ChatSim>

- @you Can you see my Fresh Breath apps? List each one with its name and web path.
- @claude Calling `list_apps`… Yes! You have one app so far:
- @claude **Tix** — nonce `aB3xK9mQ2p`, served at `/tix`. It has no files yet, so it's empty. Want me to build something into it?
- @you Not yet. What else can you do here?
- @claude Quite a bit: read, write and search an app's files, write tools for its database, look inside that database, check which services it's linked to, and read Fresh Breath's built-in guides before I build anything. Admin jobs — new services, server settings — I can do too if your role allows.

</ChatSim>

Your turn: ask the same thing for real.

<Say>
Can you see my Fresh Breath apps? List each one with its name and web path.
</Say>

## Give an app a service

Apps reach the outside world through **services**: an HTTP API, an MCP
server, and a few kinds of Fresh Breath's own (later episodes). A service is
registered once on the server, then **linked** to each app allowed to use
it. Try it from the control panel side.

<ServiceSim />

Or just ask. Your agent makes the same calls you saw in the log:

<Say>
Read the Fresh Breath services guide. Then add the GitHub API (https://api.github.com) as a proxied service, link it to my app [app], and show me the login() call the app should use.
</Say>

::: tip Proxied or not?
A **proxied** service is called through Fresh Breath (`/service/{id}/…`),
which adds the right credential on the way out and gets around CORS. An
OAuth MCP server like Notion only works proxied. Unproxied calls go straight
from the browser.
:::

## Fill in your setup

Every “Say this to Claude” box in this guide can fill in your app's details
for you. Open **⚙ Your setup** in the sidebar and put in your server, app
name and web path. They stay in this browser. You never need the app's ID
(its nonce) in a prompt: your agent looks the app up by name.

<Say>
Read the Fresh Breath guide for [what you're about to do] first. Then, in my app [app], …
</Say>

::: tip One habit that prevents most mistakes
Start bigger requests with “Read the Fresh Breath guide for this first.”
Fresh Breath ships guides for publishing, databases, services, scripts and
SSH, and an agent that reads them first gets the small details right.
:::
