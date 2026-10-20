import { NextRequest, NextResponse } from 'next/server';
import { getToken } from 'next-auth/jwt';
import { getAppUrl, sessionCookieName } from '@/lib/config';

// 認証が不要なパス。それ以外のページはログイン必須。
// /api/* は各ルートが自前で認可する（lib/guards.ts）ため、ここでは素通しする。
const PUBLIC_PATHS = ['/signin', '/signup', '/reset-password', '/update-password', '/docs'];

const isPublicPath = (pathname: string): boolean =>
  pathname.startsWith('/api/') || PUBLIC_PATHS.includes(pathname) || pathname.startsWith('/images/');

// 公開 URL。リダイレクトとヘッダーの出し分けは、プロキシ越しの Host ではなく設定値を基準にする。
// （実行時に環境変数で変えられるよう、毎回 lib/config の getAppUrl() で読む）
const appUrl = getAppUrl;

// セッション Cookie 名（https のときは __Host- 接頭辞）。lib/auth.ts の cookies 設定と同じ値を使う
const readToken = (req: NextRequest) =>
  getToken({ req, secret: process.env.NEXTAUTH_SECRET, cookieName: sessionCookieName() });

function applySecurityHeaders(res: NextResponse): NextResponse {
  const https = appUrl().startsWith('https://');
  const dev = process.env.NODE_ENV !== 'production';

  const csp = [
    "default-src 'self'",
    // Next.js のインライン bootstrap スクリプトに 'unsafe-inline' が必要（nonce 未使用）
    `script-src 'self' 'unsafe-inline'${dev ? " 'unsafe-eval'" : ''}`,
    "style-src 'self' 'unsafe-inline'",
    "font-src 'self' data:",
    "img-src 'self' data:",
    "connect-src 'self'",
    "frame-src 'none'",
    "frame-ancestors 'none'",
    "form-action 'self'",
    "base-uri 'self'",
    "object-src 'none'",
    ...(https ? ['upgrade-insecure-requests'] : []),
  ].join('; ');

  res.headers.set('Content-Security-Policy', csp);
  res.headers.set('X-Content-Type-Options', 'nosniff');
  res.headers.set('X-Frame-Options', 'DENY');
  res.headers.set('Referrer-Policy', 'strict-origin-when-cross-origin');
  res.headers.set('Permissions-Policy', 'camera=(), microphone=(), geolocation=(), interest-cohort=()');
  if (https) {
    // HSTS / COOP は HTTPS 配信のときだけ（http の LAN 運用を壊さない）
    res.headers.set('Strict-Transport-Security', 'max-age=31536000; includeSubDomains');
    res.headers.set('Cross-Origin-Opener-Policy', 'same-origin');
  }
  return res;
}

export async function middleware(req: NextRequest) {
  const { pathname, search } = req.nextUrl;

  // LP は無い: / はログイン状態に応じてダッシュボード/サインインへ
  if (pathname === '/') {
    const token = await readToken(req);
    return applySecurityHeaders(
      NextResponse.redirect(new URL(token && !token.inactive ? '/dashboard' : '/signin', appUrl()))
    );
  }

  if (!isPublicPath(pathname)) {
    const token = await readToken(req);

    if (!token || token.inactive) {
      const url = new URL('/signin', appUrl());
      url.searchParams.set('callbackUrl', `${pathname}${search}`);
      return applySecurityHeaders(NextResponse.redirect(url));
    }

    // /admin は管理者のみ（API 側でも DB を見て再確認する）
    if (pathname.startsWith('/admin') && token.role !== 'admin') {
      return applySecurityHeaders(NextResponse.redirect(new URL('/dashboard', appUrl())));
    }
  }

  return applySecurityHeaders(NextResponse.next());
}

export const config = {
  matcher: ['/((?!_next/static|_next/image|favicon.ico).*)'],
};
