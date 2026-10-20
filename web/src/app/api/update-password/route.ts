// POST /api/update-password — リセットトークンで新しいパスワードを設定
import { NextRequest, NextResponse } from 'next/server';
import { rejectUnsafeMutation } from '@/lib/request-guard';
import bcryptjs from 'bcryptjs';
import { createHash } from 'crypto';
import { db } from '@/lib/directus';
import { clientIp, hitRateLimit } from '@/lib/rate-limit';
import { validatePassword } from '@/lib/users';

export const dynamic = 'force-dynamic';

export async function POST(req: NextRequest) {
  const rejected = rejectUnsafeMutation(req);
  if (rejected) return rejected;
  try {
    if (hitRateLimit(`update-password:${clientIp(req.headers)}`, 10, 15 * 60 * 1000)) {
      return NextResponse.json({ error: 'リクエストが多すぎます。しばらくしてから再度お試しください' }, { status: 429 });
    }

    const body = await req.json().catch(() => null);
    const token = typeof body?.token === 'string' ? body.token : '';
    const password = body?.password;

    if (!token || token.length > 200) {
      return NextResponse.json({ error: 'token と password は必須です' }, { status: 400 });
    }
    const passwordError = validatePassword(password);
    if (passwordError) {
      return NextResponse.json({ error: passwordError }, { status: 400 });
    }

    const tokenHash = createHash('sha256').update(token).digest('hex');
    const { data: user, error: lookupError } = await db
      .from('users')
      .select('userId,reset_token_expires,status')
      .eq('reset_token', tokenHash)
      .maybeSingle();
    if (lookupError) throw new Error(lookupError.message);

    if (!user || user.status !== 'active') {
      return NextResponse.json({ error: '無効なリセットトークンです' }, { status: 400 });
    }

    const expires = user.reset_token_expires as string | null;
    if (!expires || new Date(expires) < new Date()) {
      return NextResponse.json({ error: 'リセットトークンの有効期限が切れています' }, { status: 400 });
    }

    const { error } = await db
      .from('users')
      .update({
        password: await bcryptjs.hash(password as string, 10),
        // これ以前に発行されたセッションを失効させる（lib/guards.ts・lib/auth.ts が照合する）
        password_changed_at: new Date().toISOString(),
        reset_token: null,
        reset_token_expires: null,
      })
      .eq('userId', String(user.userId));

    if (error) {
      console.error('update-password: update failed:', error.message);
      return NextResponse.json({ error: 'パスワードの更新に失敗しました' }, { status: 500 });
    }

    return NextResponse.json({ message: 'パスワードを更新しました' });
  } catch (error) {
    console.error('update-password error:', error);
    return NextResponse.json({ error: 'サーバーエラーが発生しました' }, { status: 500 });
  }
}
