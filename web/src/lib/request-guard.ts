// 状態を変える API 共通の CSRF 対策（同一サイト CSRF を含む）。
//
// 1. Content-Type: application/json 必須
//    HTML フォームやクロスサイトの単純リクエストは JSON を送れない（プリフライトが必要になる）。
// 2. Origin が公開 URL(NEXTAUTH_URL / APP_URL)のオリジンと完全一致
//    Origin が無い場合は Sec-Fetch-Site: same-origin のときだけ許可する。
//    トンネル用サブドメイン(*.base-domain)からのリクエストは same-site だが
//    same-origin ではないので、ここで拒否される。
//
// GET / HEAD / OPTIONS は対象外（副作用を持たせないこと）。

import { NextResponse } from 'next/server';
import { getAppUrl } from '@/lib/config';

const SAFE_METHODS = ['GET', 'HEAD', 'OPTIONS'];

export function rejectUnsafeMutation(req: Request): NextResponse | null {
  if (SAFE_METHODS.includes(req.method)) return null;

  const contentType = (req.headers.get('content-type') || '').split(';')[0].trim().toLowerCase();
  if (contentType !== 'application/json') {
    return NextResponse.json({ error: 'Content-Type must be application/json' }, { status: 415 });
  }

  const origin = req.headers.get('origin');
  if (origin) {
    let allowed = false;
    try {
      allowed = origin !== 'null' && new URL(origin).origin === new URL(getAppUrl()).origin;
    } catch {
      allowed = false;
    }
    if (!allowed) return NextResponse.json({ error: 'Forbidden' }, { status: 403 });
    return null;
  }

  if (req.headers.get('sec-fetch-site') === 'same-origin') return null;
  return NextResponse.json({ error: 'Forbidden' }, { status: 403 });
}
