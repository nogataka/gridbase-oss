// directus-init（secrets-check.mjs）と web（web/scripts/deployment-check.mjs）は、別々のイメージにビルドされるため
// 「公開構成の判定」と「既定のパスワードの判定」を同じロジックで 2 か所に持つ。食い違うと、init は通すのに
// web が止まる（またはその逆）ので、同じ入力で同じ答えを返すことをここで固定する。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as init from './secrets-check.mjs';
import * as web from '../../web/scripts/deployment-check.mjs';

const envs = [
  {},
  { GRIDBASE_BIND_ADDRESS: '127.0.0.1' },
  { GRIDBASE_BIND_ADDRESS: '0.0.0.0' },
  { GRIDBASE_BIND_ADDRESS: '::1' },
  { GRIDBASE_BIND_ADDRESS: '192.168.1.10' },
  { GRIDBASE_SITE_SCHEME: 'https://' },
  { GRIDBASE_SITE_SCHEME: 'http://' },
  { GRIDBASE_ADMIN_URL: 'https://admin.example.org' },
  { GRIDBASE_ADMIN_URL: 'http://admin.example.org' },
  { GRIDBASE_ADMIN_URL: 'http://admin.gridbase.localhost:8080' },
  { GRIDBASE_ADMIN_URL: 'http://127.0.0.1:8080' },
  { GRIDBASE_ADMIN_URL: 'http://[::1]:8080' },
  { GRIDBASE_ADMIN_URL: 'not a url' },
  { GRIDBASE_BASE_DOMAIN: 'gridbase.localhost', GRIDBASE_ADMIN_DOMAIN: 'admin.gridbase.localhost' },
  { GRIDBASE_BASE_DOMAIN: 'localhost' },
  { GRIDBASE_BASE_DOMAIN: 'localhost:8080' },
  { GRIDBASE_BASE_DOMAIN: 'app.test' },
  { GRIDBASE_BASE_DOMAIN: 'svc.internal' },
  { GRIDBASE_BASE_DOMAIN: 'tunnel.example.org' },
  { GRIDBASE_BASE_DOMAIN: 'localhost.evil.com' },
  { GRIDBASE_BASE_DOMAIN: 'evillocalhost' },
  { GRIDBASE_BASE_DOMAIN: 'x.localhost.example.org' },
  { GRIDBASE_BASE_DOMAIN: 'internal' },
  { GRIDBASE_BASE_DOMAIN: 'TEST' },
  { GRIDBASE_BASE_DOMAIN: 'FOO.LOCALHOST.' },
  { GRIDBASE_BASE_DOMAIN: 'a b.localhost' },
  { GRIDBASE_BASE_DOMAIN: 'a/b.localhost' },
  { GRIDBASE_BASE_DOMAIN: '[::1]' },
  { GRIDBASE_BASE_DOMAIN: '127.0.0.1' },
  { GRIDBASE_ADMIN_DOMAIN: 'admin.example.org' },
  { GRIDBASE_ADMIN_DOMAIN: 'admin.gridbase.localhost' },
  { GRIDBASE_BIND_ADDRESS: '127.0.0.1', GRIDBASE_SITE_SCHEME: 'http://', GRIDBASE_BASE_DOMAIN: 'example.org' },
];

test('公開構成の判定は init と web で同じ', () => {
  for (const env of envs) {
    assert.deepEqual(init.publicDeploymentReasons(env), web.publicDeploymentReasons(env), JSON.stringify(env));
  }
});

test('ローカル用ドメインの判定は init と web で同じ', () => {
  for (const host of ['localhost', 'a.localhost', 'a.test', 'a.internal', 'a.example.org', 'localhost.evil.com', '', ' ', 'a b', '127.0.0.1', 'test', 'internal', 'A.LOCALHOST.']) {
    assert.equal(init.isLocalOnlyHostname(host), web.isLocalOnlyHostname(host), JSON.stringify(host));
  }
});

test('既定のパスワードの判定と一覧は init と web で同じ', () => {
  assert.deepEqual(init.KNOWN_DEFAULT_PASSWORDS, web.KNOWN_DEFAULT_PASSWORDS);
  assert.equal(String(init.PLACEHOLDER_PATTERN), String(web.PLACEHOLDER_PATTERN));
  assert.deepEqual([...init.LOCAL_BIND_ADDRESSES].sort(), [...web.LOCAL_BIND_ADDRESSES].sort());
  for (const value of ['ChangeMe-local-1234', 'change-me-admin-password', 'change-me', 'local-dev-x', 'replace-with-x', 'LOCAL-DEV-X', 'Strong-Passw0rd-42', '', undefined, null]) {
    assert.equal(init.isDefaultPassword(value), web.isDefaultPassword(value), String(value));
  }
});

test('既定の管理者メールの一覧と検査対象の決め方は init と web で同じ', () => {
  assert.deepEqual(init.KNOWN_DEFAULT_ADMIN_EMAILS, web.KNOWN_DEFAULT_ADMIN_EMAILS);
  for (const env of [{}, { GRIDBASE_ADMIN_EMAIL: '' }, { GRIDBASE_ADMIN_EMAIL: ' Boss@Example.org ' }, { GRIDBASE_ADMIN_EMAIL: 'admin@gridbase.localhost' }]) {
    assert.deepEqual(init.defaultAdminEmails(env), web.defaultAdminEmails(env), JSON.stringify(env));
  }
});
