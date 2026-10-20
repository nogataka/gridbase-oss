import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseSubdomainWritable } from './subdomain-input';

test('HTTP: 許可リストの項目だけが返る（userId などは無視される）', () => {
  const r = parseSubdomainWritable({
    local_server_url: 'http://localhost:3000',
    userId: 'attacker',
    id: 'x',
    role: 'admin',
  });
  assert.equal(r.ok, true);
  if (r.ok) {
    assert.deepEqual(Object.keys(r.value).sort(), ['description', 'local_host', 'local_port', 'local_server_url', 'mode', 'status']);
    assert.equal(r.value.mode, 'http');
    assert.equal(r.value.status, 'active');
  }
});

test('TCP は未対応として拒否する', () => {
  const r = parseSubdomainWritable({ mode: 'tcp', local_host: '127.0.0.1', local_port: 22 });
  assert.equal(r.ok, false);
  if (!r.ok) assert.match(r.error, /TCP/);
});

test('不正な mode / status / URL を拒否する', () => {
  assert.equal(parseSubdomainWritable({ mode: 'udp', local_server_url: 'http://a' }).ok, false);
  assert.equal(parseSubdomainWritable({ status: 'banned', local_server_url: 'http://a' }).ok, false);
  assert.equal(parseSubdomainWritable({ local_server_url: 'ftp://a' }).ok, false);
  assert.equal(parseSubdomainWritable({ local_server_url: 'javascript:alert(1)' }).ok, false);
  assert.equal(parseSubdomainWritable({}).ok, false);
  assert.equal(parseSubdomainWritable(null).ok, false);
  assert.equal(parseSubdomainWritable('x').ok, false);
});
