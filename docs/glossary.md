# Glossary

Every term, in plain words.

| Term | Meaning |
|---|---|
| **Acts as** | The credential a service sends upstream. Empty means the caller's own. [More](/auth#acts-as-what-goes-upstream) |
| **Admin auth** | The auth record guarding the control panel — and every empty *Protected by*. |
| **App** | Static files hosted at a path, with a nonce, files, a database and an app service. |
| **App service** | The blank virtual service every app gets, named `app:` plus its nonce. |
| **Auth record** | A login method or stored key that apps and services point at. |
| **callTool()** | How an app calls a tool on a service. |
| **Control panel** | Fresh Breath's admin site, at `/control`. |
| **Data area** | Private storage beside an app's web files. Never served, never deployed. |
| **Default environment** | The slot the app's bare address serves. |
| **Deploy** | Copying one slot onto the next, like Development to Staging. |
| **frbr.js** | The small script apps load for logins and services. |
| **Gate** | Another word for *Protected by*: the door a caller must get through. |
| **get_guide** | The MCP tool agents use to read the built-in guides. |
| **Host** | A DNS name pointed at the server, with apps one level down on it. |
| **Link** | Permission for an app to use a service. |
| **login()** | Signs in to a service by URL and hands back a proxy for calling it. |
| **MCP** | Model Context Protocol, the standard way agents connect to tools. Fresh Breath is one, at `/mcp`. |
| **Nonce** | An app's random 10-character id, and its credential when it runs off the server. |
| **Protected by** | The auth record that decides who may come in. Empty means the admin auth, not open. [More](/auth#who-gets-through-the-door) |
| **Proxied** | A service whose calls go through Fresh Breath, which attaches the right credential. |
| **Remote** | Another Fresh Breath this one can drive through its tunnel. |
| **Roles** | Superuser, Admin, Member, Read-only — for people who maintain apps, not people who use them. |
| **Service** | Anything an app talks to: API, MCP, virtual, tasks or SSH. [More](/services) |
| **Slot** | One of an app's three copies: Development, Staging, Production. [More](/apps#three-slots-one-database) |
| **Task service** | Tools that run scripts on the server. Superusers only. |
| **Tool file** | The text file that defines a virtual or task service's tools. [More](/tools) |
| **Tunnel** | A WireGuard address for reaching the server with nothing opened to the internet. |
| **Virtual service** | Tools you write that wrap HTTP requests or SQL queries. |
| **WATCH** | A line in a tool that reports its finished calls to watchers, with an optional key. |
| **watch()** | How an app hears about a service's finished calls. [More](/services#watching-calls) |
