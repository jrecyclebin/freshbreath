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

::: tip A public login, for your people only
A GitHub or Google record can be told to take **Fresh Breath users only**.
Then a sign-in whose email matches no user here is refused, and one that
matches signs in as that user. That's how you keep GitHub as the door
without letting in the whole internet.
:::

<Say>
My Fresh Breath app [app] now requires sign-in. Read the Fresh Breath services guide, then add a small header that shows the signed-in person's name and a Sign out button.
</Say>

## Auth inside a service

Services have the same lock as apps — **Protected by** — plus a second slot
for what happens *after* the door: **Acts as**, the credential that goes on
to the API upstream.

1. **Protected by** — who may call. Empty inherits the Admin auth, same as
   an app.
2. **Acts as** — what's sent upstream (as `$token` in a tool script, or the
   header a proxied `fetch()` gets). Empty means *the caller's own*.

And one rule that makes it all feel quiet: **the door owns the gate.** A
service reached *through an app* answers to the app's gate, not its own.
Once someone's into Tix, Tix's services just work for them.

<ServiceAuthSim />

So which **Acts as** do you want?

<div class="facts">
  <div class="fact"><span class="ic">🙋</span><b>Empty</b><p>The caller’s own credential, whatever their gate gave them. Right when the gate and the API are the same provider: Microsoft SSO calling Microsoft Graph.</p></div>
  <div class="fact"><span class="ic">🐙</span><b>An OAuth record</b><p>Each person’s own account on that API, after one sign-in. GitHub sees the real person.</p></div>
  <div class="fact"><span class="ic">🔑</span><b>A stored key</b><p>One key for everybody. The only one that works with nobody present: scheduled tasks, overnight jobs.</p></div>
</div>

::: details Under the hood
What's in the browser is always a Fresh Breath token. Provider tokens ride
inside it, sealed, and the server unseals the right one on the way out.
A Fresh Breath token itself only ever goes to Fresh Breath's own API: a tool
step with a relative URL (`GET /api/me`) gets it, and nothing elsewhere ever
does. MCP services have no Acts as: the MCP server decides.
:::
