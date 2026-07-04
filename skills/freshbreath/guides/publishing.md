# How to Publish Apps to Fresh Breath

Fresh Breath can *host* your static app as well as connecting it to services. You
hand it files — a single `index.html`, a stylesheet, a script bundle, whatever —
and it serves the result at a tidy URL on the same origin as the server. Same
origin means `/frbr.js` and relative service calls Just Work — no CORS dance, no
`file://` quirks.

---

## 1. What You Can Upload

Every hosted app lives under an existing app's nonce. You can manage individual
files in the app's web directory:

- **`index.html`** — the entry point. Required for the app to be served.
- **Any other files** — CSS, JS, images, JSON, etc. They keep their relative
  paths.

Treat writes like a deploy: you can replace a whole file or patch a single chunk
of text, but there is no automatic merge of directories.

---

## 2. Publish over MCP

The MCP tools work like the file tools LLMs are used to:

- **`list_app_files`** — `{ nonce, search? }` → `{ files }`, each
  `{ path, size }`, sorted by path. Empty when nothing's published. If `search`
  is provided, only files whose path or content contains the term are returned.
- **`read_app_file`** — `{ nonce, path, offset?, limit? }` → `{ content }`,
  optionally with `{ encoding: "base64" }` for binary files. `offset` and
  `limit` are zero-based byte bounds for reading chunks.
- **`write_app_file`** — `{ nonce, path, content, old_text? }` →
  `{ status: "written" }`. Without `old_text` the entire file is replaced.
  With `old_text`, the single occurrence of `old_text` is replaced with
  `content`. An error is returned if `old_text` is not found or appears more
  than once.
- **`delete_app_file`** — `{ nonce, path }` → `{ status: "deleted" }`.

These tools read and write file data directly — no transfer URLs or base64
round-trips.

---

## Notes

- **App nonce** is the 48-char hex id from the Apps list (or `/api/apps`).
- **Replace, not merge** — writing a file replaces it; writing `index.html`
  replaces the previous entry point. If you want incremental file-level updates
  to a *remote* host instead, that's the SSH file sync API. See `guides/ssh.md`.
- **Same-origin perks** — once hosted, your app can `import` from `/frbr.js`
  (relative) and make non-proxied service calls without CORS headaches. The
  `file://` caveats in the auth guide simply don't apply.
