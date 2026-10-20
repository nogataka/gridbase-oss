# How it works

## Services

| Service | What it does |
|---|---|
| `caddy` | Reverse proxy on `:80`/`:443`. Routes `admin.<domain>` to the admin console and `*.<base-domain>` to the tunnel server. |
| `tunnel` | Go tunnel server ([tunnel/](../tunnel)). HTTP side on `:9080` (behind Caddy), tunnel port `:9443` (clients connect here directly). |
| `web` | Admin console ([web/](../web), Next.js): sign in, API key, subdomains, user management. The UI is **Japanese only**. |
| `directus` | [Directus](https://directus.io) 11 (SQLite by default) used as the database. Not exposed outside the compose network. |
| `directus-init` | One-shot, idempotent: checks that no default secret is left in an exposed setup, applies the schema, creates least-privilege policies and service tokens. |

```
 browser ──http(s)──▶ caddy ─┬─ admin.<domain>     ──▶ web :3000 ──┐
                             └─ *.<base-domain>    ──▶ tunnel :9080 │
 gridbase client ──(tunnel port, TCP)──▶ tunnel :9443               │
                                         tunnel ◀── polls 30 s ──▶ directus :8055 ◀──┘
                                         directus-init: secrets check + schema + policies + tokens (runs once per `up`)
```

## How a request flows

The browser requests `http://foo.gridbase.localhost/`. Caddy forwards it to the tunnel server. The server finds the subdomain `foo` (looked up from Directus), and sends the request over the long-lived connection opened by your `gridbase` client. The client calls your local app and sends the response back.

## What `directus-init` does

`directus/init/init.mjs` (Node 22 plus `bcryptjs`, built from [directus/init/Dockerfile](../directus/init/Dockerfile) by `docker compose up --build`) runs on every `docker compose up` and changes nothing when everything is already as expected:

0. **Checks the secrets in `.env`** ([directus/init/secrets-check.mjs](../directus/init/secrets-check.mjs)). A secret that still has the `.env.example` value, or starts with `change-me` / `local-dev-` / `replace-with-`, is "default". The checked secrets are `DIRECTUS_KEY`, `DIRECTUS_SECRET`, `DIRECTUS_ADMIN_PASSWORD`, `DIRECTUS_ADMIN_TOKEN`, `NEXTAUTH_SECRET`, `GRIDBASE_ADMIN_PASSWORD`, the two Directus service tokens, `GRIDBASE_TUNNEL_ADMIN_TOKEN` and (with the Postgres override) `POSTGRES_PASSWORD`.
   - **Local setup**: `GRIDBASE_BIND_ADDRESS=127.0.0.1`, `http`, and only `localhost` / `*.localhost` / `*.test` / `*.internal` domains. init prints a warning and continues.
   - **Exposed setup**: any one of these makes the setup exposed.
     - `GRIDBASE_BIND_ADDRESS` is anything but `127.0.0.1`.
     - `GRIDBASE_SITE_SCHEME` or `GRIDBASE_ADMIN_URL` is `https`.
     - `GRIDBASE_BASE_DOMAIN` or `GRIDBASE_ADMIN_DOMAIN` is a real domain, that is, not `localhost`, `*.localhost`, `*.test` or `*.internal`. This way, a stack bound to `127.0.0.1` and published by a reverse proxy on the host over plain `http` is also treated as exposed.

     In an exposed setup, a default secret, a too-short secret, or two secrets with the same value makes init **exit 1**. `tunnel`, `web` and `caddy` wait for init to finish successfully, so nothing is published. Copying `.env.example` to `.env` and only changing the bind address cannot produce a running public instance.
   - **The decision is made from `.env` alone.** init cannot see how the stack is published outside `.env`. `*.localhost`, `*.test` and `*.internal` count as local, and `*.internal` is a name many companies use for their internal network.

     These routes are not detected unless you also state the exposure in `.env`:
     - A reverse proxy, `cloudflared`, `ssh -R`, ngrok or similar on the host that forwards to the published ports.
     - Ports added in a `docker-compose.override.yml`.
     - An internal-only name such as `admin.gb.corp.internal` served to other machines.

     To make init treat such a setup as exposed, set `GRIDBASE_SITE_SCHEME=https://` (and `GRIDBASE_ADMIN_URL=https://…`), use a real domain in `GRIDBASE_BASE_DOMAIN` / `GRIDBASE_ADMIN_DOMAIN`, or bind to a non-loopback address. Without that, default secrets only produce a warning.
1. Waits for Directus health.
2. Applies [directus/schema/gridbase.json](../directus/schema/gridbase.json) through `/schema/diff` and `/schema/apply`. Two collections: `users` and `subdomains`. UNIQUE on `users.email`, `users.userId`, `users.api_key`, `subdomains.subdomain`. (Postgres uses `gridbase.postgres.json`, picked automatically.) If Directus rejects the snapshot because its **vendor or version** differs (Postgres, or you upgraded the Directus image), init retries once with `force=true`.
3. Creates two policies without `admin_access`/`app_access`:
   - `gridbase-web`: create/read/update/delete on `users` and `subdomains` only.
   - `gridbase-tunnel`: read-only, and only the columns the tunnel needs (no password hashes).
4. Creates one service user per policy and sets their static tokens to `GRIDBASE_WEB_DIRECTUS_TOKEN` / `GRIDBASE_TUNNEL_DIRECTUS_TOKEN`. Rotate a token by editing `.env` and running `docker compose up -d`; init resyncs it.
5. Clears the Directus cache (needed after schema and permission changes).

After step 2, init also **checks the admin accounts stored in the database**: it reads every user with `role=admin` (any `status`: a disabled admin can be re-enabled later) plus the users with a default admin e-mail (`admin@gridbase.localhost`, `admin@example.com` and the current `GRIDBASE_ADMIN_EMAIL`), even if they were demoted or disabled, and compares the stored bcrypt hash with the documented default passwords (`ChangeMe-local-1234` and the other values from `.env.example`). Users whose stored password is empty are skipped: `web` rejects them for any input, so they cannot sign in with a password. This does not depend on `GRIDBASE_ADMIN_PASSWORD` being present in `.env`, so removing it after the first start ([production checklist](./production.md#checklist), step 10) does not weaken it. In an exposed setup, init **exits 1** if an admin still has a default password, and also if the check itself cannot be done (Directus unreachable, the hash cannot be read): nothing is published. In a local setup it only warns.

`web` creates the first admin user itself on start (`scripts/seed-admin.mjs`) when `GRIDBASE_ADMIN_EMAIL`/`GRIDBASE_ADMIN_PASSWORD` are set and no active admin exists. It never promotes an existing user and never changes an existing password. In an exposed setup it repeats the stored-admin check (a second layer: `web` exits with code 3 and does not start on a default password, on a weak or default `GRIDBASE_ADMIN_PASSWORD`, or when it cannot verify the state); details in [web/README.md](../web/README.md#初回管理者).

## Tests

```sh
(cd directus/init && npm ci) && node --test directus/init/*.test.mjs   # default-secret / stored-admin-password checks
(cd web && npm ci && npm test)                      # callbackUrl / password rules / rate limit / input validation
(cd tunnel && go vet ./... && go test ./...)        # tunnel server and client
docker compose up -d --build && node tests/e2e/smoke.mjs   # end to end against the running stack
```

`tests/e2e/smoke.mjs` signs in, issues an API key, creates a subdomain, connects a client and fetches a page through the tunnel, and checks permissions, CSRF rejection, password change (other sessions are revoked) and that `directus-init` is idempotent. It needs Node 20+ and Docker. CI runs everything on every push ([.github/workflows/ci.yml](../.github/workflows/ci.yml)). For the full list of checks before a pull request, see [CONTRIBUTING.md](../CONTRIBUTING.md).

## Repository layout

```
docker-compose.yml            the stack
docker-compose.postgres.yml   optional Postgres override
Caddyfile                     reverse proxy (local HTTP by default)
.env.example                  all variables, with local-only dummy values
directus/schema/              schema snapshots (sqlite, postgres)
directus/init/                idempotent Directus bootstrap (init.mjs), the default-secret and stored-admin-password checks, Dockerfile
tunnel/                       Go tunnel server and client
web/                          admin console (Next.js)
tests/e2e/                    end-to-end smoke test (run after `docker compose up`)
docs/                         this documentation
.github/workflows/ci.yml      CI
SECURITY.md  CONTRIBUTING.md
```
