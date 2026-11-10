---
episode: 2
title: Run it, and make yourself
icon: 🚀
minutes: 4
blurb: Get the server going and give yourself a login.
short: Download it and run it — it shows up at localhost:9009. The control panel's first-run onboarding makes you the Superuser and locks the panel behind your SSH passphrase.
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

Open `http://localhost:9009/` and the control panel walks you through first
run. Nobody's signed up yet, so until you finish, the panel is open to anyone
who can reach it.

1. @you **Secure the connection** (optional). On plain HTTP, Fresh Breath
   offers to make its own certificate authority and a certificate for this
   server. Download the CA, trust it, restart, and carry on over HTTPS. Or
   skip it and stay on HTTP.
2. @you **Welcome to Fresh Breath.** Give your name, email and an SSH
   passphrase (eight characters or more).
3. **Fresh Breath** makes you the **Superuser**, seals a fresh SSH key with
   your passphrase, and locks the panel behind the built-in SSH key
   sign-in.
4. @you Sign in with your email and passphrase.

<OnboardingSim />

::: tip Already have a plan for sign-in?
**Skip — I'll set up auth myself** hides onboarding, for installs that will
use a company SSO from day one. Until you set an Admin auth in Settings, the
panel stays open.
:::

::: warning Picking an SSO for Admin auth later?
Every app and service with an empty *Protected by* inherits it, and lets in
anyone that provider signs in, not just Fresh Breath users. A company SSO is
made for that. A generic GitHub or Google login signs in the whole internet.
Episode 6 lets you see this for yourself.
:::

## Want HTTPS?

Plain HTTP works for the control panel and hosted apps. But apps you open
from your disk (`file://`) or from another site can't *stay* signed in over
HTTP: browsers only send a cross-site cookie over HTTPS.

The certificate offer only shows during onboarding, where Fresh Breath
writes `FRBR_TLS_CERT` and `FRBR_TLS_KEY` into its `.env` for you. Skipped
it? Bring your own certificate the same way.

::: details Making one with mkcert
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
