// サーバー側の実行時設定。すべて process.env から「呼び出し時」に読む。
// NEXT_PUBLIC_* は使わない（ビルド後に env で変えられるようにするため）。
// ブラウザに必要な値は app/layout.tsx（server component）から props で渡す。

const truthy = (value: string | undefined): boolean =>
  ['1', 'true', 'yes', 'on'].includes((value ?? '').trim().toLowerCase());

/**
 * 公開 URL（末尾スラッシュなし）。NEXTAUTH_URL を優先し、無ければ APP_URL。
 * 公開 URL の決定はここ 1 か所だけ（middleware・auth・request-guard はすべてこれを使う）。
 */
export function getAppUrl(): string {
  const raw = (process.env.NEXTAUTH_URL || process.env.APP_URL || 'http://localhost:3000').trim();
  return raw.replace(/\/+$/, '');
}

/**
 * セッション Cookie 名。https のときは `__Host-` 接頭辞（Secure・Path=/・Domain なしをブラウザが強制するため、
 * 兄弟サブドメインから Domain 付きの同名 Cookie を差し込まれてもセッション固定に使えない）。
 * http（ローカル）では `__Host-` は使えない（Secure が必須）ので既定名。
 */
export function sessionCookieName(): string {
  return getAppUrl().startsWith('https://') ? '__Host-next-auth.session-token' : 'next-auth.session-token';
}

/** サブドメイン表示用のベースドメイン（例: example.com）。未設定なら空文字。 */
export function getBaseDomain(): string {
  return (process.env.GRIDBASE_BASE_DOMAIN || '').trim().replace(/^\.+|\.+$/g, '');
}

/** クライアントが接続するトンネルサーバー（表示用 host:port）。未設定なら空文字。 */
export function getTunnelServer(): string {
  return (process.env.GRIDBASE_TUNNEL_SERVER || '').trim();
}

/** 新規登録の公開/閉鎖。既定は閉鎖（true/1/yes/on のときだけ公開）。 */
export function isSignupEnabled(): boolean {
  return truthy(process.env.GRIDBASE_SIGNUP_ENABLED);
}

/** SMTP が設定されているか（未設定ならパスワードリセットは無効）。 */
export function isSmtpConfigured(): boolean {
  return Boolean((process.env.SMTP_HOST || '').trim() && (process.env.SMTP_FROM || '').trim());
}

/** ブラウザに渡してよい（秘密を含まない）設定。 */
export interface PublicConfig {
  signupEnabled: boolean;
  passwordResetEnabled: boolean;
  baseDomain: string;
  tunnelServer: string;
  appUrl: string;
}

export function getPublicConfig(): PublicConfig {
  return {
    signupEnabled: isSignupEnabled(),
    passwordResetEnabled: isSmtpConfigured(),
    baseDomain: getBaseDomain(),
    tunnelServer: getTunnelServer(),
    appUrl: getAppUrl(),
  };
}
