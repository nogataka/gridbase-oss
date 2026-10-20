// 既定の秘密（.env.example の値など）が残っていないかを検査する純粋関数群。
// init.mjs から使う。副作用を持たないので node --test で単体テストできる
// （directus/init/secrets-check.test.mjs）。
//
// 公開構成の判定と「既定のパスワードか」の判定は、web/scripts/deployment-check.mjs（seed-admin が使う）と
// 同じロジックを持つ。2 つが食い違わないことは parity.test.mjs が検査する。
//
// 方針:
//   - ローカル構成（127.0.0.1 だけにバインドし、http のみ）: 警告だけ出して起動は続ける
//   - 公開構成（0.0.0.0 などにバインド、https、または localhost / *.localhost / *.test / *.internal 以外の実ドメイン）:
//     1 つでも残っていれば起動を止める

/** この値のとき「ローカル専用」とみなす。それ以外は公開構成。 */
export const LOCAL_BIND_ADDRESSES = new Set(['127.0.0.1', '::1', '[::1]', 'localhost']);

/** 値の接頭辞が「仮の値」を表すもの。.env.example の値はすべてこのどれかで始まる。 */
export const PLACEHOLDER_PATTERN = /^(change-?me|local-dev-|replace-with-)/i;

/**
 * 検査する秘密。
 *   optional  空でもよい（機能が無効になるだけ）。値があるときは検査する
 *   minLength 公開構成での最小長（ランダム値を想定）。パスワードは強度を別に検査するので 0
 */
export const SECRET_SPECS = [
  { name: 'DIRECTUS_KEY', minLength: 16 },
  { name: 'DIRECTUS_SECRET', minLength: 16 },
  { name: 'DIRECTUS_ADMIN_PASSWORD', minLength: 12 },
  { name: 'DIRECTUS_ADMIN_TOKEN', minLength: 20 },
  { name: 'GRIDBASE_WEB_DIRECTUS_TOKEN', minLength: 20 },
  { name: 'GRIDBASE_TUNNEL_DIRECTUS_TOKEN', minLength: 20 },
  { name: 'NEXTAUTH_SECRET', minLength: 16 },
  { name: 'GRIDBASE_ADMIN_PASSWORD', minLength: 0, optional: true },
  { name: 'GRIDBASE_TUNNEL_ADMIN_TOKEN', minLength: 24, optional: true },
  { name: 'POSTGRES_PASSWORD', minLength: 12, optional: true },
];

/** 値が互いに重複してはいけない秘密（使い回すと 1 つの漏えいが全体に広がる）。 */
const MUST_BE_DISTINCT = [
  'DIRECTUS_KEY',
  'DIRECTUS_SECRET',
  'DIRECTUS_ADMIN_TOKEN',
  'GRIDBASE_WEB_DIRECTUS_TOKEN',
  'GRIDBASE_TUNNEL_DIRECTUS_TOKEN',
  'NEXTAUTH_SECRET',
  'GRIDBASE_TUNNEL_ADMIN_TOKEN',
];

/** KEY=VALUE 形式のテキスト（.env.example）を Map にする。コメント・空行は無視。 */
export function parseEnvText(text) {
  const map = new Map();
  for (const rawLine of String(text).split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue;
    const eq = line.indexOf('=');
    if (eq <= 0) continue;
    const key = line.slice(0, eq).trim();
    let value = line.slice(eq + 1).trim();
    if (/^(["']).*\1$/.test(value)) value = value.slice(1, -1);
    else value = value.replace(/\s+#.*$/, '');
    map.set(key, value);
  }
  return map;
}

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
 * 1 つの値が「既定のまま」かを返す。理由の文字列、問題なければ null。
 * knownDefaults: .env.example から読んだ { 名前 -> 値 }（Map）。
 */
export function placeholderReason(name, value, knownDefaults) {
  if (!value) return null;
  if (PLACEHOLDER_PATTERN.test(value)) return '仮の値（change-me / local-dev- など）のままです';
  const example = knownDefaults?.get(name);
  if (example && value === example) return '.env.example の値のままです';
  return null;
}

/**
 * 秘密を検査する。
 * @param env            process.env 相当
 * @param knownDefaults  Map（.env.example の内容。無ければ空 Map）
 * @returns { isPublic, reasons, problems }
 *   reasons:  公開構成と判定した理由
 *   problems: 公開構成なら起動を止める項目、ローカルなら警告に出す項目（文字列の配列）
 */
export function checkSecrets(env, knownDefaults = new Map()) {
  const reasons = publicDeploymentReasons(env);
  const isPublic = reasons.length > 0;
  const problems = [];

  for (const spec of SECRET_SPECS) {
    const value = (env[spec.name] ?? '').trim();
    if (!value) {
      if (!spec.optional) problems.push(`${spec.name} が未設定です`);
      continue;
    }
    const placeholder = placeholderReason(spec.name, value, knownDefaults);
    if (placeholder) {
      problems.push(`${spec.name}: ${placeholder}`);
      continue;
    }
    if (isPublic && value.length < spec.minLength) {
      problems.push(`${spec.name}: 短すぎます（${spec.minLength} 文字以上、openssl rand -hex 32 などで生成してください）`);
    }
  }

  // 公開構成では、秘密どうしの使い回しも止める
  if (isPublic) {
    const seen = new Map();
    for (const name of MUST_BE_DISTINCT) {
      const value = (env[name] ?? '').trim();
      if (!value) continue;
      if (seen.has(value)) {
        problems.push(`${name} と ${seen.get(value)} が同じ値です。別々の値にしてください`);
      } else {
        seen.set(value, name);
      }
    }
  }

  return { isPublic, reasons, problems };
}
