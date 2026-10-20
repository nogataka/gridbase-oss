# gridbase

Self-hosted HTTP tunnel with an admin console, in one `docker compose` stack.

- Run one stack, and `https://foo.<your-domain>` reaches an app on your laptop.
- Sign in to the admin console to get an API key and create subdomains.
- Everything is included: reverse proxy (Caddy), tunnel server and client (Go), admin console (Next.js), database (Directus).

日本語版: [README.ja.md](./README.ja.md)

## Quick start

The commands below are for macOS and Linux. On Windows, run them in WSL2. Once the images are built, the steps take a few minutes.

You need Docker with Compose v2, and free ports `80`, `443` and `9443` on your machine. If one is taken, set another port in `.env` first. See [Changing ports](./docs/configuration.md#changing-ports). If you change the HTTP port (for example to `18080`), add it to every URL below, such as `http://admin.gridbase.localhost:18080/signin` and `http://foo.gridbase.localhost:18080/`.

By default, the ports are published on `127.0.0.1` only. Other computers on your network cannot reach this stack.

The admin console is in Japanese only. Button names below are written as Japanese (English).

### 1. Start the stack

```sh
git clone https://github.com/nogataka/gridbase-oss.git gridbase && cd gridbase
cp .env.example .env
docker compose up -d --build
```

The first build compiles the tunnel server (Go) and the admin console (Next.js) from source, so it is slower than a later start. A build without cache (`docker compose build --no-cache`) took 36 seconds on an Apple Silicon Mac with the base images already pulled. Expect several minutes on a slower machine or network. Later starts take about 20 seconds.

Then check the containers. `-a` is needed to also show `directus-init`, which runs once and exits:

```sh
docker compose ps -a
```

You should see something like this. `directus-init` is `Exited (0)`, and the other four are `healthy`:

```
NAME                       SERVICE         STATUS
gridbase-caddy-1           caddy           Up 40 seconds (healthy)
gridbase-directus-1        directus        Up 50 seconds (healthy)
gridbase-directus-init-1   directus-init   Exited (0) 30 seconds ago
gridbase-tunnel-1          tunnel          Up 25 seconds (healthy)
gridbase-web-1             web             Up 25 seconds (healthy)
```

If a container is not healthy yet, wait a few seconds and run the command again.

### 2. Sign in

Open <http://admin.gridbase.localhost/signin>. Enter the values of `GRIDBASE_ADMIN_EMAIL` and `GRIDBASE_ADMIN_PASSWORD` from `.env` and click `ログイン` (Log in).

- E-mail (`メールアドレス`): `admin@gridbase.localhost`
- Password (`パスワード`): `ChangeMe-local-1234`

You land on the dashboard. The header shows `ダッシュボード` (Dashboard), `アカウント` (Account), `管理画面` (Admin) and `使い方` (How to use). The page has two blocks: `API Key 情報` (API key) and `サブドメイン情報` (Subdomains).

These example secrets are for a local trial only. If `.env` marks the setup as exposed (a bind address other than `127.0.0.1`, `https`, or a real domain), `directus-init` refuses to start with them. A tunnel or proxy on the host that `.env` does not mention is not detected. See [what `directus-init` does](./docs/how-it-works.md#what-directus-init-does) and [Production](./docs/production.md).

`*.localhost` resolves to `127.0.0.1` in curl and in current browsers, so you need no DNS setup. If your OS does not resolve it, add `127.0.0.1 admin.gridbase.localhost foo.gridbase.localhost` to `/etc/hosts`.

### 3. Copy your API key

In the `API Key 情報` block, the key starts with `gb_`. Click the copy icon next to it. The tooltip changes from `クリップボードにコピー` (Copy to clipboard) to `コピーしました` (Copied).

If the key column shows `-`, click `API Key生成` (Generate API key) and confirm the dialog.

### 4. Create a subdomain

In the `サブドメイン情報` block, click `新規登録` (New). In the dialog `サブドメインを新規登録` (Register a subdomain), fill in:

| Field | Value |
|---|---|
| `トンネルモード` (Tunnel mode) | leave `HTTP (ウェブ) トンネル` (HTTP tunnel) |
| `ローカルサーバーURL` (Local server URL) | `http://host.docker.internal:8000` |
| `任意のサブドメイン (3〜16文字)` (Subdomain, optional, 3 to 16 characters) | `foo` |

Click `作成` (Create). A green notice `成功` (Success) appears, and a row `foo` with the badges `HTTP` and `active` is added to the table.

`host.docker.internal` is a name that points from inside a container to your own machine. The client in step 6 runs in a container, and your app runs on the host.

### 5. Start a local app and wait 30 seconds

In a new terminal, start any web app on port `8000`:

```sh
python3 -m http.server 8000
```

If port `8000` is in use (the command stops with `Address already in use`), pick another one (for example `8765`) and use the same number in the dialog above and in the command.

Back in your first terminal, wait **30 seconds**. The tunnel server re-reads the database every 30 seconds, so a new subdomain or API key is not known to it right away. A client that connects with a key the server does not know yet exits instead of retrying.

```sh
sleep 30
```

### 6. Connect the client

In a third terminal, in the repository directory. Replace `<API_KEY>` with the key you copied. The client runs inside Docker, so you do not need Go:

```sh
docker compose run --rm --no-deps --entrypoint gridbase-tunnel-client tunnel \
  --server tunnel:9443 --api_key <API_KEY>
```

On success the client prints lines like these and keeps running. Leave it running. The `warn` line is expected on localhost, because the local tunnel port uses plain TCP.

```
[gridbase-client] ... build info version=dev commit=unknown ...
[gridbase-client] ... warn: connecting WITHOUT TLS; the API key is sent in clear text. ...
[gridbase-client] ... starting client: server_addr=tunnel:9443 ...
[gridbase-client] ... connected remote=... attempts=1 successes=1
```

If the client exits right away, wait 30 more seconds and run it again. The server probably did not know the key yet.

To run the client on the host (binary, Linux prebuilt, TLS), see [Client on the host](./docs/configuration.md#client-on-the-host-binary).

### 7. Check that it works

Back in your first terminal:

```sh
curl http://foo.gridbase.localhost/
```

You get the page of your local app. With the Python server above, it is an HTML page titled `Directory listing for /`, and the Python terminal logs `"GET / HTTP/1.1" 200`. You can also open <http://foo.gridbase.localhost/> in a browser.

If you get an error code instead, see [Troubleshooting](./docs/troubleshooting.md).

### Stop and clean up

Stop the client and the Python server with `Ctrl+C`. Then:

```sh
docker compose down        # stop and remove the containers; your data (users, API keys, subdomains) is kept
docker compose down -v     # also delete the data volumes: the next start is a clean install
rm .env                    # optional, after down -v
```

`docker compose down -v` needs `.env` to exist. If you removed it first, run `cp .env.example .env` and try again.

## Read next

| Document | What is in it |
|---|---|
| [docs/production.md](./docs/production.md) | Put it on the internet: secrets, domains, DNS, TLS, backups, changing credentials after the first start |
| [docs/configuration.md](./docs/configuration.md) | Ports, which URL to register, client on the host, Postgres, upgrading Directus |
| [docs/security.md](./docs/security.md) | Security notes and the design behind them |
| [docs/troubleshooting.md](./docs/troubleshooting.md) | Symptoms and fixes |
| [docs/how-it-works.md](./docs/how-it-works.md) | Services, request flow, what `directus-init` does, tests, repository layout |
| [tunnel/README.md](./tunnel/README.md) | Tunnel server and client: options, HTTP endpoints, response codes |
| [web/README.md](./web/README.md) | Admin console (in Japanese) |
| [CONTRIBUTING.md](./CONTRIBUTING.md), [SECURITY.md](./SECURITY.md) | Contributing and reporting a vulnerability |

## Known limitations

- TCP tunnels have no public listener. Only HTTP tunnels work end to end. The console shows TCP as not supported.
- The admin console uses Next.js 14, which has known upstream vulnerabilities. If you expose it, put it behind a VPN, an IP allow-list or an authenticating proxy. See [Security notes](./docs/security.md).
- The console UI is Japanese only. There is no i18n.
- One request at a time per tunnel. Bodies are fully buffered in memory. Each request has a fixed 30 second timeout.
- No WebSocket, Server-Sent Events or streaming responses.
- A new or changed subdomain or API key takes up to 30 seconds to reach the tunnel server.
- A client whose API key is unknown to the server at connect time exits. Restart it.
- Rate limiting in the console is in memory, so it fits a single container.

## License

[MIT](./LICENSE). Copyright (c) 2026 nogataka.
