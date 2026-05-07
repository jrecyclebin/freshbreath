# Fresh Breath

A lightweight auth + MCP proxy server that lets static HTML apps — even `file://` apps — connect to third-party services without exposing secrets.

Fresh Breath sits between your frontend and external APIs. It handles OAuth flows, token exchange, token refresh, and MCP proxying so your app never holds client secrets. It also provides a web-based control panel for managing apps, services, users, and access control.

## What it does

- **OAuth & OIDC flows** — initiates login with any registered OIDC/OAuth2 provider, exchanges the code on the server (keeping `client_secret` safe), and hands the browser a signed access token
- **MCP proxying** — routes Model Context Protocol calls through the server so static apps can list and call tools on remote MCP servers
- **API key services** — key-auth services work too; the key is injected server-side during proxy calls, never sent to the browser
- **`file://` friendly** — works from local HTML files, not just hosted origins; CORS echoes the request origin
- **React control panel** — register apps, link services to apps, manage users, set roles, and view audit trails

## Running the server

You'll need to install Go. And Mise. But hey, install Mise first! Then it can
install Go.

```bash
mise use -g go      # install go
mise run local      # run Fresh Breath
```

Or with auto-restart during development (requires `entr`):

```bash
mise run dev        # find . -name '*.go' | entr -r go run .
```

The server starts on `:9009` by default with an SQLite database at `./freshbreath.db`.

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `FREBRE_DB_PATH` | `./freshbreath.db` | SQLite database file |
| `FREBRE_BASE_URL` | `http://localhost:9009` | External URL (used for OAuth redirects) |
| `FREBRE_LISTEN_ADDR` | `:9009` | HTTP bind address |

Create a `.env` file in the project root — the server loads it automatically on startup.

## Building & testing

```bash
mise run build        # builds the 'freshbreath' binary
mise run test         # runs the tests
mise run check        # lint + tests
```

## Using Fresh Breath in your app

For a complete guide on loading the SDK, logging in, restoring sessions, and calling tools, see the detailed skill at `skills/freshbreath/SKILL.md`.

The short version: include `env.js` and `setup.js` from the server, call `login()` with your app nonce and service URL, then use `ServiceProxy` to call `listTools()`, `callTool()`, or `fetch()`.

```html
<script src="http://localhost:9009/env.js"></script>
<script type="module">
  import { login, ServiceProxy } from "http://localhost:9009/setup.js";

  const service = await login({
    appNonce: "your-app-nonce",
    serviceURL: "https://mcp.example.com/mcp",
  });

  const tools = await service.listTools();
  const result = await service.callTool("some_tool", { arg: "value" });
</script>
```

Tokens are automatically refreshed in these calls, if possible.

## Admin panel

Visit `/control` in a browser to open the admin panel. On first boot, with no auth service configured, you are granted synthetic superuser access. You can then:

- Register **apps** (static HTML projects that use the SDK)
- Register **services** (OAuth/OIDC providers, MCP servers, or API-key services)  
- Link services to apps so only permitted services work with each app
- Manage **users** and assign them roles
- Set which service acts as the admin auth provider
- Review the **audit log**

## Project layout

```
.
├── main.go              # Entry point, config, server setup
├── handler.go           # HTTP handlers (API, OAuth, proxy, admin)
├── db.go                # SQLite store, migrations, queries
├── types.go             # Shared Go types
├── mcp.go               # MCP client / proxy logic
├── http.go              # HTTP forwarding / proxy
├── *_test.go            # Tests
├── web/
│   ├── control.html     # Admin panel shell (React CDN)
│   └── control/
│       ├── app.js       # React admin app (~1600 lines, single file)
│       └── styles.css   # Geist / geist-mono theme
│   └── setup.js         # Frontend SDK (env.js + login + ServiceProxy)
├── skills/
│   └── freshbreath/
│       └── SKILL.md     # Detailed SDK integration skill
└── mise.toml            # Task definitions
```

The backend is a single Go module. The admin panel is a single-file React app loaded from CDN. The frontend SDK is a small standalone ES module. No build step is required for the frontend — the Go server serves everything directly.
