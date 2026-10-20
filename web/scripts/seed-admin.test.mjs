// seed-admin.mjs の公開構成での振る舞い（M1-R / A-1）。
// 偽の Directus（node:http）を立て、seed-admin を子プロセスで実行して終了コードを見る。
// 実行: npm test（tsx --test が scripts/*.test.mjs も拾う）
import { test } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import bcryptjs from 'bcryptjs';

const script = fileURLToPath(new URL('./seed-admin.mjs', import.meta.url));

const DEFAULT_PASSWORD = 'ChangeMe-local-1234'; // .env.example の既定
const hashOf = (plain) => bcryptjs.hashSync(plain, 4);
const adminRow = (password) => ({ userId: 'u1', email: 'admin@example.org', status: 'active', password });

/** 偽の Directus。admins は「role=admin の users」、others は管理者以外の users（email の _in で引ける）。requests に受けた URL を残す。 */
async function startFakeDirectus(admins, others = []) {
  const requests = [];
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://x');
    requests.push(`${req.method} ${url.pathname}${url.search}`);
    res.setHeader('Content-Type', 'application/json');
    if (req.method === 'GET' && url.pathname === '/items/users') {
      const emailIn = url.searchParams.get('filter[email][_in]');
      const data =
        url.searchParams.get('filter[role][_eq]') === 'admin'
          ? admins
          : emailIn
            ? [...admins, ...others].filter((u) => emailIn.split(',').includes(u.email))
            : [];
      res.end(JSON.stringify({ data }));
      return;
    }
    res.statusCode = 404;
    res.end('{}');
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  return {
    url: `http://127.0.0.1:${server.address().port}`,
    requests,
    close: () => new Promise((resolve) => server.close(resolve)),
  };
}

function runSeed(env) {
  return new Promise((resolve) => {
    // 呼び出し元の環境（CI の GRIDBASE_* など）を引き継がない
    const child = spawn(process.execPath, [script], {
      env: { PATH: process.env.PATH, SEED_WAIT_SECONDS: '1', ...env },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let out = '';
    child.stdout.on('data', (d) => (out += d));
    child.stderr.on('data', (d) => (out += d));
    child.on('close', (status) => resolve({ status, out }));
  });
}

const publicBind = { GRIDBASE_BIND_ADDRESS: '0.0.0.0' };
// 手元の検証で使う値（既定の構成）
const localEnv = { GRIDBASE_BIND_ADDRESS: '127.0.0.1', GRIDBASE_SITE_SCHEME: 'http://', NEXTAUTH_URL: 'http://admin.gridbase.localhost' };

async function withDirectus(admins, fn, others = []) {
  const fake = await startFakeDirectus(admins, others);
  try {
    return await fn(fake, { DIRECTUS_URL: fake.url, DIRECTUS_TOKEN: 'test-token-not-a-secret' });
  } finally {
    await fake.close();
  }
}

test('M1-R: 公開構成・GRIDBASE_ADMIN_PASSWORD 未設定でも、DB の管理者が既定パスワードなら exit 3', async () => {
  await withDirectus([adminRow(hashOf(DEFAULT_PASSWORD))], async (fake, conn) => {
    const r = await runSeed({ ...conn, ...publicBind });
    assert.equal(r.status, 3, r.out);
    assert.match(r.out, /既定のまま/);
    assert.ok(fake.requests.some((q) => decodeURIComponent(q).includes('filter[role][_eq]=admin')), '管理者を問い合わせていない');
  });
});

test('M1-R: 公開構成・GRIDBASE_ADMIN_PASSWORD が強度不足でも、DB が既定パスワードなら exit 3（exit 1 で web が続行しない）', async () => {
  await withDirectus([adminRow(hashOf(DEFAULT_PASSWORD))], async (_fake, conn) => {
    const r = await runSeed({ ...conn, ...publicBind, GRIDBASE_ADMIN_EMAIL: 'admin@example.org', GRIDBASE_ADMIN_PASSWORD: 'weak' });
    assert.equal(r.status, 3, r.out);
  });
});

test('M1-R: 公開構成・強い新パスワードを .env に残していても、DB が既定パスワードなら exit 3', async () => {
  await withDirectus([adminRow(hashOf(DEFAULT_PASSWORD))], async (_fake, conn) => {
    const r = await runSeed({ ...conn, ...publicBind, GRIDBASE_ADMIN_EMAIL: 'admin@example.org', GRIDBASE_ADMIN_PASSWORD: 'Str0ng-and-unique-9' });
    assert.equal(r.status, 3, r.out);
  });
});

test('M1-R: 公開構成・検査対象の管理者のパスワードが変更済みなら、GRIDBASE_ADMIN_PASSWORD 未設定で exit 0', async () => {
  await withDirectus([adminRow(hashOf('Changed-by-the-owner-77'))], async (_fake, conn) => {
    const r = await runSeed({ ...conn, ...publicBind });
    assert.equal(r.status, 0, r.out);
  });
});

test('M1-R: 公開構成で Directus に届かないなら fail-closed（exit 3）', async () => {
  const r = await runSeed({ DIRECTUS_URL: 'http://127.0.0.1:1', DIRECTUS_TOKEN: 'test-token-not-a-secret', ...publicBind });
  assert.equal(r.status, 3, r.out);
});

test('M1-R: 公開構成で DIRECTUS_TOKEN が無いなら fail-closed（exit 3）', async () => {
  const r = await runSeed({ ...publicBind });
  assert.equal(r.status, 3, r.out);
});

test('M1-R: 公開構成でパスワード欄が読めない / ハッシュに見えないなら fail-closed（exit 3）', async () => {
  for (const row of [{ userId: 'u1', email: 'admin@example.org', status: 'active' }, adminRow('**********'), adminRow(12345)]) {
    await withDirectus([row], async (_fake, conn) => {
      const r = await runSeed({ ...conn, ...publicBind });
      assert.equal(r.status, 3, `${JSON.stringify(row)}\n${r.out}`);
    });
  }
});

test('M1-R: 公開構成で .env の GRIDBASE_ADMIN_PASSWORD が既定のままなら、Directus を見る前に exit 3', async () => {
  await withDirectus([], async (_fake, conn) => {
    const r = await runSeed({ ...conn, ...publicBind, GRIDBASE_ADMIN_EMAIL: 'admin@example.org', GRIDBASE_ADMIN_PASSWORD: DEFAULT_PASSWORD });
    assert.equal(r.status, 3, r.out);
  });
});

test('S-1: 公開構成でも、パスワードが null / 空の管理者は検査対象外（web の認証がサインインを拒否する前提。password-check.test.ts）', async () => {
  await withDirectus([adminRow(null), { ...adminRow(''), userId: 'u2', email: 'empty@example.org' }], async (_fake, conn) => {
    const r = await runSeed({ ...conn, ...publicBind });
    assert.equal(r.status, 0, r.out);
  });
});

test('S-2: 公開構成では、無効（inactive）な管理者の既定パスワードも exit 3', async () => {
  await withDirectus([{ ...adminRow(hashOf(DEFAULT_PASSWORD)), status: 'inactive' }], async (_fake, conn) => {
    const r = await runSeed({ ...conn, ...publicBind });
    assert.equal(r.status, 3, r.out);
    assert.match(r.out, /既定のまま/);
  });
});

test('S-2: 公開構成では、既定の管理者メールのユーザーは role=user・inactive でも既定パスワードなら exit 3', async () => {
  for (const status of ['active', 'inactive']) {
    await withDirectus(
      [adminRow(hashOf('Changed-by-the-owner-77'))],
      async (_fake, conn) => {
        const r = await runSeed({ ...conn, ...publicBind });
        assert.equal(r.status, 3, `${status}\n${r.out}`);
        assert.match(r.out, /admin@gridbase\.localhost.*既定のまま/);
      },
      [{ userId: 'u9', email: 'admin@gridbase.localhost', role: 'user', status, password: hashOf(DEFAULT_PASSWORD) }],
    );
  }
});

test('S-2: 公開構成でも、既定でないメールの一般ユーザーは対象外（既定パスワードでも止めない）', async () => {
  await withDirectus(
    [adminRow(hashOf('Changed-by-the-owner-77'))],
    async (_fake, conn) => {
      const r = await runSeed({ ...conn, ...publicBind });
      assert.equal(r.status, 0, r.out);
    },
    [{ userId: 'u8', email: 'someone@example.org', role: 'user', status: 'active', password: hashOf(DEFAULT_PASSWORD) }],
  );
});

test('S-2: ローカル構成では、降格した既定の管理者がいても起動を止めない', async () => {
  await withDirectus(
    [adminRow(hashOf('Changed-by-the-owner-77'))],
    async (_fake, conn) => {
      const r = await runSeed({ ...conn, ...localEnv, GRIDBASE_ADMIN_EMAIL: 'x@gridbase.localhost', GRIDBASE_ADMIN_PASSWORD: 'Str0ng-and-unique-9' });
      assert.equal(r.status, 0, r.out);
    },
    [{ userId: 'u9', email: 'admin@gridbase.localhost', role: 'user', status: 'active', password: hashOf(DEFAULT_PASSWORD) }],
  );
});

test('A-1: bind が 127.0.0.1・http でも、実ドメインなら公開構成として DB を検査する', async () => {
  for (const extra of [{ GRIDBASE_BASE_DOMAIN: 'tunnel.example.org' }, { GRIDBASE_ADMIN_DOMAIN: 'admin.example.org' }, { NEXTAUTH_URL: 'http://admin.example.org' }]) {
    await withDirectus([adminRow(hashOf(DEFAULT_PASSWORD))], async (_fake, conn) => {
      const r = await runSeed({ ...conn, ...localEnv, ...extra });
      assert.equal(r.status, 3, `${JSON.stringify(extra)}\n${r.out}`);
    });
  }
});

test('ローカル構成（localhost 系ドメイン）では従来どおり: 未設定は Directus を呼ばず exit 0 / 強度不足は exit 1', async () => {
  await withDirectus([adminRow(hashOf(DEFAULT_PASSWORD))], async (fake, conn) => {
    const skipped = await runSeed({ ...conn, ...localEnv, GRIDBASE_BASE_DOMAIN: 'gridbase.localhost', GRIDBASE_ADMIN_DOMAIN: 'admin.gridbase.localhost' });
    assert.equal(skipped.status, 0, skipped.out);
    assert.equal(fake.requests.length, 0);
    const weak = await runSeed({ ...conn, ...localEnv, GRIDBASE_ADMIN_EMAIL: 'admin@gridbase.localhost', GRIDBASE_ADMIN_PASSWORD: 'weak' });
    assert.equal(weak.status, 1, weak.out);
  });
});

test('ローカル構成では、既定パスワードの管理者がいても起動を止めない（公開構成になったときに init と web が止める）', async () => {
  await withDirectus([adminRow(hashOf(DEFAULT_PASSWORD))], async (_fake, conn) => {
    const r = await runSeed({ ...conn, ...localEnv, GRIDBASE_ADMIN_EMAIL: 'admin@gridbase.localhost', GRIDBASE_ADMIN_PASSWORD: DEFAULT_PASSWORD });
    assert.equal(r.status, 0, r.out);
  });
});
