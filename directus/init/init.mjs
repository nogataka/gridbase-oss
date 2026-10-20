// gridbase の Directus 初期化スクリプト（冪等）。
//
// docker compose の directus-init サービスが node:22-alpine で実行する。何回実行しても、
// すでに期待どおりの状態なら何も変更せず終了コード 0 で終わる。
//
//   1. Directus の health を待つ
//   2. ADMIN_TOKEN で /schema/diff -> /schema/apply（users / subdomains コレクション）
//      続けて、DB に保存済みの gridbase 管理者（role=admin の全員と、既定の管理者メールのユーザー）の
//      パスワードが既定値のままでないかを検査
//      （公開構成なら exit 1、ローカルは警告。admin-check.mjs）
//   3. ポリシー gridbase-web（users・subdomains の CRUD）と
//      gridbase-tunnel（users・subdomains の read のみ、必要な列だけ）を作成
//      （admin_access / app_access は false）
//   4. サービスユーザー 2 人を作り、静的トークンを .env の値に同期
//   5. Directus のキャッシュをクリア
//
// 起動前に、.env の秘密が既定のまま（.env.example の値・change-me・local-dev- など）でないかを検査する
// （secrets-check.mjs）。公開構成（GRIDBASE_BIND_ADDRESS が 127.0.0.1 以外、または https）で
// 既定の秘密が残っていれば exit 1 で止まり、tunnel / web / caddy は起動しない。
// ローカル構成（127.0.0.1・http・localhost 系ドメイン）では警告だけ出して続ける。
// 公開構成の判定: GRIDBASE_BIND_ADDRESS が 127.0.0.1 以外 / GRIDBASE_SITE_SCHEME・GRIDBASE_ADMIN_URL が https /
// GRIDBASE_BASE_DOMAIN・GRIDBASE_ADMIN_DOMAIN が localhost・*.localhost・*.test・*.internal 以外。
//
// 必要な環境変数:
//   DIRECTUS_URL                       既定 http://directus:8055
//   DIRECTUS_ADMIN_TOKEN               Directus 側の ADMIN_TOKEN と同じ値
//   GRIDBASE_WEB_DIRECTUS_TOKEN        web 用サービストークン
//   GRIDBASE_TUNNEL_DIRECTUS_TOKEN     tunnel 用サービストークン
// 秘密の検査のために compose が渡すもの（無くても動くが、その項目は検査されない）:
//   DIRECTUS_KEY / DIRECTUS_SECRET / DIRECTUS_ADMIN_PASSWORD / NEXTAUTH_SECRET /
//   GRIDBASE_ADMIN_PASSWORD / GRIDBASE_TUNNEL_ADMIN_TOKEN / POSTGRES_PASSWORD
//   GRIDBASE_BIND_ADDRESS / GRIDBASE_SITE_SCHEME / GRIDBASE_ADMIN_URL / GRIDBASE_BASE_DOMAIN / GRIDBASE_ADMIN_DOMAIN（公開構成の判定）
// 任意:
//   SCHEMA_FILE                        基準 snapshot。既定 /schema/gridbase.json
//                                      （同じディレクトリの gridbase.<vendor>.json があれば vendor に合わせて優先）
//   ENV_EXAMPLE_FILE                   .env.example の場所。既定 /defaults/env.example
//   INIT_WAIT_SECONDS                  Directus の準備待ち上限（既定 120）

import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import bcryptjs from 'bcryptjs';
import { CHECK_FIELDS, evaluateStoredAdmins, fetchCheckTargets } from './admin-check.mjs';
import { checkSecrets, parseEnvText } from './secrets-check.mjs';

const log = (msg) => console.log(`[directus-init] ${msg}`);
const fail = (msg) => {
  console.error(`[directus-init] ERROR: ${msg}`);
  process.exit(1);
};

const BASE = (process.env.DIRECTUS_URL || 'http://directus:8055').replace(/\/+$/, '');
const ADMIN_TOKEN = (process.env.DIRECTUS_ADMIN_TOKEN || '').trim();
const WEB_TOKEN = (process.env.GRIDBASE_WEB_DIRECTUS_TOKEN || '').trim();
const TUNNEL_TOKEN = (process.env.GRIDBASE_TUNNEL_DIRECTUS_TOKEN || '').trim();
const SCHEMA_FILE = process.env.SCHEMA_FILE || '/schema/gridbase.json';
const ENV_EXAMPLE_FILE = process.env.ENV_EXAMPLE_FILE || '/defaults/env.example';
const WAIT_SECONDS = Number(process.env.INIT_WAIT_SECONDS || 120);

for (const [name, value] of [
  ['DIRECTUS_ADMIN_TOKEN', ADMIN_TOKEN],
  ['GRIDBASE_WEB_DIRECTUS_TOKEN', WEB_TOKEN],
  ['GRIDBASE_TUNNEL_DIRECTUS_TOKEN', TUNNEL_TOKEN],
]) {
  if (!value) fail(`${name} が未設定です（.env を確認してください）`);
  if (value.length < 20 || /^change-me/i.test(value)) {
    fail(`${name} が短すぎるか、change-me のままです。openssl rand -hex 32 などで生成してください`);
  }
}
if (new Set([ADMIN_TOKEN, WEB_TOKEN, TUNNEL_TOKEN]).size !== 3) {
  fail('3 つのトークン（admin / web / tunnel）は別々の値にしてください');
}

// .env.example の内容（読めなければ空）。読めなくても、接頭辞（change-me / local-dev- / replace-with-）での検出は働く
async function loadKnownDefaults() {
  try {
    return parseEnvText(await readFile(ENV_EXAMPLE_FILE, 'utf8'));
  } catch {
    return new Map();
  }
}

// 既定の秘密が残っていないか。.env.example の値は、ファイルが読めればそれと完全一致でも検出する
async function checkDefaultSecrets() {
  const knownDefaults = await loadKnownDefaults();
  const { isPublic, reasons, problems } = checkSecrets(process.env, knownDefaults);
  if (problems.length === 0) return;

  if (isPublic) {
    console.error(`[directus-init] ERROR: 公開構成（${reasons.join(', ')}）で、既定または不適切な秘密が残っています:`);
    for (const p of problems) console.error(`  - ${p}`);
    console.error(
      '[directus-init] 強いランダム値（openssl rand -hex 32 / NEXTAUTH_SECRET は openssl rand -base64 32）に作り直して .env を更新してください。' +
        'すでに起動済みのデータは .env を変えても変わりません（管理者パスワード・Directus 管理者などの変更手順は docs/production.md の「Changing credentials after the first start」を参照）。',
    );
    process.exit(1);
  }
  console.warn('[directus-init] WARNING: .env.example のローカル検証用の値のままです（127.0.0.1 かつ http なので続行します）:');
  for (const p of problems) console.warn(`  - ${p}`);
  console.warn('[directus-init] WARNING: GRIDBASE_BIND_ADDRESS を 127.0.0.1 以外にする前に、すべて作り直してください');
}
await checkDefaultSecrets();

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const qs = (params) => new URLSearchParams(params).toString();

class ApiError extends Error {
  constructor(method, path, status, body) {
    const detail = body?.errors?.map((e) => e.message).join('; ') || '';
    super(`${method} ${path.split('?')[0]} -> ${status} ${detail}`.trim());
    this.status = status;
    this.body = body;
  }
}

async function api(path, { method = 'GET', body, token = ADMIN_TOKEN } = {}) {
  const resp = await fetch(`${BASE}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (resp.status === 204) return null;
  const json = await resp.json().catch(() => null);
  if (!resp.ok) throw new ApiError(method, path, resp.status, json);
  return json;
}

// ---- 1. health --------------------------------------------------------------

async function waitForDirectus() {
  const deadline = Date.now() + WAIT_SECONDS * 1000;
  let last = '';
  for (let n = 0; ; n++) {
    try {
      const resp = await fetch(`${BASE}/server/health`);
      if (resp.ok) {
        const me = await fetch(`${BASE}/users/me?fields=id`, { headers: { Authorization: `Bearer ${ADMIN_TOKEN}` } });
        if (me.ok) return;
        last = `ADMIN_TOKEN で /users/me が ${me.status}`;
      } else {
        last = `health ${resp.status}`;
      }
    } catch (e) {
      last = e.message;
    }
    if (Date.now() > deadline) fail(`Directus の準備が ${WAIT_SECONDS} 秒以内に整いませんでした (${last})`);
    if (n % 5 === 0) log(`Directus を待っています... (${last})`);
    await sleep(2000);
  }
}

// ---- 2. schema --------------------------------------------------------------

// 稼働中の DB の vendor に合う snapshot を選ぶ。
//   sqlite   -> gridbase.json（基準）
//   postgres -> gridbase.postgres.json（型名が vendor ごとに違い、基準を流すと毎回差分が出るため別ファイル）
// それ以外の vendor は基準の snapshot を force で適用する。
async function loadSnapshot() {
  const live = await api('/schema/snapshot');
  const vendor = live.data.vendor;
  const dir = dirname(SCHEMA_FILE);
  const vendorFile = join(dir, `gridbase.${vendor}.json`);
  for (const file of vendor === 'sqlite' ? [SCHEMA_FILE] : [vendorFile, SCHEMA_FILE]) {
    try {
      const snapshot = JSON.parse(await readFile(file, 'utf8'));
      log(`schema: DB vendor=${vendor}, snapshot=${file}`);
      return snapshot;
    } catch (e) {
      if (e.code !== 'ENOENT') throw e;
    }
  }
  throw new Error(`snapshot が見つかりません: ${SCHEMA_FILE}`);
}

async function applySchema() {
  const snapshot = await loadSnapshot();

  const diffOnce = (force) =>
    api(`/schema/diff${force ? '?force=true' : ''}`, { method: 'POST', body: snapshot });
  let force = false;
  let diff;
  try {
    diff = await diffOnce(false);
  } catch (e) {
    // snapshot は Directus 11.17.4 / SQLite で取得したもの。Postgres など別 vendor、または Directus を上げて版が
    // 変わったときは、diff が vendor / version の不一致で拒否されるので force で再試行する
    if (e instanceof ApiError && /vendor|version/i.test(e.message)) {
      log(`snapshot と Directus の vendor / 版が異なる（${e.message}）ため force で差分を取ります`);
      force = true;
      diff = await diffOnce(true);
    } else {
      throw e;
    }
  }

  if (!diff || !diff.data || !diff.data.diff) {
    log('schema: 変更なし');
    return false;
  }
  const d = diff.data.diff;
  log(
    `schema: 適用します (collections ${d.collections?.length ?? 0}, fields ${d.fields?.length ?? 0}, relations ${d.relations?.length ?? 0})`,
  );
  await api(`/schema/apply${force ? '?force=true' : ''}`, { method: 'POST', body: diff.data });
  log('schema: 適用しました');
  return true;
}

// ---- 2b. 保存済みの gridbase 管理者のパスワード（検査対象は admin-check.mjs を参照） -----------------------------------

// 管理者のパスワードは初回起動のときに DB へ保存され、.env を変えても（GRIDBASE_ADMIN_PASSWORD を消しても）変わらない。
// 「ローカルで一度試した DB」を公開構成に持っていくと、README に載っている既定の資格情報で管理者にサインインできてしまう。
// .env の値ではなく DB に保存されたハッシュを調べる。
//   公開構成: 既定のパスワードの管理者がいる、または検査できない -> exit 1（tunnel / web / caddy は起動しない）
//   ローカル: 警告だけ
async function checkStoredAdminPasswords() {
  const { isPublic, reasons, checked, lines } = await evaluateStoredAdmins({
    env: process.env,
    knownDefaults: await loadKnownDefaults(),
    compare: (plain, hash) => bcryptjs.compare(plain, hash),
    fetchAdmins: () =>
      fetchCheckTargets(process.env, async (params) =>
        (await api(`/items/users?${qs({ ...params, fields: CHECK_FIELDS, limit: '-1' })}`)).data,
      ),
  });

  if (lines.length === 0) {
    log(`管理者のパスワード: 既定のものはありません（有効な管理者 ${checked} 人を確認）`);
    return;
  }
  if (isPublic) {
    console.error(`[directus-init] ERROR: 公開構成（${reasons.join(', ')}）ですが、保存済みの管理者のパスワードに問題があります:`);
    for (const line of lines) console.error(`  - ${line}`);
    console.error(
      checked === null
        ? '[directus-init] 保存済みの管理者のパスワードを確認できないため、公開構成では起動しません。Directus の状態を確認して、もう一度起動してください。'
        : '[directus-init] .env を変えても DB のパスワードは変わりません。一度ローカル構成（GRIDBASE_BIND_ADDRESS=127.0.0.1 かつ http、実ドメインなし）で起動し、' +
            'サインインして「アカウント」画面（/account）でパスワードを変更してから、公開構成に戻してください。' +
            '新規構築なら docker compose down -v でデータを消してやり直せます（docs/production.md の「Changing credentials after the first start」を参照）。',
    );
    process.exit(1);
  }
  console.warn('[directus-init] WARNING: 保存済みの管理者のパスワードに問題があります（ローカル構成なので続行します）:');
  for (const line of lines) console.warn(`  - ${line}`);
  console.warn('[directus-init] WARNING: 公開する前に、「アカウント」画面（/account）でパスワードを変更してください');
}

// ---- 3. policies ------------------------------------------------------------

const ALL = ['*'];
const TUNNEL_USER_FIELDS = ['userId', 'api_key', 'settings', 'status'];
const TUNNEL_SUBDOMAIN_FIELDS = [
  'id', 'userId', 'subdomain', 'mode', 'local_server_url', 'local_host', 'local_port', 'status', 'description',
];

const POLICIES = [
  {
    name: 'gridbase-web',
    description: 'gridbase 管理画面(web)用。users と subdomains の CRUD のみ',
    permissions: ['users', 'subdomains'].flatMap((collection) =>
      ['create', 'read', 'update', 'delete'].map((action) => ({ collection, action, fields: ALL })),
    ),
  },
  {
    name: 'gridbase-tunnel',
    description: 'gridbase トンネルサーバー用。users と subdomains の read のみ（必要な列だけ）',
    permissions: [
      { collection: 'users', action: 'read', fields: TUNNEL_USER_FIELDS },
      { collection: 'subdomains', action: 'read', fields: TUNNEL_SUBDOMAIN_FIELDS },
    ],
  },
];

const normFields = (f) => JSON.stringify([...(f || [])].sort());
const normObj = (o) => JSON.stringify(o && Object.keys(o).length ? o : {});
const permKey = (p) => `${p.collection}:${p.action}`;

async function ensurePolicy(def) {
  let changed = false;
  const found = await api(
    `/policies?${qs({ 'filter[name][_eq]': def.name, fields: 'id,name,admin_access,app_access', limit: '-1' })}`,
  );
  let policy = found.data[0];
  if (!policy) {
    const created = await api('/policies', {
      method: 'POST',
      body: { name: def.name, icon: 'badge', description: def.description, admin_access: false, app_access: false },
    });
    policy = created.data;
    log(`policy ${def.name}: 作成しました`);
    changed = true;
  } else if (policy.admin_access !== false || policy.app_access !== false) {
    await api(`/policies/${policy.id}`, { method: 'PATCH', body: { admin_access: false, app_access: false } });
    log(`policy ${def.name}: admin_access / app_access を false に戻しました`);
    changed = true;
  }

  const current = (
    await api(
      `/permissions?${qs({
        'filter[policy][_eq]': policy.id,
        fields: 'id,collection,action,fields,permissions,validation',
        limit: '-1',
      })}`,
    )
  ).data;
  const wanted = new Map(def.permissions.map((p) => [permKey(p), p]));
  const seen = new Set();

  for (const cur of current) {
    const key = permKey(cur);
    const want = wanted.get(key);
    if (!want || seen.has(key)) {
      await api(`/permissions/${cur.id}`, { method: 'DELETE' });
      log(`policy ${def.name}: 不要な権限 ${key} を削除しました`);
      changed = true;
      continue;
    }
    seen.add(key);
    if (normFields(cur.fields) !== normFields(want.fields) || normObj(cur.permissions) !== '{}' || normObj(cur.validation) !== '{}') {
      await api(`/permissions/${cur.id}`, {
        method: 'PATCH',
        body: { fields: want.fields, permissions: {}, validation: {} },
      });
      log(`policy ${def.name}: 権限 ${key} を更新しました`);
      changed = true;
    }
  }
  for (const [key, want] of wanted) {
    if (seen.has(key)) continue;
    await api('/permissions', {
      method: 'POST',
      body: { policy: policy.id, collection: want.collection, action: want.action, fields: want.fields, permissions: {}, validation: {} },
    });
    log(`policy ${def.name}: 権限 ${key} を追加しました`);
    changed = true;
  }
  if (!changed) log(`policy ${def.name}: 変更なし`);
  return { id: policy.id, changed };
}

// ---- 4. service users -------------------------------------------------------

async function ensureServiceUser({ email, firstName, token, policyId }) {
  let changed = false;
  const found = await api(`/users?${qs({ 'filter[email][_eq]': email, fields: 'id,email,status,role', limit: '1' })}`);
  let user = found.data[0];
  if (!user) {
    const created = await api('/users', {
      method: 'POST',
      body: { email, first_name: firstName, status: 'active', role: null, token },
    });
    user = created.data;
    log(`user ${email}: 作成しました（トークン設定済み）`);
    changed = true;
  } else {
    if (user.status !== 'active' || user.role !== null) {
      await api(`/users/${user.id}`, { method: 'PATCH', body: { status: 'active', role: null } });
      log(`user ${email}: status=active / role=null に戻しました`);
      changed = true;
    }
    // 保存済みトークンは Directus が値を返さないので、.env のトークンで実際に認証できるかで判定する
    let tokenOk = false;
    try {
      const me = await api('/users/me?fields=id', { token });
      tokenOk = me.data.id === user.id;
    } catch (e) {
      if (!(e instanceof ApiError)) throw e;
    }
    if (!tokenOk) {
      await api(`/users/${user.id}`, { method: 'PATCH', body: { token } });
      log(`user ${email}: トークンを .env の値に同期しました`);
      changed = true;
    }
  }

  // ポリシーの紐づけは目的の 1 つだけにする
  const access = (
    await api(`/access?${qs({ 'filter[user][_eq]': user.id, fields: 'id,policy', limit: '-1' })}`)
  ).data;
  let linked = false;
  for (const a of access) {
    if (a.policy === policyId && !linked) {
      linked = true;
    } else {
      await api(`/access/${a.id}`, { method: 'DELETE' });
      log(`user ${email}: 余分なポリシー紐づけを外しました`);
      changed = true;
    }
  }
  if (!linked) {
    await api('/access', { method: 'POST', body: { user: user.id, policy: policyId } });
    log(`user ${email}: ポリシーを紐づけました`);
    changed = true;
  }
  if (!changed) log(`user ${email}: 変更なし`);
  return changed;
}

// ---- main -------------------------------------------------------------------

async function main() {
  log(`Directus: ${BASE}`);
  await waitForDirectus();
  log('Directus は応答しています');

  await applySchema();

  // 保存済みの管理者が既定のパスワードのまま公開されないか（公開構成では止める）
  await checkStoredAdminPasswords();

  const web = await ensurePolicy(POLICIES[0]);
  const tunnel = await ensurePolicy(POLICIES[1]);

  await ensureServiceUser({
    email: 'gridbase-web@service.example.com',
    firstName: 'gridbase-web',
    token: WEB_TOKEN,
    policyId: web.id,
  });
  await ensureServiceUser({
    email: 'gridbase-tunnel@service.example.com',
    firstName: 'gridbase-tunnel',
    token: TUNNEL_TOKEN,
    policyId: tunnel.id,
  });

  // スキーマ・権限を変えた直後は Directus のキャッシュが古いままになることがあるため、毎回クリアする
  await api('/utils/cache/clear', { method: 'POST' });
  log('cache: クリアしました');
  log('完了');
}

main().catch((e) => fail(e.message));
