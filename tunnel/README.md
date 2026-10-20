# gridbase tunnel

Self-hosted HTTP tunnel. A small Go server exposes `https://<name>.<your-domain>` and forwards each request over a long-lived connection to a client running next to your local application.

```
browser ──HTTP(S)──▶ Caddy/nginx ──▶ tunnel server :9080 ──(tunnel port :9443)──▶ gridbase client ──▶ http://127.0.0.1:3000
```

This directory contains the tunnel server and client. It is normally run as part of the gridbase docker-compose stack (Caddy + tunnel + Directus + admin UI), but it also works on its own.

- Server: `./server` (binary `gridbase-tunnel-server`)
- Client: `./client` (binary `gridbase-tunnel-client`)
- Go 1.21+, one external dependency (`gopkg.in/yaml.v3`, client config only)

## Quick start (no TLS, local)

```sh
cd tunnel
cp config/server.example.json config/server.json   # replace the domain api_key; set admin.bearer_token (>= 24 chars) or leave the admin API disabled
go run ./server --config config/server.json          # HTTP :9080, tunnel :9443 (plain TCP)

# a local app
python3 -m http.server 3000

# connect the client (separate terminal)
go run ./client --server 127.0.0.1:9443 --api_key <the api_key from server.json>

# request through the tunnel; *.localhost resolves to 127.0.0.1 in browsers and curl
curl http://web.gridbase.localhost:9080/
```

The subdomain `web` and its upstream (`local_url`) come from the `domains` entry in the config file or, in the compose stack, from Directus.

## Server configuration

JSON file (`--config`, default `config/server.json`). Every environment variable overrides the file. See `config/server.example.json` (local, plain) and `config/server.tls.example.json` (TLS on the tunnel port).

| Key | Environment variable | Default | Description |
|---|---|---|---|
| `base_domain` | `GRIDBASE_BASE_DOMAIN` | required | Apex for tunnel subdomains, e.g. `gridbase.localhost` or `tunnel.example.com`. `Host: foo.<base_domain>[:port]` is routed to subdomain `foo` (case-insensitive, port ignored). The apex itself and other hosts get `400`. |
| `http.port` | `GRIDBASE_HTTP_PORT` | `9080` | Public HTTP listener (tunnelled traffic and the admin API). Put a TLS-terminating proxy in front. |
| `http.read_timeout` / `write_timeout` / `idle_timeout` | | unset | Seconds. |
| `http.max_request_body_bytes` | `GRIDBASE_HTTP_MAX_REQUEST_BODY_BYTES` | `33554432` (32 MiB) | Ceiling for a visitor's request body (`413` above it). A subdomain's `max_request_bytes` policy can only lower it. |
| `http.max_response_body_bytes` | `GRIDBASE_HTTP_MAX_RESPONSE_BODY_BYTES` | `33554432` (32 MiB) | Ceiling for the response body a tunnel client sends back. A subdomain's `max_response_bytes` policy can only lower it. Above the limit the visitor gets `502` and the tunnel connection is dropped (the client reconnects). |
| `http.trusted_proxies` | `GRIDBASE_HTTP_TRUSTED_PROXIES` (comma separated) | empty | IPs / CIDRs of reverse proxies in front of `http.port`. See "Client IP and `ip_allow_list`" and "Protocol and `allowed_protocols`" below. |
| `tunnel.port` | `GRIDBASE_TUNNEL_PORT` | `9443` | Port the clients connect to. Expose it directly, not through an HTTP proxy. |
| `tunnel.max_frame_bytes` | `GRIDBASE_TUNNEL_MAX_FRAME_BYTES` | `16777216` (16 MiB) | Largest protocol frame accepted from a tunnel connection. A longer length header closes the connection immediately, before anything is allocated. The hello that precedes authentication is capped at 64 KiB regardless. |
| `tunnel.handshake_timeout` | `GRIDBASE_TUNNEL_HANDSHAKE_TIMEOUT` | `10` | Seconds a new tunnel connection has to finish the TLS handshake and send its hello. Silent or trickling peers are dropped. |
| `tls.cert`, `tls.key` | `GRIDBASE_TLS_CERT`, `GRIDBASE_TLS_KEY` | unset | Serve TLS on the tunnel port. **Both empty = plain TCP** (a warning is logged; use only locally or on a trusted network). Setting only one is a configuration error. |
| `tls.client_ca` | `GRIDBASE_CLIENT_CA` | unset | Optional. When set, clients must present a certificate signed by this CA (mTLS). When empty no client certificate is requested and authentication relies on the API key. Requires `tls.cert`/`tls.key`. |
| `admin.bearer_token` | `GRIDBASE_ADMIN_BEARER_TOKEN` | unset | Bearer token for the admin API. **The admin API is disabled (every call gets `401`, a warning is logged at startup) when the token is empty, shorter than 24 characters, or starts with `change-me`, `local-dev-` or `replace-with-`.** Generate one with e.g. `openssl rand -hex 32`. |
| `admin.host` | `GRIDBASE_ADMIN_HOST` | `admin.<base_domain>` | Host header on which the admin API is served. |
| `admin.cors_origin` | `GRIDBASE_ADMIN_CORS_ORIGIN` | empty | Single origin allowed to call the admin API from a browser. **Empty = no CORS headers are sent.** |
| `admin.timezone` | `GRIDBASE_ADMIN_TIMEZONE` | `UTC` | IANA zone for timestamps in `POST /api/config` responses (tzdata is embedded). |
| `directus.url` | `GRIDBASE_DIRECTUS_URL` | unset | Directus base URL. With `directus.token` set the server polls `users` and `subdomains` every 30 s. |
| `directus.token` | `GRIDBASE_DIRECTUS_TOKEN` | unset | Static token with read access to `users` and `subdomains`. Prefer the environment variable over the file. |
| `domains[]` | | optional | Static tunnels: `api_key`, `subdomain`, `type` (`http`/`tcp`), `local_url` (http) or `local_host`/`local_port` (tcp), optional `policy`. May be empty when Directus is the only source. |

CLI flags: `--config`, `--base-domain`, `--directus-url`, `--directus-token`, `--admin-bearer-token`, `--healthcheck`.

### Domains from the file and from Directus

Static `domains` and Directus records are merged (Directus wins on a subdomain conflict). A subdomain belongs to one API key; if two users claim the same subdomain the second record is skipped with a warning. `POST /api/config` re-reads the file and re-applies the last Directus snapshot, so Directus-sourced domains survive a reload.

Directus contract (collections `users` and `subdomains`, token with read access):

- `GET /items/users?fields=userId,api_key,settings,status&filter[status][_eq]=active&limit=-1`
- `GET /items/subdomains?fields=id,userId,subdomain,mode,local_server_url,local_host,local_port,status,description&filter[status][_eq]=active&limit=-1`

`users.settings` (JSON object, optional) is turned into the per-subdomain policy: `allowed_protocols`, `enable_ip_filtering` + `ip_whitelist`, `default_headers`, `max_request_bytes`, `max_response_bytes`.

One bad record never blocks the others. A record with unreadable `settings` (not a JSON object), a duplicate, a missing key or subdomain, or an unsupported mode is skipped and reported as a warning (log event `directus.snapshot_warning`, and `config_warnings` in the `POST /api/config` response). Warnings identify API keys by fingerprint only.

### Client IP and `ip_allow_list`

The address checked against `ip_whitelist` is the TCP peer address of the connection to `http.port`. `X-Forwarded-For` and `X-Real-IP` are **ignored by default**, because any visitor can send them.

Behind a reverse proxy (Caddy, nginx, a load balancer) list the proxy in `http.trusted_proxies`. For requests that arrive from a trusted proxy the server then reads `X-Forwarded-For` from the right, skips entries added by trusted proxies, and uses the first remaining address, so a value the visitor prepended is never used. The proxy must overwrite or append to the header rather than pass a visitor-supplied one through unchanged (Caddy does by default; nginx needs `proxy_set_header X-Forwarded-For $remote_addr;` when it is the first hop). The compose stack's `config/server.compose.json` trusts the private ranges because only Caddy can reach port 9080 there. Never expose `http.port` directly to the internet while it trusts proxies.

### Protocol and `allowed_protocols`

`allowed_protocols` follows the same trust rule. From a peer that is **not** in `http.trusted_proxies` the protocol is `https` only if the connection to `http.port` itself is TLS, otherwise `http`; a visitor-supplied `X-Forwarded-Proto` is ignored, so an `https`-only policy cannot be bypassed by sending `X-Forwarded-Proto: https` over plain HTTP. From a trusted proxy the server believes `X-Forwarded-Proto` (the rightmost value, i.e. the one the nearest proxy wrote; a missing header falls back to the connection). With the default empty `trusted_proxies`, an `https`-only policy therefore rejects everything unless TLS terminates at `http.port` itself, so list your TLS-terminating proxy. The compose stack (Caddy, trusted private ranges) behaves as before because Caddy sets `X-Forwarded-Proto` itself.

Forwarding headers are also not relayed blindly: for requests from a peer that is not a trusted proxy, every `X-Forwarded-*`, `X-Real-IP` and `Forwarded` header is dropped before the request goes to the tunnel client, so the backend never sees a value a visitor forged by hitting `http.port` directly. Headers from trusted proxies pass through unchanged, and `default_headers` from the policy are still added.

### HTTP endpoints (port `http.port`)

| Path | Host | Auth | Purpose |
|---|---|---|---|
| `/_gridbase/health` | any | none | Liveness, returns `200 ok`. |
| `/api/health`, `/api/tunnels`, `/api/metrics` (GET), `/api/config` (POST = reload) | `admin.host` only | `Authorization: Bearer <admin.bearer_token>` | Admin API. `/api/metrics` returns in-memory counters only (nothing is pushed to Directus). |
| everything else | `*.<base_domain>` | per-subdomain policy | Forwarded through the tunnel. `/api/*` on a tunnel host is forwarded too. |

### Response codes seen by visitors

| Status | When | Body |
|---|---|---|
| `400` | `Host` is not `<sub>.<base_domain>` (including the apex) | `invalid host header` |
| `403` | Subdomain policy rejects the request (`ip_allow_list`, `allowed_protocols`) | policy message |
| `413` | Request body above `http.max_request_body_bytes` or the policy's `max_request_bytes` | `request body exceeded N bytes` |
| `503` | Unknown or inactive subdomain | `unknown subdomain` |
| `503` | Subdomain exists but its client is not connected | `tunnel error: tunnel client not connected` |
| `502` | The tunnel request failed (timeout, protocol error, dropped connection) | `tunnel error` |
| `502` | Response body above `http.max_response_body_bytes` / the policy's `max_response_bytes` | `tunnel error: upstream response too large` |
| `502` | The client could not reach the local application, or its response exceeded the client limit | generic message from the client |

Error bodies are fixed strings; details are in the server log (`tunnel.forward_failed`).

Container health check: `gridbase-tunnel-server --healthcheck --config /etc/gridbase/server.json` (used by the image's `HEALTHCHECK`; the distroless image has no curl).

## Client

```sh
gridbase-tunnel-client --server tunnel.example.com:9443 --tls --api_key <key>
```

There is no built-in server address; `--server` (or `server_addr` in a config file, or `GRIDBASE_CLIENT_SERVER_ADDR`) is required. There is no embedded certificate.

| Flag | Config key / env | Description |
|---|---|---|
| `--server host:port` (alias `--server_addr`) | `server_addr` / `GRIDBASE_CLIENT_SERVER_ADDR` | Tunnel server. |
| `--api_key` | `api_key` / `GRIDBASE_CLIENT_API_KEY` | API key (identifies the user and their subdomains). |
| `--tls` | `tls.enabled` / `GRIDBASE_CLIENT_TLS` | Use TLS, verified against the system trust store. |
| `--ca <file>` | `tls.ca` / `GRIDBASE_CLIENT_TLS_CA` | Trust this CA instead of the system roots (implies TLS). |
| `--insecure` | `tls.insecure` / `GRIDBASE_CLIENT_TLS_INSECURE` | TLS without verification, for development only. Cannot be combined with `--ca`. |
| `--cert <file>` `--key <file>` | `tls.cert` `tls.key` | Client certificate, only when the server sets `tls.client_ca` (implies TLS; both required). |
| `--server_name` | `server_name` / `GRIDBASE_CLIENT_SERVER_NAME` | TLS server name; defaults to the host in `--server`. |
| `--local_server_url` | `http.override_url` | Fix the upstream URL on this machine. **When set, the `Target` sent by the server is ignored.** See "Which upstream the client calls" below. |
| (config / env only) | `http.max_request_bytes` / `http.max_response_bytes`, `GRIDBASE_CLIENT_HTTP_MAX_REQUEST_BYTES` / `GRIDBASE_CLIENT_HTTP_MAX_RESPONSE_BYTES` | Per-request caps on the body received from the server and on the local application's response. Default 32 MiB each; a larger local response becomes `502`. |
| `--mode`, `--tunnel_id`, `--tcp_host`, `--tcp_port` | | TCP mode (see limits below). |
| `--config <file>` | `GRIDBASE_CLIENT_CONFIG` | JSON or YAML file; see `config/client.example.json`. Precedence: file < environment < flags. |

Without any TLS option the client connects in plain TCP and prints a warning, because the API key then travels in clear text.

The client does not follow HTTP redirects from the local application: a `3xx` (with its `Location` and `Set-Cookie`) is passed to the visitor unchanged. This keeps an open redirect in your app from turning the client into a proxy to your LAN or cloud metadata endpoint.

### Which upstream the client calls

Each request frame carries a `Target` URL that the server builds from the `local_server_url` stored for the subdomain. Without `--local_server_url` the client **calls whatever `Target` the server sends**, so it trusts the server (and the network path to it) completely:

- Use `--local_server_url http://127.0.0.1:3000` (or `http.override_url`) whenever you run the client against a server you do not fully control, or over a plain-TCP tunnel port.
- Over plain TCP an attacker on the path can inject a request with any `Target` and make the client fetch internal URLs. Use `--tls` for anything beyond a local machine.
- Do not run the client inside the server's docker network without `--local_server_url`: a user could then register `http://directus:8055` (or any other internal service) as their upstream and expose it.

If the server rejects the handshake (for example an unknown API key) the client exits instead of retrying; restart it after fixing the key or after the server has loaded the domain. Dropped connections are retried with backoff.

## Security notes

- Authentication is the API key (`ClientHello.api_key`). mTLS is optional and, if used, should use a CA you control; there is no shared client certificate.
- Use TLS on the tunnel port in production (`tls.cert`/`tls.key`, certificate for the host clients dial). Without it API keys cross the network unencrypted. The server logs a `tunnel.listener_plaintext` warning at startup when TLS is off, and the client warns on every start without `--tls`.
- **API keys are never written to logs, the admin API, metrics, request ids or error messages.** Tunnels are identified by a key fingerprint (`key_id`: the first 12 hex characters of the SHA-256 of the key) in logs and in `/api/tunnels` / `/api/metrics`; request ids are random. The key still travels in the handshake, so protect the transport, not just the logs. Keys are used as in-memory map keys only.
- The admin bearer token must stay server-side and is compared in constant time. The admin API stays disabled until a non-placeholder token of at least 24 characters is configured. Do not embed it in browser code; leave `admin.cors_origin` empty unless a browser really needs to call it.
- The tunnel port accepts unauthenticated connections by design. Mitigations built in: 10 s handshake deadline (covers TLS), 64 KiB hello limit, 16 MiB frame limit, lengths are checked before any allocation. Put per-IP connection limits in front of it (firewall / `iptables` `connlimit`) if it faces the open internet.
- `ip_allow_list` and `allowed_protocols` ignore forwarding headers (`X-Forwarded-For`, `X-Real-IP`, `X-Forwarded-Proto`) unless the proxy is listed in `http.trusted_proxies`, and those headers are not relayed to the backend from untrusted peers (see above).

## Known limitations

- **TCP tunnels have no public listener.** The client and the protocol support `mode=tcp`, but the server does not open an external port for it. Only HTTP tunnels work end to end.
- **One request at a time per tunnel.** A tunnel handles a single HTTP request at once (requests queue behind each other).
- **Fully buffered.** Request and response bodies are read completely into memory before being forwarded and are base64-encoded on the wire (about 1.33x overhead). Large uploads/downloads are expensive. Both directions are capped by default at 32 MiB (`http.max_request_body_bytes`, `http.max_response_body_bytes`, and per subdomain via `max_request_bytes` / `max_response_bytes`, which can only lower the server-wide ceiling). A response over the cap fails the request with `502` and drops the tunnel connection, which the client re-establishes.
- **30 second timeout** per request (fixed).
- **No WebSocket / Server-Sent Events / streaming responses.** Upgrade requests are not supported.
- A client whose API key is unknown to the server at connect time exits (see Client).

## Build and test

```sh
go vet ./... && go test -count=1 ./...
docker build -t gridbase-tunnel:dev .
```

The image (distroless, non-root) contains `gridbase-tunnel-server` (entrypoint) and `gridbase-tunnel-client`. Mount your config at `/etc/gridbase/server.json`; `config/server.example.json` is copied to `/etc/gridbase/server.example.json`. Exposed ports: `9080` (HTTP), `9443` (tunnel).

## Wire protocol

TCP (optionally TLS), frames are a 4-byte big-endian length followed by JSON. A receiver rejects a length above its limit (hello: 64 KiB; frames: `tunnel.max_frame_bytes`, default 16 MiB) before reading the body. The client sends `ClientHello{api_key, mode, tunnel_id}`, the server answers `ServerHello{ok, message, mode, tunnel_id}`; afterwards `StreamFrame{type, request_id, payload}` frames carry requests and responses (`protocol/`).
