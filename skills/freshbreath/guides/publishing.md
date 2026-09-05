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

HTML is served in single-page app style: any URL paths that don't resolve to
filesystem paths are sent to the index.html. You can use common routers without
needing to stick to hash paths.

---

## 2. Publish over MCP

The MCP tools work like the file tools LLMs are used to:

- **`list_app_files`** — `{ nonce, search? }` → `{ files }`, each
  `{ path, size }`, sorted by path. Empty when nothing's published. If `search`
  is provided, only files whose path or content contains the term are returned.
- **`read_app_file`** — `{ nonce, path, offset?, limit?, transport? }` →
  `{ content }` (with `{ encoding: "base64" }` for binary files). `offset` and
  `limit` are zero-based byte bounds for reading chunks. `transport` (see §3)
  selects inline vs. a fetch URL; whole-file reads over 10 KiB auto-escape to
  a URL even at the default.
- **`write_app_file`** — `{ nonce, path, content, old_text?, transport? }` →
  `{ status: "written" }`. Without `old_text` the entire file is replaced.
  With `old_text`, the single occurrence of `old_text` is replaced with
  `content`. An error is returned if `old_text` is not found or appears more
  than once. `transport` (see §3) selects inline vs. a PUT URL; writes never
  auto-escape.
- **`delete_app_file`** — `{ nonce, path }` → `{ status: "deleted" }`.

---

## 3. Large files: the `transport` argument

`read_app_file` and `write_app_file` take an optional `transport` argument,
an enum of `"mcp"` (the default) or `"http"`. It decides how the bytes
travel.

**`"mcp"` (default) — inline.** The tool result carries the bytes directly,
as in §2.

A whole-file `read_app_file` (no `offset`/`limit`) on a file **larger than
10 KiB escapes automatically** to a URL, returning `{ error, url, method,
size, content_type, max_inline_bytes }` instead of the content.

**`"http"` — return a URL, fetch it yourself.** The tool returns a
short-lived URL instead of inlining. The URL is an *act token*: signed,
scoped to that one file and HTTP method, good for **10 minutes**, and
self-authenticating — no auth header needed. It still resolves to your
user, so your app/role permissions apply at the other end.

- **`read_app_file`, `transport:"http"`** → `{ url, method:"GET", size,
  content_type }`. GET the URL. Incompatible with `offset`/`limit` — the
  URL targets the whole file.
- **`write_app_file`, `transport:"http"`** → `{ url, method:"PUT" }`. PUT
  your bytes to the URL. Incompatible with `old_text` — patches always stay
  inline (they're small).

For most files, you'll want to use the http transport with something like
curl.

---

## Notes

- **App nonce** is the 48-char hex id from the Apps list (or `/api/apps`).
- **Replace, not merge** — writing a file replaces it; writing `index.html`
  replaces the previous entry point. If you want incremental file-level updates
  to a *remote* host instead, that's the SSH file sync API. See `guides/ssh.md`.
- **Same-origin perks** — once hosted, your app can `import` from `/frbr.js`
  (relative) and make non-proxied service calls without CORS headaches. The
  `file://` caveats in the auth guide simply don't apply.
