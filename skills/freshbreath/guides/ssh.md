# SSH & File Sync Guide for Fresh Breath Apps

Fresh Breath can hold an SSH key for a user and open authenticated connections to
remote servers *on the app's behalf* — so a browser app, which can't speak SSH
and must never see a private key, can still list, push, and pull files on a real
remote host. The private key lives encrypted at rest and, while you're working,
decrypted only in the server's memory. The browser only ever holds a short-lived
token.

The shape of it:

1. A Fresh Breath **user** generates an Ed25519 key (passphrase-protected).
2. You install that **public** key on the remote host's `authorized_keys`.
3. The user **logs in** with their passphrase — this loads the decrypted key into
   the server's in-memory agent *and* hands your app an access token. (Same auth
   flow described in the auth guide.)
4. Your app **opens a session** to a host and gets a `sessionId`.
5. You use that `sessionId` to **sync files** (list / diff / upload / download /
   delete) over SFTP.

---

## 0. Prerequisites

Before any `/ssh` or `/sync` call will work:

- The caller must be a **registered Fresh Breath user** (the key belongs to a
  user account, not to an anonymous app visitor).
- Your app must have an **`ssh` service** registered and **allowed** for it. An
  admin sets this in the control panel. Without it the API returns
  `403 App does not have ssh service access`.
- Every `/ssh/*` and `/sync/*` request must carry the **`X-App-Nonce`** header
  and an **`Authorization: Bearer <token>`** header (the token you get back from
  the SSH login in step 3).

Members can only reach the SSH API for apps they belong to; Admins and
Superusers bypass the membership check.

---

## 1. Generate a Key

`POST /api/me/ssh-key` with a passphrase mints a fresh Ed25519 key for the
current user. The private key is encrypted with Argon2id → AES-256-GCM and stored;
the plaintext private key never leaves the server.

```js
const res = await fetch("/api/me/ssh-key", {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
  body: JSON.stringify({ passphrase: "the-user-passphrase" }),
});
const { ssh_key } = await res.json();
// ssh_key.public_key  — OpenSSH authorized_keys line, install this on remotes
// ssh_key.fingerprint — SHA256 fingerprint
// ssh_key.key_type    — "ed25519"
```

- **`GET /api/me/ssh-key`** returns the stored key info (public key + fingerprint)
  or `null` if none exists.
- **`DELETE /api/me/ssh-key`** removes it.

Most people do this once from the control panel's profile area rather than in app
code. Either way, the **public key** is what you copy into the remote host's
`~/.ssh/authorized_keys`. The passphrase is needed again at login time — Fresh
Breath can't recover it, so don't lose it.

---

## 2. Log In (load the key into the agent)

The key sits encrypted until someone proves the passphrase. Proving it is a
*login method*, not a property of the SSH service: gate your app with the
built-in **SSH Key** auth record (its "Protected by" slot), and the ordinary
login flow runs the passphrase form.

```js
import { login } from "https://localhost:9009/frbr.js?app-nonce";

const session = await login();   // the app's own gate — here, SSH Key
```

This pops a small form asking for the user's **email + passphrase**. On success,
two things happen at once:

- The decrypted key is loaded into the server's in-memory **agent** with a
  **1-hour TTL**. That's the window in which the server can open SSH connections
  for this user.
- Your app holds an `AuthSession` carrying a Fresh Breath access token.
  **That's the bearer for every `/ssh` and `/sync` call below.**

The `ssh://` service still exists and your app still needs it linked — that's
what grants access to the `/ssh` and `/sync` endpoints. It just no longer
carries the login; the auth record does.

> Note: the agent TTL (1h) is deliberately decoupled from the web token and from
> open sessions. If the agent key expires you can still hold a valid token and an
> already-open SSH session — but you won't be able to open a *new* session until
> the user logs in again (you'll get `401 no active SSH key`).

A small helper to keep the boilerplate down. Ask the session to set the header
rather than reading the token out of it — the session knows what its own kind
implies, and it keeps working when the app's gate changes:

```js
const sshFetch = (path, init = {}) => {
  const headers = new Headers(init.headers);
  headers.set("X-App-Nonce", APP_NONCE);
  session.addAuth(headers);
  return fetch(path, { ...init, headers });
};
```

---

## 3. Open a Session

`POST /ssh/sessions` dials the host using the agent's key and opens both SSH and
SFTP. You get back a `sessionId` that everything else hangs off of.

```js
const res = await sshFetch("/ssh/sessions", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    host: "example.com",
    port: 22,            // optional, defaults to 22
    username: "deploy",  // optional, defaults to the user's email
  }),
});
const { sessionId, expiresAt } = await res.json();
```

Sessions live for **8 hours** (independent of the 1-hour agent TTL — once the
connection is open, it stays open). Inspect or close them:

- **`GET /ssh/sessions/{id}`** → `{ sessionId, host, port, username, connectedAt, expiresAt }`
- **`DELETE /ssh/sessions/{id}`** → closes the SSH + SFTP connection (`204`).
  Always close sessions you're done with; they also expire lazily on their own.

### Host key verification (TOFU)

The first time Fresh Breath connects to a host, it records that host's key
(Trust On First Use) — exactly like your OpenSSH client writing to
`known_hosts`. On later connections the key must match; if it changed, the
connection is **rejected** with a clear error (it could be a legitimate rotation,
or a MITM). To accept a new key after a genuine rotation:

- **`GET /ssh/known-hosts`** → list stored host keys (`host`, `port`,
  `fingerprint`, `trustedAt`).
- **`DELETE /ssh/known-hosts/{host}:{port}`** → forget a host so the next connect
  re-trusts it. (Admin-ish housekeeping — do it deliberately.)

---

## 4. File Sync

With a `sessionId` in hand, the `/sync` API does file transfer over the session's
SFTP channel. Paths are remote paths on the host you connected to; they're
cleaned server-side to block traversal (`..`) tricks. Dot-files are hidden from
listings.

### List a directory

`GET /sync/files?sessionId=...&path=/some/dir` (path defaults to `/`).

```js
const res = await sshFetch(
  `/sync/files?sessionId=${sessionId}&path=${encodeURIComponent("/var/www")}`
);
const { files } = await res.json();
// each: { path, name, size, isDir, updatedAt }
```

### Upload a file

`PUT /sync/files/{path}?sessionId=...` — the request body *is* the file content.
Parent directories are created as needed. Pass an **`X-Hash`** header with the
SHA-256 of the content and the server verifies it after writing, deleting the
file and returning `409` on mismatch (cheap integrity guard).

```js
async function uploadFile(remotePath, content /* Blob | ArrayBuffer | string */) {
  const buf = content instanceof Blob ? await content.arrayBuffer()
            : typeof content === "string" ? new TextEncoder().encode(content)
            : content;
  const digest = await crypto.subtle.digest("SHA-256", buf);
  const hash = [...new Uint8Array(digest)]
    .map(b => b.toString(16).padStart(2, "0")).join("");

  const res = await sshFetch(
    `/sync/files/${remotePath}?sessionId=${sessionId}`,
    { method: "PUT", headers: { "X-Hash": hash }, body: buf }
  );
  return res.json(); // { path, hash }
}
```

### Download a file

`GET /sync/files/{path}?sessionId=...` streams the raw bytes
(`application/octet-stream`).

```js
const res = await sshFetch(`/sync/files/${remotePath}?sessionId=${sessionId}`);
const blob = await res.blob();
```

### Delete a file

`DELETE /sync/files/{path}?sessionId=...` → `204`.

---

## 5. Diff-based Sync

The clever bit. Rather than re-uploading everything, ask the server what's
actually different. `POST /sync/files/diff` takes your local file list (path +
SHA-256 hash) and a `basePath`; it walks the remote tree under that base and
hashes every file, then tells you what to do.

```js
const res = await sshFetch("/sync/files/diff", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    sessionId,
    basePath: "/var/www",
    files: [
      { path: "/var/www/index.html", hash: "<sha256>" },
      { path: "/var/www/app.js",     hash: "<sha256>" },
    ],
  }),
});
const { upload, delete: toDelete } = await res.json();
```

The response is two path lists, computed one-way (your local set is the source of
truth):

- **`upload`** — remote files whose hash differs from yours, **plus** files you
  have that the remote is missing. Push these with the upload call above.
- **`delete`** — files present on the remote but absent from your list. Remove
  these with the delete call. Files with matching hashes appear in neither list —
  nothing to do.

So a full one-way mirror is: diff → `PUT` everything in `upload` → `DELETE`
everything in `delete`. That's "make the remote look like my local folder," and
it skips every byte that's already in place.

---

## 6. Putting It Together

```js
import { login } from "https://localhost:9009/frbr.js?your-app-nonce";

// The app is gated by the built-in SSH Key record, so this is the
// passphrase form — and it loads the key into the agent on the way through.
const session = await login();

const sshFetch = (path, init = {}) => {
  const headers = new Headers(init.headers);
  headers.set("X-App-Nonce", APP_NONCE);
  session.addAuth(headers);
  return fetch(path, { ...init, headers });
};

// open
const { sessionId } = await (await sshFetch("/ssh/sessions", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ host: "example.com", username: "deploy" }),
})).json();

// list
const { files } = await (await sshFetch(
  `/sync/files?sessionId=${sessionId}&path=/var/www`
)).json();
console.log(files);

// close when done
await sshFetch(`/ssh/sessions/${sessionId}`, { method: "DELETE" });
```

---

## Notes

- **Two tokens, two clocks.** The browser holds a Fresh Breath access token
  (used as the bearer). The server holds the decrypted key in its agent for 1
  hour. Open SSH sessions last 8 hours. They expire independently — a stale agent
  blocks *new* sessions but not existing ones.
- **`X-App-Nonce` is mandatory** on every `/ssh` and `/sync` request. Forgetting
  it returns `401 Missing X-App-Nonce header`, not a 400 — easy to misread.
- **The `/ssh` and `/sync` endpoints are not service-proxied.** Unlike
  `service.fetch()` (which routes through `/service/{id}/`), you call these paths
  directly with your own headers. The `sshFetch` helper above is the pattern.
- **Path safety.** Remote paths are cleaned server-side; `..` can't escape. Hidden
  dot-files are filtered from directory listings (but you can still target one
  explicitly by path for upload/download/delete).
- **Integrity.** Upload's `X-Hash` is optional but recommended — it turns a
  silent corrupt write into a loud `409`. Diff relies on the same SHA-256s, so
  reuse them.
- **No remote command execution (yet).** Sessions currently expose **SFTP file
  operations only** — there's no endpoint to run shell commands or `git` over the
  session. If you need to drive Git on the remote, that capability isn't wired up
  through this API today.
