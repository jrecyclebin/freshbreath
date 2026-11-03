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

### Files over HTTP

Each app has two file areas, `web` and `data` (see below), and every path in
them has a URL:

- **`GET /api/apps/{nonce}/web/<path>`** — a folder lists its files as
  `{ files: [{ path, size }] }` (paths relative to the area's root, sorted);
  a file returns its bytes. A bare `GET …/web` lists everything, and
  `?search=<term>` filters a listing by path or content.
- **`PUT …/web/<file>`** — replaces the file with the raw request body.
- **`DELETE …/web/<file>`** — deletes the file. A bare `DELETE …/web` clears
  the whole Development slot.
- **`POST …/web[/<folder>]`** — a bulk upload (below).
- **`GET /api/apps/{nonce}/download`** — the whole Development slot as a zip.

Swap `web` for `data` for the private area; it works the same, except that a
bulk upload adds to a folder rather than replacing it.

Plain paths go in the URL as written (`…/web/blog/2026`). Only characters that
mean something in a URL — `?`, `#`, `%`, spaces — need escaping, one path
segment at a time; from an app, `escapePath()` in `/frbr.js` does it for you.

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

Add a folder to the URL to replace **just that folder** —
`POST /api/apps/{nonce}/web/blog/2026` clears `blog/2026` and puts the
bundle there, leaving the rest of the app alone. A folder upload needs no
HTML file.

HTML is served in single-page app style: any URL path that doesn't resolve to
a file is sent to the nearest `index.html` at or above it — `/blog/post/1` goes
to `blog/index.html` if there is one, otherwise the root `index.html`. You can
use common routers without needing to stick to hash paths, and give a
subdirectory its own app.

---

## 2. Publish over MCP

The MCP tools work like the file tools LLMs are used to. Paths are always
relative to the area's root, and `dir` narrows a call to one folder
(`"blog/2026"`; omit it for the whole area).

- **`list_app_files`** — `{ nonce, area?, dir?, search? }` → `{ files }`, each
  `{ path, size }`, sorted by path. Empty when nothing's published; a `dir`
  that isn't there is an error. If `search` is provided, only files whose path
  or content contains the term are returned.
- **`search_app_files`** — `{ pattern, nonce?, area?, dir?, ignore_case? }` →
  `{ matches, truncated }`, each match `{ nonce, app, path, line, text }`.
  Works like `grep`/`rg`: `pattern` is a regular expression (RE2 syntax)
  matched per line, and `line` is 1-based. Omit `nonce` to search every app you
  can access (apps without `dir` are skipped). Binary files are skipped, long
  lines are clipped around the match, and results stop at 500 matches with
  `truncated: true`. Over HTTP:
  `GET /api/apps/search?pattern=…&nonce=…&area=…&dir=…&ignore_case=true`.
- **`read_app_files`** — `{ nonce, files, area?, transport }`, where each of
  `files` is a path or `{ path, offset?, limit? }` → `{ files }`, each
  `{ path, content }` (with `encoding: "base64"` for binary files), or
  `{ path, error }`. `offset` and `limit` are zero-based byte bounds for
  reading chunks. `transport` (see §3) selects inline vs. fetch URLs.
- **`write_app_files`** — `{ nonce, files, area?, dir?, transport }`, each of
  `files` `{ path, new_text?, old_text?, encoding? }` (paths relative to
  `dir`) → `{ files }`, each `{ path, status: "written" }` or `{ path, error }`.
  Without `old_text` the entire file is replaced with `new_text`; with
  `old_text`, its single occurrence is replaced with `new_text`, and it's an
  error if it's missing or appears more than once. `encoding: "base64"` sends
  binary `new_text` (whole files only). Other files in the folder are left
  alone. Every path is checked before anything is written.
- **`delete_app_file`** — `{ nonce, path, area? }` → `{ status: "deleted" }`.

`read_app_files` and `write_app_files` report each file on its own, so one bad
file doesn't stop the rest — but if any file failed, the whole result is
flagged as an error.

### Web files vs. data files

Every tool above takes an optional `area`:

- **`"web"`** (default) — the Development slot. Everything here is served by
  the web server and copied to Staging/Production on deploy. (Note that
  only the web content has these slots - all data and services assigned to
  the app are shared between all the slots.)
- **`"data"`** — private storage beside the web files that is **never served**
  and never deployed. Use it for things like user uploads. Replacing or
  deleting the web files leaves it alone; deleting the app removes it.

Over HTTP, data files live at `/api/apps/{nonce}/data/<path>` and work like
web files (§1). A multipart `POST …/data[/<folder>]` adds one or more `file`
parts at their filenames' paths, stored as-is (zips are not expanded),
replacing same-named files and leaving the rest. Data files are returned with
`Content-Security-Policy: sandbox`, so an uploaded HTML or SVG file can't run
script on the server's origin.

---

## 3. Large files: the `transport` argument

`read_app_files` and `write_app_files` take a `transport` argument, `"inline"`
or `"http"`. It decides how the bytes travel.

**`"inline"` — in the tool call.** The tool result carries the bytes directly,
as in §2.

A whole-file inline read (no `offset`/`limit`) of a file **larger than
10 KiB escapes automatically** to a URL, returning `{ path, error, url,
method, size, content_type, max_inline_bytes }` for that file instead of its
content. (That's not counted as a failure — the URL is the answer.)

**`"http"` — return URLs, fetch them yourself.** The tool returns a
short-lived URL per file, good for **10 minutes**. No auth header needed —
the ticket *is* the auth, granting you access to read or write the specific
file.

- **`read_app_files`, `transport:"http"`** → each `{ path, url, method:"GET",
  size, content_type }`. GET the URL. Incompatible with `offset`/`limit` — the
  URL targets the whole file.
- **`write_app_files`, `transport:"http"`** → each `{ path, url, method:"PUT" }`.
  PUT your bytes to the URL. Incompatible with `old_text` — patches always
  stay inline (they're small).

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
