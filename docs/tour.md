# The Full Tour

<div class="short">

Fresh Breath is a home for the little apps you build. It puts them online and gives them what a plain HTML file can't have on its own: logins, a place to save data, and connections to other tools. It's also an MCP server, so you can build on it by talking to an agent.

</div>

<div class="like" data-icon="🔌">

**Think of it as a universal adapter.** Your app plugs in on one side. On the other side plug in whatever it needs — a database, a sign-in, GitHub, Notion, an AI model, an SSH server. Fresh Breath makes them fit.

</div>

My own example: a personal finance app, hosted on Fresh Breath so I can use it in a browser. A Telegram bot reaches the same data through an MCP, so I can ask questions while I'm out. And it still sends updates to YNAB, which is set up as a service, because I still like it for now.

## What a plain HTML app can't do alone

<div class="facts">

- 🔐 **Know who's using it** Fresh Breath signs people in — with their own GitHub, Google or company accounts.
- 🗄️ **Remember things** Every app can have its own SQLite database, reached through tools you write.
- 🔗 **Talk to other tools** APIs and MCP servers, proxied so CORS and client secrets stop being your problem.
- 🌐 **Live at a real address** Drop the files in and it's served at `/your-app`, in three slots.
- ⚙️ **Touch the machine** Wrap a bash or PowerShell script as a tool. Push files over SSH, sign git commits.
- 🤖 **Be built by an agent** Fresh Breath is an MCP server with built-in guides, so an agent can publish straight to it.

</div>

## Three ideas worth remembering

<div class="steps">

1. **People sign in with their own accounts.** Someone using your app doesn't need a Fresh Breath account. What their browser keeps is a Fresh Breath token with any provider credential sealed inside, and no sessions sit in Fresh Breath's database.
2. **Apps share logins.** Sign in once, and every app behind the same gate can use it. The second app prompts zero times.
3. **Fresh Breath speaks your agent's language.** It's an MCP server at `/mcp`, and every virtual or task service you make is an MCP server too, at `/mcp/its-name`.

</div>

## The pieces

| Piece | What it is | Everyday comparison |
|---|---|---|
| **App** | Static files hosted at a path, with a 10-character **nonce** that identifies it | A shop in a mall |
| **Service** | Something an app talks to: an API, an MCP server, a virtual service, a task service, or SSH | Utilities hooked up to the shop |
| **Link** | Permission for an app to use a service. Unlinked services are off-limits to it | Which utilities the shop is connected to |
| **Auth record** | A login method or stored key, shared by everything that names it | A type of key card |
| **Protected by** | The auth record that guards the door of an app or service | The lock on the front door |
| **Acts as** | The credential a service sends upstream | Whose badge is shown at the next door |
| **User** | Someone who *manages* apps and services: Superuser, Admin, Member or Read-only | The mall office staff |

That last row trips people up: users are for maintaining things. People who just *use* your apps don't need to be users at all. [The glossary](/glossary) has the rest.

## A quick lap

<div class="path">

1. [Hosting apps](/apps) Upload files or a zip, keep links relative, and step a version from Development to Staging to Production. **Simulation:** three slots, one database.
2. [Logins and gates](/auth) Protected by, acts as, and why "empty" doesn't mean "open." **Simulations:** who gets through the door, and what `login()` costs.
3. [Services](/services) The five kinds, and how your app calls them with `callTool()`, `fetch()` and `watch()`. **Simulation:** two browsers watching one service.
4. [Tools and databases](/tools) The tool-file format, where a SQL query becomes a tool. **Playground:** write one and see what it makes.
5. [Using with agents](/agents) The exact words to say for each of those jobs.

</div>

## Beyond one machine

These go past a first afternoon, so they're only sketched here. The [README](https://github.com/jrecyclebin/freshbreath#readme) has the details.

<div class="facts">

- 🏘️ **Hosts** Point a DNS name at the server and put apps one level down on it, like `https://blog.example.com/notes`. Apps on a host share their logins, on purpose.
- 🕳️ **The tunnel** Give the server a WireGuard address you can reach from anywhere, with nothing opened to the internet.
- 🛰️ **Remotes** Drive another Fresh Breath through its tunnel, and transfer apps and services to it.
- 📦 **Remote updates** Ship apps to someone else's Fresh Breath, then push updates to them from an encrypted archive.

</div>

::: warning Personal, not public
All of this is built for you and the people you trust. Keep it off the open internet.
:::
