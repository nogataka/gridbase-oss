// docker-entrypoint.sh の起動判断。seed-admin.mjs と server.js を差し替えた一時ディレクトリで実行する。
// seed-admin の終了コードが 0 以外のとき、公開構成（または判定不能）では web を起動せず、ローカル構成では起動を続ける。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const webDir = fileURLToPath(new URL('..', import.meta.url));

function run({ seedExit, env, withModeScript = true }) {
  const dir = mkdtempSync(join(tmpdir(), 'gridbase-entrypoint-'));
  try {
    mkdirSync(join(dir, 'scripts'));
    copyFileSync(join(webDir, 'docker-entrypoint.sh'), join(dir, 'docker-entrypoint.sh'));
    copyFileSync(join(webDir, 'scripts/deployment-check.mjs'), join(dir, 'scripts/deployment-check.mjs'));
    if (withModeScript) copyFileSync(join(webDir, 'scripts/deployment-mode.mjs'), join(dir, 'scripts/deployment-mode.mjs'));
    writeFileSync(join(dir, 'scripts/seed-admin.mjs'), `process.exit(${seedExit});\n`);
    writeFileSync(join(dir, 'server.js'), "console.log('WEB-STARTED');\n");
    const r = spawnSync('sh', ['docker-entrypoint.sh'], {
      cwd: dir,
      env: { PATH: process.env.PATH, ...env },
      encoding: 'utf8',
    });
    return { status: r.status, started: r.stdout.includes('WEB-STARTED'), out: r.stdout + r.stderr };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const publicEnv = { GRIDBASE_BIND_ADDRESS: '0.0.0.0' };
const localEnv = { GRIDBASE_BIND_ADDRESS: '127.0.0.1', GRIDBASE_SITE_SCHEME: 'http://', NEXTAUTH_URL: 'http://admin.gridbase.localhost' };

test('seed-admin が 0 なら、構成に関係なく web を起動する', () => {
  for (const env of [publicEnv, localEnv]) {
    const r = run({ seedExit: 0, env });
    assert.equal(r.started, true, r.out);
    assert.equal(r.status, 0, r.out);
  }
});

test('公開構成では、seed-admin が 0 以外ならどの終了コードでも web を起動しない（1 / 2 / 3 / 137 を含む）', () => {
  for (const seedExit of [1, 2, 3, 4, 70, 137]) {
    const r = run({ seedExit, env: publicEnv });
    assert.equal(r.started, false, `exit ${seedExit}\n${r.out}`);
    assert.notEqual(r.status, 0, `exit ${seedExit}\n${r.out}`);
  }
});

test('実ドメイン（bind は 127.0.0.1）の公開構成でも、seed-admin が異常終了なら起動しない', () => {
  const r = run({ seedExit: 1, env: { ...localEnv, GRIDBASE_BASE_DOMAIN: 'tunnel.example.org' } });
  assert.equal(r.started, false, r.out);
});

test('構成を判定できない（deployment-mode.mjs が無い）ときも、seed-admin が異常終了なら起動しない（fail-closed）', () => {
  const r = run({ seedExit: 1, env: localEnv, withModeScript: false });
  assert.equal(r.started, false, r.out);
});

test('ローカル構成では従来どおり、seed-admin が 1 で終わっても web を起動する。3 のときだけは止める', () => {
  const failed = run({ seedExit: 1, env: localEnv });
  assert.equal(failed.started, true, failed.out);
  assert.equal(failed.status, 0, failed.out);
  const refused = run({ seedExit: 3, env: localEnv });
  assert.equal(refused.started, false, refused.out);
});
