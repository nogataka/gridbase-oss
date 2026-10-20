# Troubleshooting

Start with `docker compose ps -a` (the `-a` also shows the one-shot `directus-init`) and `docker compose logs <service>`.

| Symptom | Cause / fix |
|---|---|
| `directus-init` exits non-zero | `docker compose logs directus-init`. Typical: an exposed setup (`GRIDBASE_BIND_ADDRESS` not `127.0.0.1`, `https`, or a real domain in `GRIDBASE_BASE_DOMAIN` / `GRIDBASE_ADMIN_DOMAIN`) still has a default secret from `.env.example`; a stored admin still has a default password ("保存済みの管理者のパスワードに問題があります"); a token is shorter than 20 characters or two tokens are equal. |
| `web` exits right after start with "公開構成" | Second layer: the setup is exposed and the stored admin password is still the default (or could not be verified). See [Changing credentials after the first start](./production.md#changing-credentials-after-the-first-start). |
| `docker compose down -v` fails with "DIRECTUS_KEY … missing" | Compose needs the variables of `.env` even to stop the stack. Run `cp .env.example .env` first (or `docker compose --env-file .env.example down -v`). |
| Directus never creates an admin (`401` on every call) | `DIRECTUS_ADMIN_EMAIL` was rejected as an invalid address (e.g. `….localhost`). Fix `.env`, then `docker compose down -v` (the half-initialised database must be recreated). |
| `bind: address already in use` | Change `GRIDBASE_HOST_*_PORT` in `.env` (see [Changing ports](./configuration.md#changing-ports)). |
| Connections to host port `80` are reset, but caddy is healthy | Seen once on macOS with Docker Desktop (the cause was not identified; port `18080` worked). Use another port: [Changing ports](./configuration.md#changing-ports). |
| `curl http://foo.gridbase.localhost/` returns `503 unknown subdomain` | The 30 s sync has not happened yet, or the subdomain does not exist / is not `active`. |
| … returns `503 tunnel error: tunnel client not connected` | The subdomain is known but no client is connected (or it just disconnected). |
| … returns `502 tunnel error` | A client is connected but the request failed: your local app is down or unreachable from the client, or the response was too large (`502 tunnel error: upstream response too large`). |
| Sign-in does nothing / 403 on writes | `GRIDBASE_ADMIN_URL` does not match the URL in the browser (scheme, host and port must match exactly). |
| Subdomain created but client exits at handshake | Wait 30 s after creating the subdomain, then start the client. |

All response codes that visitors can see are listed in [tunnel/README.md](../tunnel/README.md#response-codes-seen-by-visitors).
