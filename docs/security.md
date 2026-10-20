# Security notes

To report a vulnerability, see [SECURITY.md](../SECURITY.md).

- The admin console should live on its own registrable domain (see [production.md](./production.md)).
- The tunnel server's own admin API (`/api/*` on its admin host) is not reachable from outside: the compose config gives it an internal-only host name (`tunnel-admin.internal`) that Caddy never routes.
- The tunnel server's logs and admin API identify API keys only by a fingerprint (`key_id`), never by the key itself. `GRIDBASE_TUNNEL_ADMIN_TOKEN` must be empty (admin API disabled) or a random value of at least 24 characters; a value starting with `change-me`, `local-dev-` or `replace-with-` leaves the admin API disabled (the `.env.example` value is one of those), and `directus-init` rejects it in an exposed setup.
- Authentication on the tunnel port is the API key. mTLS is optional and, if enabled, uses a CA you control.
- A client uses the target URL the server sends it unless `--local_server_url` is given. The Quick start runs a client inside the compose network, which can reach internal services; in production always pin `--local_server_url` on the client.
- Directus service tokens are least-privilege (see [how-it-works.md](./how-it-works.md#what-directus-init-does)), but they are still database credentials; do not reuse them elsewhere.
- Sessions: over https the cookie is `__Host-next-auth.session-token` (host-only, `Secure`, `Path=/`), so a sibling subdomain cannot plant a session cookie. Changing or resetting a password signs out all other sessions. Login is rate limited per (IP, e-mail) and per e-mail.
- Next.js 14 has unresolved advisories (`npm audit --omit=dev` reports next and postcss; the fix is the breaking upgrade to Next.js 16). Putting the console behind a reverse proxy does **not** mitigate them. In this setup the image-optimizer, rewrites, Server Actions, i18n and Windows advisories do not apply (the feature is unused or disabled, e.g. `images.unoptimized`), but the App Router / React Server Components denial-of-service advisories may apply, and a caching proxy in front would make the cache-poisoning ones relevant. For a public instance, put the console behind a VPN, an IP allow-list or an authenticating proxy until the Next.js 16 migration. The per-advisory table is in [web/README.md](../web/README.md#依存関係メモ).

- Forwarding headers on the tunnel server:
  - The server uses `X-Forwarded-Proto` and `X-Forwarded-For` only when the TCP peer is listed in `http.trusted_proxies`. For any other peer, it decides by whether the connection itself is TLS.
  - If `trusted_proxies` is empty, a subdomain policy that allows only `https` (`allowed_protocols`) rejects every request, unless TLS is terminated on `http.port` itself.
  - For requests from a peer that is not trusted, `X-Forwarded-*`, `X-Real-IP` and `Forwarded` headers are not passed on to your local app.
  - In the compose stack, connections from Caddy (over Docker's internal network) are trusted by default, so this works without extra settings. Details: [Protocol and `allowed_protocols`](../tunnel/README.md#protocol-and-allowed_protocols) in tunnel/README.md.

More detail on the tunnel side (handshake limits, `ip_allow_list`, trusted proxies) is in [tunnel/README.md](../tunnel/README.md#security-notes).
