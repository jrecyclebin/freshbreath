---
episode: 5
title: Dev, Staging, Production
icon: 🚦
minutes: 4
blurb: Change things safely, then ship them when they’re ready.
short: Every app has three copies. Uploads always land in Development. Deploying copies Development to Staging, and Staging to Production. The bare address serves whichever copy you call the default.
---

## Three copies of every app

Every hosted app gets three **slots**, each its own folder:

1. **Development** — where every upload and every edit lands.
2. **Staging** — a frozen copy of Development, made by deploying.
3. **Production** — the same idea, one step further along.

Each one has its own address: `/tix@dev`, `/tix@staging`, `/tix@prod`. And
the bare `/tix` serves whichever slot is the app's **default environment**
(Development, until you change it).

::: info ✏️ Think of it like draft, review copy, final
You scribble on the draft. When it looks right, you print a review copy.
When everyone signs off, that becomes the final.
:::

## Ship a version

Upload a few versions, move them along, and get one live. Each button shows
the API call it makes.

<SlotSim />

## Things that trip people up

::: warning Hardcoded paths break in other slots
An app that bakes `/tix/` into its links will miss when it's served from
`/tix@staging/`. Use relative paths (`./app.js`) and the same files work in
every slot. Don't add a `<base>` tag either; Fresh Breath adds the right one
itself when it needs to.
:::

::: warning The database isn't slotted
Only the web files have slots. The app's database, its data files and its
services are shared by all three copies. Test with fake data.
:::

Some apps never need staging at all. They can live on Development forever —
the bare address serves it by default.

Deploying is a button on the app's hosting panel. (Or, from a script,
`POST /api/apps/{nonce}/deploy` with `{"target":"staging"}`.) Your agent's
edits always land in Development, so it can't push anything live by
accident.
