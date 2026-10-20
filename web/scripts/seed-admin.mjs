#!/usr/bin/env node
// 初回管理者を作成する冪等スクリプト。
//
// GRIDBASE_ADMIN_EMAIL / GRIDBASE_ADMIN_PASSWORD が設定されていて、
// users コレクションに有効な（status=active の）role=admin のユーザーが 1 人もいない場合にだけ作成する。
// 既に有効な管理者がいれば何もしない（毎回の起動で呼んでも安全）。
//
// 安全のための振る舞い:
//   - 同じメールの既存ユーザーがいる場合は、昇格もパスワード変更もせず、警告して終了する
//     （サインアップを公開している間に管理者のメールで先に登録されても、乗っ取られないようにする）。
//   - 管理者が「無効のものだけ」の場合も何も作らない（意図して無効化された可能性があるため）。
//   - 公開構成（GRIDBASE_BIND_ADDRESS が 127.0.0.1 以外、https、または localhost / *.localhost / *.test / *.internal
//     以外の実ドメイン。判定は deployment-check.mjs）では、既定のパスワード
//     （.env.example の値・change-me・local-dev- など）での作成を拒否する。
//     また、すでに DB にいる管理者のパスワードが既定のままなら、起動を止める。検査するのは role=admin の全員
//     （status を問わない）と、既定の管理者メール（admin@gridbase.localhost など）のユーザー（role・status を問わない）。
//     パスワードが null / 空のユーザーは、web の認証（src/lib/password-check.ts）が入力に関わらず拒否するので対象外。この DB の検査は
//     GRIDBASE_ADMIN_* が未設定でも行う（README は「作成後は GRIDBASE_ADMIN_PASSWORD を消す」ことを勧めているため）。
//     （同じ検査は directus-init も行う。そちらが止まれば web はそもそも起動しない。これは二重の防御）
//   - 公開構成では「検査できない」「失敗した」場合も web を起動しない（fail-closed。終了コード 3）。
//   - パスワードの強度はサインアップと同じ規則（8〜200 文字、英字と数字を含む）。
//
// 終了コード: 0 = 正常 / 1 = 失敗（ローカル構成のみ。web の起動は続ける） /
//             3 = 公開構成で既定のパスワードを検出、または公開構成での失敗（web を起動しない）
//
// 使い方:
//   node scripts/seed-admin.mjs            # Docker では docker-entrypoint.sh が起動時に呼ぶ
//   docker compose run --rm web node scripts/seed-admin.mjs
//
// 必要な環境変数: DIRECTUS_URL, DIRECTUS_TOKEN（初回管理者を作るときは GRIDBASE_ADMIN_EMAIL, GRIDBASE_ADMIN_PASSWORD も）
// 任意: SEED_WAIT_SECONDS（Directus の準備を待つ最大秒数。既定 120）

import bcryptjs from 'bcryptjs';
import { randomBytes, randomUUID } from 'node:crypto';
import { KNOWN_DEFAULT_PASSWORDS, defaultAdminEmails, isDefaultPassword, webPublicDeploymentReasons } from './deployment-check.mjs';

const EXIT_FAILED = 1;
const EXIT_REFUSED = 3;

const log = (msg) => console.log(`[seed-admin] ${msg}`);
const warn = (msg) => console.warn(`[seed-admin] WARNING: ${msg}`);

const email = (process.env.GRIDBASE_ADMIN_EMAIL || '').trim().toLowerCase();
const password = process.env.GRIDBASE_ADMIN_PASSWORD || '';
const directusUrl = (process.env.DIRECTUS_URL || '').trim().replace(/\/+$/, '');
const directusToken = (process.env.DIRECTUS_TOKEN || '').trim();

// ---- 公開構成かどうか / 既定のパスワードか -------------------------------------
// 判定は deployment-check.mjs（directus/init/secrets-check.mjs と同じロジック。parity.test.mjs が一致を検査する）。

// web には GRIDBASE_ADMIN_URL が NEXTAUTH_URL として渡される（webPublicDeploymentReasons が補う）
const publicReasons = webPublicDeploymentReasons(process.env);
const isPublic = publicReasons.length > 0;

// web/src/lib/users.ts の validatePassword と同じ規則
function validatePassword(value) {
  if (typeof value !== 'string' || value.length < 8) return '8 文字以上にしてください';
  if (value.length > 200) return '長すぎます（200 文字以内）';
  if (!/[A-Za-z]/.test(value) || !/\d/.test(value)) return '英字と数字をそれぞれ 1 文字以上含めてください';
  return null;
}

function refuse(message) {
  console.error(`[seed-admin] ERROR: ${message}`);
  process.exit(EXIT_REFUSED);
}

// 失敗の終わり方。ローカル構成では web の起動を続ける（exit 1）が、公開構成では起動しない（fail-closed）
function failed(message) {
  if (isPublic) {
    refuse(`${message}（公開構成（${publicReasons.join(', ')}）のため、安全を確認できないまま web を起動しません）`);
  }
  log(message);
  process.exit(EXIT_FAILED);
}

// ---- 入力の検査 ----------------------------------------------------------------

const hasAdminEnv = Boolean(email && password);
// 公開構成の DB 検査は、GRIDBASE_ADMIN_* が無くても行う。そのために DIRECTUS_* は公開構成では必須
if ((isPublic || hasAdminEnv) && (!directusUrl || !directusToken)) {
  failed('DIRECTUS_URL / DIRECTUS_TOKEN が未設定です');
}

const envPasswordIsDefault = hasAdminEnv && isDefaultPassword(password);

if (!hasAdminEnv) {
  if (!isPublic) {
    log('GRIDBASE_ADMIN_EMAIL / GRIDBASE_ADMIN_PASSWORD が未設定のためスキップします');
    process.exit(0);
  }
  log('GRIDBASE_ADMIN_EMAIL / GRIDBASE_ADMIN_PASSWORD は未設定です（初回管理者は作りません）。公開構成なので、保存済みの管理者を検査します');
} else {
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
    failed('GRIDBASE_ADMIN_EMAIL がメールアドレスの形式ではありません');
  }
  const passwordProblem = validatePassword(password);
  if (passwordProblem) {
    failed(`GRIDBASE_ADMIN_PASSWORD: ${passwordProblem}`);
  }
  if (envPasswordIsDefault) {
    if (isPublic) {
      refuse(
        `公開構成（${publicReasons.join(', ')}）で GRIDBASE_ADMIN_PASSWORD が既定のままです。` +
          '推測できない新しいパスワードに変えるか、初回管理者の作成後は GRIDBASE_ADMIN_PASSWORD を .env から消してください' +
          '（消した場合も、DB に保存済みの管理者のパスワードが既定のままなら起動しません）。',
      );
    }
    warn('GRIDBASE_ADMIN_PASSWORD が .env.example の既定のままです（ローカル構成なので続行します）。公開する前に必ず変更してください');
  }
}

// ---- Directus ------------------------------------------------------------------

async function directus(path, options = {}) {
  const resp = await fetch(`${directusUrl}${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${directusToken}`,
      ...(options.body ? { 'Content-Type': 'application/json' } : {}),
    },
  });
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({}));
    throw new Error(`${options.method || 'GET'} ${path.split('?')[0]} -> ${resp.status} ${body?.errors?.[0]?.message || ''}`.trim());
  }
  return resp.status === 204 ? null : resp.json();
}

const query = (params) => new URLSearchParams(params).toString();

// Directus 起動・スキーマ適用（users コレクション作成）を待つ
async function waitForUsersCollection() {
  const deadline = Date.now() + Number(process.env.SEED_WAIT_SECONDS || 120) * 1000;
  let lastError;
  let waited = 0;
  for (;;) {
    try {
      await directus(`/items/users?${query({ limit: '1', fields: 'id' })}`);
      return;
    } catch (e) {
      lastError = e;
      if (Date.now() > deadline) throw new Error(`Directus の users コレクションに到達できません: ${lastError.message}`);
      if (waited++ % 5 === 0) log(`Directus の準備を待っています... (${lastError.message})`);
      await new Promise((r) => setTimeout(r, 2000));
    }
  }
}

// 公開構成で、DB にいる管理者（と既定の管理者メールのユーザー）のパスワードが既定のままなら止める。
// （.env を直しても DB の管理者パスワードは変わらないため、.env の検査だけでは足りない）
async function refuseIfAdminHasDefaultPassword(admins) {
  const candidates = Array.from(new Set([...KNOWN_DEFAULT_PASSWORDS, ...(envPasswordIsDefault ? [password] : [])]));
  for (const admin of admins) {
    // password 欄が無い・ハッシュに見えない = 検査できない。公開構成では安全とみなさない
    if (!Object.prototype.hasOwnProperty.call(admin, 'password')) {
      refuse(`ユーザー ${admin.email} のパスワード欄を読めないため、既定のパスワードでないことを確認できません。DIRECTUS_TOKEN の権限を確認してください。`);
    }
    // パスワードを持たない: web の認証は保存済みのパスワードが null / 空のユーザーを必ず拒否する（password-check.ts）ので、サインインできない
    if (admin.password === null || admin.password === '') continue;
    if (typeof admin.password !== 'string' || !/^\$2[abxy]\$\d{2}\$.{53}$/.test(admin.password)) {
      refuse(`ユーザー ${admin.email} のパスワードがハッシュとして読めないため、既定のパスワードでないことを確認できません。`);
    }
    for (const candidate of candidates) {
      if (await bcryptjs.compare(candidate, admin.password)) {
        refuse(
          `公開構成（${publicReasons.join(', ')}）ですが、ユーザー ${admin.email} のパスワードが既定のままです。` +
            '.env を変えても DB のパスワードは変わりません。一度ローカル構成（GRIDBASE_BIND_ADDRESS=127.0.0.1 かつ http、実ドメインなし）で起動し、' +
            'ログイン後に「アカウント」画面（/account）でパスワードを変更してから公開構成に戻してください。' +
            '新規構築なら docker compose down -v でデータを消してやり直せます。',
        );
      }
    }
  }
}

async function main() {
  await waitForUsersCollection();

  const admins = (
    await directus(
      `/items/users?${query({ 'filter[role][_eq]': 'admin', fields: 'userId,email,status,password', limit: '-1' })}`,
    )
  ).data;
  const activeAdmins = admins.filter((a) => a.status === 'active');

  if (isPublic) {
    // 無効な管理者（後で有効に戻される）と、user に降格した既定の管理者も検査する
    const byDefaultEmail = (
      await directus(
        `/items/users?${query({
          'filter[email][_in]': defaultAdminEmails({ GRIDBASE_ADMIN_EMAIL: email }).join(','),
          fields: 'userId,email,status,password',
          limit: '-1',
        })}`,
      )
    ).data;
    const seen = new Set();
    const targets = [...admins, ...byDefaultEmail].filter((u) => {
      const key = String(u.userId ?? u.email);
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
    await refuseIfAdminHasDefaultPassword(targets);
  }

  if (activeAdmins.length > 0) {
    log('有効な管理者が既に存在するため何もしません');
    return;
  }
  if (!hasAdminEnv) {
    log('有効な管理者はいませんが、GRIDBASE_ADMIN_EMAIL / GRIDBASE_ADMIN_PASSWORD が未設定のため作成しません');
    return;
  }
  if (admins.length > 0) {
    warn(
      '管理者は存在しますが、すべて無効（status=inactive）です。新しい管理者は作成しません。' +
        'Directus の管理 UI で users の status を active に戻してください（README の「Changing credentials after the first start」を参照）',
    );
    return;
  }

  const existing = await directus(
    `/items/users?${query({ 'filter[email][_eq]': email, fields: 'id,role,status', limit: '1' })}`,
  );
  if (existing.data.length > 0) {
    warn(
      `GRIDBASE_ADMIN_EMAIL (${email}) のユーザーが既に存在します（role=${existing.data[0].role}）。` +
        '管理者への昇格もパスワード変更もしません（登録済みのメールを持つ別人が管理者になることを防ぐため）。' +
        '別の GRIDBASE_ADMIN_EMAIL を指定するか、本人確認のうえ Directus の管理 UI で role を変更してください',
    );
    if (isPublic) process.exit(EXIT_REFUSED);
    process.exit(EXIT_FAILED);
  }

  const id = randomUUID();
  await directus('/items/users', {
    method: 'POST',
    body: JSON.stringify({
      id,
      userId: id,
      email,
      password: await bcryptjs.hash(password, 10),
      role: 'admin',
      status: 'active',
      display_name: 'Administrator',
      api_key: `gb_${randomBytes(32).toString('base64url')}`,
      created_at: new Date().toISOString(),
      last_login_at: null,
      description: null,
    }),
  });
  log('初回管理者を作成しました');
}

main().catch((e) => failed(`失敗しました: ${e.message}`));
