// POST /api/account/password — ログイン中のユーザーが自分のパスワードを変更する。
//
// - 現在のパスワードの確認が必須（セッションを奪われただけではパスワードを変えられない）
// - CSRF ガード（Origin 完全一致・JSON 必須）は requireUser 経由で適用される
// - 変更時に password_changed_at を更新する。これ以前に発行された他のセッション（他の端末・ブラウザ）は
//   次のリクエストから 401 になる。呼び出し元のブラウザは、成功後に新しいパスワードで再ログインして
//   新しいセッションを得る（画面側で自動実行）。
import { NextResponse } from 'next/server';
import bcryptjs from 'bcryptjs';
import { db } from '@/lib/directus';
import { requireUser } from '@/lib/guards';
import { attemptWithLimit } from '@/lib/rate-limit';
import { validatePassword } from '@/lib/users';

export const dynamic = 'force-dynamic';

// 現在のパスワードの確認の試行回数（ユーザー単位。成功すると戻る = 実質は間違えた回数）。窓内に 5 回で一時停止。
// 検証の前に数える（同時に届いたリクエストでも上限を超えられない。attemptWithLimit）
const WRONG_LIMIT = 5;
const WINDOW_MS = 15 * 60 * 1000;

export async function POST(req: Request) {
  try {
    const actor = await requireUser(req);
    if (actor instanceof NextResponse) return actor;

    const limitKey = `change-password:${actor.userId}`;

    const body = await req.json().catch(() => null);
    const currentPassword = typeof body?.currentPassword === 'string' ? body.currentPassword : '';
    const newPassword = body?.newPassword;
    if (!currentPassword || currentPassword.length > 200) {
      return NextResponse.json({ error: '現在のパスワードを入力してください' }, { status: 400 });
    }
    const passwordError = validatePassword(newPassword);
    if (passwordError) {
      return NextResponse.json({ error: passwordError }, { status: 400 });
    }
    if (newPassword === currentPassword) {
      return NextResponse.json({ error: '新しいパスワードは現在のパスワードと異なる値にしてください' }, { status: 400 });
    }

    const check = await attemptWithLimit(limitKey, WRONG_LIMIT, WINDOW_MS, async () => {
      const { data: user, error: lookupError } = await db
        .from('users')
        .select('password')
        .eq('userId', actor.userId)
        .maybeSingle();
      if (lookupError) throw new Error(lookupError.message);
      const hash = typeof user?.password === 'string' ? user.password : '';
      return hash && (await bcryptjs.compare(currentPassword, hash)) ? true : null;
    });
    if (check.limited) {
      return NextResponse.json({ error: '試行回数が多すぎます。しばらく待ってから再度お試しください' }, { status: 429 });
    }
    if (!check.value) {
      return NextResponse.json({ error: '現在のパスワードが正しくありません' }, { status: 400 });
    }

    const { error } = await db
      .from('users')
      .update({
        password: await bcryptjs.hash(newPassword as string, 10),
        password_changed_at: new Date().toISOString(),
        // 未使用のリセットリンクが残っていても、このパスワード変更の後は使えないようにする
        reset_token: null,
        reset_token_expires: null,
      })
      .eq('userId', actor.userId);
    if (error) throw new Error(error.message);

    return NextResponse.json({ message: 'パスワードを変更しました。他の端末のセッションは無効になりました' });
  } catch (error) {
    console.error('account/password POST error:', error);
    return NextResponse.json({ error: 'Internal server error' }, { status: 500 });
  }
}
