# Hosting Apps

<div class="short">

Give Fresh Breath your files and it serves them at `/your-app`. Every change lands in **Development**; deploy to copy it to **Staging**, then **Production**. Only the pages have slots — the database, data files and services are shared by all three.

</div>

<div class="like" data-icon="📝">

**Think of draft, review copy and final.** You keep editing the draft. When it's good you freeze a review copy, and when that passes it becomes the version everyone sees.

</div>

## Where an app lives

An app's **URL** decides where it's served:

| You set | It's served at | Notes |
|---|---|---|
| A slug, like `blog` | `/blog/` | Slashes are tidied for you. No subdirectories — make a `blog` app and publish into its `admin` folder. `api`, `control`, `mcp`, `service` and `oauth` are taken. |
| A full URL on a listed host | `https://blog.example.com/notes` | Apps sit one level down on a host, never at its root. See *Hosts* in the [tour](/tour#beyond-one-machine). |
| Any other full URL | wherever it runs | `https://myapp.netlify.app` or a `localhost` dev server. Fresh Breath doesn't serve it, but lets it call in with its nonce. |
| Nothing | the slug of its name — or, with no files, your disk | A page you open from `file://` works with no setup at all. |

## Getting files up

<div class="facts">

- 🖱️ **Drag and drop** Drop files or a zip onto the app's upload area in the control panel.
- 🤖 **Your agent** `write_app_files` over the MCP — whole files, or a single patch with `old_text`. [The prompts.](/agents#things-to-say)
- 🧰 **curl** `POST /api/apps/{nonce}/web` with `file` parts replaces the Development slot in one go.
- 🛰️ **Another Fresh Breath** Transfer apps to a remote, or ship remote updates. Both land in Development.

</div>

A zip is expanded where it sits, a single top-level folder is unwrapped (so uploading `dist/…` publishes its contents), and if there's no `index.html` the first `.html` file becomes it. A bad upload is refused before anything is replaced, so the live files stay put. Up to 100 MB per request.

::: details Under the hood: the upload over curl
```sh
curl -F "file=@index.html" -F "file=@dist/site.css;filename=css/site.css" \
     https://<server>/api/apps/<nonce>/web
```
Add a folder to the URL — `…/web/blog/2026` — to replace just that folder and leave the rest alone. Swap `web` for `data` to add to the private area instead.
:::

## Keep links relative

The same files are served at `/tix/`, `/tix@dev/`, `/tix@staging/` and `/tix@prod/`. A link like `/tix/app.js` names one of them and misses in the others.

<div class="steps">

1. **Your own files: relative.** `app.js`, `./css/site.css`. Never a hardcoded base path.
2. **Fresh Breath's: absolute.** `/frbr.js` and `/api/...` stay as they are.
3. **No `<base>` tag.** For deeper URLs — `/tix/projects/42` answered by `index.html` — the server adds the right `<base>` itself, slot and all.

</div>

Routing is single-page style: a path that doesn't match a file goes to the nearest `index.html` at or above it, so ordinary routers work without hash paths.

## Three slots, one database

<SlotSim />

What to notice:

- **Edits only ever touch Development.** Production doesn't move until you deploy.
- **The bare address follows the default environment.** Flip it to Production when you go live.
- **An undeployed slot is an honest 404.** Fresh Breath won't quietly serve a different slot.
- **Restore** copies Staging back over Development — handy when a dev upload goes sideways.
- **The database doesn't have slots.** A row saved from `@dev` is in Production too. Test with fake data, and be careful with migrations.

Small personal apps can live on Development forever — the bare address serves it by default.

## Web files and data files

| Area | Served? | Has slots? | Good for |
|---|---|---|---|
| `web` | Yes, at the app's address | Yes | The app itself |
| `data` | Never | No | Uploads, backups, files a script keeps |

Data files come back with a sandboxing security header, so an uploaded HTML or SVG file can't run script on your server's origin. Deleting the app deletes both areas.

## Apps that aren't hosted

You don't have to host an app to use Fresh Breath. A page opened from disk works as long as it names its app in the script URL:

```html
<script type="module" src="https://localhost:9009/frbr.js?YOUR-NONCE"></script>
```

The nonce is that app's whole credential, so the server never tells it to anyone who doesn't already have it. A great way to test before publishing — but only open pages from disk that you trust, since any local file can read the same browser storage.

::: tip Plain HTTP and off-server apps
Browsers only send a cross-site cookie over HTTPS, so an app on `file://` or another origin can't stay signed in to a plain-HTTP server for long. Turn on HTTPS — the control panel offers to make a certificate.
:::
