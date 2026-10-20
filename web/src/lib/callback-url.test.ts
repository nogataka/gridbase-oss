import { test } from 'node:test';
import assert from 'node:assert/strict';
import { safeCallbackPath } from './callback-url';

const ORIGIN = 'https://admin.example.org';
const safe = (v: string | null | undefined) => safeCallbackPath(v, ORIGIN);

test('同一オリジンのパスは通す（クエリ・ハッシュも保つ）', () => {
  assert.equal(safe('/dashboard'), '/dashboard');
  assert.equal(safe('/admin?q=a%20b#top'), '/admin?q=a%20b#top');
  assert.equal(safe('/'), '/');
});

test('同一オリジンの絶対 URL はパスに直す', () => {
  assert.equal(safe('https://admin.example.org/dashboard?x=1'), '/dashboard?x=1');
});

test('空・未指定は fallback', () => {
  assert.equal(safe(null), '/dashboard');
  assert.equal(safe(undefined), '/dashboard');
  assert.equal(safe(''), '/dashboard');
});

test('オープンリダイレクト: 外部 URL・プロトコル相対・バックスラッシュ', () => {
  const attacks = [
    'https://evil.com',
    'https://evil.com/dashboard',
    'http://admin.example.org/dashboard', // スキームが違えばオリジンも違う
    'https://admin.example.org.evil.com/',
    'https://admin.example.org@evil.com/',
    '//evil.com',
    '//evil.com/x',
    '/\\evil.com', // searchParams.get('callbackUrl') が /%5Cevil.com をデコードした形
    '/\\/evil.com',
    '\\\\evil.com',
    '/\t/evil.com', // タブ・改行は URL パーサーが除去する
    '/\n/evil.com',
    '/\r/evil.com',
    '/..//evil.com', // 正規化後に // で始まる
    '/.//evil.com',
    '/a/..//evil.com',
    'javascript:alert(1)',
    'JaVaScRiPt:alert(1)',
    'data:text/html,<script>alert(1)</script>',
    'vbscript:x',
    '  //evil.com',
    ' https://evil.com',
  ];
  for (const attack of attacks) {
    assert.equal(safe(attack), '/dashboard', `通ってしまった: ${JSON.stringify(attack)}`);
  }
});

test('結果は常に同一オリジンで、// やバックスラッシュで始まらない', () => {
  // '\\evil.com'（先頭が単独のバックスラッシュ）は同一オリジンの /evil.com に解決される＝安全
  const inputs = ['/x', '\\evil.com', '/%5Cevil.com', '/%2F%2Fevil.com', '/..%2F%2Fevil.com', '/a?next=//evil.com', '/\\x'];
  for (const input of inputs) {
    const out = safe(input);
    assert.ok(out.startsWith('/'), out);
    assert.ok(!out.startsWith('//') && !out.startsWith('/\\'), out);
    assert.equal(new URL(out, ORIGIN).origin, ORIGIN, out);
  }
});

test('fallback を指定できる', () => {
  assert.equal(safeCallbackPath('//evil.com', ORIGIN, '/signin'), '/signin');
});

test('origin が不正でも例外にしない', () => {
  assert.equal(safeCallbackPath('/x', 'not a url'), '/dashboard');
});
