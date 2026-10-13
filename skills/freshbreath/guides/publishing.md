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

### Uploading a whole bundle

`POST /api/apps/{nonce}/web` (multipart) replaces the Development slot in one
go. Send one or more `file` parts; each part's filename is its relative path:

```sh
curl -F "file=@index.html" -F "file=@dist/site.css;filename=css/site.css" \
     https://<server>/api/apps/<nonce>/web
```

A `.zip` part is expanded where it sits. The whole set is then treated like
one zip: a single top-level folder shared by every file is unwrapped (so
uploading `dist/…` publishes its contents), and if there's no `index.html`,
the first `.html` file alphabetically becomes it. The upload is checked
before anything is replaced: an upload with no HTML file, a bad zip or an
escaping path is rejected and the live files stay put. 100 MB per request.

HTML is served in single-page app style: any URL paths that don't resolve to
filesystem paths are sent to the index.html. You can use common routers without
needing to stick to hash paths.

---

## 2. Publish over MCP

The MCP tools work like the file tools LLMs are used to:

- **`list_app_files`** — `{ nonce, area?, search? }` → `{ files }`, each
  `{ path, size }`, sorted by path. Empty when nothing's published. If `search`
  is provided, only files whose path or content contains the term are returned.
- **`search_app_files`** — `{ pattern, nonce?, area?, ignore_case? }` →
  `{ matches, truncated }`, each match `{ nonce, app, path, line, text }`.
  Works like `grep`/`rg`: `pattern` is a regular expression (RE2 syntax)
  matched per line, and `line` is 1-based. Omit `nonce` to search every app you
  can access. Binary files are skipped, long lines are clipped around the
  match, and results stop at 500 matches with `truncated: true`. Over HTTP:
  `GET /api/apps/search?pattern=…&nonce=…&area=…&ignore_case=true`.
- **`read_app_file`** — `{ nonce, path, area?, offset?, limit?, transport? }` →
  `{ content }` (with `{ encoding: "base64" }` for binary files). `offset` and
  `limit` are zero-based byte bounds for reading chunks. `transport` (see §3)
  selects inline vs. a fetch URL; whole-file reads over 10 KiB auto-escape to
  a URL even at the default.
- **`write_app_file`** — `{ nonce, path, area?, content, old_text?, transport? }` →
  `{ status: "written" }`. Without `old_text` the entire file is replaced.
  With `old_text`, the single occurrence of `old_text` is replaced with
  `content`. An error is returned if `old_text` is not found or appears more
  than once. `transport` (see §3) selects inline vs. a PUT URL; writes never
  auto-escape.
- **`delete_app_file`** — `{ nonce, path, area? }` → `{ status: "deleted" }`.

### Web files vs. data files

Every tool above takes an optional `area`:

- **`"web"`** (default) — the Development slot. Everything here is served by
  the web server and copied to Staging/Production on deploy.
- **`"data"`** — private storage beside the web files that is **never served**
  and never deployed. Use it for things like user uploads. Replacing or
  deleting the web files leaves it alone; deleting the app removes it.

Over HTTP, data files mirror the web single-file calls on their own route:
`GET`/`PUT`/`DELETE /api/apps/{nonce}/data?file=<path>`, and a bare
`GET /api/apps/{nonce}/data` lists them as `{ files }`. A multipart
`POST /api/apps/{nonce}/data` adds one or more `file` parts at their
filenames' paths, stored as-is (zips are not expanded), replacing same-named
files and leaving the rest. Data files are
returned with `Content-Security-Policy: sandbox`, so an uploaded HTML or SVG
file can't run script on the server's origin.

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
short-lived URL, good for **10 minutes**. No auth header needed —
the ticket *is* the auth, granting you access to read or write the specific
file.

- **`read_app_file`, `transport:"http"`** → `{ url, method:"GET", size,
  content_type }`. GET the URL. Incompatible with `offset`/`limit` — the
  URL targets the whole file.
- **`write_app_file`, `transport:"http"`** → `{ url, method:"PUT" }`. PUT
  your bytes to the URL. Incompatible with `old_text` — patches always stay
  inline (they're small).

**IMPORTANT:** For most files, you'll want to use the http transport with
something like curl - unless you are actually editing file chunks directly on
the server. (Note that Fresh Breath may have self-signed certs.)

---

## Notes

- **App nonce** is the 10-character id from the Apps list (or `/api/apps`).
- **Replace, not merge** — writing a file replaces it; writing `index.html`
  replaces the previous entry point. If you want incremental file-level updates
  to a *remote* host instead, that's the SSH file sync API. See `guides/ssh.md`.
- **Same-origin perks** — once hosted, your app can `import` from `/frbr.js`
  (relative) and make non-proxied service calls without CORS headaches. The
  `file://` caveats in the auth guide simply don't apply.
