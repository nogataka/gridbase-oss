// 「公開構成か」「既定のパスワードか」を判定する純粋関数群。seed-admin.mjs が使う。
//
// directus/init/secrets-check.mjs（directus-init が使う）と同じ判定でなければならない。
// 2 つのイメージ（web / directus-init）は別々にビルドされ、互いのファイルを持てないので、
// 同じロジックを 2 か所に置いている。2 つが食い違わないことは directus/init/parity.test.mjs が検査する。
// どちらかを変えたら、もう一方も同じように変えること。

/** bind がこの値のとき「ローカル専用」。それ以外は公開構成。 */
export const LOCAL_BIND_ADDRESSES = new Set(['127.0.0.1', '::1', '[::1]', 'localhost']);

/** 値の接頭辞が「仮の値」を表すもの。.env.example の値はすべてこのどれかで始まる。 */
export const PLACEHOLDER_PATTERN = /^(change-?me|local-dev-|replace-with-)/i;

/** .env.example / web/.env.example の既定の管理者パスワード。 */
export const KNOWN_DEFAULT_PASSWORDS = ['ChangeMe-local-1234', 'change-me-admin-password'];

/** .env.example / web/.env.example に載っている gridbase の既定の管理者メール。 */
export const KNOWN_DEFAULT_ADMIN_EMAILS = ['admin@gridbase.localhost', 'admin@example.com'];

/**
 * 既定のパスワードの検査対象に加える、管理者以外も含めたメール（小文字）。
 * 既知の既定メールと、いまの GRIDBASE_ADMIN_EMAIL。role や status に関係なく検査する
 * （既定の管理者を user に降格・無効化しても、README の資格情報でサインインできる状態が残らないように）。
 */
export function defaultAdminEmails(env) {
  const emails = new Set(KNOWN_DEFAULT_ADMIN_EMAILS);
  const current = (env.GRIDBASE_ADMIN_EMAIL ?? '').trim().toLowerCase();
  if (current) emails.add(current);
  return [...emails];
}

/** 値が仮の値（change-me / local-dev- / replace-with- で始まる、または既知の既定パスワード）か。 */
export const isDefaultPassword = (value) =>
  PLACEHOLDER_PATTERN.test(String(value ?? '')) || KNOWN_DEFAULT_PASSWORDS.includes(String(value ?? ''));

/**
 * ホスト名が「手元だけで使うドメイン」か。localhost・*.localhost・*.test・*.internal だけが該当する。
 * 判定できない値（空白やスラッシュを含むなど）は false = 実ドメイン扱い（公開構成側に倒す）。
 */
export function isLocalOnlyHostname(hostname) {
  const host = String(hostname ?? '').trim().toLowerCase().replace(/\.$/, '');
  if (!/^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/.test(host)) return false;
  return host === 'localhost' || host.endsWith('.localhost') || host.endsWith('.test') || host.endsWith('.internal');
}

/** `GRIDBASE_BASE_DOMAIN` のような「ホスト名（任意でポート付き）」から、ホスト名だけを取り出す。 */
function hostnameOfDomainSetting(value) {
  const trimmed = String(value ?? '').trim();
  if (trimmed.startsWith('[')) return trimmed; // IPv6 リテラルは実ドメイン扱い（isLocalOnlyHostname が false を返す）
  return trimmed.replace(/:\d*$/, '');
}

/**
 * 公開構成かどうかを判定し、理由の一覧を返す（空なら公開構成ではない）。
 *   - GRIDBASE_BIND_ADDRESS が 127.0.0.1 以外
 *   - GRIDBASE_SITE_SCHEME / GRIDBASE_ADMIN_URL が https://
 *   - GRIDBASE_BASE_DOMAIN / GRIDBASE_ADMIN_DOMAIN / GRIDBASE_ADMIN_URL のホストが
 *     localhost・*.localhost・*.test・*.internal 以外（= 実ドメイン。URL だけはループバック IP も可）。
 *     bind が 127.0.0.1 でも、ホスト側の nginx などから http で公開している構成を見逃さないため。
 *     空の値は compose の既定（*.gridbase.localhost）なので、ローカル扱い。
 */
export function publicDeploymentReasons(env) {
  const reasons = [];
  const bind = (env.GRIDBASE_BIND_ADDRESS ?? '').trim() || '127.0.0.1';
  if (!LOCAL_BIND_ADDRESSES.has(bind.toLowerCase())) {
    reasons.push(`GRIDBASE_BIND_ADDRESS=${bind}`);
  }
  if ((env.GRIDBASE_SITE_SCHEME ?? '').trim().toLowerCase().startsWith('https://')) {
    reasons.push('GRIDBASE_SITE_SCHEME=https://');
  }
  const adminUrl = (env.GRIDBASE_ADMIN_URL ?? '').trim();
  if (adminUrl.toLowerCase().startsWith('https://')) {
    reasons.push('GRIDBASE_ADMIN_URL が https://');
  }
  for (const name of ['GRIDBASE_BASE_DOMAIN', 'GRIDBASE_ADMIN_DOMAIN']) {
    const raw = (env[name] ?? '').trim();
    if (raw && !isLocalOnlyHostname(hostnameOfDomainSetting(raw))) {
      reasons.push(`${name}=${raw}（localhost / *.localhost / *.test / *.internal 以外）`);
    }
  }
  if (adminUrl) {
    let host = '';
    try {
      host = new URL(adminUrl).hostname;
    } catch {
      // URL として読めない値は実ドメイン扱い
    }
    // URL では、ブラウザでそのまま開けるループバックの IP（http://127.0.0.1:8080 など）もローカル扱いにする
    const isLoopbackIp = /^127(\.\d{1,3}){3}$/.test(host) || host === '[::1]';
    if (!isLocalOnlyHostname(host) && !isLoopbackIp) {
      reasons.push('GRIDBASE_ADMIN_URL のホストが localhost / *.localhost / *.test / *.internal 以外');
    }
  }
  return reasons;
}

/**
 * web コンテナの環境変数から公開構成かを判定する。web には GRIDBASE_ADMIN_URL が NEXTAUTH_URL として渡されるので、それを補う。
 * seed-admin.mjs と deployment-mode.mjs（docker-entrypoint.sh が使う）の両方がこれを使い、判定を 1 か所にそろえる。
 * （web 専用。directus/init には対応する関数が無いので、parity.test.mjs の対象外）
 */
export function webPublicDeploymentReasons(env) {
  return publicDeploymentReasons({
    ...env,
    GRIDBASE_ADMIN_URL: env.GRIDBASE_ADMIN_URL || env.NEXTAUTH_URL || env.APP_URL || '',
  });
}
