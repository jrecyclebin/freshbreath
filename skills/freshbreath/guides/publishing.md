# How to Publish Apps to Fresh Breath

Fresh Breath can *host* your static app, not just connect it to services. You
hand it a single `.html` file or a `.zip` of a whole site, and it serves the
result at a tidy URL on the same origin as the server. Same origin means
`/frbr.js` and relative service calls Just Work — no CORS dance, no `file://`
quirks.

This is an **admin operation**. Uploading, downloading, and deleting hosted
files all require the **Admin** or **Superuser** role. (Members can *use* a
hosted app, but they can't publish to it.)

---

## 1. What You Can Upload

Every hosted app lives under an existing app's nonce. You publish one of two
things:

- **A single `.html` file** — saved as `index.html`. Perfect for the classic
  one-file Fresh Breath app.
- **A `.zip` archive** — extracted into the app's web directory. The entry point
  is `index.html`; if there isn't one, the first `.html` file alphabetically is
  renamed to `index.html`. Bring along your CSS, JS, images, whatever.

Anything else (a stray `.js`, a `.tar.gz`) is rejected. Over HTTP the upload cap
is **50 MB**.

Each publish **replaces** the previous contents — the web directory is wiped and
rewritten, not merged. Treat it like a deploy, not a sync.

---

## 2. Where It Gets Served

A published app appears at `/{slug}/` on the Fresh Breath server. The slug is
derived from the app:

- If the app's **URL** field is a bare path (no `://`), that path *is* the slug —
  e.g. URL `my-cool-app` → served at `/my-cool-app/`.
- Otherwise the slug is a slugified version of the app's **name** —
  e.g. "My Cool App" → `/my-cool-app/`.

You can also point the server's `default_app` setting at a hosted app, and then
`/` redirects straight to it. Set that in the control panel under Settings.

---

## 3. Publish over HTTP

`POST /api/apps/{nonce}/web` with a `multipart/form-data` body and the file in a
field named `file`. You must be authenticated as Admin/Superuser (a panel JWT in
the `Authorization` header or session cookie).

```js
const form = new FormData();
form.append("file", fileBlob, "index.html"); // or "site.zip"

const res = await fetch(`/api/apps/${APP_NONCE}/web`, {
  method: "POST",
  headers: { Authorization: `Bearer ${adminToken}` },
  body: form, // don't set Content-Type — the browser adds the multipart boundary
});

const { route } = await res.json(); // e.g. "/my-cool-app"
```

**Returns:** `{ "route": "/<slug>" }` — where the app now lives.

From a plain shell, the same thing with `curl`:

```bash
curl -X POST "https://localhost:9009/api/apps/$NONCE/web" \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@site.zip"
```

---

## 4. Download What's Published

`GET /api/apps/{nonce}/web` streams the current web directory back as a `.zip`
(`Content-Disposition` names it `<slug>.zip`). Handy for grabbing a backup or
seeing exactly what's live.

```js
const res = await fetch(`/api/apps/${APP_NONCE}/web`, {
  headers: { Authorization: `Bearer ${adminToken}` },
});
const blob = await res.blob(); // a zip of the hosted files
```

Returns `404` if nothing has been uploaded yet.

---

## 5. Unpublish

`DELETE /api/apps/{nonce}/web` removes the web directory and clears the app's
hosting details. The route stops resolving immediately.

```js
await fetch(`/api/apps/${APP_NONCE}/web`, {
  method: "DELETE",
  headers: { Authorization: `Bearer ${adminToken}` },
});
```

Returns `204 No Content` on success.

---

## 6. Publish over MCP

If you're driving Fresh Breath from an agent or MCP client instead of a browser,
the central MCP server exposes the same three operations as tools (all gated to
Admin+):

- **`publish_app_files`** — `{ nonce, filename, content }` where `content` is the
  **base64-encoded** bytes of your `.html` or `.zip`, and `filename` carries the
  extension so the server knows how to handle it. Returns the route.
- **`download_app_files`** — `{ nonce }` → base64-encoded zip + filename.
- **`delete_app_files`** — `{ nonce }` → removes the hosted files.

These mirror the HTTP handlers exactly (same core code underneath), so behavior
is identical — single `.html` becomes `index.html`, `.zip` is extracted, each
publish replaces the last.

---

## 7. The Control Panel

Don't forget the easy path: the **Apps** section of the Fresh Breath control
panel has upload / download / delete buttons per app. Same operations, same
permissions, no code. Good for one-off publishes and for grabbing the app nonce
you'll need everywhere else.

---

## Notes

- **App nonce** is the 48-char hex id from the Apps list (or `/api/apps`). It's
  the `{nonce}` in every URL above.
- **Replace, not merge** — re-uploading wipes the old web dir first. If you want
  incremental file-level updates to a *remote* host instead, that's the SSH file
  sync API, not this. See `guides/ssh.md`.
- **Same-origin perks** — once hosted, your app can `import` from `/frbr.js`
  (relative) and make non-proxied service calls without CORS headaches. The
  `file://` caveats in the auth guide simply don't apply.
- **Entry point rule** for zips: `index.html` wins; otherwise the alphabetically
  first `.html` is promoted to `index.html`. Name your landing page
  `index.html` and skip the guesswork.
