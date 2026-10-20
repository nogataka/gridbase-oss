// DB に保存された gridbase 管理者のパスワードが「既定のまま」でないかを検査する。
// init.mjs から使う。bcrypt の比較（compare）は呼び出し側から渡すので、単体テストで差し替えられる。
//
// なぜ init で検査するのか: 管理者のパスワードは初回起動のときに DB へ bcrypt ハッシュで保存され、
// その後 .env の GRIDBASE_ADMIN_PASSWORD を消しても変えても DB は変わらない。
// .env の値だけを見る検査では、「ローカルで一度試した DB を公開構成へ持っていった」ときの
// 既定資格情報（README に書かれている）を見逃す。init が止まれば tunnel / web / caddy は起動しないので、
// ここで止めるのが最も確実である。
//
// 検査の対象は「role=admin の全員（status を問わない）」と「既定の管理者メール
// （README に載っている admin@gridbase.localhost など。いまの GRIDBASE_ADMIN_EMAIL も）のユーザー（role・status を問わない）」。
// 既定のアカウントを user に降格したり無効化したりしても、README の資格情報が DB に残ったまま公開されることを防ぐ。
// 全ユーザーを調べないのは、bcrypt の比較が 1 人あたり数百ミリ秒かかり、ユーザー数に比例して起動が遅くなるため。
//
// パスワードが null / 空のユーザーは検査対象外（パスワードではサインインできない）。この前提は web の認証が
// 保証している（web/src/lib/password-check.ts: 保存済みハッシュが無ければ入力に関わらず拒否。password-check.test.ts が固定）。
//
// 検査できなかった場合（管理者の一覧は取れたがパスワード欄が読めない、ハッシュの比較が例外になる等）は
// 「検査できない」として別に返す。公開構成ではこれも起動を止める理由にする（fail-closed）。

import { KNOWN_DEFAULT_PASSWORDS, defaultAdminEmails, isDefaultPassword, publicDeploymentReasons } from './secrets-check.mjs';

const BCRYPT_HASH = /^\$2[abxy]\$\d{2}\$.{53}$/;

/**
 * 既定のパスワードの候補。
 *   - 既知の既定値（README・.env.example に載っているもの）
 *   - .env.example の GRIDBASE_ADMIN_PASSWORD（knownDefaults から。値が変わっても追随する）
 *   - いまの環境変数 GRIDBASE_ADMIN_PASSWORD が仮の値のとき、その値
 */
export function defaultPasswordCandidates(env, knownDefaults = new Map()) {
  const candidates = new Set(KNOWN_DEFAULT_PASSWORDS);
  const example = knownDefaults.get('GRIDBASE_ADMIN_PASSWORD');
  if (example) candidates.add(example);
  const current = (env.GRIDBASE_ADMIN_PASSWORD ?? '').trim();
  if (current && isDefaultPassword(current)) candidates.add(current);
  return [...candidates];
}

/**
 * 検査対象のユーザーのうち、既定のパスワードを持つものと、検査できなかったものを返す。
 * @param admins      [{ email, password }]（検査対象の行。fetchCheckTargets の結果）。password 欄が無い行は「検査できない」
 * @param candidates  既定のパスワードの候補
 * @param compare     async (plain, hash) => boolean（bcrypt.compare）
 * @returns { defaults: string[], unverifiable: string[] }  それぞれ該当する管理者のメール
 */
export async function inspectAdminPasswords(admins, candidates, compare) {
  const defaults = [];
  const unverifiable = [];
  for (const admin of admins) {
    const who = String(admin?.email ?? admin?.userId ?? '(unknown)');
    if (!admin || !Object.prototype.hasOwnProperty.call(admin, 'password')) {
      // 欄ごと返ってこない = 権限などで読めなかった。安全とは言えない
      unverifiable.push(who);
      continue;
    }
    const hash = admin.password;
    // パスワードを持たない（web の認証はパスワードが無いユーザーを必ず拒否するのでサインインできない）
    if (hash === null || hash === '') continue;
    // bcrypt のハッシュに見えないもの（Directus が伏せ字にした値など）は比べても意味がないので「検査できない」
    if (typeof hash !== 'string' || !BCRYPT_HASH.test(hash)) {
      unverifiable.push(who);
      continue;
    }
    try {
      let matched = false;
      for (const candidate of candidates) {
        if (await compare(candidate, hash)) {
          matched = true;
          break;
        }
      }
      if (matched) defaults.push(who);
    } catch {
      unverifiable.push(who);
    }
  }
  return { defaults, unverifiable };
}

export const CHECK_FIELDS = 'userId,email,password,role,status';

/**
 * 検査対象のユーザーを集める: role=admin の全員（status を問わない）＋既定の管理者メールのユーザー（role・status を問わない）。
 * @param env     process.env 相当
 * @param select  async (params) => 行の配列。params は Directus の filter を表すオブジェクト（fields・limit は呼び出し側が付ける）
 * @returns       userId（無ければメール）で重複を除いた行の配列
 */
export async function fetchCheckTargets(env, select) {
  const admins = await select({ 'filter[role][_eq]': 'admin' });
  const byDefaultEmail = await select({ 'filter[email][_in]': defaultAdminEmails(env).join(',') });
  if (!Array.isArray(admins) || !Array.isArray(byDefaultEmail)) throw new Error('応答の形式が想定と違います');
  const seen = new Set();
  const rows = [];
  for (const row of [...admins, ...byDefaultEmail]) {
    const key = String(row?.userId ?? row?.email ?? JSON.stringify(row));
    if (seen.has(key)) continue;
    seen.add(key);
    rows.push(row);
  }
  return rows;
}

/**
 * 保存済みの管理者のパスワードを検査し、結果を返す（init.mjs が表示と終了コードに使う）。
 * @param env           process.env 相当（公開構成の判定と、既定の候補に使う）
 * @param fetchAdmins   async () => [{ userId, email, password }]。検査対象の行（fetchCheckTargets）。到達できなければ例外を投げる
 * @param compare       async (plain, hash) => boolean（bcrypt.compare）
 * @param knownDefaults .env.example の内容（Map）
 * @returns { isPublic, reasons, checked, lines }
 *   lines:   問題の説明（空なら問題なし）。公開構成なら起動を止め、ローカルなら警告に出す
 *   checked: 検査した管理者の人数（取得できなかったときは null）
 * 取得できなかった場合も lines に入れる。「確認できない」を「問題なし」にしない（fail-closed）。
 */
export async function evaluateStoredAdmins({ env, fetchAdmins, compare, knownDefaults = new Map() }) {
  const reasons = publicDeploymentReasons(env);
  const isPublic = reasons.length > 0;

  let admins;
  try {
    admins = await fetchAdmins();
    if (!Array.isArray(admins)) throw new Error('応答の形式が想定と違います');
  } catch (e) {
    return {
      isPublic,
      reasons,
      checked: null,
      lines: [`管理者の一覧を取得できませんでした（${e?.message ?? e}）`],
    };
  }

  const { defaults, unverifiable } = await inspectAdminPasswords(admins, defaultPasswordCandidates(env, knownDefaults), compare);
  return {
    isPublic,
    reasons,
    checked: admins.length,
    lines: [
      ...defaults.map((who) => `${who}: パスワードが既定のままです（README に載っている値でサインインできます）`),
      ...unverifiable.map((who) => `${who}: パスワードのハッシュを検査できませんでした`),
    ],
  };
}
