// POST /api/reset-password — パスワードリセットメール送信（SMTP 設定時のみ有効）
// 存在しないメールでも同一レスポンスを返す（ユーザー列挙対策。応答時間も揃えるため、送信は非同期）。
// DB にはトークンそのものではなく SHA-256 を保存する。
import { NextRequest, NextResponse } from 'next/server';
import { rejectUnsafeMutation } from '@/lib/request-guard';
import { createHash, randomBytes } from 'crypto';
import { db } from '@/lib/directus';
import { getAppUrl, isSmtpConfigured } from '@/lib/config';
import { createTransporter } from '@/lib/mailer';
import { clientIp, hitRateLimit } from '@/lib/rate-limit';
import { normalizeEmail } from '@/lib/users';

export const dynamic = 'force-dynamic';

const OK_MESSAGE = 'リセットメールを送信しました（該当する場合）';

async function sendResetMail(userId: string, to: string): Promise<void> {
  const token = randomBytes(32).toString('hex');
  const tokenHash = createHash('sha256').update(token).digest('hex');
  const expires = new Date(Date.now() + 60 * 60 * 1000).toISOString();

  const { error } = await db
    .from('users')
    .update({ reset_token: tokenHash, reset_token_expires: expires })
    .eq('userId', userId);
  if (error) throw new Error(error.message);

  const resetLink = `${getAppUrl()}/update-password?token=${token}`;
  await createTransporter().sendMail({
    from: process.env.SMTP_FROM,
    to,
    subject: 'パスワードリセットのご案内',
    text: `以下のリンクからパスワードをリセットしてください（有効期限: 1時間）:\n\n${resetLink}\n\n心当たりがない場合はこのメールを無視してください。`,
    html: `
      <p>パスワードリセットのリクエストを受け付けました。</p>
      <p>以下のリンクからパスワードをリセットしてください（有効期限: 1時間）。</p>
      <p><a href="${resetLink}">${resetLink}</a></p>
      <p>心当たりがない場合はこのメールを無視してください。</p>
    `,
  });
}

export async function POST(req: NextRequest) {
  const rejected = rejectUnsafeMutation(req);
  if (rejected) return rejected;
  if (!isSmtpConfigured()) {
    return NextResponse.json(
      { error: 'メール送信(SMTP)が設定されていないため、パスワードリセットは利用できません。管理者に連絡してください。' },
      { status: 503 }
    );
  }
  if (hitRateLimit(`reset:${clientIp(req.headers)}`, 5, 15 * 60 * 1000)) {
    return NextResponse.json({ error: 'リクエストが多すぎます。しばらくしてから再度お試しください' }, { status: 429 });
  }

  try {
    const body = await req.json().catch(() => null);
    const email = normalizeEmail(typeof body?.email === 'string' ? body.email : '');
    if (!email || email.length > 254) {
      return NextResponse.json({ message: OK_MESSAGE });
    }

    // 同じメールへの連続リクエスト（メール爆撃）を抑える。超えても応答は変えない（列挙対策）
    if (hitRateLimit(`reset-email:${email}`, 3, 60 * 60 * 1000)) {
      return NextResponse.json({ message: OK_MESSAGE });
    }

    const { data: user, error: lookupError } = await db
      .from('users')
      .select('userId,email,status')
      .eq('email', email)
      .maybeSingle();
    if (lookupError) throw new Error(lookupError.message);

    if (user && user.status === 'active') {
      // トークン保存とメール送信は応答を待たずに行う。
      // 待つと、存在するメールだけ応答が遅くなり、応答時間でユーザーの有無が分かってしまう。
      void sendResetMail(String(user.userId), String(user.email)).catch((error) => {
        console.error('reset-password: send failed:', error);
      });
    }

    return NextResponse.json({ message: OK_MESSAGE });
  } catch (error) {
    console.error('reset-password error:', error);
    // 詳細は外部に漏らさない
    return NextResponse.json({ message: OK_MESSAGE });
  }
}
