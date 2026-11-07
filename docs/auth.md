# Logins and Gates

<div class="short">

Every app and service has a **Protected by** slot (who may come in) and every service has an **Acts as** slot (what credential goes out). Both point at an **auth record**. Empty Protected by means *the admin login* — not *open*.

</div>

<div class="like" data-icon="🚪">

**Think of a building with a front door and phone lines.** Protected by is the lock on the front door. Acts as is whose badge gets shown when the app calls somewhere else.

</div>

## Auth records

An auth record is a login method or a stored key that stands on its own. Anything can point at it, and everything that does shares it.

| Kind | Who gets through as a gate | Example |
|---|---|---|
| **Anonymous** | Anyone who can reach the server | A guestbook on your LAN |
| **API key** | Anyone holding the key | A webhook from another server |
| **OIDC** / **OAuth 2** | Anyone with an account at the provider | Company SSO, GitHub, Google |
| **SSH key** (built in) | Fresh Breath users with an SSH key and its passphrase | The control panel, step 2 of setup |

OIDC and OAuth 2 records have a **Fresh Breath users only** checkbox. Tick it, and a provider login whose email matches no user is refused — GitHub becomes a way for *your* people to sign in, not everybody's.

## Who gets through the door?

Pick the app's Protected by and the server's Admin auth, and watch who gets in.

<GateSim />

The thing to take away: the admin record is the one every empty slot inherits. A company SSO there is just right — that's your people. A plain GitHub login there opens every inheriting app and service, task services included, to anyone on the internet.

Notice too that the coworker gets in without a Fresh Breath account. That's the point: apps run on the accounts of the people using them.

## Two rules that keep logins quiet

<div class="steps">

1. **The door owns the gate.** A service reached through your app answers to *your app's* gate, not its own. So clearing the app's gate is most of what any login does.
2. **Credentials are shared by gate.** Two apps behind the same record find the same stored credential. Apps served from the same origin share one store, so the second one prompts zero times.

</div>

## What does login() cost?

Your app calls `login()` with a service's URL. Before it prompts for anything, it asks the server what that door needs and looks in the store. Only a credential that's genuinely missing opens a window.

<LoginSim />

::: warning Call login() from a click
A popup with no click behind it is a popup the browser blocks. Check silently at boot with `login(url, "silent")`, and if that comes back `null`, draw a Connect button.
:::

## Acts as: what goes upstream

| Acts as | What the service sends | Works with nobody there? |
|---|---|---|
| *(empty)* | The caller's own credential | No |
| An OIDC or OAuth 2 record | The caller's own login at that provider | No |
| An API key or SSH key record | That stored key, the same for every caller | **Yes** |

MCP services have no acts-as: the MCP server decides, and an OAuth one (like Notion) is signed in to as a step of `login()`. Anything that runs unattended — a scheduled task, a bot — needs a stored key, because there's no one there to sign in.

## Sessions

```js
const svc = await login("/mcp/app:")
svc.session.user_name   // for the header
svc.session.subject     // "frbr:3" for a Fresh Breath user, "ext:github:12345" otherwise

currentSession()        // null when nothing live is stored
signOut()               // forgets this app's gate and ends that login on the server
```

`subject` is a handy unique id: it also tells you whether the person has a Fresh Breath account or signed in from outside.

::: details Under the hood: what's actually stored
The browser only ever holds Fresh Breath tokens. A provider's credential rides sealed inside one, unreadable in the browser, and the server unseals it on the way out. Client secrets never reach the browser — token exchange and refresh run on the server. For apps on the Fresh Breath origin, refresh tokens sit in HttpOnly cookies your code can't read. The worst thing readable out of localStorage is a token that expires.
:::
