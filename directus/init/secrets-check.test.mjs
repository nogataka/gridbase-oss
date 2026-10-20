// node --test directus/init で実行する。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { checkSecrets, parseEnvText, placeholderReason, publicDeploymentReasons } from './secrets-check.mjs';

const examplePath = fileURLToPath(new URL('../../.env.example', import.meta.url));
const exampleText = readFileSync(examplePath, 'utf8');
const defaults = parseEnvText(exampleText);

// ランダム相当の強い値（テスト用。実在の秘密ではない）
const strong = (label) => `t-${label}-9f3a7c1d5b2e8a4f6c0d`;
const strongEnv = () => ({
  DIRECTUS_KEY: strong('key'),
  DIRECTUS_SECRET: strong('secret'),
  DIRECTUS_ADMIN_PASSWORD: strong('adminpw'),
  DIRECTUS_ADMIN_TOKEN: strong('admintoken'),
  GRIDBASE_WEB_DIRECTUS_TOKEN: strong('web'),
  GRIDBASE_TUNNEL_DIRECTUS_TOKEN: strong('tunnel'),
  NEXTAUTH_SECRET: strong('nextauth'),
  GRIDBASE_ADMIN_PASSWORD: 'Zx9-unique-admin-pass',
  GRIDBASE_TUNNEL_ADMIN_TOKEN: strong('tunneladmin'),
});

test('.env.example を解析できる', () => {
  assert.ok(defaults.get('DIRECTUS_KEY'));
  assert.equal(defaults.get('GRIDBASE_BIND_ADDRESS'), '127.0.0.1');
});

test('.env.example の値はすべて「既定のまま」と判定される（コピーしたまま公開できない）', () => {
  const names = [
    'DIRECTUS_KEY', 'DIRECTUS_SECRET', 'DIRECTUS_ADMIN_PASSWORD', 'DIRECTUS_ADMIN_TOKEN',
    'GRIDBASE_WEB_DIRECTUS_TOKEN', 'GRIDBASE_TUNNEL_DIRECTUS_TOKEN', 'NEXTAUTH_SECRET',
    'GRIDBASE_ADMIN_PASSWORD', 'GRIDBASE_TUNNEL_ADMIN_TOKEN', 'POSTGRES_PASSWORD',
  ];
  for (const name of names) {
    assert.ok(placeholderReason(name, defaults.get(name), defaults), `${name} が検出されない`);
    // .env.example が無い環境でも、接頭辞で検出できる
    assert.ok(placeholderReason(name, defaults.get(name), new Map()), `${name} が接頭辞で検出されない`);
  }
});

test('公開構成の判定: bind / https', () => {
  assert.deepEqual(publicDeploymentReasons({}), []);
  assert.deepEqual(publicDeploymentReasons({ GRIDBASE_BIND_ADDRESS: '127.0.0.1' }), []);
  assert.equal(publicDeploymentReasons({ GRIDBASE_BIND_ADDRESS: '0.0.0.0' }).length, 1);
  assert.equal(publicDeploymentReasons({ GRIDBASE_BIND_ADDRESS: '192.168.1.10' }).length, 1);
  assert.equal(publicDeploymentReasons({ GRIDBASE_SITE_SCHEME: 'https://' }).length, 1);
  assert.equal(publicDeploymentReasons({ GRIDBASE_SITE_SCHEME: 'http://' }).length, 0);
  // https の管理画面 URL は、https であることと実ドメインであることの 2 つが理由になる
  const httpsAdmin = publicDeploymentReasons({ GRIDBASE_ADMIN_URL: 'https://admin.example.org' });
  assert.ok(httpsAdmin.some((r) => r.includes('https://')), httpsAdmin.join('\n'));
  assert.equal(publicDeploymentReasons({ GRIDBASE_ADMIN_URL: 'https://admin.gridbase.localhost' }).length, 1);
});

test('bind が 127.0.0.1・http でも、localhost 系以外のドメインなら公開構成', () => {
  const reasons = (env) => publicDeploymentReasons(env).length;
  for (const local of ['localhost', 'gridbase.localhost', 'admin.gridbase.localhost', 'app.test', 'svc.internal', 'gridbase.localhost:8080', 'FOO.LOCALHOST']) {
    assert.equal(reasons({ GRIDBASE_BASE_DOMAIN: local }), 0, `BASE_DOMAIN=${local}`);
    assert.equal(reasons({ GRIDBASE_ADMIN_DOMAIN: local }), 0, `ADMIN_DOMAIN=${local}`);
  }
  for (const real of ['example.org', 'tunnel.example.org', 'localhost.evil.com', 'evillocalhost', 'internal', 'test', '127.0.0.1', 'a b.localhost', '[::1]']) {
    assert.equal(reasons({ GRIDBASE_BASE_DOMAIN: real }), 1, `BASE_DOMAIN=${real}`);
    assert.equal(reasons({ GRIDBASE_ADMIN_DOMAIN: real }), 1, `ADMIN_DOMAIN=${real}`);
  }
  // http の管理画面 URL でも、実ドメインなら公開構成（ホスト側の nginx から公開する構成）
  assert.equal(reasons({ GRIDBASE_ADMIN_URL: 'http://admin.example.org' }), 1);
  assert.equal(reasons({ GRIDBASE_ADMIN_URL: 'http://admin.gridbase.localhost:8080' }), 0);
  assert.equal(reasons({ GRIDBASE_ADMIN_URL: 'http://127.0.0.1:8080' }), 0);
  assert.equal(reasons({ GRIDBASE_ADMIN_URL: 'garbage' }), 1);
});

test('既定の秘密のまま、bind が 127.0.0.1 でも実ドメインなら起動を止める側（isPublic）になる', () => {
  const env = { ...Object.fromEntries(defaults), GRIDBASE_BASE_DOMAIN: 'tunnel.example.org' };
  const result = checkSecrets(env, defaults);
  assert.equal(result.isPublic, true);
  assert.ok(result.problems.length >= 9);
});

test('ローカル構成で既定値のまま: 問題は列挙されるが isPublic は false', () => {
  const env = Object.fromEntries(defaults);
  const result = checkSecrets(env, defaults);
  assert.equal(result.isPublic, false);
  assert.ok(result.problems.length >= 9, result.problems.join('\n'));
});

test('公開構成（0.0.0.0）で .env.example のまま: 全秘密が問題として挙がる', () => {
  const env = { ...Object.fromEntries(defaults), GRIDBASE_BIND_ADDRESS: '0.0.0.0' };
  const result = checkSecrets(env, defaults);
  assert.equal(result.isPublic, true);
  for (const name of ['DIRECTUS_KEY', 'DIRECTUS_SECRET', 'DIRECTUS_ADMIN_PASSWORD', 'DIRECTUS_ADMIN_TOKEN', 'NEXTAUTH_SECRET', 'GRIDBASE_ADMIN_PASSWORD', 'GRIDBASE_WEB_DIRECTUS_TOKEN', 'GRIDBASE_TUNNEL_DIRECTUS_TOKEN', 'GRIDBASE_TUNNEL_ADMIN_TOKEN']) {
    assert.ok(result.problems.some((p) => p.startsWith(name)), `${name} が挙がらない`);
  }
});

test('公開構成（https）でも同様に検出される', () => {
  const env = { ...Object.fromEntries(defaults), GRIDBASE_SITE_SCHEME: 'https://' };
  assert.equal(checkSecrets(env, defaults).isPublic, true);
});

test('強い値だけなら公開構成でも問題なし', () => {
  const env = { ...strongEnv(), GRIDBASE_BIND_ADDRESS: '0.0.0.0', GRIDBASE_SITE_SCHEME: 'https://' };
  assert.deepEqual(checkSecrets(env, defaults).problems, []);
});

test('任意の秘密は空でよいが、値があれば検査する', () => {
  const base = { ...strongEnv(), GRIDBASE_BIND_ADDRESS: '0.0.0.0' };
  assert.deepEqual(checkSecrets({ ...base, GRIDBASE_TUNNEL_ADMIN_TOKEN: '', GRIDBASE_ADMIN_PASSWORD: '' }, defaults).problems, []);
  const bad = checkSecrets({ ...base, GRIDBASE_TUNNEL_ADMIN_TOKEN: 'local-dev-x' }, defaults);
  assert.equal(bad.problems.length, 1);
});

test('公開構成では短い値と使い回しを止める', () => {
  const base = { ...strongEnv(), GRIDBASE_BIND_ADDRESS: '0.0.0.0' };
  assert.ok(checkSecrets({ ...base, NEXTAUTH_SECRET: 'short' }, defaults).problems.some((p) => p.startsWith('NEXTAUTH_SECRET')));
  const reused = checkSecrets({ ...base, GRIDBASE_WEB_DIRECTUS_TOKEN: base.DIRECTUS_ADMIN_TOKEN }, defaults);
  assert.ok(reused.problems.some((p) => p.includes('同じ値')));
});

test('必須の秘密が空なら問題', () => {
  const env = { ...strongEnv(), DIRECTUS_KEY: '' };
  assert.ok(checkSecrets(env, defaults).problems.some((p) => p.startsWith('DIRECTUS_KEY')));
});

test('parseEnvText: コメントと引用符', () => {
  const m = parseEnvText('# c\nA=1\nB="x y"\nC=z # tail\n\nD=\n');
  assert.equal(m.get('A'), '1');
  assert.equal(m.get('B'), 'x y');
  assert.equal(m.get('C'), 'z');
  assert.equal(m.get('D'), '');
});
