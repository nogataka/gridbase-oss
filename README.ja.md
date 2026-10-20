# gridbase

セルフホストできる HTTP トンネルと管理画面を、`docker compose` 1 つで動かせます。

- スタックを 1 つ起動すると、`https://foo.<あなたのドメイン>` から手元のアプリに届きます。
- 管理画面にサインインして、API キーの取得とサブドメインの作成を行います。
- 必要なものはすべて入っています。リバースプロキシ（Caddy）、トンネルのサーバーとクライアント（Go）、管理画面（Next.js）、データベース（Directus）です。

English: [README.md](./README.md)

## クイックスタート

以下のコマンドは macOS と Linux 向けです。Windows では WSL2 の中で実行してください。イメージのビルドが終われば、手順は数分で終わります。

必要なものは、Docker（Compose v2）、手順 5 のサンプルアプリ用の Python 3（手持ちの Web アプリでも代わりになります）、手元で空いているポート `80`・`443`・`9443` です。埋まっているポートがあれば、先に `.env` で別のポートに変えてください。手順は [ポートの変更](./docs/configuration.md#changing-ports)（英語）にあります。HTTP のポートを変えた場合（例: `18080`）は、以下の URL すべてにその番号を付けてください（`http://admin.gridbase.localhost:18080/signin`、`http://foo.gridbase.localhost:18080/`）。

既定では、gridbase のコンテナのポートは `127.0.0.1` にだけ公開されます。同じネットワークの他のコンピュータからは届きません（手順 5 で起動するサンプルアプリは別のプログラムです。手順 5 の注意を読んでください）。

管理画面は日本語のみです。以下の画面名とボタン名は、画面に表示されるとおりに書いています。

### 1. スタックを起動する

```sh
git clone https://github.com/nogataka/gridbase-oss.git gridbase && cd gridbase
cp .env.example .env
docker compose up -d --build
```

初回は、トンネルサーバー（Go）と管理画面（Next.js）をソースからビルドするため、2 回目以降より時間がかかります。キャッシュなしのビルド（`docker compose build --no-cache`）は、ベースイメージ取得済みの Apple Silicon の Mac で 36 秒でした。性能やネットワークが劣る環境では数分を見てください。2 回目以降の起動は 20 秒ほどです。

起動したらコンテナを確認します。`directus-init` は 1 回動いて終了するので、表示するには `-a` が必要です。

```sh
docker compose ps -a
```

次のような表示になれば成功です。`directus-init` は `Exited (0)`、ほかの 4 つは `healthy` です。

```
NAME                       SERVICE         STATUS
gridbase-caddy-1           caddy           Up 40 seconds (healthy)
gridbase-directus-1        directus        Up 50 seconds (healthy)
gridbase-directus-init-1   directus-init   Exited (0) 30 seconds ago
gridbase-tunnel-1          tunnel          Up 25 seconds (healthy)
gridbase-web-1             web             Up 25 seconds (healthy)
```

まだ healthy になっていないコンテナがあれば、数秒待ってからもう一度実行してください。

### 2. サインインする

<http://admin.gridbase.localhost/signin> を開きます。`.env` の `GRIDBASE_ADMIN_EMAIL` と `GRIDBASE_ADMIN_PASSWORD` の値を入力して、「ログイン」を押します。

- 「メールアドレス」: `admin@gridbase.localhost`
- 「パスワード」: `ChangeMe-local-1234`

ダッシュボードが開きます。ヘッダーには「ダッシュボード」「アカウント」「管理画面」「使い方」が並びます。画面は「API Key 情報」と「サブドメイン情報」の 2 つのブロックでできています。

この例の秘密値は、ローカルで試すためのものです。`.env` が公開構成を示す場合（`127.0.0.1` 以外への bind、`https`、実ドメインのいずれか）は、この値のままでは `directus-init` が起動しません。`.env` に書かれていないホスト側のトンネルやプロキシは検知されません。詳しくは [directus-init の動作](./docs/how-it-works.md#what-directus-init-does)（英語）と [本番運用](./docs/production.md)（英語）を読んでください。

`*.localhost` は、curl と現行のブラウザで `127.0.0.1` に解決されます。DNS の設定は要りません。解決されない OS では、`/etc/hosts` に `127.0.0.1 admin.gridbase.localhost foo.gridbase.localhost` を追加してください。

### 3. API キーをコピーする

「API Key 情報」ブロックに、`gb_` で始まるキーが表示されています。右にあるコピーのアイコンを押します。ツールチップが「クリップボードにコピー」から「コピーしました」に変わります。

キーの欄が `-` のときは、「API Key生成」を押して、確認ダイアログを承認してください。

### 4. サブドメインを作る

「サブドメイン情報」ブロックの「新規登録」を押します。「サブドメインを新規登録」のダイアログに、次のとおり入力します。

| 項目 | 入力する値 |
|---|---|
| 「トンネルモード」 | 「HTTP (ウェブ) トンネル」のまま |
| 「ローカルサーバーURL」 | `http://host.docker.internal:8000` |
| 「任意のサブドメイン (3〜16文字)」 | `foo` |

「作成」を押すと、緑の通知「成功」が出ます。表には、`foo` の行が `HTTP` と `active` のバッジ付きで追加されます。

`host.docker.internal` は、コンテナの中から手元のマシンを指す名前です。手順 6 のクライアントはコンテナで動き、アプリはホストで動くので、この名前を使います。

### 5. ローカルアプリを起動して 30 秒待つ

新しいターミナルで、適当な Web アプリを `8000` 番で起動します。このサンプルは、リポジトリではなく専用のフォルダから 1 ページだけを配信します（`.env` が配信されることはありません）。

```sh
mkdir -p /tmp/gridbase-demo && echo 'hello from my laptop' > /tmp/gridbase-demo/index.html
python3 -m http.server 8000 --directory /tmp/gridbase-demo
```

`python3 -m http.server` はすべてのネットワークインターフェースで待ち受けます。起動している間は、同じネットワークの他のコンピュータからもこのページを開けます。配信するのはデモ用のフォルダだけにして、試し終わったら `Ctrl+C` で止めてください。

`8000` 番が使用中（コマンドが `Address already in use` で止まる）なら、別の番号（例: `8765`）にして、上のダイアログとこのコマンドで同じ番号を使ってください。

最初のターミナルに戻り、**30 秒**待ちます。トンネルサーバーはおよそ 30 秒ごとにデータベースを読み直す（データベースの応答が遅いときや障害時はもっとかかる）ので、作成したばかりのサブドメインや API キーはすぐには認識されません。サーバーがまだ知らないキーで接続したクライアントは、再試行せずに終了します。

```sh
sleep 30
```

### 6. クライアントを接続する

3 つ目のターミナルで、リポジトリのディレクトリから実行します。`<API_KEY>` は、コピーしたキーに置き換えてください。クライアントは Docker の中で動くので、Go は要りません。

```sh
docker compose run --rm --no-deps --entrypoint gridbase-tunnel-client tunnel \
  --server tunnel:9443 --api_key <API_KEY>
```

成功すると、次のような行が出て、そのまま動き続けます。このターミナルは開いたままにしてください。`warn` の行は、ローカルのトンネルポートが平文 TCP のために出るもので、ローカルでは問題ありません。

```
[gridbase-client] ... build info version=dev commit=unknown ...
[gridbase-client] ... warn: connecting WITHOUT TLS; the API key is sent in clear text. ...
[gridbase-client] ... starting client: server_addr=tunnel:9443 ...
[gridbase-client] ... connected remote=... attempts=1 successes=1
```

すぐに終了してしまったときは、あと 30 秒待ってからもう一度実行してください。サーバーがまだキーを知らなかった可能性があります。何度も終了するときは、`docker compose logs tunnel` で同期のエラーが出ていないか確認してください。

クライアントをホスト上で動かしたい場合（バイナリ、Linux 用のビルド済み、TLS）は、[ホストで動かすクライアント](./docs/configuration.md#client-on-the-host-binary)（英語）を見てください。

### 7. 届いていることを確認する

最初のターミナルで実行します。

```sh
curl http://foo.gridbase.localhost/
```

手元のアプリのページが返ってきます。上のサンプルなら `hello from my laptop` と表示されます。Python のターミナルには `"GET / HTTP/1.1" 200` が出ます。ブラウザで <http://foo.gridbase.localhost/> を開いても確認できます。

エラーコードが返るときは、[トラブルシューティング](./docs/troubleshooting.md)（英語）を見てください。

### 止め方と片付け方

クライアントと Python サーバーを `Ctrl+C` で止めます。

データ（ユーザー、API キー、サブドメイン）を残したまま止めるときは、次を実行します。

```sh
docker compose down
```

まっさらな状態からやり直したいときは、データのボリュームも削除します。ユーザー、API キー、サブドメインがすべて消え、元に戻せません。

```sh
docker compose down -v
```

不要になった `.env` は、そのあと `rm .env` で消せます。`docker compose down -v` には `.env` が要ります。先に消してしまったときは、`cp .env.example .env` してからやり直してください。

## 次に読むもの

次のドキュメントは英語です。

| ドキュメント | 内容 |
|---|---|
| [docs/production.md](./docs/production.md) | インターネットに公開する。秘密値、ドメイン、DNS、TLS、バックアップ、初回起動後の認証情報の変更 |
| [docs/configuration.md](./docs/configuration.md) | ポート、登録する URL、ホストで動かすクライアント、Postgres、Directus のアップグレード |
| [docs/security.md](./docs/security.md) | セキュリティ上の注意と、その背景の設計 |
| [docs/troubleshooting.md](./docs/troubleshooting.md) | 症状と対処 |
| [docs/how-it-works.md](./docs/how-it-works.md) | サービス構成、リクエストの流れ、`directus-init` の動作、テスト、リポジトリ構成 |
| [tunnel/README.md](./tunnel/README.md) | トンネルのサーバーとクライアント。オプション、HTTP エンドポイント、応答コード |
| [web/README.md](./web/README.md) | 管理画面（日本語） |
| [CONTRIBUTING.md](./CONTRIBUTING.md)、[SECURITY.md](./SECURITY.md) | コントリビュートの方法と、脆弱性の報告 |

## 既知の制約

- TCP トンネルには外部の待受ポートがありません。最後まで通して動くのは HTTP トンネルだけです。管理画面は TCP を未対応と表示します。
- 管理画面は Next.js 14 を使っており、上流に既知の脆弱性があります。公開する場合は、VPN、IP 制限、認証プロキシの内側に置いてください。詳しくは [セキュリティの注意点](./docs/security.md)（英語）を読んでください。
- 管理画面の UI は日本語のみです。多言語化の仕組みはありません。
- 1 つのトンネルで同時に処理できるリクエストは 1 つです。本文は全量をメモリに載せます。リクエスト本文を読み終えたあとの、トンネルへの転送処理には 30 秒の固定タイムアウトがあります。
- WebSocket、Server-Sent Events、ストリーミング応答は使えません。
- サブドメインや API キーの変更がトンネルサーバーに届くまで、通常は 30 秒ほどかかります。データベースの応答が遅いときや障害時は、もっとかかります。
- 接続時にサーバーが API キーを知らないと、クライアントは終了します。再起動してください。
- 管理画面のレート制限はメモリ内です。コンテナ 1 台での利用を想定しています。

## ライセンス

[MIT](./LICENSE) / Copyright (c) 2026 nogataka

MIT ライセンスが対象とするのは、このリポジトリのコードです。起動時に取得する他社のイメージには、それぞれのライセンスが適用されます。特に Directus 11.17.4 は Business Source License 1.1 で、オープンソースのライセンスではありません。利用者と関連会社の直近12か月の総収益・予算・資金調達額のうち最大のものが500万米ドルを超える場合、本番で使うには Directus の商用ライセンスが必要です。本番で使う前に [Directus のライセンス](https://github.com/directus/directus/blob/v11.17.4/license) を確認してください。
