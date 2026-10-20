# Production

How to run gridbase on a public host. For a local trial, use the [Quick start](../README.md#quick-start) instead.

## Checklist

1. Start from fresh secrets, before the first start. Replace every secret in `.env` (`openssl rand -hex 32` for tokens, `DIRECTUS_KEY`/`DIRECTUS_SECRET` and passwords, `openssl rand -base64 32` for `NEXTAUTH_SECRET`), including `GRIDBASE_ADMIN_PASSWORD` (8+ characters with a letter and a digit) and `DIRECTUS_ADMIN_PASSWORD`. `GRIDBASE_TUNNEL_ADMIN_TOKEN` must be empty (admin API off) or 24+ random characters. If `.env` marks the setup as exposed, `directus-init` refuses to start otherwise. It decides from `.env` alone, so a tunnel or proxy on the host that `.env` does not mention is not detected (see [how-it-works.md](./how-it-works.md#what-directus-init-does)). If you already started once with the defaults, read [Changing credentials after the first start](#changing-credentials-after-the-first-start): editing `.env` is not enough for the stored admin accounts.
2. Choose domains. Put the admin console on a different registrable domain than the tunnel domain. For example, use `admin.example.org` for the admin console and `*.tunnel.example.com` for tunnels. Tunnel users control what is served on their subdomains; if the console shares a registrable domain with them, cookie tossing and other same-site attacks become possible (see [web/README.md](../web/README.md)).
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
5. TLS. The admin host can use Caddy's normal automatic HTTPS. A wildcard certificate for `*.tunnel.example.com` needs the DNS-01 challenge, which needs a Caddy build with your DNS provider's plugin. Example for Cloudflare (this example is not exercised by the repository's tests):

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

   Clients: `gridbase-tunnel-client --server tunnel.example.com:9443 --tls --api_key <key> --local_server_url http://127.0.0.1:8000` (always pin `--local_server_url`). See [tunnel/README.md](../tunnel/README.md) for mTLS and the other settings.
7. Open ports `80`, `443`, `9443`. Do not publish Directus; keep `directus` without `ports:`.
8. Back up the `directus-db` volume (SQLite file) or the Postgres volume.
9. Keep `GRIDBASE_SIGNUP_ENABLED=false` unless you really want open registration.
10. After the first start, remove `GRIDBASE_ADMIN_PASSWORD` from `.env` (it is only used once, and is visible through `docker inspect`). The check of the stored admin passwords keeps running without it.

Also read [security.md](./security.md) before you expose the console. The console uses Next.js 14, which has known upstream vulnerabilities. Put it behind a VPN, an IP allow-list or an authenticating proxy (see [security.md](./security.md)).

## Changing credentials after the first start

Several values in `.env` are only read once, when the database is created. Editing `.env` afterwards does not change what is stored.

| What | When `.env` is read | How to change it later |
|---|---|---|
| gridbase admin (`GRIDBASE_ADMIN_EMAIL` / `GRIDBASE_ADMIN_PASSWORD`) | Only on the first start, while no active admin exists. The password is stored hashed in the `users` collection. | Sign in, open アカウント (Account, `/account`), and change it there. All other sessions are signed out. If you lost the password, see [If you lost the gridbase admin password](#if-you-lost-the-gridbase-admin-password). |
| Directus admin (`DIRECTUS_ADMIN_EMAIL`, `DIRECTUS_ADMIN_PASSWORD`, `DIRECTUS_ADMIN_TOKEN`) | Only when Directus creates its first admin. Changing them later changes nothing in the database, and a changed `DIRECTUS_ADMIN_TOKEN` makes `directus-init` fail (it can no longer authenticate). | Enable the `ports:` block of `directus`, sign in to the Directus UI with the old values, change the password and the token on the admin user, then put the new token in `.env` (`DIRECTUS_ADMIN_TOKEN`) and run `docker compose up -d`. Or start over with `docker compose down -v`. |
| `GRIDBASE_WEB_DIRECTUS_TOKEN`, `GRIDBASE_TUNNEL_DIRECTUS_TOKEN` | Every `up` (init resyncs the service users). | Edit `.env`, `docker compose up -d`. |
| `NEXTAUTH_SECRET` | Every start of `web`. | Edit `.env`, `docker compose up -d`. Every admin-console session ends. |
| `DIRECTUS_KEY`, `DIRECTUS_SECRET` | Every start of `directus`. | Edit `.env`, `docker compose up -d`. gridbase uses static tokens, so nothing else is affected. |
| `GRIDBASE_TUNNEL_ADMIN_TOKEN` | Every start of `tunnel`. | Edit `.env`, `docker compose up -d`. |
| `POSTGRES_PASSWORD` | Only when the Postgres volume is first created. | `ALTER USER directus PASSWORD '…'` inside Postgres, then update `.env`. |

### If you lost the gridbase admin password

Choose one of these:

- Use the e-mail reset. This needs SMTP.
- Recreate the data with `docker compose down -v`. All users, API keys and subdomains are deleted.
- Put a new bcrypt hash on the user in the Directus UI. The command below generates the hash.

If you set the hash by hand, also set `password_changed_at` of that user to the current time (ISO 8601, for example `2026-10-20T12:00:00.000Z`). Sessions issued before that moment are rejected, so an attacker who holds a session of a hijacked account is signed out. If you only replace the hash, existing sessions stay valid for up to 7 days.

This command generates a bcrypt hash for the manual reset above. The password is typed without echo and goes through stdin, so it is not stored in the shell history or shown in `ps`. Use your own new password, not a value copied from this page.

```sh
printf 'New password: '; stty -echo; read -r NEW_PW; stty echo; echo
printf '%s' "$NEW_PW" | docker compose exec -T web node --input-type=module \
  -e "import b from 'bcryptjs'; let s=''; for await (const c of process.stdin) s+=c; console.log(b.hashSync(s, 10))"
unset NEW_PW
```

### Moving a local instance to an exposed setup

If an instance already ran with the default admin password, `directus-init` exits 1 when a stored admin still has a default password, so `tunnel`, `web` and `caddy` do not start (`web` itself also exits with code 3 as a second layer). This holds whether or not `GRIDBASE_ADMIN_PASSWORD` is still in `.env`. Start it locally (`GRIDBASE_BIND_ADDRESS=127.0.0.1`, `http`, `*.localhost` domains), change the password in アカウント (Account), then switch to the exposed setup.
