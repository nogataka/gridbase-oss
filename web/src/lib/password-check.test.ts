// パスワードを持たないユーザー（null / 空）は、ダミーのハッシュの元の文字列を含むどんな入力でもログインできない。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import bcryptjs from 'bcryptjs';
import { verifyStoredPassword } from './password-check';

const DUMMY_PLAIN = 'gridbase-dummy-password';

test('パスワードが null / 空 / 文字列でないユーザーは、どんな入力でも false（ダミーの元の文字列でも）', async () => {
  const inputs = [DUMMY_PLAIN, '', 'x', 'Passw0rd-12345', ' ' + DUMMY_PLAIN, DUMMY_PLAIN + ' ', DUMMY_PLAIN.toUpperCase()];
  for (const stored of [null, undefined, '', 0, false, {}, []]) {
    for (const input of inputs) {
      assert.equal(await verifyStoredPassword(input, stored), false, `stored=${JSON.stringify(stored)} input=${JSON.stringify(input)}`);
    }
  }
});

test('パスワードを持たないユーザーでも時間を揃えるための bcrypt 比較は 1 回行う', async () => {
  const original = bcryptjs.compare;
  let calls = 0;
  (bcryptjs as { compare: unknown }).compare = (...args: Parameters<typeof original>) => {
    calls += 1;
    return original(...args);
  };
  try {
    assert.equal(await verifyStoredPassword(DUMMY_PLAIN, null), false);
    assert.equal(calls, 1);
    assert.equal(await verifyStoredPassword(DUMMY_PLAIN, ''), false);
    assert.equal(calls, 2);
  } finally {
    (bcryptjs as { compare: unknown }).compare = original;
  }
});

test('正しいハッシュを持つユーザーは、正しい入力だけ true', async () => {
  const hash = bcryptjs.hashSync('Correct-horse-9', 4);
  assert.equal(await verifyStoredPassword('Correct-horse-9', hash), true);
  assert.equal(await verifyStoredPassword('Correct-horse-8', hash), false);
  assert.equal(await verifyStoredPassword(DUMMY_PLAIN, hash), false);
  assert.equal(await verifyStoredPassword('', hash), false);
});

test('ハッシュに見えない文字列が保存されていても、その文字列そのものではログインできない', async () => {
  assert.equal(await verifyStoredPassword('not-a-hash', 'not-a-hash'), false);
});
