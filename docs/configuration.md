# Configuration

All variables are in [.env.example](../.env.example), with comments. This page covers the choices you are most likely to make.

## Changing ports

Edit `.env`:

```env
GRIDBASE_HOST_HTTP_PORT=18080
GRIDBASE_HOST_HTTPS_PORT=18443
GRIDBASE_HOST_TUNNEL_PORT=19443
GRIDBASE_ADMIN_URL=http://admin.gridbase.localhost:18080   # must match what the browser opens
GRIDBASE_TUNNEL_SERVER=localhost:19443
```

The tunnel is then `http://foo.gridbase.localhost:18080/`. Pick ports that are free on your machine: common development ports such as `8000` and `8080` are often taken by other apps (a taken `8080` makes caddy fail to start, with the other services still running). Apply with `docker compose up -d`; the client inside docker still uses `--server tunnel:9443` (the container port does not change), a client on the host uses `--server 127.0.0.1:19443`.

If a host port is taken, only the service that needs it (caddy for `80`/`443`, tunnel for `9443`) fails to start. The other services stay up.

By default every published port is bound to `127.0.0.1` (`GRIDBASE_BIND_ADDRESS`). Set `0.0.0.0` only after replacing all secrets: with default secrets still in `.env`, `directus-init` stops the start (see [how-it-works.md](./how-it-works.md#what-directus-init-does)).

## Which URL do I give the subdomain?

`local_server_url` is where the **client** forwards requests, so it must be reachable from where the client runs.

| Client runs | App runs | `local_server_url` |
|---|---|---|
| In docker (the Quick start command) | On the host | `http://host.docker.internal:8000` (compose adds the `host-gateway` mapping for Linux) |
| On the host (binary) | On the host | `http://127.0.0.1:8000` |

You can also leave the registered URL alone and override it on the client with `--local_server_url http://127.0.0.1:8000`. **In production always pin the target this way**: a client otherwise uses the target URL that the server sends it, and the Quick start's in-compose client can reach internal services such as `http://directus:8055` if a subdomain is registered with such a URL.

## Client on the host (binary)

```sh
cd tunnel && go build -o gridbase-tunnel-client ./client      # Go 1.21+
./gridbase-tunnel-client --server 127.0.0.1:9443 --api_key <API_KEY> \
                         --local_server_url http://127.0.0.1:8000
```

On **Linux** you can instead copy a prebuilt client out of the image: `docker compose cp tunnel:/usr/local/bin/gridbase-tunnel-client .`. That binary is a Linux executable (static), so it does **not** run on macOS or Windows; build it with `go build` there.

Without `--tls` the client talks plain TCP and prints a warning (the API key is sent in clear text). That is fine on localhost; use TLS everywhere else. All client options are in [tunnel/README.md](../tunnel/README.md#client).

## Postgres instead of SQLite

```sh
docker compose -f docker-compose.yml -f docker-compose.postgres.yml up -d --build
```

Set `POSTGRES_PASSWORD` in `.env`. Existing SQLite data is not migrated.

## The Directus admin account and UI

Directus has its own admin account (`DIRECTUS_ADMIN_EMAIL`/`DIRECTUS_ADMIN_PASSWORD`). It is separate from the gridbase admin. Note Directus rejects some email domains: use something like `you@example.com`, not `…@gridbase.localhost`. To open the Directus UI from the host, uncomment the `ports:` block of the `directus` service in `docker-compose.yml`.

## Upgrading Directus

The snapshot in `directus/schema/` was taken with Directus 11.17.4. When you change the image tag in `docker-compose.yml`:

1. Back up the `directus-db` volume, then start the stack with the new image (`docker compose up -d`). Directus migrates its own database on start; if the version differs from the snapshot, init says so and retries with `force`, so it keeps working.
2. Take a fresh snapshot so the repository matches the version you run, and commit it:

```sh
docker compose exec directus npx directus schema snapshot --yes --format json /tmp/gridbase.json
docker compose cp directus:/tmp/gridbase.json directus/schema/gridbase.json   # SQLite; for Postgres overwrite gridbase.postgres.json
docker compose run --rm directus-init                                        # must report "変更なし" (no change)
```
