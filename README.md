# mcp-full-off-mcps

The gateway that is full of MCPs.

One MCP endpoint that fronts many MCP servers. Add remote MCPs once in a dark-mode web UI; every client (Claude, Cursor, VS Code, …) connects to a single URL and sees all your tools, namespaced as `slug__tool`.

- **Go** backend, single static binary, SQLite storage, website embedded (Vite + React + shadcn/ui + Geist).
- **Multi-user.** Every user owns their own private MCPs, tokens and logins. Nobody sees anyone else's.
- **Three ways in** for MCP clients: OAuth, bearer token, or no auth (opt-in, public MCPs only).
- **Website login via OIDC** (Hack Club Auth, Tinyauth, or any OIDC provider you configure).
- **Upstreams** can be Streamable HTTP or legacy SSE, with no auth, an API key/bearer header, or their own OAuth (the gateway logs in, stores and refreshes the tokens).

## How a request is authenticated

| Client sends | Endpoint | Sees |
| --- | --- | --- |
| OAuth access token (client did the browser login) | `/mcp` | all of that user's MCPs |
| Bearer token `mcpt_…` created in the UI | `/mcp` | all of that user's MCPs, or only the ones the token is limited to |
| Nothing | `/mcp/public` | only MCPs their owner marked **Public**, and only if an admin turned the public endpoint on |

`/mcp` always requires credentials, which is what makes OAuth login prompts work in clients like Claude. The no-auth endpoint is a separate URL for that reason. It is off by default.

The gateway is a full OAuth 2.1 authorization server for MCP clients: protected-resource and authorization-server metadata, dynamic client registration, authorization code + PKCE (S256), and rotating refresh tokens. The user approves on a consent screen after logging in to the website.

## Quick start

You need an OIDC provider that can issue a client ID and secret for this app. Register this redirect URI with it: `BASE_URL/auth/callback/<provider-id>`.

```sh
cp .env.example .env     # fill in BASE_URL and your provider(s)
docker compose up -d     # gateway + Caddy (automatic HTTPS)
docker compose logs gateway | grep SETUP
```

On first start the log prints a one-time setup link, `…/setup?token=…`. Open it and sign in with the account that should be the admin. It works once and only while no admin exists. After that:

1. **Users** → add people by email (or turn on open sign-up under **Settings**). They sign in with your provider and are matched by email.
2. **MCPs** → **Add MCP**, paste the server URL, choose how to authenticate (none / API key / OAuth). For OAuth press **Connect** to log in to that MCP.
3. **Overview** → copy the gateway URL, or the one-line install command for your client.

Images are built by GitHub Actions on every push to `main` and every `v*` tag, and published to `ghcr.io/<owner>/mcp-full-off-mcps` (`edge`, version and short-sha tags; amd64 and arm64). To use one instead of building locally, replace `build: .` in `docker-compose.yml` with `image: ghcr.io/<owner>/mcp-full-off-mcps:edge`. The package is private until you make it public in the repo's Packages settings.

Without Docker:

```sh
make build
BASE_URL=http://localhost:8080 OIDC_PROVIDERS=… ./bin/gateway
```

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `BASE_URL` | required | Public URL, no trailing slash. Used for redirects and OAuth metadata. `https://` turns on secure cookies and HSTS. |
| `OIDC_PROVIDERS` | none | Comma-separated provider ids, e.g. `hackclub,tinyauth`. Ids use `a-z`, `0-9`, `-`. |
| `OIDC_<ID>_ISSUER` | required per provider | OIDC issuer URL (discovery is fetched from `/.well-known/openid-configuration`). |
| `OIDC_<ID>_CLIENT_ID` / `_CLIENT_SECRET` | required / optional | Credentials from the provider. |
| `OIDC_<ID>_LABEL` | the id | Button text on the login page. |
| `OIDC_<ID>_SCOPES` | `openid email profile` | Space-separated. |
| `SECRET_KEY` | generated | Encrypts stored upstream credentials (AES-256-GCM). If unset, a key is generated once into `DATA_DIR/secret.key`. **Back it up with the database**, because without it stored credentials are unreadable. |
| `DATA_DIR` | `./data` | SQLite database and generated key. |
| `ADDR` | `:8080` | Listen address. |
| `TRUST_PROXY` | `false` | Use `X-Forwarded-For` for rate limiting. Set it when behind a reverse proxy. |
| `ALLOW_PRIVATE_UPSTREAMS` | `false` | Allow upstream MCPs on loopback/private/link-local addresses. See below. |

Admin-only switches live in the UI under **Settings**: *open sign-up* (off by default) and the *public no-auth endpoint* (off by default).

## Security notes

- **Upstream URLs are user-supplied**, so by default the gateway refuses to connect to loopback, private, link-local and CGNAT addresses (checked at dial time, so DNS rebinding doesn't bypass it). If your MCPs live on a private network, set `ALLOW_PRIVATE_UPSTREAMS=true`, ideally only on an instance where every user is trusted. Outbound HTTP proxies (`HTTPS_PROXY`) are not used for upstream traffic.
- Stored upstream secrets and OAuth tokens are encrypted at rest. Bearer tokens, OAuth access/refresh tokens, authorization codes and website sessions are stored only as SHA-256 hashes.
- A **Public** MCP is callable by anyone who can reach `/mcp/public`, using the owner's stored credentials for it. Only mark MCPs public if that's what you want.
- Disabling a user immediately invalidates their website sessions and OAuth tokens, and their bearer tokens stop working.
- Website sign-in requires an email from the provider, and `email_verified` must not be `false`. Make sure your provider verifies emails before turning on open sign-up.
- Website cookies are `HttpOnly`, `SameSite=Lax`, and state-changing API calls reject cross-origin requests.

## Limits to know about

- Gateway ↔ client is **stateless**: tool lists are fetched per request (each upstream's catalogue is cached for 60 s). Upstream-initiated requests (sampling, elicitation, progress, list-changed notifications) are not forwarded.
- Resource *templates* are not forwarded; concrete resources, tools and prompts are.
- Upstream OAuth refresh does not send the RFC 8707 `resource` parameter (the initial authorization does).
- SQLite means one instance. Run a single replica.
- Local `stdio` MCP servers are not supported, only remote HTTP/SSE ones.

## Development

```sh
make test          # go vet, go test, tsc
cd web && npm run dev   # UI on :5173, proxies /api, /auth, /oauth to :8080
```

Layout: `internal/server` (HTTP, auth, OAuth server, gateway), `internal/upstream` (client connections, SSRF guard, upstream OAuth), `internal/store` (SQLite), `web/` (React UI, built into `internal/webui/dist` and embedded).
