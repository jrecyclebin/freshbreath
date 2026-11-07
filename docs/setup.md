# The Four-Step Setup

<div class="short">

Run the server, make yourself a Superuser with an SSH key, then have an agent build and publish an app straight onto it. Twenty minutes, tops — most of it waiting on the agent.

</div>

Fresh Breath is a **personal app server**: publish apps to it, give those apps databases, scripts and logins, and let them call out to everything else.

::: warning Personal, not public
Don't just host this on the Internet!!
:::

## Step 1. Run it

Easiest way to start is to grab a [release ZIP](https://github.com/jrecyclebin/freshbreath/releases)
and crack it open. Run the `freshbreath` app from that directory and it shows up at
`http://localhost:9009/`.

If you have [Mise](https://mise.jdx.dev) installed:

```bash
mise use -g github:jrecyclebin/freshbreath
freshbreath
```

Or Docker:

```bash
docker run -d --name freshbreath \
  -p 9009:9009 \
  -v freshbreath-data:/data \
  ghcr.io/jrecyclebin/freshbreath:latest
```

## Step 2. Make yourself

Set up a user for yourself right away, using the built-in SSH auth.

<div class="steps">

1. [You] Go to `http://localhost:9009/control/users` and add a user, filling in everything. Make yourself a **Superuser**.
2. [You] At the bottom, set an SSH passphrase. It can't be recovered, so keep it somewhere safe.
3. [You] Go to `http://localhost:9009/control/settings` and change **Admin auth** to "Built-in - SSH key".
4. [You] Refresh, and sign in with the passphrase you just made.

</div>

::: danger Don't skip this one
Until **Admin auth** is set, the control panel and the `/mcp` endpoint are open to anyone who can reach the server — an agent connecting gets in as a Superuser. That's what makes bootstrapping easy, and why you lock it down first.
:::

::: tip If you pick an SSO for Admin auth later
Every app and service with an empty *Protected by* inherits it, and lets in anyone that provider signs in — not just Fresh Breath users. A company SSO is made for that. A generic GitHub or Google login isn't: it signs in the whole internet. [See it happen in the gate simulator.](/auth#who-gets-through-the-door)
:::

## Step 3. Host an app

Say we have a Linear subscription, and we'd like to drop it and make our own issue tracker. If you're feeling lazy, have an agent do it.

<div class="steps">

1. [You] Connect Claude Code, Pi, Opencode, etc. to the MCP at `http://localhost:9009/mcp`. (Exact commands are on [Using with agents](/agents#connect-your-agent).)
2. [You] You'll be prompted for the creds you just set up.
3. [You] Check the connection:

   ```say
   Can you see my Fresh Breath apps?
   ```

4. [You] Then ask for the app:

   ```say
   Ok can you real quick create a custom issue tracker for me? This will be a Fresh Breath app - create the app there, have it use the same auth as the admin area, and publish files directly to it. Call it "Tix" and host it at /tix.

   It should allow me to manage projects, modules within those projects, and issues within those. Store this all in a Fresh Breath database - by creating a virtual SQL service. I'll want to keep this at `/mcp/tix` and it should also inherit auth from the admin area.

   I don't need to tell you to make no mistakes because you and I are way past that now, aren't we? 😘
   ```

5. [Claude] Reads Fresh Breath's built-in guides, creates the app and the service, writes the tools, and publishes the files.
6. [You] Visit `http://localhost:9009/tix`. Check that it asks you to sign in — then cancel your Linear subscription.

</div>

If you'd rather build it by hand — true respect to you. 🙏 Write the files to a folder, then drag and drop them into the upload area on the app's page in the control panel.

## Step 4. Slop up your tix

None of us likes writing tickets. Literally not one. Now that there's an MCP wrapped around the ticket database, an agent can write them for you.

<div class="steps">

1. [You] Connect to the MCP at `http://localhost:9009/mcp/tix`.
2. [You] Since it inherits admin auth, you'll be asked to sign in. Use your Fresh Breath creds.
3. [You] Ask:

   ```say
   Create a new project in Tix called "Garden" with a module for "Irrigation", and file three issues in it for the things I keep forgetting to fix.
   ```

4. [You] Refresh `/tix` and there it is.

</div>

## Possible steps 5, 6, 7...

- **HTTPS.** On a fresh install served over plain HTTP, the control panel offers to make a certificate for you. It's worth it: apps opened from `file://` or another origin can't stay signed in over plain HTTP.
- **The rest of it.** [The full tour](/tour) has lots of ideas for what to do with Fresh Breath.
