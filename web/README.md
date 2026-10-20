# gridbase-web

gridbase の管理画面です。Next.js (App Router) + NextAuth + Directus で動きます。

- サインイン / 新規登録（公開・閉鎖を環境変数で切替）
- パスワードリセット（SMTP を設定したときだけ有効）
- ダッシュボード: API Key の表示・再発行、サブドメイン（HTTP）の作成・編集・削除
  （TCP トンネルはトンネルサーバーに外部待受ポートが無いため未対応。選択肢には「未対応」と表示し、API も 400 を返します）
- アカウント (`/account`): ログイン中のユーザーが自分のパスワードを変更（現在のパスワードの確認が必須。変更すると他の端末のセッションは無効）
- 管理画面 (`/admin`): ユーザーの一覧・有効/無効・管理者の付与/解除・削除
- 使い方ページ (`/docs`)

画面の文言は**日本語のみ**です（多言語化の仕組みはありません）。

データはすべて Directus の `users` / `subdomains` コレクションに保存します。トンネルサーバーは Directus を
約30秒ごとに読み込むので、この画面での変更は通常30秒ほどでトンネルへ反映されます（データベースの応答が遅いときや障害時はもっとかかります）（トンネルへ直接通知はしません）。

## 配置の推奨: 管理画面はトンネル用ドメインと別のサイトに置く

管理画面（`NEXTAUTH_URL`）を、トンネルが公開するサブドメイン（`GRIDBASE_BASE_DOMAIN` 配下）と
**別の登録可能ドメイン**に置くことを推奨します。

- 悪い例: 管理画面 `admin.example.com`、トンネル `*.example.com`
- 良い例: 管理画面 `admin.example.org`、トンネル `*.example.com`

同じ登録可能ドメインに置くと、トンネル利用者が公開したサイト（`foo.example.com`）が管理画面と
「同一サイト」になります。Origin の完全一致検査で `foo.example.com` からの書き込みは拒否しますが、
`Domain=example.com` の Cookie を差し込まれてセッションを固定されるなど、同一サイトであることに由来する
リスクは残ります。別ドメインにすればブラウザが両者を別サイトとして分離します。
管理画面の Cookie は `Domain` 属性を持たない（ホスト限定）設定です。

## 環境変数

すべて実行時に読みます。`NEXT_PUBLIC_*` は使わないので、ビルド済みイメージに環境変数だけで設定を渡せます。
例は [.env.example](./.env.example) を参照してください。

| 変数 | 必須 | 説明 |
|---|---|---|
| `NEXTAUTH_URL`（別名 `APP_URL`） | 必須 | 管理画面の公開 URL（末尾スラッシュなし）。リセットメールのリンク、Origin 検査、HSTS の出し分けに使います。ブラウザで開く URL と完全に一致させてください |
| `NEXTAUTH_SECRET` | 必須 | セッション JWT の署名鍵（`openssl rand -base64 32` など） |
| `DIRECTUS_URL` | 必須 | Directus の URL。実行時のみ必要で、`npm run build` には不要です |
| `DIRECTUS_TOKEN` | 必須 | Directus の静的トークン。`users` / `subdomains` の読み書きだけを許可した最小権限のものを使ってください |
| `GRIDBASE_BASE_DOMAIN` | 任意 | サブドメインの公開 URL 表示用（例 `example.com`） |
| `GRIDBASE_TUNNEL_SERVER` | 任意 | クライアント接続先の表示用 `host:port` |
| `GRIDBASE_SIGNUP_ENABLED` | 任意 | `true` で新規登録を公開。未設定・それ以外は閉鎖（既定） |
| `GRIDBASE_ADMIN_EMAIL` / `GRIDBASE_ADMIN_PASSWORD` | 任意 | 初回管理者（後述） |
| `SMTP_HOST` `SMTP_PORT` `SMTP_USER` `SMTP_PASS` `SMTP_FROM` | 任意 | パスワードリセットメール。`SMTP_HOST` と `SMTP_FROM` があるときだけリセットが有効。465 は暗黙 TLS、それ以外は STARTTLS |
| `GRIDBASE_SESSION_RECHECK_SECONDS` | 任意 | ログイン済みセッションの role / status / パスワード変更を Directus で再確認する間隔（既定 300 秒）。API は毎回 DB で確認します |
| `SEED_WAIT_SECONDS` | 任意 | 起動時に Directus の `users` コレクションを待つ最大秒数（既定 120） |
| `GRIDBASE_BIND_ADDRESS` / `GRIDBASE_SITE_SCHEME` | 任意 | compose が渡す値。`seed-admin` が「公開構成」（127.0.0.1 以外、または https）かを判定するのに使います |

`NEXTAUTH_URL` が `https://` のときだけ、HSTS・`upgrade-insecure-requests`・COOP と Secure Cookie が有効になり、
セッション Cookie 名は `__Host-next-auth.session-token`（`Domain` なし・`Path=/`・`Secure` をブラウザが強制）になります。
http の LAN 運用では付きません（`__Host-` は Secure が必須のため）。

## Directus 側の前提

- コレクション `users`: `id`(uuid, PK) / `userId`(text, **UNIQUE**) / `email`(text, **UNIQUE** 推奨) / `password` / `role` / `status` /
  `display_name` / `description` / `api_key` / `created_at` / `last_login_at` / `reset_token` / `reset_token_expires` / `settings`(json)
- コレクション `subdomains`: `id`(uuid, PK) / `userId` / `subdomain`(text, **UNIQUE**) / `mode` / `status` / `description` /
  `local_server_url` / `local_host` / `local_port` / `created_at`
- `users.api_key` と `users.password_changed_at`（パスワード変更時刻。変更前のセッションを失効させるために使う）も必要です。
  `api_key` は UNIQUE です。
- `subdomains.subdomain` と `users.email` の UNIQUE 制約は、アプリ側の重複チェックが競合したときの最終防衛線です。必ず付けてください。
- `DIRECTUS_TOKEN` に必要な権限: `users` と `subdomains` の read / create / update / delete。他のコレクションは不要です。

## 初回管理者

`GRIDBASE_ADMIN_EMAIL` と `GRIDBASE_ADMIN_PASSWORD`（8〜200文字、英字と数字を含む。サインアップと同じ規則）を設定すると、
コンテナ起動時に `scripts/seed-admin.mjs` が実行されます（`docker-entrypoint.sh` 経由）。

- 有効な（`status=active`）`role=admin` が 1 人もいないときだけ作成します（冪等。毎回の起動で呼んでも安全）。
- **`.env` を後から変えても、DB に作成済みの管理者は変わりません**（作成は 1 回だけ）。パスワードは `/account` 画面で変えます。
- 同じメールのユーザーが既にいる場合は、**昇格もパスワード変更もせず**、警告して終了します
  （サインアップ公開中に管理者のメールで先に登録されても乗っ取られないようにするため）。別の `GRIDBASE_ADMIN_EMAIL` を使うか、本人確認のうえ Directus で role を変えてください。
- 管理者が「無効のものだけ」存在する場合も、何も作らず警告します（Directus の管理 UI で `status` を戻してください）。
- **公開構成**（`GRIDBASE_BIND_ADDRESS` が 127.0.0.1 以外、https、または `GRIDBASE_BASE_DOMAIN` / `GRIDBASE_ADMIN_DOMAIN` が
  `localhost`・`*.localhost`・`*.test`・`*.internal` 以外。判定は `scripts/deployment-check.mjs`、`directus/init/secrets-check.mjs` と同じ）では、
  既定のパスワード（`.env.example` の値・`change-me`・`local-dev-` など）を拒否します。DB に既定のパスワードの管理者（`role=admin` の全員。無効なものも含む）や、
  既定の管理者メール（`admin@gridbase.localhost` など）のユーザー（降格・無効化されていても）が残っている場合も、
  終了コード 3 で **web を起動しません**（ローカル構成で起動して `/account` で変更してから公開構成に戻します）。
  この DB の検査は `GRIDBASE_ADMIN_EMAIL` / `GRIDBASE_ADMIN_PASSWORD` が未設定でも行い、公開構成では検査できない場合
  （Directus に届かない、パスワード欄を読めない）や、`GRIDBASE_ADMIN_PASSWORD` の強度不足などの失敗も終了コード 3 にします（fail-closed）。
  同じ検査は `directus-init` も行います（そちらが止まれば web はそもそも起動しません）。
- Directus の `users` コレクションが使えるようになるまで最大120秒待ちます（`SEED_WAIT_SECONDS` で変更可）。
- ローカル構成では、失敗しても web の起動は続けます（ログに `[seed-admin]` が出ます）。公開構成では失敗しません（上記）。
  `docker-entrypoint.sh` は、seed-admin の終了コードが 3 のときは常に、それ以外の 0 以外（未処理の例外、OOM などのシグナルを含む）のときは
  `scripts/deployment-mode.mjs` でローカル構成と確認できない限り、web を起動しません（公開構成、または判定できないときは起動しない）。再実行: `docker compose run --rm web node scripts/seed-admin.mjs`
- 作成後は環境変数から `GRIDBASE_ADMIN_PASSWORD` を消して構いません（`docker inspect` で見えるため、消すことを推奨します）。

## セキュリティ上の設計

- `password` / `reset_token` / `reset_token_expires` は API から返しません。`api_key` は本人の `/api/dashboard/user` だけに返します。
- 書き込みは許可リスト方式です。一般ユーザーは `display_name` / `description` / サブドメイン項目だけを変更できます。
  `role` / `status` / `password` / `api_key` / `userId` は書けません（`userId` はセッション由来）。
- 管理者 API は管理者のみ。自分自身の降格・無効化・削除はできません。
- 認可は API ごとに Directus の最新の `role` / `status` で行います。無効化・削除は発行済みセッションにも即時に効きます
  （JWT 内の値も `GRIDBASE_SESSION_RECHECK_SECONDS` ごとに再確認し、無効なら無効化します）。
- パスワードを変更（`/account`）またはリセット（メール）すると、`users.password_changed_at` が更新されます。セッション（JWT）は発行時の値を持ち、
  API は毎回 DB の値と比べるので、**変更前に発行された他のセッションは次のリクエストから 401 になります**。`/account` はこの画面のセッションも
  新しいパスワードで再ログインして引き継ぎます。
- 状態を変える API はすべて、`Content-Type: application/json` 必須、かつ `Origin` が `NEXTAUTH_URL` のオリジンと
  完全一致（`Origin` が無い場合は `Sec-Fetch-Site: same-origin` のみ許可）でなければ拒否します
  （実装: `src/lib/request-guard.ts`）。サインインは NextAuth 自身の CSRF トークンで保護されます。
- API キーは `crypto.randomBytes(32)` 由来（`gb_` + base64url）。パスワードリセットトークンは SHA-256 のみ保存します。
- ログイン・新規登録・リセット・パスワード変更にはメモリ内のレート制限があります（コンテナ1台構成向けのベストエフォートです。
  プロキシで `X-Forwarded-For` を上書きしてください）。ログインは (IP, メール) ごとに 15 分 10 回に加えて、
  **メール（アカウント）ごとの失敗 20 回**でも止まります（IP を分散した総当たり対策。裏返しとして、他人のメールで失敗を重ねると
  最大 15 分ロックアウトできます）。回数はパスワードの検証より**前**に数える（成功すると戻る）ので、同時に送られたリクエストでも上限を超えて検証されません
  （パスワード変更の「現在のパスワード」の確認も同じ。`attemptWithLimit`）。
- パスワードリセットの応答は、メールの有無にかかわらず同じ内容・ほぼ同じ時間です（トークン保存とメール送信は応答後に非同期で行います）。
- ログイン後の遷移先（`callbackUrl`）は、URL として解決した結果が同一オリジンのパスのときだけ使います（`/\evil.com` などのバックスラッシュ経由の迂回を含む。`src/lib/callback-url.ts`）。
- `local_server_url` は `http://` または `https://` で始まるものだけ受け付けます。

## ローカル開発

```bash
cp .env.example .env.local   # 値を実環境に合わせる
npm install
npm run dev                  # http://localhost:3000
npm run lint && npm run typecheck && npm test
```

`NEXTAUTH_URL` を `http://localhost:3000` にしてください（Origin 検査が一致しないと書き込みが 403 になります）。

## Docker

```bash
docker build -t gridbase-web:dev .
docker run --rm -p 3000:3000 --env-file .env gridbase-web:dev
```

- Next.js の `output: 'standalone'`、ポート 3000、非 root（`node`）で動きます。
- ヘルスチェック: `GET /api/health`（DB には触れません）。
- ビルドに環境変数は不要です。

## 依存関係メモ

- Next.js は 14.2 系の最新（14.2.35）、Mantine は 6、next-auth は 4 の組み合わせです。`npm audit --omit=dev` は next（critical）と
  postcss（high）を報告し、「修正は Next.js 16 系（破壊的変更）」と表示します。移行（Next.js 16 / Mantine 7 / Auth.js）は今後の課題です。
- 「リバースプロキシの背後に置く」ことは、これらの脆弱性の**軽減策にはなりません**（プロキシはアプリへのリクエストをそのまま通します）。
  この構成に当てはまるかどうかは、設定を見て次のように整理しています。

| advisory の種類 | この構成での状況 |
|---|---|
| 画像最適化 API（remotePatterns の DoS、AVIF の RCE、`next/image` のディスクキャッシュ、画像最適化の DoS） | 当てはまらない。`next.config.js` で `images.unoptimized: true` にしており、`/_next/image` は 404 を返す。`next/image` は使っていない（`<img>` のみ） |
| rewrites（リクエストスムーグリング、SSRF） | 当てはまらない。`rewrites()` を使っていない |
| Server Actions（DoS、ペイロード無制限、SSRF、内部エンドポイントの露出） | 当てはまらない。Server Actions（`"use server"`）を使っていない |
| Pages Router の i18n ミドルウェアのバイパス | 当てはまらない。Pages Router も i18n も使っていない |
| CSP nonce を使う App Router の XSS、`beforeInteractive` スクリプトの XSS | 当てはまらない。nonce も `beforeInteractive` も使っていない（CSP は `'unsafe-inline'` 許可・nonce なし） |
| Windows ホストでの RCE | 当てはまらない。Linux コンテナで動かす |
| App Router の React Server Components への DoS（GHSA-h25m-26qc-wcjf、GHSA-q4gf-8mx6-v5v3、GHSA-8h8q-6873-q5fj） | **当てはまりうる**（App Router を使っている。実機での再現は未検証）。認証前のページ（`/signin` など）も対象になりうる |
| キャッシュポイズニング／キャッシュ混同（RSC、ミドルウェアのリダイレクト、本文付きリクエスト） | 共有キャッシュ（CDN・キャッシュ付きプロキシ）を前段に置いたときに問題になる。同梱の Caddy はキャッシュしない。**管理画面の前段にキャッシュを置かない**こと |
| WebSocket アップグレード経由の SSRF | 未検証。管理画面は WebSocket を使わないので、Caddy 側で管理画面ホストの `Upgrade` を通さない運用が安全 |
| postcss（XSS・ファイル読み取り） | ビルド時の CSS 処理に使われる依存。実行時に攻撃者の CSS を処理しない |

  公開インターネットに置く場合は、(1) 管理画面を VPN／IP 制限／認証プロキシの内側に置く、(2) 上の「当てはまりうる」項目を許容できるか判断する、
  のどちらかをおすすめします。上流の修正（Next.js 16）への移行が最終的な対処です。
- `nodemailer` は next-auth の peer 範囲外の新しい版を使うため、`package.json` の `overrides` で peer を上書きしています。
