// POST /api/signup — 新規登録（GRIDBASE_SIGNUP_ENABLED=true のときだけ受け付ける）
import { NextRequest, NextResponse } from 'next/server';
import { rejectUnsafeMutation } from '@/lib/request-guard';
import bcryptjs from 'bcryptjs';
import { randomUUID } from 'crypto';
import { db } from '@/lib/directus';
import { isSignupEnabled } from '@/lib/config';
import { generateApiKey } from '@/lib/api-key';
import { clientIp, hitRateLimit } from '@/lib/rate-limit';
import { EMAIL_PATTERN, normalizeEmail, validatePassword } from '@/lib/users';

export const dynamic = 'force-dynamic';

export async function POST(req: NextRequest) {
  try {
    const rejected = rejectUnsafeMutation(req);
    if (rejected) return rejected;
    if (!isSignupEnabled()) {
      return NextResponse.json({ error: '現在、新規登録は受け付けていません' }, { status: 403 });
    }
    if (hitRateLimit(`signup:${clientIp(req.headers)}`, 10, 60 * 60 * 1000)) {
      return NextResponse.json({ error: 'リクエストが多すぎます。しばらくしてから再度お試しください' }, { status: 429 });
    }

    const body = await req.json().catch(() => null);
    const email = normalizeEmail(typeof body?.email === 'string' ? body.email : '');
    const name = typeof body?.name === 'string' ? body.name.trim() : '';
    const password = body?.password;

    if (!email || email.length > 254 || !EMAIL_PATTERN.test(email)) {
      return NextResponse.json({ error: '有効なメールアドレスを入力してください' }, { status: 400 });
    }
    const passwordError = validatePassword(password);
    if (passwordError) {
      return NextResponse.json({ error: passwordError }, { status: 400 });
    }
    if (name.length < 2 || name.length > 50) {
      return NextResponse.json({ error: '名前は2〜50文字で入力してください' }, { status: 400 });
    }

    // 重複チェックは fail-closed（DB エラー時は登録を進めず 500）
    const { data: existing, error: lookupError } = await db
      .from('users')
      .select('userId')
      .eq('email', email)
      .maybeSingle();
    if (lookupError) throw new Error(lookupError.message);
    if (existing) {
      return NextResponse.json({ error: 'このメールアドレスは既に登録されています' }, { status: 400 });
    }

    const newId = randomUUID();
    const { error: insertError } = await db.from('users').insert({
      id: newId,
      userId: newId,
      email,
      password: await bcryptjs.hash(password as string, 10),
      // 登録時に付与する値はすべてサーバー側で固定（body は email/password/name のみ使用）
      status: 'active',
      role: 'user',
      display_name: name,
      api_key: generateApiKey(),
      created_at: new Date().toISOString(),
      last_login_at: null,
      description: null,
    });

    if (insertError) {
      console.error('signup: user insert failed:', insertError.message);
      return NextResponse.json({ error: 'ユーザー情報の作成に失敗しました' }, { status: 500 });
    }

    return NextResponse.json({ message: 'ユーザーが正常に作成されました', user: { id: newId, email } });
  } catch (error) {
    console.error('signup error:', error);
    return NextResponse.json({ error: 'サーバーエラーが発生しました' }, { status: 500 });
  }
}
