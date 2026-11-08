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

## Links that work in every slot

The same files are served at `/tools/`, `/tools@dev/`, `/tools@staging/`
and `/tools@prod/`. And because apps are served single-page style, a deep
URL like `/tools/email/review/42` is answered by the nearest `index.html`
above it. A page's relative links would point at the wrong folder there,
so Fresh Breath fixes them up: it adds a `<base>` tag pointing at the
page's own folder, slot and all.

Pick a URL and follow what the server does.

<BaseSim />

What that means for your app:

1. **Use relative links** for the app's own files (`app.js`, `css/site.css`).
   With the `<base>` Fresh Breath adds, they find the right file at any
   depth, in every slot.
2. **Don't add a `<base>` tag** or a script that builds one. Fresh Breath
   leaves a page with its own alone, and a hand-written one names one slot.
3. **Keep Fresh Breath's own paths absolute**: `/frbr.js`, `/api/…`.
4. **Route with absolute paths** in a single-page app: under an added
   `<base>`, a bare `#section` link resolves against the base too.

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
