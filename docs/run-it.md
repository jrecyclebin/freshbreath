---
episode: 2
title: Run it, and make yourself
icon: 🚀
minutes: 4
blurb: Get the server going and give yourself a login.
short: Download it and run it — it shows up at localhost:9009. Then make yourself a Superuser with an SSH passphrase, and switch the control panel over to that login.
---

## Start the server

Pick whichever way suits you. They all end the same: Fresh Breath running at
`http://localhost:9009/`.

::: code-group

```txt [Release ZIP]
1. Grab a ZIP from github.com/jrecyclebin/freshbreath/releases
2. Unzip it anywhere
3. Run the freshbreath app inside
```

```bash [Mise]
mise use -g github:jrecyclebin/freshbreath
freshbreath
```

```bash [Docker]
docker run -d --name freshbreath \
  -p 9009:9009 \
  -v freshbreath-data:/data \
  ghcr.io/jrecyclebin/freshbreath:latest
```

:::

::: tip The unzip sweet spot
Run it straight from the unzipped folder and everything finds itself: the
control panel files sit next to the binary and the database lands in the
current folder. No settings needed.
:::

## Make yourself

Right now the control panel is wide open. Fix that first: make a user for
yourself, then make the panel ask for that user's passphrase.

<Checklist title="Do this in your browser">

- Open **localhost:9009/control/users** and add a user — fill everything in and make yourself a **Superuser**.
- At the bottom of that form, set an **SSH passphrase**.
- Open **localhost:9009/control/settings** and change **Admin auth** to **Built-in – SSH key**.
- Refresh, and sign in with the passphrase you just made.

</Checklist>

::: warning Picking an SSO for Admin auth later?
Every app and service with an empty *Protected by* inherits it — and lets in
anyone that provider signs in, not just Fresh Breath users. A company SSO is
made for that. A generic GitHub or Google login signs in the whole internet.
Episode 6 lets you see this for yourself.
:::

## Want HTTPS?

Plain HTTP works for the control panel and hosted apps. But apps you open
from your disk (`file://`) or from another site can't *stay* signed in over
HTTP: browsers only send a cross-site cookie over HTTPS.

On a fresh install, the control panel's onboarding offers to make a
certificate for you: it creates a certificate authority for this install,
issues a certificate, and writes the settings. Download the CA from the
panel, trust it on each device, and restart.

::: details Doing it yourself with mkcert
```bash
mkcert -install
mkcert localhost 127.0.0.1 ::1
```

Then put these in a `.env` file next to the binary:

```bash
FRBR_TLS_CERT=./localhost+2.pem
FRBR_TLS_KEY=./localhost+2-key.pem
FRBR_BASE_URL=https://localhost:9009
```

Node-based clients such as Claude Code also need `NODE_EXTRA_CA_CERTS`
pointed at the CA.
:::
