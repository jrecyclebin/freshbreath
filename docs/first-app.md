---
episode: 4
title: Your first app
icon: 🎫
minutes: 5
blurb: Make an app, give it an address, and have Claude publish into it.
short: An app is a nonce (its ID) plus an address. Make one in the control panel, then have your agent write its files. Visitors see the web files; the data files stay private.
---

## Make an app

In the control panel, open **Apps** and add one. Give it a name — say,
**Tix**, a little issue tracker to replace a subscription you're tired of
paying for.

Fresh Breath gives every app a **nonce**: a random 10-character ID shown
right under its name. That's how your agent (and the app's own code) tells
Fresh Breath which app it means.

::: info 🏬 Think of it like a shop in a mall
The app name is the sign over the door. The nonce is the unit number on the
lease. The address is where people find it.
:::

## Where an app lives

An app's **URL** setting decides where it's served. Usually it's just a
slug, like `tix`. Play with it — and see what Fresh Breath won't let you do.

<SlugSim />

::: details The full rules
- **A slug** — `blog` serves the app at `/blog/`. Slashes at either end are
  cleaned up. No subdirectories; `api`, `control`, `mcp`, `service` and
  `oauth` are taken.
- **A full URL on a listed host** — `https://blog.example.com/notes`, when
  `blog.example.com` is on your Hosts list and its DNS points here. Apps sit
  one level down, never at the root.
- **Any other full URL** — `https://myapp.netlify.app`, or a localhost dev
  server. Fresh Breath doesn't serve it, but lets it call in with its nonce.
- **Blank** — the slug of the app's name, or, with no files, a page you're
  running from disk.
:::

## Publish it

You've got an empty app. Now have your agent fill it.

<ChatSim title="Try it: publishing with Claude">

- @you Read the Fresh Breath publishing guide first. Then build a small issue tracker and publish it to my app Tix as index.html. Keep every file link relative.
- @claude Reading `get_guide("publishing")`… Got it: files go in the app's **web** area, and relative links (`./app.js`) keep it working in every slot.
- @claude Calling `write_app_files` with `index.html`, `app.js` and `style.css`… All three written.
- @claude It's live at **localhost:9009/tix**. Projects, modules and issues all work, but nothing is saved yet — want me to give it a database next?
- @you Yes, next episode!

</ChatSim>

<Say>
Read the Fresh Breath publishing guide first. Then build [what the app should do] and publish it to my Fresh Breath app [app] (ID [nonce]) as index.html. Keep every file link relative so it works in every slot. Tell me the address to open when you're done.
</Say>

::: tip Building it by hand?
True respect. Write the files to a folder, then drag them (or a zip) onto the
upload area on the app's page. A Claude Design zip works too — not the Claude
Code handoff zip, the normal one.
:::

## Web files and data files

Every app has two areas for files:

<div class="facts">
  <div class="fact"><span class="ic">🪟</span><b>web</b><p>What visitors see. Served at the app’s address, and copied when you deploy.</p></div>
  <div class="fact"><span class="ic">🗄️</span><b>data</b><p>Private. Never served, never deployed. Good for uploads and backups.</p></div>
</div>

Writing a file **replaces** it — nothing is merged. So before a big change,
have your agent back things up into the data area:

<Say>
In my Fresh Breath app [app] (ID [nonce]), I want to [describe the change]. First save a backup of index.html in the app's data area, in a backups folder with today's date. Then make only the edits needed and tell me what you changed.
</Say>

::: details Under the hood
Your agent uses `read_app_files` and `write_app_files`. With `old_text`, a
write swaps one exact piece of a file (which must appear exactly once);
without it, the whole file is replaced. Files over 10 KB travel through a
short-lived link (`transport: "http"`) that lasts 10 minutes. `area: "data"`
is the private storeroom.
:::
