// 保存済みの管理者のパスワード検査（M1-R）。node --test directus/init で実行する（bcryptjs が要る: npm ci）。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import bcryptjs from 'bcryptjs';
import { CHECK_FIELDS, defaultPasswordCandidates, evaluateStoredAdmins, fetchCheckTargets, inspectAdminPasswords } from './admin-check.mjs';
import { parseEnvText } from './secrets-check.mjs';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const compare = (plain, hash) => bcryptjs.compare(plain, hash);
const hashOf = (plain) => bcryptjs.hashSync(plain, 4);
const knownDefaults = parseEnvText(readFileSync(fileURLToPath(new URL('../../.env.example', import.meta.url)), 'utf8'));
const DEFAULT_PASSWORD = knownDefaults.get('GRIDBASE_ADMIN_PASSWORD');

const row = (password, email = 'admin@example.org') => ({ userId: 'u', email, password });
const PUBLIC = { GRIDBASE_BIND_ADDRESS: '0.0.0.0' };
const LOCAL = { GRIDBASE_BIND_ADDRESS: '127.0.0.1' };
const evaluate = (env, fetchAdmins) => evaluateStoredAdmins({ env, fetchAdmins, compare, knownDefaults });

test('.env.example の GRIDBASE_ADMIN_PASSWORD は既定の候補に含まれる', () => {
  assert.ok(DEFAULT_PASSWORD, '.env.example に GRIDBASE_ADMIN_PASSWORD が無い');
  assert.ok(defaultPasswordCandidates({}, knownDefaults).includes(DEFAULT_PASSWORD));
  // .env.example を読めなくても、README に載っている値は固定で入っている
  assert.ok(defaultPasswordCandidates({}, new Map()).includes('ChangeMe-local-1234'));
});

test('いまの GRIDBASE_ADMIN_PASSWORD が仮の値なら候補に加わり、強い値は加わらない', () => {
  assert.ok(defaultPasswordCandidates({ GRIDBASE_ADMIN_PASSWORD: 'change-me-custom' }, new Map()).includes('change-me-custom'));
  assert.ok(!defaultPasswordCandidates({ GRIDBASE_ADMIN_PASSWORD: 'Zx9-unique-admin-pass' }, new Map()).includes('Zx9-unique-admin-pass'));
});

test('公開構成・GRIDBASE_ADMIN_PASSWORD 未設定でも、DB の管理者が既定パスワードなら問題として返す', async () => {
  const r = await evaluate({ ...PUBLIC }, async () => [row(hashOf(DEFAULT_PASSWORD))]);
  assert.equal(r.isPublic, true);
  assert.equal(r.lines.length, 1);
  assert.match(r.lines[0], /admin@example\.org.*既定のまま/);
});

test('公開構成・GRIDBASE_ADMIN_PASSWORD が弱い値でも、結果は DB のハッシュだけで決まる（既定なら問題）', async () => {
  const r = await evaluate({ ...PUBLIC, GRIDBASE_ADMIN_PASSWORD: 'weak' }, async () => [row(hashOf(DEFAULT_PASSWORD))]);
  assert.equal(r.lines.length, 1);
});

test('README・.env.example 以外の既知の既定値（web/.env.example）も検出する', async () => {
  const r = await evaluate({ ...PUBLIC }, async () => [row(hashOf('change-me-admin-password'))]);
  assert.equal(r.lines.length, 1);
});

test('いまの .env が仮の値（change-me-…）で作られた管理者も検出する', async () => {
  const r = await evaluate({ ...PUBLIC, GRIDBASE_ADMIN_PASSWORD: 'change-me-custom-1' }, async () => [row(hashOf('change-me-custom-1'))]);
  assert.equal(r.lines.length, 1);
});

test('既定でないパスワード（変更済み）なら問題なし。検査した人数を返す', async () => {
  const r = await evaluate({ ...PUBLIC }, async () => [row(hashOf('Changed-by-the-owner-77'), 'a@example.org'), row(hashOf('Another-strong-pass-1'), 'b@example.org')]);
  assert.deepEqual(r.lines, []);
  assert.equal(r.checked, 2);
});

test('管理者が 1 人もいなければ問題なし', async () => {
  const r = await evaluate({ ...PUBLIC }, async () => []);
  assert.deepEqual(r.lines, []);
  assert.equal(r.checked, 0);
});

test('複数の管理者のうち 1 人でも既定なら、その人だけを挙げる', async () => {
  const r = await evaluate({ ...PUBLIC }, async () => [row(hashOf('Changed-by-the-owner-77'), 'ok@example.org'), row(hashOf(DEFAULT_PASSWORD), 'bad@example.org')]);
  assert.equal(r.lines.length, 1);
  assert.match(r.lines[0], /^bad@example\.org/);
});

test('Directus に到達できない・権限がない: 「問題なし」にせず lines に入れ、checked は null（fail-closed）', async () => {
  const unreachable = await evaluate({ ...PUBLIC }, async () => {
    throw new Error('fetch failed');
  });
  assert.equal(unreachable.checked, null);
  assert.equal(unreachable.lines.length, 1);
  assert.match(unreachable.lines[0], /取得できませんでした/);

  const malformed = await evaluate({ ...PUBLIC }, async () => ({ not: 'an array' }));
  assert.equal(malformed.checked, null);
  assert.equal(malformed.lines.length, 1);
});

test('パスワード欄が返らない・ハッシュに見えない・比較が例外になる: 検査できないものとして挙げる', async () => {
  const r = await inspectAdminPasswords(
    [{ userId: 'u', email: 'noField@example.org' }, row('**********', 'masked@example.org'), row(42, 'number@example.org'), row(hashOf('Changed-by-the-owner-77'), 'throws@example.org')],
    ['ChangeMe-local-1234'],
    async () => {
      throw new Error('boom');
    },
  );
  assert.deepEqual(r.defaults, []);
  assert.deepEqual(r.unverifiable.sort(), ['masked@example.org', 'noField@example.org', 'number@example.org', 'throws@example.org']);
});

// 前提: web の認証は、保存済みのパスワードが null / 空のユーザーを入力に関わらず拒否する
// （web/src/lib/password-check.ts。web/src/lib/password-check.test.ts が固定している）。だから検査対象外にできる。
test('パスワードを持たない管理者（null / 空）は、web の認証が必ず拒否するので問題にしない', async () => {
  const r = await evaluate({ ...PUBLIC }, async () => [row(null, 'sso@example.org'), row('', 'empty@example.org')]);
  assert.deepEqual(r.lines, []);
});

test('ローカル構成でも結果は同じ（扱いを決めるのは呼び出し側。isPublic=false）', async () => {
  const r = await evaluate({ ...LOCAL }, async () => [row(hashOf(DEFAULT_PASSWORD))]);
  assert.equal(r.isPublic, false);
  assert.equal(r.lines.length, 1);
});

test('A-1: bind が 127.0.0.1 でも実ドメインなら isPublic', async () => {
  const r = await evaluate({ ...LOCAL, GRIDBASE_BASE_DOMAIN: 'tunnel.example.org' }, async () => []);
  assert.equal(r.isPublic, true);
});

// ---- S-2: 検査の対象 -----------------------------------------------------------------

/** users テーブルの代わり。select(params) が filter に応じた行を返す。 */
const fakeSelect = (users) => async (params) => {
  if (params['filter[role][_eq]']) return users.filter((u) => u.role === params['filter[role][_eq]']);
  if (params['filter[email][_in]']) {
    const emails = params['filter[email][_in]'].split(',');
    return users.filter((u) => emails.includes(u.email));
  }
  return [];
};
const user = (email, role, status, password) => ({ userId: `id-${email}`, email, role, status, password });

test('S-2: 無効（inactive）な管理者も検査対象。既定のパスワードなら問題にする', async () => {
  const users = [user('old@example.org', 'admin', 'inactive', hashOf(DEFAULT_PASSWORD))];
  const r = await evaluate({ ...PUBLIC }, () => fetchCheckTargets({ ...PUBLIC }, fakeSelect(users)));
  assert.equal(r.lines.length, 1);
  assert.match(r.lines[0], /^old@example\.org/);
});

test('S-2: 既定の管理者メールのユーザーは、role=user に降格・無効化されていても検査対象', async () => {
  const users = [
    user('admin@gridbase.localhost', 'user', 'active', hashOf(DEFAULT_PASSWORD)),
    user('admin@example.com', 'user', 'inactive', hashOf('change-me-admin-password')),
  ];
  const r = await evaluate({ ...PUBLIC }, () => fetchCheckTargets({ ...PUBLIC }, fakeSelect(users)));
  assert.equal(r.lines.length, 2);
  assert.match(r.lines.join('\n'), /admin@gridbase\.localhost/);
  assert.match(r.lines.join('\n'), /admin@example\.com/);
});

test('S-2: いまの GRIDBASE_ADMIN_EMAIL のユーザーも、降格後に検査対象になる', async () => {
  const env = { ...PUBLIC, GRIDBASE_ADMIN_EMAIL: 'Boss@Example.org' };
  const users = [user('boss@example.org', 'user', 'active', hashOf(DEFAULT_PASSWORD))];
  const r = await evaluate(env, () => fetchCheckTargets(env, fakeSelect(users)));
  assert.equal(r.lines.length, 1);
});

test('S-2: 既定でないメールの一般ユーザーは対象外（遅くならない）。管理者かつ既定メールの人は 1 回だけ数える', async () => {
  const users = [
    user('someone@example.org', 'user', 'active', hashOf(DEFAULT_PASSWORD)),
    user('admin@gridbase.localhost', 'admin', 'active', hashOf(DEFAULT_PASSWORD)),
  ];
  const targets = await fetchCheckTargets({ ...PUBLIC }, fakeSelect(users));
  assert.deepEqual(targets.map((t) => t.email), ['admin@gridbase.localhost']);
  const r = await evaluate({ ...PUBLIC }, async () => targets);
  assert.equal(r.lines.length, 1);
});

test('S-2: 検査対象の取得に失敗・形式が想定外なら、fail-closed（取得できなかった）', async () => {
  const r1 = await evaluate({ ...PUBLIC }, () => fetchCheckTargets({ ...PUBLIC }, async () => ({ not: 'an array' })));
  assert.equal(r1.checked, null);
  const r2 = await evaluate({ ...PUBLIC }, () =>
    fetchCheckTargets({ ...PUBLIC }, async () => {
      throw new Error('403');
    }),
  );
  assert.equal(r2.checked, null);
});

test('S-2: 取得するフィールドに role と status がある', () => {
  assert.match(CHECK_FIELDS, /\bpassword\b/);
  assert.match(CHECK_FIELDS, /\brole\b/);
  assert.match(CHECK_FIELDS, /\bstatus\b/);
});
