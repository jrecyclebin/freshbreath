---
episode: 6
title: Who gets in
icon: 🚪
minutes: 4
blurb: Protected by, Admin auth, and why empty doesn’t mean open.
short: Each app has a “Protected by” setting — the sign-in someone needs to get in. Left empty, it inherits the control panel’s Admin auth. Only the Anonymous option makes an app open.
---

## The front door

Two settings decide who can open an app:

- **Admin auth** (in Settings) — the sign-in that guards the control panel.
- **Protected by** (on each app and service) — the sign-in that guards *that*
  door.

An empty **Protected by** doesn't mean “no lock.” It means “use the same lock
as the control panel.”

::: info 🔑 Think of it like key cards
An auth record is a kind of key card — your company badge, a GitHub account,
a Fresh Breath passphrase. Each door says which kind of card it takes.
:::

## Be the bouncer

Four people want into your app. Change the settings and see who gets in.

<GateSim />

## The sign-in options

An **auth record** is a sign-in method, set up once and shared by everything
that names it. The common ones:

- **Built-in – SSH key** — Fresh Breath users with a passphrase. Nobody else.
- **OIDC / OAuth** — a company SSO (Microsoft Entra, Okta, Google Workspace),
  or a public one like GitHub. Anyone that provider vouches for gets in,
  Fresh Breath account or not.
- **Anonymous** — open to everyone. You have to pick it on purpose.

That middle one is the whole point: your coworkers sign in with their work
account, and you never make accounts for them.

<Say>
My Fresh Breath app [app] (ID [nonce]) now requires sign-in. Read the Fresh Breath services guide, then add a small header that shows the signed-in person's name and a Sign out button.
</Say>

::: details Under the hood: the door owns the gate
A service you reach *through* an app answers to the app's gate, not its own.
So once someone's through the app's door, calling the app's services usually
needs no further login. And apps behind the same auth record share the stored
credential — which is the next episode.
:::
