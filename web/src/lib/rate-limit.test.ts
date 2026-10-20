import { test } from 'node:test';
import assert from 'node:assert/strict';
import { attemptWithLimit, hitRateLimit, resetRateLimit } from './rate-limit';

test('hitRateLimit は limit 回までは false、超えたら true', () => {
  const key = `t1:${Math.random()}`;
  for (let i = 0; i < 3; i++) assert.equal(hitRateLimit(key, 3, 60_000), false);
  assert.equal(hitRateLimit(key, 3, 60_000), true);
});

test('resetRateLimit は数を戻す', () => {
  const key = `t2:${Math.random()}`;
  hitRateLimit(key, 1, 60_000);
  assert.equal(hitRateLimit(key, 1, 60_000), true);
  resetRateLimit(key);
  assert.equal(hitRateLimit(key, 1, 60_000), false);
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

test('A-2: 同時に届いた試行でも、検証が走る回数は上限を超えない', async () => {
  const key = `t3:${Math.random()}`;
  let verified = 0;
  const attempt = async () => {
    verified += 1;
    await sleep(20); // bcrypt に相当する非同期の待ち。この間に他の試行が届く
    return null; // 失敗
  };
  const results = await Promise.all(Array.from({ length: 50 }, () => attemptWithLimit(key, 5, 60_000, attempt)));
  assert.equal(verified, 5);
  assert.equal(results.filter((r) => r.limited).length, 45);
  assert.equal(results.filter((r) => !r.limited).length, 5);
});

test('A-2: 検証より先に数えるので、限度に達したあとは正しい値でも検証されない', async () => {
  const key = `t4:${Math.random()}`;
  const wrong = async () => null;
  for (let i = 0; i < 3; i++) assert.deepEqual(await attemptWithLimit(key, 3, 60_000, wrong), { limited: false, value: null });
  let ran = false;
  const right = await attemptWithLimit(key, 3, 60_000, async () => {
    ran = true;
    return 'ok';
  });
  assert.deepEqual(right, { limited: true });
  assert.equal(ran, false);
});

test('A-2: 成功すると数が戻る（連続ログインで上限に達しない）。失敗は残る', async () => {
  const key = `t5:${Math.random()}`;
  for (let i = 0; i < 20; i++) {
    assert.deepEqual(await attemptWithLimit(key, 3, 60_000, async () => 'ok'), { limited: false, value: 'ok' });
  }
  await attemptWithLimit(key, 3, 60_000, async () => null);
  await attemptWithLimit(key, 3, 60_000, async () => null);
  assert.deepEqual(await attemptWithLimit(key, 3, 60_000, async () => null), { limited: false, value: null });
  assert.deepEqual(await attemptWithLimit(key, 3, 60_000, async () => 'ok'), { limited: true });
});

test('A-2: 検証が例外を投げても数えたまま残る（例外で上限を回避できない）', async () => {
  const key = `t6:${Math.random()}`;
  for (let i = 0; i < 2; i++) {
    await assert.rejects(attemptWithLimit(key, 2, 60_000, async () => { throw new Error('db down'); }));
  }
  assert.deepEqual(await attemptWithLimit(key, 2, 60_000, async () => 'ok'), { limited: true });
});
