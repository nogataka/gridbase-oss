import bcryptjs from 'bcryptjs';

// ユーザーが存在しない・パスワードを持たない場合も bcrypt を 1 回実行し、応答時間で存在を推測されにくくする。
// このダミーのハッシュは「時間を揃えるためだけ」のもの。元の文字列はソースに書かれているので、
// ダミーと比べて一致した結果を、ログインの成功に使ってはいけない。
const DUMMY_HASH = bcryptjs.hashSync('gridbase-dummy-password', 10);

/**
 * 保存済みのパスワード（bcrypt ハッシュ）に対して入力を検証する。
 * stored が null・空・文字列でないとき（パスワードを持たないユーザー）は、時間を揃えるために比較は行うが、
 * 結果に関わらず必ず false を返す。
 *
 * directus/init/admin-check.mjs と web/scripts/seed-admin.mjs は、この性質を前提に
 * 「パスワードが null / 空の管理者はパスワードでサインインできない」ので既定パスワードの検査対象外としている。
 * この関数を変えるときは、password-check.test.ts と、その 2 か所の前提を見直すこと。
 */
export async function verifyStoredPassword(plain: string, stored: unknown): Promise<boolean> {
  const hasHash = typeof stored === 'string' && stored !== '';
  const matched = await bcryptjs.compare(plain, hasHash ? stored : DUMMY_HASH);
  return hasHash && matched;
}
