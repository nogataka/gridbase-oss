import NextAuth from 'next-auth';
import type { NextAuthOptions } from 'next-auth';
import CredentialsProvider from 'next-auth/providers/credentials';
import { getAppUrl, sessionCookieName } from '@/lib/config';
import { db } from '@/lib/directus';
import { verifyStoredPassword } from '@/lib/password-check';
import { attemptWithLimit, clientIp, hitRateLimit } from '@/lib/rate-limit';
import { normalizeEmail, toEpoch } from '@/lib/users';

// NextAuth は NEXTAUTH_URL を参照する。APP_URL だけが設定されている場合の別名対応。
if (!process.env.NEXTAUTH_URL && process.env.APP_URL) {
  process.env.NEXTAUTH_URL = getAppUrl();
}

// (IP, メール) ごとの試行回数。成否を問わず数える
const LOGIN_LIMIT = 10;
const LOGIN_WINDOW_MS = 15 * 60 * 1000;
// メール（アカウント）ごとの試行回数（成功すると戻る = 実質は失敗回数）。IP を分散した総当たりを止める。
// 超えた間は、正しいパスワードでもログインできない（窓が切れるまで最大 15 分）。
// パスワードの検証より前に数えるので、同時に送られたリクエストでも上限を超えられない。
// 他人のメールに対する失敗を重ねてロックアウトさせる嫌がらせは理論上可能なので、上限は高め（20）にしている。
const ACCOUNT_FAIL_LIMIT = 20;

// JWT に入れた role / status を Directus で再確認する間隔（既定 5 分。GRIDBASE_SESSION_RECHECK_SECONDS で変更可）
const recheckIntervalMs = (): number => {
  const seconds = Number(process.env.GRIDBASE_SESSION_RECHECK_SECONDS);
  return (Number.isFinite(seconds) && seconds > 0 ? seconds : 300) * 1000;
};

const useSecureCookie = getAppUrl().startsWith('https://');

export const authOptions = {
  debug: false,
  pages: {
    signIn: '/signin',
    error: '/signin',
  },
  session: { strategy: 'jwt', maxAge: 60 * 60 * 24 * 7 },
  cookies: {
    // https のときは __Host- 接頭辞（Domain なし・Path=/・Secure）にして、兄弟サブドメインからのセッション固定を防ぐ
    sessionToken: {
      name: sessionCookieName(),
      options: { httpOnly: true, sameSite: 'lax', path: '/', secure: useSecureCookie },
    },
  },
  providers: [
    CredentialsProvider({
      id: 'credentials',
      name: 'Credentials',
      credentials: {
        email: { label: 'Email', type: 'text' },
        password: { label: 'Password', type: 'password' },
      },
      async authorize(credentials, req) {
        const email = normalizeEmail(String(credentials?.email ?? ''));
        const password = String(credentials?.password ?? '');
        if (!email || !password) return null;

        if (hitRateLimit(`login:${clientIp(req.headers ?? {})}:${email}`, LOGIN_LIMIT, LOGIN_WINDOW_MS)) {
          throw new Error('RATE_LIMITED');
        }

        // メール（アカウント）単位の制限は、パスワードを検証する「前」に数える（同時に届いた総当たりが上限を超えないように）。
        // 成功したときだけ数が戻る
        const result = await attemptWithLimit(`login-fail:${email}`, ACCOUNT_FAIL_LIMIT, LOGIN_WINDOW_MS, async () => {
          const { data: user, error: lookupError } = await db
            .from('users')
            .select('userId,email,password,role,status,password_changed_at')
            .eq('email', email)
            .maybeSingle();
          if (lookupError) {
            console.error('authorize: user lookup failed:', lookupError.message);
            return null; // fail-closed
          }

          // ユーザーがいない・パスワードを持たない場合も比較は行う（時間を揃える）が、結果は必ず false になる
          const valid = await verifyStoredPassword(password, user?.password);
          // パスワードが正しくても status が active 以外ならログインさせない
          if (!user || !valid || user.status !== 'active') return null;
          return user;
        });
        if (result.limited) throw new Error('RATE_LIMITED');
        const user = result.value;
        if (!user) return null;

        await db
          .from('users')
          .update({ last_login_at: new Date().toISOString() })
          .eq('userId', String(user.userId));

        return {
          id: String(user.userId),
          email: String(user.email),
          role: user.role === 'admin' ? 'admin' : 'user',
          pwdAt: toEpoch(user.password_changed_at),
        };
      },
    }),
  ],
  callbacks: {
    async jwt({ token, user }) {
      if (user) {
        token.uid = user.id;
        token.role = user.role;
        token.pwdAt = user.pwdAt;
        token.checkedAt = Date.now();
        token.inactive = false;
        return token;
      }

      // 発行済み JWT の role/status/パスワード変更時刻を一定間隔で DB と突き合わせる。
      // 削除・無効化されたユーザー、およびパスワード変更後の古いセッションは inactive 扱い
      // （session にユーザー情報を載せない）。降格は role に反映する。
      // DB 障害時は次回に再試行（API 側は毎回 DB で認可するので安全側）。
      const now = Date.now();
      if (token.uid && (!token.checkedAt || now - token.checkedAt > recheckIntervalMs())) {
        const { data, error } = await db
          .from('users')
          .select('role,status,password_changed_at')
          .eq('userId', token.uid)
          .maybeSingle();
        if (!error) {
          if (!data || data.status !== 'active' || toEpoch(data.password_changed_at) !== (token.pwdAt ?? 0)) {
            token.inactive = true;
          } else {
            token.inactive = false;
            token.role = data.role === 'admin' ? 'admin' : 'user';
          }
          token.checkedAt = now;
        }
      }
      return token;
    },
    async session({ session, token }) {
      if (token.inactive) {
        // 無効化/削除されたユーザー: ユーザー情報を持たないセッションを返す
        return { expires: session.expires } as unknown as typeof session;
      }
      session.user.name = '';
      session.user.image = '';
      session.user.email = token.email;
      session.user.role = token.role;
      session.user.uid = token.uid;
      session.user.pwdAt = token.pwdAt ?? 0;
      return session;
    },
    async redirect({ url, baseUrl }) {
      // 相対パスか同一オリジンのみ許可（オープンリダイレクト防止）。
      // 文字列の見た目ではなく、URL として解決した結果のオリジンで判定する（/\evil.com 対策）
      try {
        const resolved = new URL(url, baseUrl);
        if (resolved.origin === new URL(baseUrl).origin) return resolved.toString();
      } catch {
        // fallthrough
      }
      return `${baseUrl}/dashboard`;
    },
  },
  secret: process.env.NEXTAUTH_SECRET,
} satisfies NextAuthOptions;

export const handler = NextAuth(authOptions);
