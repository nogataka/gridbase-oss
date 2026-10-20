# gridbase

Self-hosted HTTP tunnel with an admin console. Run one command and `https://foo.<your-domain>` reaches an app on your laptop.

日本語版: [README.ja.md](./README.ja.md)

```sh
cp .env.example .env && docker compose up -d
```

The stack contains everything:

| Service | What it does |
|---|---|
| `caddy` | Reverse proxy on `:80`/`:443`. Routes `admin.<domain>` to the admin console and `*.<base-domain>` to the tunnel server. |
| `tunnel` | Go tunnel server ([tunnel/](./tunnel)). HTTP side on `:9080` (behind Caddy), tunnel port `:9443` (clients connect here directly). |
| `web` | Admin console ([web/](./web), Next.js): sign in, API key, subdomains, user management. The UI is **Japanese only**. |
| `directus` | [Directus](https://directus.io) 11 (SQLite by default) used as the database. Not exposed outside the compose network. |
| `directus-init` | One-shot, idempotent: checks that no default secret is left in an exposed setup, applies the schema, creates least-privilege policies and service tokens. |

```
 browser ──http(s)──▶ caddy ─┬─ admin.<domain>     ──▶ web :3000 ──┐
                             └─ *.<base-domain>    ──▶ tunnel :9080 │
 gridbase client ──(tunnel port, TCP)──▶ tunnel :9443               │
                                         tunnel ◀── polls 30 s ──▶ directus :8055 ◀──┘
                                         directus-init: secrets check + schema + policies + tokens (runs once per `up`)
```

How a request flows: the browser requests `http://foo.gridbase.localhost/`; Caddy forwards it to the tunnel server; the server finds the subdomain `foo` (looked up from Directus), sends the request over the long-lived connection opened by your `gridbase` client, the client calls your local app and sends the response back.

## Quick start (about 5 minutes)

Requirements: Docker with Compose v2. Ports `80`, `443`, `9443` free on the host. If any of them is taken, caddy (or tunnel) fails to start while the other services stay up; change the ports first, see [Changing ports](#changing-ports). The console UI is **Japanese only** (there is no i18n).

```sh
git clone <this repository> gridbase && cd gridbase
cp .env.example .env
docker compose up -d --build        # first build takes a few minutes
docker compose ps                   # everything healthy; directus-init is "Exited (0)"
```

1. Open <http://admin.gridbase.localhost> and sign in with `GRIDBASE_ADMIN_EMAIL` / `GRIDBASE_ADMIN_PASSWORD` from `.env` (defaults: `admin@gridbase.localhost` / `ChangeMe-local-1234`). These defaults are for a local trial only: **the stack refuses to start with them once it is exposed** (see [Production](#production)).
2. On the dashboard copy your **API key** and create a subdomain, for example `foo`, mode `http`, local server URL `http://host.docker.internal:8000` (see below for what URL to use).
3. Wait up to **30 seconds**. The tunnel server re-reads Directus every 30 s, so a new subdomain is not known to it immediately. A client that connects with an API key the server does not know yet exits instead of retrying.
4. Start a local app and connect a client. The examples use port `8000` for the app; if that port is already in use on your machine, pick another one (e.g. `8765`) and use the same number everywhere: in the command below and in the subdomain's local server URL (or `--local_server_url`).

```sh
python3 -m http.server 8000          # your app, on the host

# client running inside docker (no Go needed). Replace <API_KEY>.
docker compose run --rm --no-deps --entrypoint gridbase-tunnel-client tunnel \
  --server tunnel:9443 --api_key <API_KEY>

curl http://foo.gridbase.localhost/  # -> the page served by your app
```

`*.localhost` resolves to `127.0.0.1` in curl and in current browsers, so no DNS setup is needed. If your OS does not resolve it, add `127.0.0.1 admin.gridbase.localhost foo.gridbase.localhost` to `/etc/hosts`.

### Which URL do I give the subdomain?

`local_server_url` is where the **client** forwards requests, so it must be reachable from where the client runs.

| Client runs | App runs | `local_server_url` |
|---|---|---|
| In docker (command above) | On the host | `http://host.docker.internal:8000` (compose adds the `host-gateway` mapping for Linux) |
| On the host (binary) | On the host | `http://127.0.0.1:8000` |

You can also leave the registered URL alone and override it on the client with `--local_server_url http://127.0.0.1:8000`. **In production always pin the target this way**: a client otherwise uses the target URL that the server sends it, and the quick start's in-compose client can reach internal services such as `http://directus:8055` if a subdomain is registered with such a URL.

### Client on the host (binary)

```sh
cd tunnel && go build -o gridbase-tunnel-client ./client      # Go 1.21+
./gridbase-tunnel-client --server 127.0.0.1:9443 --api_key <API_KEY> \
                         --local_server_url http://127.0.0.1:8000
```

On **Linux** you can instead copy a prebuilt client out of the image: `docker compose cp tunnel:/usr/local/bin/gridbase-tunnel-client .`. That binary is a Linux executable (static), so it does **not** run on macOS or Windows; build it with `go build` there.

Without `--tls` the client talks plain TCP and prints a warning (the API key is sent in clear text). That is fine on localhost; use TLS everywhere else. All client options are in [tunnel/README.md](./tunnel/README.md#client).

### Changing ports

Edit `.env`:

```env
GRIDBASE_HOST_HTTP_PORT=18080
GRIDBASE_HOST_HTTPS_PORT=18443
GRIDBASE_HOST_TUNNEL_PORT=19443
GRIDBASE_ADMIN_URL=http://admin.gridbase.localhost:18080   # must match what the browser opens
GRIDBASE_TUNNEL_SERVER=localhost:19443
```

The tunnel is then `http://foo.gridbase.localhost:18080/`. Pick ports that are free on your machine: common development ports such as `8000` and `8080` are often taken by other apps (a taken `8080` makes caddy fail to start, with the other services still running). Apply with `docker compose up -d`; the client inside docker still uses `--server tunnel:9443` (the container port does not change), a client on the host uses `--server 127.0.0.1:19443`.

By default every published port is bound to `127.0.0.1` (`GRIDBASE_BIND_ADDRESS`). Set `0.0.0.0` only after replacing all secrets: with default secrets still in `.env`, `directus-init` stops the start (see below).

## What `directus-init` does

`directus/init/init.mjs` (Node 22 plus `bcryptjs`, built from [directus/init/Dockerfile](./directus/init/Dockerfile) by `docker compose up --build`) runs on every `docker compose up` and changes nothing when everything is already as expected:

0. **Checks the secrets in `.env`** ([directus/init/secrets-check.mjs](./directus/init/secrets-check.mjs)). A secret that still has the `.env.example` value, or starts with `change-me` / `local-dev-` / `replace-with-`, is "default". The checked secrets are `DIRECTUS_KEY`, `DIRECTUS_SECRET`, `DIRECTUS_ADMIN_PASSWORD`, `DIRECTUS_ADMIN_TOKEN`, `NEXTAUTH_SECRET`, `GRIDBASE_ADMIN_PASSWORD`, the two Directus service tokens, `GRIDBASE_TUNNEL_ADMIN_TOKEN` and (with the Postgres override) `POSTGRES_PASSWORD`.
   - **Local setup** (`GRIDBASE_BIND_ADDRESS=127.0.0.1`, `http`, and only `localhost` / `*.localhost` / `*.test` / `*.internal` domains): prints a warning and continues.
   - **Exposed setup** (`GRIDBASE_BIND_ADDRESS` is anything but `127.0.0.1`; or `GRIDBASE_SITE_SCHEME` / `GRIDBASE_ADMIN_URL` is `https`; or `GRIDBASE_BASE_DOMAIN` / `GRIDBASE_ADMIN_DOMAIN` is a real domain, i.e. not `localhost`, `*.localhost`, `*.test` or `*.internal`, so that a stack bound to `127.0.0.1` and published by a reverse proxy on the host over plain `http` is also treated as exposed): any default secret, a too-short secret, or two secrets with the same value makes init **exit 1**. `tunnel`, `web` and `caddy` wait for init to finish successfully, so nothing is published. Copying `.env.example` to `.env` and only changing the bind address cannot produce a running public instance.
   - **The decision is made from `.env` alone.** `*.localhost`, `*.test` and `*.internal` count as local, and `*.internal` is a name many companies use for their internal network. init cannot see how the stack is published outside `.env`. If you expose it by any of these routes, state it in `.env` so that it is treated as exposed, for example `GRIDBASE_SITE_SCHEME=https://` (and `GRIDBASE_ADMIN_URL=https://…`), or use a real domain in `GRIDBASE_BASE_DOMAIN` / `GRIDBASE_ADMIN_DOMAIN`, or bind to a non-loopback address: (a) a reverse proxy, `cloudflared`, `ssh -R`, ngrok or similar on the host that forwards to the published ports; (b) ports added in a `docker-compose.override.yml`; (c) an internal-only name such as `admin.gb.corp.internal` served to other machines. Without it, default secrets only produce a warning.
1. Waits for Directus health.
2. Applies [directus/schema/gridbase.json](./directus/schema/gridbase.json) through `/schema/diff` and `/schema/apply`. Two collections: `users` and `subdomains`. UNIQUE on `users.email`, `users.userId`, `users.api_key`, `subdomains.subdomain`. (Postgres uses `gridbase.postgres.json`, picked automatically.) If Directus rejects the snapshot because its **vendor or version** differs (Postgres, or you upgraded the Directus image), init retries once with `force=true`.
3. Creates two policies without `admin_access`/`app_access`:
   - `gridbase-web`: create/read/update/delete on `users` and `subdomains` only.
   - `gridbase-tunnel`: read-only, and only the columns the tunnel needs (no password hashes).
4. Creates one service user per policy and sets their static tokens to `GRIDBASE_WEB_DIRECTUS_TOKEN` / `GRIDBASE_TUNNEL_DIRECTUS_TOKEN`. Rotate a token by editing `.env` and running `docker compose up -d`; init resyncs it.
5. Clears the Directus cache (needed after schema and permission changes).

After step 2, init also **checks the admin accounts stored in the database**: it reads every user with `role=admin` (any `status`: a disabled admin can be re-enabled later) plus the users with a default admin e-mail (`admin@gridbase.localhost`, `admin@example.com` and the current `GRIDBASE_ADMIN_EMAIL`), even if they were demoted or disabled, and compares the stored bcrypt hash with the documented default passwords (`ChangeMe-local-1234` and the other values from `.env.example`). Users whose stored password is empty are skipped: `web` rejects them for any input, so they cannot sign in with a password. This does not depend on `GRIDBASE_ADMIN_PASSWORD` being present in `.env`, so removing it after the first start (Production step 10) does not weaken it. In an exposed setup, init **exits 1** if an admin still has a default password, and also if the check itself cannot be done (Directus unreachable, the hash cannot be read): nothing is published. In a local setup it only warns.

`web` creates the first admin user itself on start (`scripts/seed-admin.mjs`) when `GRIDBASE_ADMIN_EMAIL`/`GRIDBASE_ADMIN_PASSWORD` are set and no active admin exists. It never promotes an existing user and never changes an existing password. In an exposed setup it repeats the stored-admin check (a second layer: `web` exits with code 3 and does not start on a default password, on a weak or default `GRIDBASE_ADMIN_PASSWORD`, or when it cannot verify the state); details in [web/README.md](./web/README.md#初回管理者).

Directus has its own admin account (`DIRECTUS_ADMIN_EMAIL`/`DIRECTUS_ADMIN_PASSWORD`). Note Directus rejects some email domains: use something like `you@example.com`, not `…@gridbase.localhost`. To open the Directus UI from the host, uncomment the `ports:` block of the `directus` service in `docker-compose.yml`.

### Upgrading Directus

The snapshot in `directus/schema/` was taken with Directus 11.17.4. When you change the image tag in `docker-compose.yml`:

1. Back up the `directus-db` volume, then start the stack with the new image (`docker compose up -d`). Directus migrates its own database on start; if the version differs from the snapshot, init says so and retries with `force`, so it keeps working.
2. Take a fresh snapshot so the repository matches the version you run, and commit it:

```sh
docker compose exec directus npx directus schema snapshot --yes --format json /tmp/gridbase.json
docker compose cp directus:/tmp/gridbase.json directus/schema/gridbase.json   # SQLite; for Postgres overwrite gridbase.postgres.json
docker compose run --rm directus-init                                        # must report "変更なし" (no change)
```

### Postgres instead of SQLite

```sh
docker compose -f docker-compose.yml -f docker-compose.postgres.yml up -d --build
```

Set `POSTGRES_PASSWORD` in `.env`. Existing SQLite data is not migrated.

## Production

Checklist:

1. **Start from fresh secrets, before the first start.** Replace every secret in `.env` (`openssl rand -hex 32` for tokens, `DIRECTUS_KEY`/`DIRECTUS_SECRET` and passwords, `openssl rand -base64 32` for `NEXTAUTH_SECRET`), including `GRIDBASE_ADMIN_PASSWORD` (8+ characters with a letter and a digit) and `DIRECTUS_ADMIN_PASSWORD`. `GRIDBASE_TUNNEL_ADMIN_TOKEN` must be empty (admin API off) or 24+ random characters. In an exposed setup `directus-init` refuses to start otherwise (see above). If you already started once with the defaults, read [Changing credentials after the first start](#changing-credentials-after-the-first-start): editing `.env` is **not enough** for the stored admin accounts.
2. Choose domains. **Put the admin console on a different registrable domain than the tunnel domain**: e.g. admin `admin.example.org`, tunnels `*.tunnel.example.com`. Tunnel users control what is served on their subdomains; if the console shares a registrable domain with them, cookie tossing and other same-site attacks become possible (see [web/README.md](./web/README.md)).
3. Set in `.env`:

```env
GRIDBASE_BASE_DOMAIN=tunnel.example.com
GRIDBASE_ADMIN_DOMAIN=admin.example.org
GRIDBASE_ADMIN_URL=https://admin.example.org
GRIDBASE_SITE_SCHEME=https://
GRIDBASE_TUNNEL_SERVER=tunnel.example.com:9443
GRIDBASE_BIND_ADDRESS=0.0.0.0
```

4. DNS: `A`/`AAAA` for `admin.example.org`, and a wildcard `*.tunnel.example.com` plus `tunnel.example.com` pointing at the host.
5. TLS. The admin host can use Caddy's normal automatic HTTPS. A wildcard certificate for `*.tunnel.example.com` needs the **DNS-01** challenge, which needs a Caddy build with your DNS provider's plugin. Example for Cloudflare (this example is not exercised by the repository's tests):

`caddy/Dockerfile`:

```dockerfile
FROM caddy:2-builder AS builder
RUN xcaddy build --with github.com/caddy-dns/cloudflare
FROM caddy:2-alpine
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
```

`docker-compose.override.yml`:

```yaml
services:
  caddy:
    build: ./caddy
    environment:
      CLOUDFLARE_API_TOKEN: ${CLOUDFLARE_API_TOKEN}   # Zone:DNS:Edit for the tunnel zone only
```

`Caddyfile`:

```caddyfile
{
	email you@example.com
}

admin.example.org {
	encode zstd gzip
	reverse_proxy web:3000
}

*.tunnel.example.com {
	tls {
		dns cloudflare {env.CLOUDFLARE_API_TOKEN}
	}
	reverse_proxy tunnel:9080
}
```

6. TLS on the tunnel port. Port `9443` is plain TCP by default and the API key travels over it. Give the tunnel server a certificate for the host clients dial and make clients use `--tls`. Example override (certificate files you provide, e.g. exported from Caddy or from another ACME client):

```yaml
services:
  tunnel:
    environment:
      GRIDBASE_TLS_CERT: /certs/fullchain.pem
      GRIDBASE_TLS_KEY: /certs/privkey.pem
    volumes:
      - ./certs:/certs:ro
```

   Clients: `gridbase-tunnel-client --server tunnel.example.com:9443 --tls --api_key <key> --local_server_url http://127.0.0.1:8000` (always pin `--local_server_url`). See [tunnel/README.md](./tunnel/README.md) for mTLS and the other settings.
7. Open ports `80`, `443`, `9443`. Do not publish Directus; keep `directus` without `ports:`.
8. Back up the `directus-db` volume (SQLite file) or the Postgres volume.
9. Keep `GRIDBASE_SIGNUP_ENABLED=false` unless you really want open registration.
10. After the first start, remove `GRIDBASE_ADMIN_PASSWORD` from `.env` (it is only used once, and is visible through `docker inspect`). The check of the stored admin passwords keeps running without it.

### Changing credentials after the first start

Several values in `.env` are only read **once**, when the database is created. Editing `.env` afterwards does not change what is stored.

| What | When `.env` is read | How to change it later |
|---|---|---|
| gridbase admin (`GRIDBASE_ADMIN_EMAIL` / `GRIDBASE_ADMIN_PASSWORD`) | Only on the first start, while no active admin exists. The password is stored hashed in the `users` collection. | Sign in, open **アカウント** (`/account`), enter the current password and a new one. All other sessions are signed out. If you lost the password: use the e-mail reset (needs SMTP), or recreate the data with `docker compose down -v`, or put a new bcrypt hash on the user in the Directus UI (generate it with the command below the table; the password is read without echo and never appears in the shell history or the process list). **Also set `password_changed_at` of that user to the current time** (ISO 8601, e.g. `2026-10-20T12:00:00.000Z`): sessions issued before that moment are rejected, so an attacker who already holds a session of a hijacked account is signed out. If you only replace the hash, existing sessions stay valid for up to 7 days. |
| Directus admin (`DIRECTUS_ADMIN_EMAIL`, `DIRECTUS_ADMIN_PASSWORD`, `DIRECTUS_ADMIN_TOKEN`) | Only when Directus creates its first admin. Changing them later changes nothing in the database, and a changed `DIRECTUS_ADMIN_TOKEN` makes `directus-init` fail (it can no longer authenticate). | Enable the `ports:` block of `directus`, sign in to the Directus UI with the **old** values, change the password and the token on the admin user, then put the **new** token in `.env` (`DIRECTUS_ADMIN_TOKEN`) and run `docker compose up -d`. Or start over with `docker compose down -v`. |
| `GRIDBASE_WEB_DIRECTUS_TOKEN`, `GRIDBASE_TUNNEL_DIRECTUS_TOKEN` | Every `up` (init resyncs the service users). | Edit `.env`, `docker compose up -d`. |
| `NEXTAUTH_SECRET` | Every start of `web`. | Edit `.env`, `docker compose up -d`. Every admin-console session ends. |
| `DIRECTUS_KEY`, `DIRECTUS_SECRET` | Every start of `directus`. | Edit `.env`, `docker compose up -d`. gridbase uses static tokens, so nothing else is affected. |
| `GRIDBASE_TUNNEL_ADMIN_TOKEN` | Every start of `tunnel`. | Edit `.env`, `docker compose up -d`. |
| `POSTGRES_PASSWORD` | Only when the Postgres volume is first created. | `ALTER USER directus PASSWORD '…'` inside Postgres, then update `.env`. |

Generating a bcrypt hash for the manual reset above (the password is typed without echo and goes through stdin, so it is not stored in the shell history or shown in `ps`; use your own new password, not a value copied from this page):

```sh
printf 'New password: '; stty -echo; read -r NEW_PW; stty echo; echo
printf '%s' "$NEW_PW" | docker compose exec -T web node --input-type=module \
  -e "import b from 'bcryptjs'; let s=''; for await (const c of process.stdin) s+=c; console.log(b.hashSync(s, 10))"
unset NEW_PW
```

Moving an instance that already ran with the default admin password to an exposed setup: `directus-init` exits 1 when a stored admin still has a default password, so `tunnel`, `web` and `caddy` do not start (`web` itself also exits with code 3 as a second layer). This holds whether or not `GRIDBASE_ADMIN_PASSWORD` is still in `.env`. Start it locally (`GRIDBASE_BIND_ADDRESS=127.0.0.1`, `http`, `*.localhost` domains), change the password in **アカウント**, then switch to the exposed setup.

## Security notes

- The admin console should live on its own registrable domain (see above).
- The tunnel server's own admin API (`/api/*` on its admin host) is not reachable from outside: the compose config gives it an internal-only host name (`tunnel-admin.internal`) that Caddy never routes.
- The tunnel server's logs and admin API identify API keys only by a fingerprint (`key_id`), never by the key itself. `GRIDBASE_TUNNEL_ADMIN_TOKEN` must be empty (admin API disabled) or a random value of at least 24 characters; a value starting with `change-me`, `local-dev-` or `replace-with-` leaves the admin API disabled (the `.env.example` value is one of those), and `directus-init` rejects it in an exposed setup.
- Authentication on the tunnel port is the API key. mTLS is optional and, if enabled, uses a CA you control.
- A client uses the target URL the server sends it unless `--local_server_url` is given. The quick start runs a client inside the compose network, which can reach internal services; in production always pin `--local_server_url` on the client.
- Directus service tokens are least-privilege (see above), but they are still database credentials; do not reuse them elsewhere.
- Sessions: over https the cookie is `__Host-next-auth.session-token` (host-only, `Secure`, `Path=/`), so a sibling subdomain cannot plant a session cookie. Changing or resetting a password signs out all other sessions. Login is rate limited per (IP, e-mail) and per e-mail.
- Next.js 14 has unresolved advisories (`npm audit --omit=dev` reports next and postcss; the fix is the breaking upgrade to Next.js 16). Putting the console behind a reverse proxy does **not** mitigate them. In this setup the image-optimizer, rewrites, Server Actions, i18n and Windows advisories do not apply (the feature is unused or disabled, e.g. `images.unoptimized`), but the App Router / React Server Components denial-of-service advisories may apply, and a caching proxy in front would make the cache-poisoning ones relevant. For a public instance, put the console behind a VPN, an IP allow-list or an authenticating proxy until the Next.js 16 migration. The per-advisory table is in [web/README.md](./web/README.md#依存関係メモ).
- To report a vulnerability, see [SECURITY.md](./SECURITY.md).

## Known limitations

- **TCP tunnels have no public listener.** The client supports `mode=tcp` but the server does not open an external port; only HTTP tunnels work end to end. The console shows TCP as "not supported" and its API answers `400` for `mode=tcp`.
- **The console UI is Japanese only** (no i18n).
- **One request at a time per tunnel**, fully buffered in memory (base64 on the wire, ~1.33x), with a fixed **30 second** timeout per request.
- **No WebSocket, Server-Sent Events or streaming responses.**
- A new or changed subdomain/API key takes up to **30 seconds** to reach the tunnel server (polling).
- A client whose API key is unknown to the server at connect time exits; restart it.
- Rate limiting in the console is in-memory (single container).

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `directus-init` exits non-zero | `docker compose logs directus-init`. Typical: an exposed setup (`GRIDBASE_BIND_ADDRESS` not `127.0.0.1`, `https`, or a real domain in `GRIDBASE_BASE_DOMAIN` / `GRIDBASE_ADMIN_DOMAIN`) still has a default secret from `.env.example`; a stored admin still has a default password ("保存済みの管理者のパスワードに問題があります"); a token is shorter than 20 characters or two tokens are equal. |
| `web` exits right after start with "公開構成" | Second layer: the setup is exposed and the stored admin password is still the default (or could not be verified). See [Changing credentials after the first start](#changing-credentials-after-the-first-start). |
| `docker compose down -v` fails with "DIRECTUS_KEY … missing" | Compose needs the variables of `.env` even to stop the stack. Run `cp .env.example .env` first (or `docker compose --env-file .env.example down -v`). |
| Directus never creates an admin (`401` on every call) | `DIRECTUS_ADMIN_EMAIL` was rejected as an invalid address (e.g. `….localhost`). Fix `.env`, then `docker compose down -v` (the half-initialised database must be recreated). |
| `bind: address already in use` | Change `GRIDBASE_HOST_*_PORT` in `.env` (see [Changing ports](#changing-ports)). |
| `curl http://foo.gridbase.localhost/` returns `503 unknown subdomain` | The 30 s sync has not happened yet, or the subdomain does not exist / is not `active`. |
| … returns `503 tunnel error: tunnel client not connected` | The subdomain is known but no client is connected (or it just disconnected). |
| … returns `502 tunnel error` | A client is connected but the request failed: your local app is down or unreachable from the client, or the response was too large (`502 tunnel error: upstream response too large`). |
| Sign-in does nothing / 403 on writes | `GRIDBASE_ADMIN_URL` does not match the URL in the browser (scheme, host and port must match exactly). |
| Subdomain created but client exits at handshake | Wait 30 s after creating the subdomain, then start the client. |

## Tests

```sh
(cd directus/init && npm ci) && node --test directus/init/*.test.mjs   # default-secret / stored-admin-password checks
(cd web && npm ci && npm test)                      # callbackUrl / password rules / rate limit / input validation
(cd tunnel && go vet ./... && go test ./...)        # tunnel server and client
docker compose up -d --build && node tests/e2e/smoke.mjs   # end to end against the running stack
```

`tests/e2e/smoke.mjs` signs in, issues an API key, creates a subdomain, connects a client and fetches a page through the tunnel, and checks permissions, CSRF rejection, password change (other sessions are revoked) and that `directus-init` is idempotent. It needs Node 20+ and Docker. CI runs everything on every push ([.github/workflows/ci.yml](./.github/workflows/ci.yml)).

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
.github/workflows/ci.yml      CI
SECURITY.md  CONTRIBUTING.md
```

## License

[MIT](./LICENSE). Copyright (c) 2026 nogataka.
