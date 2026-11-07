# Using with Agents

<div class="short">

Connect your agent to `/mcp` and it can see your apps, read and write their files, write their tools and look in their databases. Tell it what you want in plain words, and have it read Fresh Breath's built-in guide for the job first.

</div>

<div class="like" data-icon="🧑‍🔧">

**Think of your agent as a builder with a key.** The MCP connection is the key to your server. What it opens depends on whose key it is: a Superuser's opens nearly everything, a Member's only the apps they belong to.

</div>

## Your setup

<YourSetup />

## Connect your agent

The MCP address is your server plus `/mcp`. The first time, your agent opens a browser window to sign you in, with the same login as the control panel.

**Claude Code**

```bash
claude mcp add --transport http freshbreath http://localhost:9009/mcp
```

**Claude Desktop and Cowork** need an `mcpServers` entry. [LMFriend](https://github.com/jrecyclebin/lmfriend) writes it for you and keeps the connection alive when you get logged out, so you skip the pile of connection popups:

```bash
lmfriend setup freshbreath=https://localhost:9009
```

**Anything else** that speaks MCP over HTTP works the same way. Then check it:

```say
Can you see my Fresh Breath apps? List each one with its name, nonce and web path.
```

::: tip One habit that prevents most mistakes
Start bigger requests with "Read the Fresh Breath guide for this first." The MCP has built-in guides — `publishing`, `services`, `virtuals`, `tasks` and `ssh` — loaded with `get_guide`. They save your agent from small formatting slips, like an unindented SQL line.
:::

## What your agent can do

<div class="can">
<div class="yes">

#### Over the MCP

- List apps and services, and look up their details
- Read, search, write, patch and delete an app's files — the web files and the private data area
- Write tool files for virtual services and app services
- Look inside a database: `db_schema`, `db_query`
- Load the built-in guides
- Manage its own SSH key
- **Admins and Superusers:** create apps, services, auth records and users, and link services to apps

</div>
<div class="ask">

#### In the control panel (or by you)

- Deploying to Staging and Production, and picking the default environment
- Writing to a database directly with `db_execute` — off unless a Superuser sets database access to full
- Task services — only a Superuser can create one, or read and write its scripts
- Server settings, hosts, the tunnel and remotes

</div>
</div>

A Member's agent only sees the apps they belong to. Sign your agent in as the role you want it to have.

## Things to say

Each of these is a recipe. The prompts fill in from [your setup](#your-setup); words in *[brackets]* are yours to fill in.

### Start a new app

<div class="steps">

1. [You] Ask:

   ```say
   Read the Fresh Breath publishing and virtuals guides first. Create a new Fresh Breath app called {app} at /{slug} that [describe what it does and who uses it]. Keep it to one index.html with relative file links. If it needs to save anything, use a database through the app's own service. Publish it, then tell me what to test.
   ```

2. [Claude] Creates the app, writes its tools if it needs a database, and publishes the page.
3. [You] Open the app's `@dev` address and try it. Ask for changes in plain words.

</div>

### Change an app that's already live

<div class="steps">

1. [You] Ask for the change, with a backup first:

   ```say
   In my Fresh Breath app {app} (nonce {nonce}), I want to [describe the change]. First read the current files and save a copy of index.html in the app's data area under backups/ with today's date. Then make only the edits needed, as patches rather than whole-file rewrites, and tell me what you changed.
   ```

2. [Claude] Reads the live files, backs them up, and patches just the parts that change.
3. [You] Refresh the `@dev` address. Not right? Say what you see, or roll back:

   ```say
   Restore index.html in my Fresh Breath app {nonce} from the most recent backup in its data area.
   ```

</div>

::: warning Writing a file replaces it
Nothing is merged, which is why the backup matters. And the database is shared by every slot, so a change there is live right away. [See why in the slot simulator.](/apps#three-slots-one-database)
:::

### Give it a database

<div class="steps">

1. [You] Decide what to save, like planning spreadsheet columns — *clients with a name, phone number and status*.
2. [You] Ask:

   ```say
   I want my Fresh Breath app {app} (nonce {nonce}) to save [what to save]. Read the Fresh Breath virtuals guide first. Write tools into the app's own service (app:{nonce}): a migrate tool that creates the tables, plus tools to add, list, update and delete entries. Have the app run migrate when it opens, and save and load through those tools.
   ```

3. [Claude] Designs the tables and writes the tools. [Here's what that file looks like.](/tools#what-does-this-tool-file-make)
4. [You] Add a test entry and refresh. Still there? It worked. Then double-check:

   ```say
   Look in my Fresh Breath app {nonce}'s database and show me what tables exist and what's saved so far.
   ```

</div>

### Make people sign in

<div class="steps">

1. [You] [Admin] Pick the app's **Protected by** — in the control panel, or by asking an admin's agent. Leaving it empty means *the admin login*, not *open*. [Try the gate simulator first.](/auth#who-gets-through-the-door)
2. [You] Ask:

   ```say
   My Fresh Breath app {app} (nonce {nonce}) now requires sign-in. Read the Fresh Breath services guide, then add a small header with the signed-in person's name and a Sign out button. If we save data, record who added each entry with $token_id.
   ```

3. [You] Open the app in a private window to check it asks you to sign in.

</div>

### Connect another tool

<div class="steps">

1. [You] See what's there:

   ```say
   Which services are on my Fresh Breath server, and which are linked to my app {nonce}? Say what each one is for.
   ```

2. [Admin] Links the service you need to the app — and adds it to the server if it isn't there yet.
3. [You] Ask:

   ```say
   My Fresh Breath app {nonce} now has [service name] linked. Read the Fresh Breath services guide, then add a button that connects to it and [what it should do]. Call login() from the button's click so the sign-in window isn't blocked.
   ```

</div>

### Fix something that's broken

```say
My Fresh Breath app {app} (nonce {nonce}) has a problem: [what you see, or the error]. Read the relevant Fresh Breath guide, check the app's files, its linked services and its database, and tell me what's wrong before you change anything.
```

## Make your agent remember the app

Paste this into the project's `CLAUDE.md` or `AGENTS.md`, so every session knows where the app lives and how to work on it.

```say Paste into CLAUDE.md or AGENTS.md
This project's app is hosted on Fresh Breath.
- App: {app} (nonce {nonce})
- Live: {server}/{slug} · Draft: {server}/{slug}@dev
- Its own tools and database: app:{nonce}

How to work on it:
- Use the Fresh Breath MCP for anything to do with this app.
- Before building something new, load the matching guide with get_guide (publishing, services, virtuals, tasks or ssh).
- Read files before editing. Back up index.html to the data area under backups/ before replacing it. Prefer patches over whole-file writes.
- Keep file links relative (./) so the app works in every slot.
- Save data through tools in app:{nonce}, not in localStorage.
```

## Bringing work over from elsewhere

<div class="facts">

- 🎨 **Claude Design** Drop the normal Claude Design zip (not the Claude Code handoff zip) into the app's upload area and it's hosted. If the zip has a README, Fresh Breath offers a prompt to hand to Claude Code to finish the job.
- 🗂️ **Cowork artifacts** Drop them in the same way, once the services they need exist.
- 📎 **A file you already have** Hand it to your agent and ask it to publish it to your app. Data kept in the chat's own storage needs moving into a database.

</div>
