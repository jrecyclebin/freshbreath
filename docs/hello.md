---
episode: 1
title: Hello, Fresh Breath
icon: 🌬️
minutes: 3
blurb: What it is, and what a plain HTML file can’t do without it.
short: Fresh Breath is a home for the little apps you build. It puts them online and gives them what a plain HTML file can’t have on its own — logins, a place to save data, and connections to other tools.
---

## One app, two lives

Here's a tiny app. Try each button as a **plain HTML file** first, the way
it would run if you double-clicked it on your desktop. Then flip the switch
and run the very same file **on Fresh Breath**.

<PlugSim />

## What just happened

That app didn't change. What changed is where it lives. Fresh Breath gives a
static page four things it can't have by itself:

<div class="facts">
  <div class="fact"><span class="ic">🔐</span><b>Know who’s using it</b><p>Sign-in, with people’s own accounts.</p></div>
  <div class="fact"><span class="ic">🗄️</span><b>Remember things</b><p>Every app gets its own database.</p></div>
  <div class="fact"><span class="ic">🔗</span><b>Talk to other tools</b><p>APIs, MCP servers, scripts. Proxied, so CORS isn’t your problem.</p></div>
  <div class="fact"><span class="ic">🌐</span><b>Live at a real address</b><p>So teammates can open it in a browser.</p></div>
</div>

::: info 🔌 Think of it like a universal adapter
Your app plugs in on one side. Everything it needs plugs in on the other: a
database, a login, GitHub, Notion, an AI model. Fresh Breath makes them fit.
:::

## Three ideas to keep

1. **People sign in with their own accounts.** Someone using your app
   doesn't need a Fresh Breath account. Their login stays in their own
   browser, sealed so it can't be read.
2. **Apps share connections.** Set up a sign-in once and every app behind it
   can use it, so nobody gets asked to log in over and over.
3. **Fresh Breath speaks your agent's language.** It's an MCP server, so
   Claude (or Pi, or OpenCode…) can build on it directly.

::: warning Personal, not public
Fresh Breath is for you and your team. Don't put it on the open internet.
:::

::: details Under the hood (optional, for the curious)
Fresh Breath is a single Go program with a SQLite database. It hosts static
apps and offers them five kinds of service: web APIs, MCP servers, virtual
services (tools you write that wrap web requests or SQL), task services
(shell scripts) and SSH. A login leaves a Fresh Breath token in the browser
with the real credentials sealed inside; the server unseals them on the way
out, and client secrets never reach the browser.
:::
