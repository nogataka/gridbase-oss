// users コレクションの列定義と入力検証。
// 秘密列（password / reset_token / reset_token_expires）は API から返さない。
// api_key は本人向け（OWN_USER_FIELDS）だけに含める。

export const OWN_USER_FIELDS =
  'userId,email,display_name,description,status,role,api_key,created_at,last_login_at';

export const ADMIN_USER_LIST_FIELDS =
  'userId,email,display_name,description,status,role,created_at,last_login_at';

export const ACTOR_FIELDS = 'userId,email,role,status,password_changed_at';

/** Directus のレコード ID（users.userId・subdomains.id）として受け付ける形式。 */
export const RECORD_ID_PATTERN = /^[A-Za-z0-9-]{1,64}$/;

export const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
export const PASSWORD_MIN_LENGTH = 8;

/**
 * Directus の timestamp 値を比較用のエポックミリ秒にする（空・不正は 0）。
 * password_changed_at をセッション（JWT）に持たせて DB と比べ、パスワード変更前のセッションを失効させる。
 * 文字列の表記ゆれ（Z の有無・小数秒）に左右されないよう、数値にして比べる。
 */
export const toEpoch = (value: unknown): number => {
  if (typeof value !== 'string' || !value) return 0;
  const t = Date.parse(value);
  return Number.isFinite(t) ? t : 0;
};

export const normalizeEmail = (value: string): string => value.trim().toLowerCase();

export function validatePassword(password: unknown): string | null {
  if (typeof password !== 'string' || password.length < PASSWORD_MIN_LENGTH) {
    return `パスワードは${PASSWORD_MIN_LENGTH}文字以上で指定してください`;
  }
  if (password.length > 200) return 'パスワードが長すぎます';
  if (!/[A-Za-z]/.test(password) || !/\d/.test(password)) {
    return 'パスワードは少なくとも1つの文字と数字を含む必要があります';
  }
  return null;
}
