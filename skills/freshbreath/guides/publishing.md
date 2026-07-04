# How to Publish Apps to Fresh Breath

Fresh Breath can *host* your static app as well as connecting it to services. You
hand it a single `.html` file or a `.zip` of a whole site, and it serves the
result at a tidy URL on the same origin as the server. Same origin means
`/frbr.js` and relative service calls Just Work — no CORS dance, no `file://`
quirks.

---

## 1. What You Can Upload

Every hosted app lives under an existing app's nonce. You publish one of two
things:

- **A single `.html` file** — saved as `index.html`. Perfect for the classic
  one-file Fresh Breath app.
- **A `.zip` archive** — extracted into the app's web directory. The entry point
  is `index.html`; if there isn't one, the first `.html` file alphabetically is
  renamed to `index.html`. Bring along your CSS, JS, images, whatever.

Each publish **replaces** the previous contents — the web directory is wiped and
rewritten, not merged. Treat it like a deploy, not a sync.

---

## 2. Publish over MCP

The tools:

- **`publish_app_files`** — `{ nonce, filename }` where `filename` carries the
  `.html` or `.zip` extension so the server knows how to handle it. Returns
  `{ url }`. **POST the raw file bytes** to that URL (e.g. `curl -X POST
  --data-binary @site.zip <url>`) to publish; it returns the route.
- **`download_app_files`** — `{ nonce }` → `{ url, filename }`. **GET the url**
  to stream back the `.zip`.
- **`list_app_files`** — `{ nonce }` → `{ files }`, each `{ path, size }`,
  sorted by path. Empty when nothing's published. A quick peek at what's hosted
  without pulling the whole zip.

Transfer URLs are single-use and expire after **5 minutes** — mint one right
before you move the bytes. Under the hood it's the same core code as the HTTP
handlers, so behavior is identical: single `.html` becomes `index.html`, `.zip`
is extracted, each publish replaces the last.

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
- **Legacy inline data** — if you cannot POST bytes to the transfer URL, the
  `publish_app_files` tool accepts an optional `legacy_data` field. For `.zip`
  uploads this must be base64-encoded; for `.html` uploads it is plain text.
  When `legacy_data` is provided the file is published directly and the tool
  returns `{ route }` with no transfer URL. This is discouraged because it
  clutters the context window. `download_app_files` has a matching
  `legacy_mode: true` option that returns the zip as base64 `data` instead of
  a URL.
