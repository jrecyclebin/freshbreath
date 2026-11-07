---
episode: 3
title: Bring in your agent
icon: 🤝
minutes: 3
blurb: Connect Claude to Fresh Breath, and check it can see your apps.
short: Fresh Breath is an MCP server at /mcp. Point Claude Code (or Pi, OpenCode, Claude Desktop…) at it, sign in with the Fresh Breath login you just made, and ask it to list your apps.
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

- @you Can you see my Fresh Breath apps? List each one with its name, ID (nonce) and web path.
- @claude Calling `list_apps`… Yes! You have one app so far:
- @claude **Tix** — nonce `aB3xK9mQ2p`, served at `/tix`. It has no files yet, so it's empty. Want me to build something into it?
- @you Not yet. What else can you do here?
- @claude Quite a bit: read, write and search an app's files, write tools for its database, look inside that database, check which services it's linked to, and read Fresh Breath's built-in guides before I build anything. Admin jobs — new services, server settings — I can do too if your role allows.

</ChatSim>

Your turn: ask the same thing for real.

<Say>
Can you see my Fresh Breath apps? List each one with its name, ID (nonce) and web path.
</Say>

## Fill in your setup

Every “Say this to Claude” box in this guide can fill in your app's details
for you. Open **⚙ Your setup** in the sidebar and put in your server, app
name, nonce and web path. They stay in this browser.

<Say>
Read the Fresh Breath guide for [what you're about to do] first. Then, in my app [app] (ID [nonce]), …
</Say>

::: tip One habit that prevents most mistakes
Start bigger requests with “Read the Fresh Breath guide for this first.”
Fresh Breath ships guides for publishing, databases, services, scripts and
SSH, and an agent that reads them first gets the small details right.
:::
