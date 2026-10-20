#!/usr/bin/env node
// gridbase の end-to-end smoke テスト。compose を起動した後に流す。
//
//   cp .env.example .env && docker compose up -d --build
//   node tests/e2e/smoke.mjs
//
// 確認すること:
//   1. 未ログインの拒否、管理者でのログイン、API キー発行、サブドメイン作成
//   2. 権限（一般ユーザーは管理 API・他人のサブドメインを操作できない / 項目の書き換え = mass assignment 不可）
//   3. CSRF 拒否（Origin 不一致・Origin 無し・Content-Type 違い）
//   4. パスワード変更（現在のパスワード必須・他のセッションが失効する）
//   5. トンネル疎通（ホストのアプリ -> client -> tunnel -> caddy -> 手元の curl 相当）と、未接続時の 503
//   6. directus-init の冪等性（2 回目は「変更なし」）と、公開構成での既定の秘密の拒否
//
// 必要なもの: Node 20 以上、Docker（compose v2）。外部パッケージは使わない。
// 設定は環境変数、無ければリポジトリ直下の .env から読む:
//   GRIDBASE_HOST_HTTP_PORT / GRIDBASE_ADMIN_DOMAIN / GRIDBASE_BASE_DOMAIN / GRIDBASE_ADMIN_URL /
//   GRIDBASE_ADMIN_EMAIL / GRIDBASE_ADMIN_PASSWORD
// 終了コード: すべて PASS なら 0、1 つでも FAIL なら 1。

import http from 'node:http';
import { spawn, spawnSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..', '..');

// ---- 設定 -------------------------------------------------------------------

function readDotEnv() {
  const file = join(ROOT, '.env');
  const values = {};
  if (!existsSync(file)) return values;
  for (const raw of readFileSync(file, 'utf8').split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const eq = line.indexOf('=');
    if (eq <= 0) continue;
    let value = line.slice(eq + 1).trim();
    if (/^(["']).*\1$/.test(value)) value = value.slice(1, -1);
    else value = value.replace(/\s+#.*$/, '');
    values[line.slice(0, eq).trim()] = value;
  }
  return values;
}
const dotenv = readDotEnv();
const conf = (name, fallback) => process.env[name] || dotenv[name] || fallback;

const HTTP_PORT = Number(conf('GRIDBASE_HOST_HTTP_PORT', '80'));
const ADMIN_DOMAIN = conf('GRIDBASE_ADMIN_DOMAIN', 'admin.gridbase.localhost');
const BASE_DOMAIN = conf('GRIDBASE_BASE_DOMAIN', 'gridbase.localhost');
const ADMIN_URL = conf('GRIDBASE_ADMIN_URL', 'http://admin.gridbase.localhost').replace(/\/+$/, '');
const ADMIN_EMAIL = conf('GRIDBASE_ADMIN_EMAIL', 'admin@gridbase.localhost');
const ADMIN_PASSWORD = conf('GRIDBASE_ADMIN_PASSWORD', 'ChangeMe-local-1234');
const hostHeader = (domain) => (HTTP_PORT === 80 ? domain : `${domain}:${HTTP_PORT}`);

// ---- 結果の記録 -------------------------------------------------------------

let passed = 0;
const failures = [];
function check(name, condition, detail = '') {
  if (condition) {
    passed += 1;
    console.log(`  PASS  ${name}`);
  } else {
    failures.push(name);
    console.log(`  FAIL  ${name}${detail ? `\n        ${String(detail).replace(/\n/g, '\n        ')}` : ''}`);
  }
}
const section = (title) => console.log(`\n== ${title}`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- HTTP（127.0.0.1:<port> に接続し、Host ヘッダーで振り分ける）-----------

class Jar {
  constructor() {
    this.cookies = new Map();
  }
  update(setCookie) {
    for (const line of setCookie || []) {
      const [pair, ...attrs] = line.split(';');
      const eq = pair.indexOf('=');
      const name = pair.slice(0, eq).trim();
      const value = pair.slice(eq + 1).trim();
      const expired = attrs.some((a) => /^\s*max-age=0\s*$/i.test(a)) || value === '';
      if (expired) this.cookies.delete(name);
      else this.cookies.set(name, value);
    }
  }
  header() {
    return [...this.cookies].map(([k, v]) => `${k}=${v}`).join('; ');
  }
  has(namePart) {
    return [...this.cookies.keys()].some((k) => k.includes(namePart));
  }
}

function rawRequest({ host, path, method = 'GET', headers = {}, body, jar }) {
  return new Promise((resolve, reject) => {
    const h = { Host: hostHeader(host), ...headers };
    if (jar && jar.cookies.size) h.Cookie = jar.header();
    if (body !== undefined) h['Content-Length'] = Buffer.byteLength(body);
    const req = http.request({ host: '127.0.0.1', port: HTTP_PORT, path, method, headers: h }, (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => {
        const text = Buffer.concat(chunks).toString('utf8');
        if (jar) jar.update(res.headers['set-cookie']);
        let json = null;
        if ((res.headers['content-type'] || '').includes('json')) {
          try {
            json = JSON.parse(text);
          } catch {
            json = null;
          }
        }
        resolve({ status: res.statusCode, headers: res.headers, text, json });
      });
    });
    req.setTimeout(20000, () => req.destroy(new Error(`timeout ${method} ${host}${path}`)));
    req.on('error', reject);
    if (body !== undefined) req.write(body);
    req.end();
  });
}

/** 管理画面への API 呼び出し。書き込みには正しい Origin と JSON を付ける。 */
function api(jar, method, path, payload, overrides = {}) {
  const headers = { Origin: ADMIN_URL, 'Content-Type': 'application/json', ...overrides };
  for (const k of Object.keys(headers)) if (headers[k] === null) delete headers[k];
  return rawRequest({
    host: ADMIN_DOMAIN,
    path,
    method,
    headers,
    body: method === 'GET' || method === 'HEAD' ? undefined : JSON.stringify(payload ?? {}),
    jar,
  });
}

async function signIn(email, password) {
  const jar = new Jar();
  const csrf = await rawRequest({ host: ADMIN_DOMAIN, path: '/api/auth/csrf', jar });
  const body = new URLSearchParams({
    csrfToken: csrf.json?.csrfToken ?? '',
    email,
    password,
    callbackUrl: `${ADMIN_URL}/dashboard`,
    json: 'true',
  }).toString();
  const res = await rawRequest({
    host: ADMIN_DOMAIN,
    path: '/api/auth/callback/credentials',
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded', Origin: ADMIN_URL },
    body,
    jar,
  });
  const session = await rawRequest({ host: ADMIN_DOMAIN, path: '/api/auth/session', jar });
  return { jar, res, session: session.json, ok: Boolean(session.json?.user?.email) };
}

// ---- docker compose ----------------------------------------------------------

function compose(args, { input, timeout = 120000 } = {}) {
  const r = spawnSync('docker', ['compose', ...args], { cwd: ROOT, encoding: 'utf8', input, timeout });
  return { status: r.status, out: `${r.stdout || ''}${r.stderr || ''}` };
}

/** web コンテナの中で、Directus に一般ユーザーを直接作る（管理者の招待機能が無く、サインアップは既定で閉鎖のため）。 */
function createUserViaWeb(email, password) {
  const code = `
    import bcrypt from 'bcryptjs';
    import { randomUUID, randomBytes } from 'node:crypto';
    const id = randomUUID();
    const resp = await fetch(process.env.DIRECTUS_URL + '/items/users', {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + process.env.DIRECTUS_TOKEN, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id, userId: id, email: process.env.E2E_EMAIL, password: bcrypt.hashSync(process.env.E2E_PASSWORD, 10),
        role: 'user', status: 'active', display_name: 'e2e user',
        api_key: 'gb_' + randomBytes(32).toString('base64url'), created_at: new Date().toISOString(),
      }),
    });
    if (!resp.ok) { console.error(resp.status, await resp.text()); process.exit(1); }
    console.log(id);
  `;
  return compose(['exec', '-T', '-e', `E2E_EMAIL=${email}`, '-e', `E2E_PASSWORD=${password}`, 'web', 'node', '--input-type=module', '-e', code]);
}

// ---- テスト本体 -------------------------------------------------------------

const RUN = randomBytes(3).toString('hex');
const subName = (label) => `e2e${label}${RUN}`; // 3〜16 文字・英小文字と数字
const USER1 = { email: `e2e-u1-${RUN}@example.com`, password: `First-pass-${RUN}1`, newPassword: `Second-pass-${RUN}2` };
const USER2 = { email: `e2e-u2-${RUN}@example.com`, password: `Other-pass-${RUN}3` };

async function testAnonymous() {
  section('未ログインの扱い');
  const health = await rawRequest({ host: ADMIN_DOMAIN, path: '/api/health' });
  check('管理画面の /api/health が 200', health.status === 200, `status=${health.status}`);
  const top = await rawRequest({ host: ADMIN_DOMAIN, path: '/dashboard' });
  check('未ログインの /dashboard は /signin へ', top.status >= 300 && top.status < 400 && /\/signin/.test(top.headers.location || ''), `status=${top.status} location=${top.headers.location}`);
  check('未ログインの GET /api/dashboard/user は 401', (await api(new Jar(), 'GET', '/api/dashboard/user')).status === 401);
  check('未ログインの GET /api/admin/users は 401', (await api(new Jar(), 'GET', '/api/admin/users')).status === 401);
  check('未ログインの POST /api/account/password は 401', (await api(new Jar(), 'POST', '/api/account/password', { currentPassword: 'x', newPassword: 'y' })).status === 401);
  const image = await rawRequest({ host: ADMIN_DOMAIN, path: '/_next/image?url=%2Fimages%2Flogo_new.png&w=64&q=75' });
  check('画像最適化 API（/_next/image）は 404（images.unoptimized）', image.status === 404, `status=${image.status}`);
  const unknown = await rawRequest({ host: `nosuch${RUN}.${BASE_DOMAIN}`, path: '/' });
  check('未登録のサブドメインは 503', unknown.status === 503, `status=${unknown.status}`);

  const bad = await signIn(ADMIN_EMAIL, `${ADMIN_PASSWORD}-wrong`);
  check('誤ったパスワードではログインできない', !bad.ok);
}

async function testAdminFlow(ctx) {
  section('管理者: ログイン・API キー・サブドメイン');
  const admin = await signIn(ADMIN_EMAIL, ADMIN_PASSWORD);
  check('管理者でログインできる', admin.ok, `status=${admin.res.status} body=${admin.res.text.slice(0, 200)}`);
  if (!admin.ok) throw new Error('管理者でログインできないため中断します');
  ctx.admin = admin;
  check('セッション Cookie は http では __Host- なし（next-auth.session-token）', admin.jar.has('next-auth.session-token') && !admin.jar.has('__Host-'));

  const evilCallback = await (async () => {
    const jar = new Jar();
    const csrf = await rawRequest({ host: ADMIN_DOMAIN, path: '/api/auth/csrf', jar });
    return rawRequest({
      host: ADMIN_DOMAIN,
      path: '/api/auth/callback/credentials',
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', Origin: ADMIN_URL },
      body: new URLSearchParams({ csrfToken: csrf.json?.csrfToken ?? '', email: ADMIN_EMAIL, password: ADMIN_PASSWORD, callbackUrl: 'https://evil.example.net/x', json: 'true' }).toString(),
      jar,
    });
  })();
  check('NextAuth の callbackUrl に外部 URL を渡しても同一オリジンに戻る', Boolean(evilCallback.json?.url) && evilCallback.json.url.startsWith(ADMIN_URL), `url=${evilCallback.json?.url}`);

  const me = await api(admin.jar, 'GET', '/api/dashboard/user');
  check('GET /api/dashboard/user が 200 で role=admin', me.status === 200 && me.json?.user?.role === 'admin', `status=${me.status}`);
  check('応答に password / reset_token を含まない', !/"password"|reset_token/.test(me.text));
  ctx.adminId = me.json?.user?.userId;

  const key = await api(admin.jar, 'POST', '/api/dashboard/api-key', {});
  check('API キーを発行できる（gb_ で始まる）', key.status === 200 && /^gb_/.test(key.json?.apiKey ?? ''), `status=${key.status}`);
  ctx.apiKey = key.json?.apiKey;

  const sub = subName('main');
  ctx.subdomain = sub;
  const created = await api(admin.jar, 'POST', '/api/dashboard/subdomains', { subdomain: sub, mode: 'http', local_server_url: 'http://host.docker.internal:1' });
  check('サブドメインを作成できる', created.status === 200 && created.json?.subdomain === sub, `status=${created.status} ${created.text.slice(0, 160)}`);
  const list = await api(admin.jar, 'GET', '/api/dashboard/subdomains');
  const record = list.json?.subdomains?.find((s) => s.subdomain === sub);
  ctx.subdomainId = record?.id;
  check('一覧にサブドメインが出る（last_active_at は返さない）', Boolean(record) && !('last_active_at' in record), JSON.stringify(record));

  const dup = await api(admin.jar, 'POST', '/api/dashboard/subdomains', { subdomain: sub, local_server_url: 'http://host.docker.internal:1' });
  check('重複したサブドメインは 409', dup.status === 409, `status=${dup.status}`);
  const reserved = await api(admin.jar, 'POST', '/api/dashboard/subdomains', { subdomain: 'admin', local_server_url: 'http://host.docker.internal:1' });
  check('予約語のサブドメインは 409', reserved.status === 409, `status=${reserved.status}`);
  const badFormat = await api(admin.jar, 'POST', '/api/dashboard/subdomains', { subdomain: 'Bad_Name', local_server_url: 'http://host.docker.internal:1' });
  check('不正な形式のサブドメインは 400', badFormat.status === 400, `status=${badFormat.status}`);
  const tcp = await api(admin.jar, 'POST', '/api/dashboard/subdomains', { subdomain: subName('tcp'), mode: 'tcp', local_host: '127.0.0.1', local_port: 22 });
  check('TCP モードは未対応として 400', tcp.status === 400 && /TCP/.test(tcp.text), `status=${tcp.status} ${tcp.text.slice(0, 120)}`);
  const badUrl = await api(admin.jar, 'POST', '/api/dashboard/subdomains', { subdomain: subName('url'), local_server_url: 'javascript:alert(1)' });
  check('http(s) 以外の local_server_url は 400', badUrl.status === 400, `status=${badUrl.status}`);

  const users = await api(admin.jar, 'GET', '/api/admin/users');
  check('管理者は /api/admin/users を読める', users.status === 200 && Array.isArray(users.json?.users), `status=${users.status}`);
  check('一覧に api_key / password を含まない', !/api_key|"password"/.test(users.text));
}

async function testCsrf(ctx) {
  section('CSRF / 入力形式');
  const path = '/api/dashboard/user';
  const payload = { display_name: 'csrf check' };
  const ok = await api(ctx.admin.jar, 'PATCH', path, payload);
  check('正しい Origin + JSON は 200', ok.status === 200, `status=${ok.status}`);
  const evil = await api(ctx.admin.jar, 'PATCH', path, payload, { Origin: 'http://evil.example.com' });
  check('他サイトの Origin は 403', evil.status === 403, `status=${evil.status}`);
  const tunnelOrigin = await api(ctx.admin.jar, 'PATCH', path, payload, { Origin: `http://${ctx.subdomain}.${BASE_DOMAIN}` });
  check('トンネル側サブドメインの Origin は 403', tunnelOrigin.status === 403, `status=${tunnelOrigin.status}`);
  const nullOrigin = await api(ctx.admin.jar, 'PATCH', path, payload, { Origin: 'null' });
  check('Origin: null は 403', nullOrigin.status === 403, `status=${nullOrigin.status}`);
  const noOrigin = await api(ctx.admin.jar, 'PATCH', path, payload, { Origin: null });
  check('Origin が無い書き込みは 403', noOrigin.status === 403, `status=${noOrigin.status}`);
  const form = await api(ctx.admin.jar, 'PATCH', path, payload, { 'Content-Type': 'application/x-www-form-urlencoded' });
  check('Content-Type が JSON でない書き込みは 415', form.status === 415, `status=${form.status}`);
  const notObject = await rawRequest({ host: ADMIN_DOMAIN, path, method: 'PATCH', headers: { Origin: ADMIN_URL, 'Content-Type': 'application/json' }, body: '"just a string"', jar: ctx.admin.jar });
  check('JSON がオブジェクトでない PATCH /api/dashboard/user は 400（500 にならない）', notObject.status === 400, `status=${notObject.status}`);
  const signup = await api(new Jar(), 'POST', '/api/signup', { email: 'x@example.com', password: 'abcdefg1', name: 'xx' }, { Origin: 'http://evil.example.com' });
  check('他サイトの Origin からのサインアップは 403', signup.status === 403, `status=${signup.status}`);
}

async function testPermissions(ctx) {
  section('一般ユーザーの権限');
  for (const u of [USER1, USER2]) {
    const r = createUserViaWeb(u.email, u.password);
    check(`一般ユーザー ${u.email} を作成`, r.status === 0, r.out.slice(-300));
    if (r.status === 0) u.id = r.out.trim().split(/\s+/).pop();
  }
  const s1 = await signIn(USER1.email, USER1.password);
  const s2 = await signIn(USER2.email, USER2.password);
  check('一般ユーザーでログインできる', s1.ok && s2.ok);
  if (!s1.ok || !s2.ok) throw new Error('一般ユーザーでログインできないため中断します');
  ctx.s1 = s1;
  ctx.s2 = s2;

  check('一般ユーザーは GET /api/admin/users が 403', (await api(s1.jar, 'GET', '/api/admin/users')).status === 403);
  check('一般ユーザーは PATCH /api/admin/users/<admin> が 403', (await api(s1.jar, 'PATCH', `/api/admin/users/${ctx.adminId}`, { role: 'user', status: 'inactive' })).status === 403);
  check('一般ユーザーは DELETE /api/admin/users/<admin> が 403', (await api(s1.jar, 'DELETE', `/api/admin/users/${ctx.adminId}`, {})).status === 403);
  check('一般ユーザーは自分を管理者にできない', (await api(s1.jar, 'PATCH', `/api/admin/users/${USER1.id}`, { role: 'admin' })).status === 403);
  check('一般ユーザーは /api/admin/users/<id>/subdomains が 403', (await api(s1.jar, 'GET', `/api/admin/users/${ctx.adminId}/subdomains`)).status === 403);
  const adminPage = await rawRequest({ host: ADMIN_DOMAIN, path: '/admin', jar: s1.jar });
  check('一般ユーザーの /admin は /dashboard へ戻される', adminPage.status >= 300 && adminPage.status < 400 && /\/dashboard/.test(adminPage.headers.location || ''), `status=${adminPage.status} location=${adminPage.headers.location}`);

  // 項目の書き換え（mass assignment）
  const mass = await api(s1.jar, 'PATCH', '/api/dashboard/user', { role: 'admin', status: 'inactive', password: 'hacked-1234', api_key: 'gb_stolen', userId: ctx.adminId });
  check('書き換え不可の項目だけの PATCH は 400', mass.status === 400, `status=${mass.status}`);
  const mixed = await api(s1.jar, 'PATCH', '/api/dashboard/user', { display_name: 'renamed', role: 'admin' });
  check('表示名の変更は 200', mixed.status === 200, `status=${mixed.status}`);
  const me1 = await api(s1.jar, 'GET', '/api/dashboard/user');
  check('role は user のまま、表示名だけ変わる', me1.json?.user?.role === 'user' && me1.json?.user?.display_name === 'renamed', JSON.stringify(me1.json?.user));
  check('自分の api_key は返る', /^gb_/.test(me1.json?.user?.api_key ?? ''));

  // 他人のサブドメイン
  const u2sub = subName('u2');
  const created = await api(s2.jar, 'POST', '/api/dashboard/subdomains', { subdomain: u2sub, local_server_url: 'http://host.docker.internal:1' });
  check('USER2 がサブドメインを作成', created.status === 200, `status=${created.status}`);
  const u2list = await api(s2.jar, 'GET', '/api/dashboard/subdomains');
  const u2id = u2list.json?.subdomains?.find((s) => s.subdomain === u2sub)?.id;
  check('USER1 は USER2 のサブドメインを PATCH できない（403）', (await api(s1.jar, 'PATCH', '/api/dashboard/subdomains', { id: u2id, local_server_url: 'http://evil.example.com' })).status === 403);
  check('USER1 は USER2 のサブドメインを DELETE できない（403）', (await api(s1.jar, 'DELETE', `/api/dashboard/subdomains?id=${encodeURIComponent(u2id)}`, {})).status === 403);
  const after = await api(s2.jar, 'GET', '/api/dashboard/subdomains');
  const stillThere = after.json?.subdomains?.find((s) => s.id === u2id);
  check('USER2 のサブドメインは無傷', Boolean(stillThere) && stillThere.local_server_url === 'http://host.docker.internal:1');
  const spoof = await api(s1.jar, 'POST', '/api/dashboard/subdomains', { subdomain: subName('sp'), local_server_url: 'http://host.docker.internal:1', userId: USER2.id });
  check('body の userId は無視される（USER1 として作成）', spoof.status === 200);
  const u2after = await api(s2.jar, 'GET', '/api/dashboard/subdomains');
  check('USER2 の一覧に USER1 の作成物は現れない', !u2after.json?.subdomains?.some((s) => s.subdomain === subName('sp')));
}

async function testPasswordChange(ctx) {
  section('パスワード変更（現在のパスワード必須・他のセッションは失効）');
  const other = await signIn(USER1.email, USER1.password); // 別端末のセッション
  check('別端末のセッションでログインできる', other.ok);
  const url = '/api/account/password';

  check('現在のパスワードが違うと 400', (await api(ctx.s1.jar, 'POST', url, { currentPassword: 'wrong-pass-1', newPassword: USER1.newPassword })).status === 400);
  check('弱い新パスワードは 400', (await api(ctx.s1.jar, 'POST', url, { currentPassword: USER1.password, newPassword: 'short' })).status === 400);
  check('現在と同じ新パスワードは 400', (await api(ctx.s1.jar, 'POST', url, { currentPassword: USER1.password, newPassword: USER1.password })).status === 400);
  check('Origin 不一致は 403（CSRF）', (await api(ctx.s1.jar, 'POST', url, { currentPassword: USER1.password, newPassword: USER1.newPassword }, { Origin: 'http://evil.example.com' })).status === 403);
  check('まだ有効: 失敗後も元のセッションは使える', (await api(ctx.s1.jar, 'GET', '/api/dashboard/user')).status === 200);

  // 同時に届いた総当たりでも、試行回数の上限（5 回）を超えて検証されない（先に数えてから判定する）。USER2 のセッションで行う
  const burst = await Promise.all(Array.from({ length: 12 }, (_, i) => api(ctx.s2.jar, 'POST', url, { currentPassword: `wrong-pass-${i}`, newPassword: USER1.newPassword })));
  const wrongChecked = burst.filter((r) => r.status === 400).length;
  check('同時 12 回の誤ったパスワード変更: 検証されるのは上限の 5 回まで', wrongChecked <= 5 && burst.filter((r) => r.status === 429).length >= 7, `400=${wrongChecked} 429=${burst.filter((r) => r.status === 429).length} その他=${burst.filter((r) => r.status !== 400 && r.status !== 429).map((r) => r.status)}`);

  const changed = await api(ctx.s1.jar, 'POST', url, { currentPassword: USER1.password, newPassword: USER1.newPassword });
  check('正しい現在のパスワードで変更できる', changed.status === 200, `status=${changed.status} ${changed.text.slice(0, 160)}`);
  check('変更後、別端末のセッションは 401（失効）', (await api(other.jar, 'GET', '/api/dashboard/user')).status === 401);
  check('変更後、書き込み API も 401', (await api(other.jar, 'PATCH', '/api/dashboard/user', { display_name: 'x' })).status === 401);
  check('変更を行ったセッション自身も 401（画面は新しいパスワードで再ログインして引き継ぐ）', (await api(ctx.s1.jar, 'GET', '/api/dashboard/user')).status === 401);
  check('古いパスワードではログインできない', !(await signIn(USER1.email, USER1.password)).ok);
  const fresh = await signIn(USER1.email, USER1.newPassword);
  check('新しいパスワードでログインでき、API が使える', fresh.ok && (await api(fresh.jar, 'GET', '/api/dashboard/user')).status === 200);
  check('他のユーザー（USER2）のセッションは影響を受けない', (await api(ctx.s2.jar, 'GET', '/api/dashboard/user')).status === 200);
}

function runHttpApp() {
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      res.setHeader('Content-Type', 'text/plain; charset=utf-8');
      res.setHeader('X-E2E-Marker', RUN);
      res.end(`gridbase-e2e ${RUN} ${req.method} ${req.url} body=${Buffer.concat(chunks).toString('utf8')}`);
    });
  });
  return new Promise((resolve) => server.listen(0, '0.0.0.0', () => resolve(server)));
}

async function testTunnel(ctx) {
  section('トンネル疎通（ホストのアプリ -> client -> tunnel -> caddy）');
  const app = await runHttpApp();
  const port = app.address().port;
  const patched = await api(ctx.admin.jar, 'PATCH', '/api/dashboard/subdomains', { id: ctx.subdomainId, status: 'active', local_server_url: `http://host.docker.internal:${port}` });
  check('サブドメインの転送先を更新', patched.status === 200, `status=${patched.status}`);

  const containerName = `gridbase-e2e-client-${RUN}`;
  const target = { host: `${ctx.subdomain}.${BASE_DOMAIN}` };
  let child = null;
  let childOutput = '';
  const startClient = () => {
    child = spawn('docker', ['compose', 'run', '--rm', '--no-deps', '--name', containerName, '--entrypoint', 'gridbase-tunnel-client', 'tunnel', '--server', 'tunnel:9443', '--api_key', ctx.apiKey], { cwd: ROOT, stdio: ['ignore', 'pipe', 'pipe'] });
    childOutput = '';
    child.stdout.on('data', (d) => { childOutput += d; });
    child.stderr.on('data', (d) => { childOutput += d; });
  };

  try {
    // トンネルサーバーは Directus を 30 秒ごとに読み直す。未知の API キーで接続したクライアントは終了するので、起動し直す
    const deadline = Date.now() + 120000;
    let body = null;
    while (Date.now() < deadline) {
      if (!child || child.exitCode !== null) {
        spawnSync('docker', ['rm', '-f', containerName], { stdio: 'ignore' });
        startClient();
      }
      await sleep(4000);
      const res = await rawRequest({ host: target.host, path: '/hello?x=1' }).catch(() => null);
      if (res && res.status === 200) {
        body = res;
        break;
      }
    }
    check('トンネル経由で 200 が返る', body !== null, `client output:\n${childOutput.slice(-600)}`);
    check('本文がホストのアプリのもの（パスとクエリも届く）', Boolean(body) && body.text.includes(`gridbase-e2e ${RUN} GET /hello?x=1`), body?.text);
    check('応答ヘッダーが届く', body?.headers['x-e2e-marker'] === RUN);
    const post = await rawRequest({ host: target.host, path: '/submit', method: 'POST', headers: { 'Content-Type': 'text/plain' }, body: 'ping-body' });
    check('POST の本文も届く', post.status === 200 && post.text.includes('POST /submit body=ping-body'), `status=${post.status} ${post.text}`);

    // クライアントを止めると、サブドメインは既知のまま「未接続」(503) になる
    spawnSync('docker', ['rm', '-f', containerName], { stdio: 'ignore' });
    // 切断をサーバーが検知するまでの間は 502（死んだ接続への転送失敗）が返ることがある。503 になるまで待つ
    let gone = null;
    for (let i = 0; i < 20; i += 1) {
      gone = await rawRequest({ host: target.host, path: '/' }).catch(() => null);
      if (gone && gone.status === 503) break;
      await sleep(1500);
    }
    check('クライアント切断後は 503 tunnel client not connected', gone?.status === 503 && /not connected/.test(gone.text), `status=${gone?.status} ${gone?.text}`);
  } finally {
    if (child && child.exitCode === null) child.kill('SIGTERM');
    spawnSync('docker', ['rm', '-f', containerName], { stdio: 'ignore' });
    app.close();
  }
}

function testInit() {
  section('directus-init（冪等性と、公開構成での既定の秘密の拒否）');
  const again = compose(['run', '--rm', '--no-deps', 'directus-init']);
  check('2 回目の init は exit 0', again.status === 0, again.out.slice(-600));
  for (const marker of ['schema: 変更なし', 'policy gridbase-web: 変更なし', 'policy gridbase-tunnel: 変更なし', 'user gridbase-web@service.example.com: 変更なし', 'user gridbase-tunnel@service.example.com: 変更なし']) {
    check(`init 出力: ${marker}`, again.out.includes(marker), again.out.slice(-800));
  }
  check('init は何も作成・更新・削除しない', !/作成しました|適用します|更新しました|同期しました|削除しました|戻しました/.test(again.out), again.out.slice(-800));

  // .env の内容に関わらず「既定の値」を確実に作るため、NEXTAUTH_SECRET だけ明示的に仮の値で上書きする
  const weak = ['-e', 'NEXTAUTH_SECRET=change-me-e2e-weak'];
  const exposed = compose(['run', '--rm', '--no-deps', '-e', 'GRIDBASE_BIND_ADDRESS=0.0.0.0', ...weak, 'directus-init']);
  check('0.0.0.0 に公開して既定の秘密が残っていると init は exit 1', exposed.status === 1 && /公開構成/.test(exposed.out) && /NEXTAUTH_SECRET/.test(exposed.out), `status=${exposed.status}\n${exposed.out.slice(-600)}`);
  const https = compose(['run', '--rm', '--no-deps', '-e', 'GRIDBASE_SITE_SCHEME=https://', ...weak, 'directus-init']);
  check('https でも同様に exit 1', https.status === 1 && /公開構成/.test(https.out), `status=${https.status}\n${https.out.slice(-600)}`);
  const local = compose(['run', '--rm', '--no-deps', ...weak, 'directus-init']);
  check('ローカル（127.0.0.1・http）では警告のみで exit 0', local.status === 0 && /WARNING/.test(local.out), `status=${local.status}\n${local.out.slice(-600)}`);
}

async function main() {
  console.log(`gridbase e2e smoke (run ${RUN}) -> 127.0.0.1:${HTTP_PORT}, admin=${ADMIN_DOMAIN}, base=${BASE_DOMAIN}`);
  const ctx = {};
  try {
    await testAnonymous();
    await testAdminFlow(ctx);
    await testCsrf(ctx);
    await testPermissions(ctx);
    await testPasswordChange(ctx);
    await testTunnel(ctx);
    testInit();
  } catch (e) {
    failures.push(`中断: ${e.message}`);
    console.log(`  FAIL  中断: ${e.stack || e.message}`);
  }
  console.log(`\n${passed} passed, ${failures.length} failed`);
  if (failures.length) {
    for (const f of failures) console.log(`  - ${f}`);
    process.exit(1);
  }
}

main();
