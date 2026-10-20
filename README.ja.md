# gridbase

セルフホストできる HTTP トンネルと管理画面です。次の 1 行で `https://foo.<あなたのドメイン>` から手元のアプリに届くようになります。

English: [README.md](./README.md)

```sh
cp .env.example .env && docker compose up -d
```

| サービス | 役割 |
|---|---|
| `caddy` | `:80`/`:443` のリバースプロキシ。`admin.<ドメイン>` を管理画面へ、`*.<ベースドメイン>` をトンネルサーバーへ振り分けます |
| `tunnel` | Go 製トンネルサーバー（[tunnel/](./tunnel)）。HTTP 側 `:9080`（Caddy の背後）、トンネルポート `:9443`（クライアントが直接接続） |
| `web` | 管理画面（[web/](./web)、Next.js）。サインイン、API キー、サブドメイン、ユーザー管理。UI は**日本語のみ**です |
| `directus` | DB として使う [Directus](https://directus.io) 11（既定は SQLite）。compose ネットワークの外には公開しません |
| `directus-init` | 1 回で終わる冪等な初期化。公開構成で既定の秘密が残っていないかの検査、スキーマ適用、最小権限ポリシー、サービストークンの作成 |

```
 ブラウザ ──http(s)──▶ caddy ─┬─ admin.<ドメイン>      ──▶ web :3000 ──┐
                              └─ *.<ベースドメイン>    ──▶ tunnel :9080 │
 gridbase クライアント ──(トンネルポート, TCP)──▶ tunnel :9443           │
                                  tunnel ◀── 30 秒ごとに取得 ──▶ directus :8055 ◀──┘
                                  directus-init: 秘密の検査 + スキーマ + ポリシー + トークン（`up` のたびに 1 回）
```

リクエストの流れ: ブラウザが `http://foo.gridbase.localhost/` を要求 → Caddy がトンネルサーバーへ転送 → サーバーが（Directus から読んだ）サブドメイン `foo` を見つけ、`gridbase` クライアントが張っている常時接続へ送る → クライアントが手元のアプリを呼んで応答を返す。

## クイックスタート（約 5 分）

必要なもの: Docker（Compose v2）。ホストの `80` `443` `9443` が空いていること。埋まっていると、そのポートのサービス（caddy または tunnel）だけが起動に失敗して他は起動したままになります。先に下記「ポートの変更」を行ってください。

```sh
git clone <このリポジトリ> gridbase && cd gridbase
cp .env.example .env
docker compose up -d --build        # 初回のビルドは数分かかります
docker compose ps                   # すべて healthy、directus-init は Exited (0)
```

1. <http://admin.gridbase.localhost> を開き、`.env` の `GRIDBASE_ADMIN_EMAIL` / `GRIDBASE_ADMIN_PASSWORD`（既定 `admin@gridbase.localhost` / `ChangeMe-local-1234`）でサインインします。この既定値はローカルで試すためのものです。**外部に公開する構成では、既定のままだと起動しません**（下記「本番運用」）。
2. ダッシュボードで **API キー** を控え、サブドメイン（例 `foo`、モード `http`、ローカルサーバー URL `http://host.docker.internal:8000`）を作成します。
3. **最大 30 秒** 待ちます。トンネルサーバーは Directus を 30 秒ごとに読み直すため、作成直後は未知のサブドメインです。サーバーが知らない API キーで接続したクライアントは、再試行せずに終了します。
4. ローカルアプリを起動してクライアントを接続します。例ではアプリのポートを `8000` にしています。その番号が手元で使用中なら別の番号（例 `8765`）にして、下のコマンドとサブドメインのローカルサーバー URL（または `--local_server_url`）で同じ番号を使ってください。

```sh
python3 -m http.server 8000          # ホスト上のアプリ

# docker 内のクライアント（Go 不要）。<API_KEY> を置き換えます
docker compose run --rm --no-deps --entrypoint gridbase-tunnel-client tunnel \
  --server tunnel:9443 --api_key <API_KEY>

curl http://foo.gridbase.localhost/  # -> アプリのページ
```

`*.localhost` は curl と現行ブラウザで `127.0.0.1` に解決されるので DNS 設定は不要です。解決されない OS では `/etc/hosts` に `127.0.0.1 admin.gridbase.localhost foo.gridbase.localhost` を追加してください。

### サブドメインに設定する URL

`local_server_url` は **クライアント** が転送する先なので、クライアントから届く URL にします。

| クライアント | アプリ | `local_server_url` |
|---|---|---|
| docker 内（上のコマンド） | ホスト | `http://host.docker.internal:8000`（Linux 向けに compose が host-gateway を追加済み） |
| ホスト（バイナリ） | ホスト | `http://127.0.0.1:8000` |

登録した URL はそのままに、クライアントの `--local_server_url` で上書きもできます。**本番では必ずこの方法で転送先を固定してください**。固定しないと、クライアントはサーバーから届いた転送先 URL をそのまま使います。クイックスタートの compose 内クライアントは `http://directus:8055` のような内部サービスにも届くため、そうした URL で登録されたサブドメインがあると内部サービスが外に出てしまいます。

### ホストで動かすクライアント

```sh
cd tunnel && go build -o gridbase-tunnel-client ./client      # Go 1.21+
./gridbase-tunnel-client --server 127.0.0.1:9443 --api_key <API_KEY> \
                         --local_server_url http://127.0.0.1:8000
```

**Linux** なら、ビルド済みのクライアントをイメージから取り出せます: `docker compose cp tunnel:/usr/local/bin/gridbase-tunnel-client .`。これは Linux 用の実行ファイル（静的リンク）なので、**macOS や Windows では動きません**。そちらでは `go build` してください。

`--tls` を付けないと平文 TCP で接続し、警告を出します（API キーが平文で流れます）。localhost 以外では必ず TLS を使ってください。オプションは [tunnel/README.md](./tunnel/README.md#client) にあります。

### ポートの変更

`.env` を編集します。

```env
GRIDBASE_HOST_HTTP_PORT=18080
GRIDBASE_HOST_HTTPS_PORT=18443
GRIDBASE_HOST_TUNNEL_PORT=19443
GRIDBASE_ADMIN_URL=http://admin.gridbase.localhost:18080   # ブラウザで開く URL と完全一致させる
GRIDBASE_TUNNEL_SERVER=localhost:19443
```

このときトンネルは `http://foo.gridbase.localhost:18080/` です。手元で空いているポートを選んでください。`8000` や `8080` のような開発でよく使うポートは、他のアプリが使っていることがよくあります（`8080` が埋まっていると caddy だけが起動に失敗し、他のサービスは起動したままになります）。`docker compose up -d` で反映します。docker 内のクライアントは変わらず `--server tunnel:9443`（コンテナ側のポートは変わりません）、ホストのクライアントは `--server 127.0.0.1:19443` です。

公開ポートは既定ですべて `127.0.0.1` にだけバインドされます（`GRIDBASE_BIND_ADDRESS`）。`0.0.0.0` にするのは秘密をすべて作り直してからにしてください。既定の秘密が残っていると、`directus-init` が起動を止めます（下記）。

## `directus-init` がやること

`directus/init/init.mjs`（Node 22 と `bcryptjs`。[directus/init/Dockerfile](./directus/init/Dockerfile) から `docker compose up --build` でビルドされます）は `docker compose up` のたびに走り、すでに期待どおりなら何も変更しません。

0. **`.env` の秘密を検査**（[directus/init/secrets-check.mjs](./directus/init/secrets-check.mjs)）。`.env.example` と同じ値、または `change-me` / `local-dev-` / `replace-with-` で始まる値を「既定」とみなします。対象は `DIRECTUS_KEY`、`DIRECTUS_SECRET`、`DIRECTUS_ADMIN_PASSWORD`、`DIRECTUS_ADMIN_TOKEN`、`NEXTAUTH_SECRET`、`GRIDBASE_ADMIN_PASSWORD`、Directus のサービストークン 2 つ、`GRIDBASE_TUNNEL_ADMIN_TOKEN`、（Postgres を使うときは）`POSTGRES_PASSWORD` です。
   - **ローカル構成**（`GRIDBASE_BIND_ADDRESS=127.0.0.1` かつ `http` で、ドメインが `localhost` / `*.localhost` / `*.test` / `*.internal` だけ）: 警告を出して続行します。
   - **公開構成**（`GRIDBASE_BIND_ADDRESS` が 127.0.0.1 以外、`GRIDBASE_SITE_SCHEME` / `GRIDBASE_ADMIN_URL` が `https`、または `GRIDBASE_BASE_DOMAIN` / `GRIDBASE_ADMIN_DOMAIN` が実ドメイン（`localhost`・`*.localhost`・`*.test`・`*.internal` 以外）。`127.0.0.1` にバインドしたまま、ホスト側のリバースプロキシから `http` で公開する構成も公開構成として扱います）: 既定の秘密、短すぎる秘密、秘密どうしの同じ値のどれかがあれば **exit 1** で止まります。`tunnel`・`web`・`caddy` は init の正常終了を待つので、何も公開されません。`.env.example` をコピーしてバインドアドレスだけ変えても、公開されたインスタンスは動きません。
   - **判定は `.env` だけで行います。** `*.localhost`・`*.test`・`*.internal` はローカル扱いで、`*.internal` は社内ネットワークの名前としてよく使われます。`.env` の外での公開方法は init から見えません。次のような方法で公開するときは、`.env` で公開構成であることを明示してください。たとえば `GRIDBASE_SITE_SCHEME=https://`（と `GRIDBASE_ADMIN_URL=https://…`）、`GRIDBASE_BASE_DOMAIN` / `GRIDBASE_ADMIN_DOMAIN` に実ドメインを入れる、`GRIDBASE_BIND_ADDRESS` を 127.0.0.1 以外にする、のどれかです。(a) ホスト側の nginx・`cloudflared`・`ssh -R`・ngrok などで公開ポートへ転送する、(b) `docker-compose.override.yml` でポートを足す、(c) `admin.gb.corp.internal` のような社内専用の名前で他のマシンに見せる。明示しないと、既定の秘密は警告が出るだけで起動します。
1. Directus の health を待つ
2. [directus/schema/gridbase.json](./directus/schema/gridbase.json) を `/schema/diff`→`/schema/apply` で適用（`users` と `subdomains`。`users.email` / `users.userId` / `users.api_key` / `subdomains.subdomain` は UNIQUE。Postgres では `gridbase.postgres.json` を自動選択）。Directus が snapshot を **vendor または版の不一致**で拒否したとき（Postgres、または Directus のイメージを上げたとき）は、`force=true` で 1 回だけ再試行します。
3. `admin_access` / `app_access` なしのポリシーを 2 つ作成: `gridbase-web`（`users`・`subdomains` の CRUD のみ）、`gridbase-tunnel`（読み取りのみ・必要な列だけ。パスワードのハッシュは含まない）
4. ポリシーごとのサービスユーザーを作り、静的トークンを `.env` の `GRIDBASE_WEB_DIRECTUS_TOKEN` / `GRIDBASE_TUNNEL_DIRECTUS_TOKEN` に同期（ローテーションは `.env` を直して `docker compose up -d`）
5. Directus のキャッシュをクリア（スキーマ・権限を変えた直後に必要）

手順 2 のあとで、init は**データベースに保存済みの管理者**も検査します。`role=admin` のユーザー（`status` は問いません。無効な管理者も後で有効に戻せるため）と、既定の管理者メール（`admin@gridbase.localhost`・`admin@example.com`・いまの `GRIDBASE_ADMIN_EMAIL`）のユーザー（降格・無効化されていても）を読み、保存されている bcrypt ハッシュを、README に載っている既定のパスワード（`ChangeMe-local-1234` と `.env.example` のその他の値）と照合します。保存されているパスワードが空（null / 空文字）のユーザーは対象外です（`web` は入力に関わらず拒否するので、パスワードではサインインできません）。この検査は `.env` に `GRIDBASE_ADMIN_PASSWORD` があるかどうかに依存しないので、初回起動後に消しても（本番運用の手順 10）弱くなりません。公開構成では、既定のパスワードの管理者がいる場合に加えて、検査そのものができない場合（Directus に届かない、ハッシュを読めない）も **exit 1** で止まり、何も公開されません。ローカル構成では警告だけです。

初回管理者は `web` が起動時に作成します（`scripts/seed-admin.mjs`。`GRIDBASE_ADMIN_EMAIL`/`GRIDBASE_ADMIN_PASSWORD` を設定し、有効な管理者が 1 人もいないとき）。既存ユーザーの昇格も、既存のパスワードの変更もしません。公開構成では保存済みの管理者の検査をもう一度行います（二重の防御。既定のパスワード、弱い・既定の `GRIDBASE_ADMIN_PASSWORD`、状態を確認できないとき、のどれでも終了コード 3 で `web` は起動しません）。詳細は [web/README.md](./web/README.md#初回管理者)。

Directus 自身の管理者（`DIRECTUS_ADMIN_EMAIL`/`DIRECTUS_ADMIN_PASSWORD`）には `….localhost` のようなドメインは使えません（`you@example.com` のようにしてください）。Directus の UI をホストから開くときは、`docker-compose.yml` の `directus` の `ports:` のコメントを外します。

### Directus を上げるとき

`directus/schema/` の snapshot は Directus 11.17.4 で取ったものです。`docker-compose.yml` のイメージのタグを変えたら:

1. `directus-db` ボリュームをバックアップしてから、新しいイメージで起動します（`docker compose up -d`）。Directus は起動時に自身の DB を移行します。版が snapshot と違えば init がそう表示して `force` で再試行するので、動き続けます。
2. リポジトリが動かしている版と一致するよう、snapshot を取り直してコミットします。

```sh
docker compose exec directus npx directus schema snapshot --yes --format json /tmp/gridbase.json
docker compose cp directus:/tmp/gridbase.json directus/schema/gridbase.json   # SQLite。Postgres は gridbase.postgres.json を上書き
docker compose run --rm directus-init                                        # 「変更なし」と出ること
```

### SQLite の代わりに Postgres を使う

```sh
docker compose -f docker-compose.yml -f docker-compose.postgres.yml up -d --build
```

`.env` に `POSTGRES_PASSWORD` を設定します。SQLite のデータは引き継ぎません。

## 本番運用

1. **最初の起動の前に、秘密を作り直す。** `.env` の秘密をすべて作り直します（トークン、`DIRECTUS_KEY`/`DIRECTUS_SECRET`、パスワードは `openssl rand -hex 32`、`NEXTAUTH_SECRET` は `openssl rand -base64 32`）。`GRIDBASE_ADMIN_PASSWORD`（8 文字以上で英字と数字を含む）と `DIRECTUS_ADMIN_PASSWORD` も含みます。`GRIDBASE_TUNNEL_ADMIN_TOKEN` は空（管理 API 無効）か、24 文字以上のランダム値にします。公開構成では、そうなっていないと `directus-init` が起動を止めます。すでに既定の値で一度起動した場合は、下の「初回起動後に認証情報を変える」を読んでください。保存済みの管理者アカウントは `.env` を直すだけでは変わりません。
2. **管理画面はトンネル用ドメインと別の登録可能ドメインに置く**（例: 管理画面 `admin.example.org`、トンネル `*.tunnel.example.com`）。同じ登録可能ドメインだと Cookie の差し込みなど同一サイト由来のリスクが残ります（[web/README.md](./web/README.md)）。
3. `.env` に次を設定:

```env
GRIDBASE_BASE_DOMAIN=tunnel.example.com
GRIDBASE_ADMIN_DOMAIN=admin.example.org
GRIDBASE_ADMIN_URL=https://admin.example.org
GRIDBASE_SITE_SCHEME=https://
GRIDBASE_TUNNEL_SERVER=tunnel.example.com:9443
GRIDBASE_BIND_ADDRESS=0.0.0.0
```

4. DNS: `admin.example.org` と、ワイルドカード `*.tunnel.example.com`（と `tunnel.example.com`）をホストへ向ける。
5. TLS: 管理画面のホストは Caddy の自動 HTTPS をそのまま使えます。`*.tunnel.example.com` のワイルドカード証明書は DNS-01 が必要で、DNS プロバイダのプラグイン入りの Caddy をビルドします。Cloudflare の例（`caddy/Dockerfile`、`docker-compose.override.yml`、`Caddyfile`）は [README.md](./README.md#production) の英語版にあります（この例はリポジトリのテストでは検証していません）。
6. トンネルポート 9443 は既定で平文 TCP で、API キーが平文で流れます。`GRIDBASE_TLS_CERT` / `GRIDBASE_TLS_KEY` に証明書を渡し、クライアントは `--tls` と `--local_server_url`（転送先の固定）付きで接続します（英語版 README 参照）。
7. 公開するのは `80` `443` `9443` のみ。Directus は公開しない（`directus` に `ports:` を付けない）。
8. `directus-db` ボリューム（または Postgres）をバックアップ。
9. 公開登録が不要なら `GRIDBASE_SIGNUP_ENABLED=false` のままにする。
10. 初回起動の後は、`.env` から `GRIDBASE_ADMIN_PASSWORD` を消す（初回にしか使われず、`docker inspect` で見えるため）。保存済みの管理者パスワードの検査は、消しても続きます。

### 初回起動後に認証情報を変える

`.env` の値のうち、データベースを作るときに**一度だけ**読まれるものがあります。その後 `.env` を書き換えても、保存済みの内容は変わりません。

| 対象 | `.env` が読まれるとき | 後から変える方法 |
|---|---|---|
| gridbase の管理者（`GRIDBASE_ADMIN_EMAIL` / `GRIDBASE_ADMIN_PASSWORD`） | 最初の起動で、有効な管理者がいないときだけ。パスワードは `users` にハッシュで保存されます | サインインして **アカウント**（`/account`）で、現在のパスワードと新しいパスワードを入力します。他のセッションはすべてサインアウトされます。忘れた場合: メールでのリセット（SMTP が必要）、`docker compose down -v` でデータを作り直す、または Directus の UI で bcrypt ハッシュを直接設定する（ハッシュは表の下のコマンドで作ります。パスワードは画面に表示されず、シェルの履歴にもプロセス一覧にも残りません）。**このとき、そのユーザーの `password_changed_at` も現在時刻（ISO 8601。例 `2026-10-20T12:00:00.000Z`）に更新してください。** この時刻より前に発行されたセッションは拒否されるので、乗っ取られたアカウントで既にセッションを持っている相手もサインアウトされます。ハッシュだけを置き換えると、既存のセッションは最大 7 日間有効のままです |
| Directus の管理者（`DIRECTUS_ADMIN_EMAIL`、`DIRECTUS_ADMIN_PASSWORD`、`DIRECTUS_ADMIN_TOKEN`） | Directus が最初の管理者を作るときだけ。後から変えても DB は変わらず、`DIRECTUS_ADMIN_TOKEN` だけ変えると `directus-init` が認証できず失敗します | `directus` の `ports:` を有効にし、**元の**値で Directus の UI にサインインして、管理者のパスワードとトークンを変更します。そのうえで **新しい**トークンを `.env` の `DIRECTUS_ADMIN_TOKEN` に書き、`docker compose up -d`。または `docker compose down -v` でやり直します |
| `GRIDBASE_WEB_DIRECTUS_TOKEN`、`GRIDBASE_TUNNEL_DIRECTUS_TOKEN` | `up` のたび（init がサービスユーザーに同期） | `.env` を直して `docker compose up -d` |
| `NEXTAUTH_SECRET` | `web` の起動のたび | `.env` を直して `docker compose up -d`。管理画面のセッションはすべて終了します |
| `DIRECTUS_KEY`、`DIRECTUS_SECRET` | `directus` の起動のたび | `.env` を直して `docker compose up -d`。gridbase は静的トークンを使うので、他への影響はありません |
| `GRIDBASE_TUNNEL_ADMIN_TOKEN` | `tunnel` の起動のたび | `.env` を直して `docker compose up -d` |
| `POSTGRES_PASSWORD` | Postgres のボリュームを最初に作るときだけ | Postgres 内で `ALTER USER directus PASSWORD '…'` を実行し、`.env` も更新 |

上の表の「ハッシュを直接設定する」ためのハッシュは、次のコマンドで作ります（パスワードは画面に表示されず標準入力で渡すので、シェルの履歴にもプロセス一覧にも残りません。この README からコピーした値ではなく、自分の新しいパスワードを使ってください）。

```sh
printf 'New password: '; stty -echo; read -r NEW_PW; stty echo; echo
printf '%s' "$NEW_PW" | docker compose exec -T web node --input-type=module \
  -e "import b from 'bcryptjs'; let s=''; for await (const c of process.stdin) s+=c; console.log(b.hashSync(s, 10))"
unset NEW_PW
```

既定の管理者パスワードのまま動かしていたインスタンスを公開構成にする場合: 保存済みの管理者のパスワードが既定のままだと、`directus-init` が exit 1 で止まり、`tunnel`・`web`・`caddy` は起動しません（二重の防御として `web` 自身も終了コード 3 で止まります）。`.env` に `GRIDBASE_ADMIN_PASSWORD` が残っているかどうかに関係なく同じです。いったんローカル構成（`GRIDBASE_BIND_ADDRESS=127.0.0.1`、`http`、`*.localhost` のドメイン）で起動し、**アカウント**でパスワードを変えてから公開構成にしてください。

## セキュリティ上の注意

- 管理画面は専用の登録可能ドメインに置いてください（上記）。
- トンネルサーバー自身の管理 API（管理ホスト上の `/api/*`）は外から届きません。compose の設定では内部専用のホスト名（`tunnel-admin.internal`）を与えてあり、Caddy は中継しません。
- トンネルサーバーのログと管理 API は、API キーを指紋（`key_id`）でしか表示せず、キーそのものは出しません。`GRIDBASE_TUNNEL_ADMIN_TOKEN` は空（管理 API 無効）か 24 文字以上のランダム値にしてください。`change-me` / `local-dev-` / `replace-with-` で始まる値だと管理 API は無効のままになります（`.env.example` の値はこれに当たります）。公開構成では `directus-init` が拒否します。
- トンネルポートの認証は API キーです。mTLS は任意で、使う場合は自分で管理する CA を使います。
- クライアントは `--local_server_url` を指定しない限り、サーバーから届いた転送先 URL を使います。クイックスタートの compose 内クライアントは内部サービスにも届くので、本番では必ずクライアントで `--local_server_url` を固定してください。
- Directus のサービストークンは最小権限ですが、DB の認証情報であることに変わりはありません。他で使い回さないでください。
- セッション: https では Cookie 名が `__Host-next-auth.session-token`（ホスト限定・`Secure`・`Path=/`）になり、兄弟サブドメインからセッション Cookie を差し込まれません。パスワードを変更またはリセットすると、他のセッションはすべて無効になります。ログインは (IP, メール) ごと、およびメールごとにレート制限されます。
- Next.js 14 系には未解決の脆弱性があります（`npm audit --omit=dev` が next と postcss を報告。修正は破壊的変更を伴う Next.js 16 への移行）。リバースプロキシの背後に置いても**軽減にはなりません**。この構成では画像最適化・rewrites・Server Actions・i18n・Windows 向けの advisory は当てはまりません（機能を使っていない、または `images.unoptimized` で無効化済み）。一方、App Router / React Server Components への DoS 系は当てはまりうるうえ、前段にキャッシュ付きプロキシを置くとキャッシュポイズニング系が問題になります。公開するなら、Next.js 16 への移行までは VPN・IP 制限・認証付きプロキシの内側に置いてください。advisory ごとの整理は [web/README.md](./web/README.md#依存関係メモ) にあります。
- 脆弱性の報告は [SECURITY.md](./SECURITY.md) を参照してください。

## 既知の制約

- **TCP トンネルは外部待受ポートが未実装**です（HTTP のみ動作）。管理画面は TCP を「未対応」と表示し、API も `mode=tcp` に `400` を返します。
- **管理画面の UI は日本語のみ**です（多言語化の仕組みはありません）。
- **1 トンネルにつき同時 1 リクエスト**、全量をメモリにバッファ、リクエストごとに **30 秒** 固定のタイムアウト。
- **WebSocket / SSE / ストリーミング応答は非対応**です。
- サブドメインや API キーの変更がトンネルに反映されるまで最大 **30 秒**（ポーリング）。
- サーバーが知らない API キーで接続したクライアントは終了します（再起動してください）。
- 管理画面のレート制限はメモリ内（コンテナ 1 台向け）です。

## トラブルシューティング

| 症状 | 原因・対処 |
|---|---|
| `directus-init` が 0 以外で終了 | `docker compose logs directus-init`。公開構成（`GRIDBASE_BIND_ADDRESS` が `127.0.0.1` 以外、`https`、または `GRIDBASE_BASE_DOMAIN` / `GRIDBASE_ADMIN_DOMAIN` が実ドメイン）で `.env.example` の既定の秘密が残っている、保存済みの管理者のパスワードが既定のまま（「保存済みの管理者のパスワードに問題があります」）、トークンが 20 文字未満、または 2 つが同じ値 |
| `web` が起動直後に「公開構成」で終了 | 二重の防御。公開構成で、保存済みの管理者パスワードが既定のまま（または確認できない）。「初回起動後に認証情報を変える」を参照 |
| `docker compose down -v` が「DIRECTUS_KEY … missing」で失敗 | compose はスタックを止めるときにも `.env` の変数を必要とします。先に `cp .env.example .env`（または `docker compose --env-file .env.example down -v`） |
| Directus の管理者が作られない（全リクエストが 401） | `DIRECTUS_ADMIN_EMAIL` が不正なアドレスとして拒否された（`….localhost` など）。`.env` を直し `docker compose down -v` で作り直す |
| `address already in use` | `.env` の `GRIDBASE_HOST_*_PORT` を変更（「ポートの変更」） |
| `503 unknown subdomain` | 30 秒の同期前、またはサブドメインが無い / `active` でない |
| `503 tunnel error: tunnel client not connected` | サブドメインは既知だがクライアントが未接続（または切断直後） |
| `502 tunnel error` | クライアントは接続しているが転送に失敗した。手元のアプリが落ちている・クライアントから届かない、または応答が大きすぎる（`502 tunnel error: upstream response too large`） |
| サインインできない / 書き込みが 403 | `GRIDBASE_ADMIN_URL` がブラウザの URL（スキーム・ホスト・ポート）と一致していない |
| サブドメインを作ったのにクライアントがハンドシェイクで終了する | 作成から 30 秒待ってからクライアントを起動する |

## テスト

```sh
(cd directus/init && npm ci) && node --test directus/init/*.test.mjs   # 既定の秘密・保存済み管理者パスワードの検査
(cd web && npm ci && npm test)                      # callbackUrl / パスワード規則 / レート制限 / 入力検証
(cd tunnel && go vet ./... && go test ./...)        # トンネルサーバーとクライアント
docker compose up -d --build && node tests/e2e/smoke.mjs   # 起動中のスタックに対する end-to-end
```

`tests/e2e/smoke.mjs` は、サインイン、API キー発行、サブドメイン作成、クライアント接続とトンネル経由のページ取得に加えて、権限、CSRF の拒否、パスワード変更（他のセッションが失効すること）、`directus-init` の冪等性を確認します。Node 20 以上と Docker が必要です。CI は push のたびにすべてを実行します（[.github/workflows/ci.yml](./.github/workflows/ci.yml)）。

## リポジトリ構成

```
docker-compose.yml            スタック本体
docker-compose.postgres.yml   Postgres を使う場合の override
Caddyfile                     リバースプロキシ（既定はローカル HTTP）
.env.example                  全変数と、ローカル専用のダミー値
directus/schema/              スキーマ snapshot（sqlite, postgres）
directus/init/                冪等な Directus 初期化（init.mjs）、既定の秘密・保存済み管理者パスワードの検査、Dockerfile
tunnel/                       Go 製トンネルサーバーとクライアント
web/                          管理画面（Next.js）
tests/e2e/                    end-to-end smoke テスト（`docker compose up` の後に実行）
.github/workflows/ci.yml      CI
SECURITY.md  CONTRIBUTING.md
```

## ライセンス

[MIT](./LICENSE) / Copyright (c) 2026 nogataka
