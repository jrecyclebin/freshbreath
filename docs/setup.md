# The Four-Step Setup

Let's get Fresh Breath up and going so you can try it out. (It's a **personal
app server** — publish apps to it, set up custom databases or bash/Powershell
scripts for those apps and set up auth for it all.)

> *Personal* not *public*. Don't just host this on the Internet!!

## Step 1. Run it

Easiest way to start is to just grab a [release ZIP](https://github.com/jrecyclebin/freshbreath/releases)
and crack it open. You can just run the `freshbreath` app from that directory
and it should show up on `http://localhost:9009/`.

If you have [Mise](https://mise.jdx.dev) installed, you can also just do it
from there:

```bash
mise use -g github:jrecyclebin/freshbreath
freshbreath
```

If you want a Docker image, that's available as well.

```bash
docker run -d --name freshbreath \
  -p 9009:9009 \
  -v freshbreath-data:/data \
  ghcr.io/jrecyclebin/freshbreath:latest
```

## Step 2. Make yourself

I'd immediately set up a user for yourself, using the built-in SSH auth.

* Go to http://localhost:9009/control/users.
* Add a user, filling in everything, making yourself a Superuser.
  * And, at the bottom, set up an SSH password.
* Go to http://localhost:9009/control/settings.
* Change the **Admin auth** to "Built-in - SSH key".

You'll now need to refresh and log in using the password you just created.

## Step 3. Host an app

Go to the *Apps* area in the control panel and create a new one. For example,
let's say we have a Linear subscription and we want to drop it and just make
our own issue tracker.

If you're feeling lazy, just have an agent do it:

1. Connect Claude Code, Pi, Opencode, etc. to the MCP at
   http://localhost:9009/mcp.
2. You will be prompted for your creds you just set up.
3. Use the prompt "Can you see my Fresh Breath apps?" to ensure the connection
   works.

Then you can prompt it to create a Linear replacement:

```
Ok can you real quick create a custom issue tracker for me? This will be a Fresh
Breath app - create the app there, have it use the same auth as the admin area,
and publish files directly to it. Call it "Tix" and host it at /tix.

It should allow me to manage projects, modules within those projects, and issues
within those. Store this all in a Fresh Breath database - by creating a virtual
SQL service. I'll want to keep this at `/mcp/tix` and it should also inherit
auth from the admin area.

I don't need to tell you to make no mistakes because you and I are way past that
now, aren't we? 😘
```

If you want to build it by hand, though - true respect to you. 🙏 You can just
write the files to a folder - then drag-and-drop them into the designated area
of the app edit pane in Fresh Breath.

Finally, assuming the agent actually delivered, cancel your Linear subscription
and visit http://localhost:9009/tix from now on. (Verify that the app is
protected by auth.)

## Step 4. Slop up your tix

None of us likes writing tickets. Literally not one. Now that we have an MCP
wrapper around our ticket database, we can connect an agent to our new database
as well.

1. Connect to the MCP at http://localhost:9009/mcp/tix.
2. Since we inherited admin auth, you should be prompted for log-in - use
   those Fresh Breath creds.
3. Ask the agent to create a new project in Tix.

You can verify the creation of your new project in the web app.

## Possible steps 5, 6, 7...

If you want to set up an SSL cert...

Or just move on to [The Full Tour](/tour) — lots of ideas there for what to do
with Fresh Breath.
