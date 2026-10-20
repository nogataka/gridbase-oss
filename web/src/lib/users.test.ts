import { test } from 'node:test';
import assert from 'node:assert/strict';
import { toEpoch, validatePassword, RECORD_ID_PATTERN } from './users';

test('toEpoch: 空・不正は 0、表記ゆれは同じ値になる', () => {
  assert.equal(toEpoch(null), 0);
  assert.equal(toEpoch(undefined), 0);
  assert.equal(toEpoch(''), 0);
  assert.equal(toEpoch('not a date'), 0);
  assert.equal(toEpoch(12345), 0);
  assert.equal(toEpoch('2026-10-20T10:00:00.000Z'), toEpoch('2026-10-20T10:00:00Z'));
  assert.ok(toEpoch('2026-10-20T10:00:01Z') > toEpoch('2026-10-20T10:00:00Z'));
});

test('validatePassword: サインアップ・パスワード変更・初回管理者で共通の規則', () => {
  assert.equal(validatePassword('abcd1234'), null);
  assert.ok(validatePassword('short1'));
  assert.ok(validatePassword('abcdefgh'));
  assert.ok(validatePassword('12345678'));
  assert.ok(validatePassword('a1'.repeat(101)));
  assert.ok(validatePassword(undefined));
});

test('RECORD_ID_PATTERN', () => {
  assert.ok(RECORD_ID_PATTERN.test('3f2a-9b'));
  assert.ok(!RECORD_ID_PATTERN.test('a/b'));
  assert.ok(!RECORD_ID_PATTERN.test(''));
});
