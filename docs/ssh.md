---
advanced: 3
title: SSH and file sync
icon: 🔐
minutes: 5
blurb: Let a browser app push files to a real server, without ever holding a key.
short: Fresh Breath keeps an SSH key for each user, sealed with their passphrase. Signing in unseals it into the server's agent for an hour; your app opens a session to a host and syncs files over it, all through api(). The browser never sees the key.
---

## Your key

A browser app can't speak SSH, and must never hold a private key. So Fresh
Breath holds it for you: one Ed25519 key per user, encrypted with their
passphrase. Onboarding already made yours.

1. @you Get your **public** key: from your profile in the control panel, or
   just ask.
2. @you Add it to `~/.ssh/authorized_keys` on the server you want to reach.
3. **Fresh Breath** can now connect there as you, whenever you've unlocked
   the key with your passphrase.

<Say>
Show me my Fresh Breath SSH public key, and tell me how to add it to authorized_keys on [your server].
</Say>

::: info 🗝️ Think of it like a key safe
The key lives in a safe on the server. Your passphrase opens the safe for an
hour at a time. Your app never gets the key, only permission to ask the
server to use it.
:::

## Sign in and connect

An app that uses SSH needs two things set up in the control panel:

1. **Protected by** the built-in **SSH key** sign-in, so that signing in is
   typing the passphrase, which unlocks the key.
2. The server's **ssh** service linked to it. That's what lets it reach the
   SSH and sync endpoints.

Then it's `login()`, and `api()` for everything after:

```js
import { login, api } from "/frbr.js";

const session = await login();   // the passphrase form; unlocks the key for 1 hour

const res = await api(session, "/api/ssh/sessions", {
  method: "POST",
  body: { host: "example.com", username: "deploy" },
});
const { sessionId } = await res.json();   // good for 8 hours
```

`api(session, path, options)` is `fetch` for Fresh Breath's own API. It adds
the app's nonce and the session's token, refreshes the token when it needs
to, and sends an object `body` as JSON. A string, `Blob` or `File` goes
through untouched, so file contents arrive byte for byte.

## Sync a folder

With a session open, the sync API lists, uploads, downloads and deletes
files on the remote host. The clever bit is the **diff**: send your files'
hashes, and the server tells you only what's different.

<SyncSim />

```js
const { upload, delete: gone } = await (await api(session, "/api/sync/files/diff", {
  method: "POST",
  body: { sessionId, basePath: "/var/www", files: [{ path: "/var/www/index.html", hash }] },
})).json();

for (const path of upload) {
  await api(session, `/api/sync/files${path}?sessionId=${sessionId}`,
    { method: "PUT", headers: { "X-Hash": hashOf[path] }, body: fileFor[path] });
}
for (const path of gone) {
  await api(session, `/api/sync/files${path}?sessionId=${sessionId}`, { method: "DELETE" });
}
```

Your folder is the source of truth: `upload` is what's changed or missing
remotely, `delete` is what's there that you don't have. Files that match
never move. The `X-Hash` header makes the server check each upload, and
refuse a corrupt one with a `409`.

<Say>
Read the Fresh Breath SSH guide. Then add a Deploy button to my app [app] that mirrors its build folder to /var/www on [your server] using the diff, through api().
</Say>

## Two clocks, and a few more things

1. **The unlocked key lasts an hour.** After that, opening a *new* session
   needs the passphrase again. `api()` sees the server ask and shows the
   passphrase form for you, then retries. Nothing for your code to do.
2. **A session lasts eight hours**, whatever the key's doing. Past that its
   id still works for another day: the next call reconnects it (after the
   passphrase, if the key has lapsed).
3. **Host keys are trusted on first use**, like your own `known_hosts`. If a
   host's key changes later, the connection is refused until someone
   deliberately forgets the old one.
4. **Git, too.** The same key can sign commits and pull from or push to a
   repository over SSH, through `/api/ssh/git/…`.

::: warning Files, not shells
Sessions do file operations over SFTP. There's no endpoint for running
commands on the remote host.
:::

::: details Under the hood
The key is encrypted at rest with Argon2id and AES-256-GCM, and decrypted
only into the server's memory. The `/api/ssh` and `/api/sync` endpoints
aren't proxied services: you call them directly, which is exactly what
`api()` is for. Every request needs the `X-App-Nonce` header (missing, it's
a `401`, not a `400`), and sessions answer only to the user who opened them.
:::
