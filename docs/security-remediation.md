# Security remediation plan

Findings from the external-surface review (HTTP API, browser login flows,
OAuth server, central and per-service MCP, service proxy, update feeds,
SSH/git/sync, app databases). Each fix is its own commit on the branch so
they can be taken or dropped individually. Items marked **open** are not
fixed in code and need a design decision.

This is a point-in-time code review, not a guarantee. Have an independent
penetration test done before an internet-facing deployment, and keep in
mind the README's own advice: Fresh Breath is a personal server and should
not be exposed directly to the internet.

## Critical

| # | Area | Problem | Fix |
|---|------|---------|-----|
| 1 | Login pages | Request values (`state`, `return`) are written into inline scripts without script-safe encoding. | Encode every interpolated value with JSON (HTML-escaped) before it reaches a page. |
| 2 | OAuth server | `/oauth/authorize` completes without asking the user, and dynamic client registration accepts any `redirect_uri` scheme. | Show a consent step naming the redirect destination; accept only `https`, loopback `http`, or private-use app schemes; build the redirect with proper query encoding. |
| 3 | Login hand-off | The finished-login page posts the token to any opener window, and a credentialed CORS request from an opaque (`null`) origin is accepted for every app. | Post only to the app's own origin (this server for hosted apps, the registered origin otherwise). Treat `null` as an origin only for apps registered with a `file:` URL. |
| 4 | Setup mode / local installs | Requests with any `Host` header are served, so a website can reach a local install through DNS rebinding — and setup mode is an unauthenticated Superuser. | Reject requests whose `Host` is not this server's configured host, a loopback name, the machine's hostname, or an IP literal. |
| 5 | Hosted apps (**open**) | Hosted apps run on the same origin as the control panel and the shared token store, so anyone who can write an app's files can act as anyone who visits it. | Serve apps from their own origin (per-app subdomain or port — FRBR-15). Until then, only trusted people should get write access to any app. |

## High

| # | Area | Problem | Fix |
|---|------|---------|-----|
| 6 | Roles | Read-only members can write, upload, delete and deploy app files, edit the app's service definition and write its databases. | Writes need Member or above; Read-only gets read-only database connections. |
| 7 | Login forms | No limit on passphrase or API-key attempts; each passphrase check costs 64 MiB of Argon2 memory. Error text distinguishes accounts with no key. | Per-IP attempt limit, a cap on concurrent passphrase checks, a uniform error message. |
| 8 | SSH keys | `GET /api/me/ssh-key` and `/api/users/{id}/ssh-key` return the encrypted private key, salt and nonce. | Return only the public fields, as the MCP tools already do. |
| 9 | OIDC / OAuth identity | Accounts are linked by provider-reported email without checking `email_verified`. | Ignore an email the provider marks unverified. Linking by `(issuer, sub)` is the stronger fix and is left **open**. |

## Medium

| # | Area | Problem | Fix |
|---|------|---------|-----|
| 10 | Service proxy | Encoded `..` segments step outside the service's configured base path while the service credential is attached. Browser cookies are forwarded upstream and upstream `Set-Cookie` lands on this origin. | Refuse paths that leave the base path; strip `Cookie` on the way out and `Set-Cookie` on the way back. |
| 11 | Response headers | No framing protection or content-type hardening on the control panel, login pages or API. | `X-Frame-Options: DENY`, `frame-ancestors 'none'`, `nosniff`, `Referrer-Policy` on server-owned pages and API responses. |
| 12 | SSH / git | Any user with SSH access can delete the shared trusted host-key list. The git gateway accepts `file:` and plain `http:` URLs. | Host-key deletion is Admin+; git URLs must be `ssh` or `https`. |
| 13 | Update feeds | An archive op's `source` path is not traversal-checked. Upload extraction has no total-size cap. | Validate op sources inside the archive; cap extracted upload size. |
| 14 | Update feeds (**open**) | Applying feeds is anonymous, service-file updates take effect immediately, and there is no rollback protection. | Decide whether apply should need an admin, and require increasing versions. |
| 15 | Virtual tools (**open**) | Path variables in URL templates are substituted unescaped. | Escape path variables by default with an opt-out for templates that pass whole paths. Changes template behavior, so it needs a decision. |

## Low

| # | Area | Problem | Fix |
|---|------|---------|-----|
| 16 | Login return path | A return path starting `/\` is accepted; browsers treat it as another host. | Reject backslashes in return paths. |
| 17 | CORS | The origin check uses the whole raw query string as the app nonce. | Read `app_nonce` from the query. (Folded into #3.) |
| 18 | Admin boundary | Admins could write task scripts, so Admin was effectively equal to Superuser and to the server's OS account. | Task services are Superuser-only: creating, changing, deleting, reading and writing them, and replacing them through update feeds (whose owner must be a Superuser when the feed is applied). |
| 19 | Proxy headers (**open**) | `X-Forwarded-Proto` is trusted from any client. | Trust it only from configured proxies. |
| 20 | `ssh_key` acts_as (**open**) | A stored private key is sent upstream as a bearer token when such a record backs an HTTP service. | Refuse `ssh_key` as acts_as for proxied HTTP services. |
| 21 | Open registration (**open**) | Dynamic client registration has no rate limit. | Rate-limit registration and expire unused clients. |
