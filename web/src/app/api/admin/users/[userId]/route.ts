// PATCH / DELETE /api/admin/users/[userId] — 管理者のみ。自分自身の降格・無効化・削除は不可。
import { NextResponse } from 'next/server';
import { db } from '@/lib/directus';
import { requireAdmin } from '@/lib/guards';
import { RECORD_ID_PATTERN } from '@/lib/users';

export const dynamic = 'force-dynamic';

type Ctx = { params: { userId: string } };

const internalError = () => NextResponse.json({ error: 'Internal server error' }, { status: 500 });

export async function PATCH(req: Request, { params }: Ctx) {
  try {
    const actor = await requireAdmin(req);
    if (actor instanceof NextResponse) return actor;

    const { userId } = params;
    if (!RECORD_ID_PATTERN.test(userId)) {
      return NextResponse.json({ error: 'Invalid userId' }, { status: 400 });
    }
    if (userId === actor.userId) {
      return NextResponse.json({ error: '自分自身のロール・ステータスは変更できません' }, { status: 400 });
    }

    const body = await req.json().catch(() => null);
    const updates: Record<string, string> = {};
    if (body?.status === 'active' || body?.status === 'inactive') updates.status = body.status;
    if (body?.role === 'user' || body?.role === 'admin') updates.role = body.role;

    if (Object.keys(updates).length === 0) {
      return NextResponse.json({ error: 'No valid fields to update' }, { status: 400 });
    }

    const { data: target, error: lookupError } = await db.from('users').select('userId').eq('userId', userId).maybeSingle();
    if (lookupError) throw new Error(lookupError.message);
    if (!target) return NextResponse.json({ error: 'User not found' }, { status: 404 });

    const { error } = await db.from('users').update(updates).eq('userId', userId);
    if (error) throw new Error(error.message);

    return NextResponse.json({ success: true });
  } catch (error) {
    console.error('Admin user update error:', error);
    return internalError();
  }
}

export async function DELETE(req: Request, { params }: Ctx) {
  try {
    const actor = await requireAdmin(req);
    if (actor instanceof NextResponse) return actor;

    const { userId } = params;
    if (!RECORD_ID_PATTERN.test(userId)) {
      return NextResponse.json({ error: 'Invalid userId' }, { status: 400 });
    }
    if (userId === actor.userId) {
      return NextResponse.json({ error: '自分自身は削除できません' }, { status: 400 });
    }

    const { data: target, error: lookupError } = await db.from('users').select('userId').eq('userId', userId).maybeSingle();
    if (lookupError) throw new Error(lookupError.message);
    if (!target) return NextResponse.json({ error: 'User not found' }, { status: 404 });

    const { error: subdomainError } = await db.from('subdomains').delete().eq('userId', userId);
    if (subdomainError) throw new Error(subdomainError.message);

    const { error: userError } = await db.from('users').delete().eq('userId', userId);
    if (userError) throw new Error(userError.message);

    // トンネルは Directus を30秒間隔でポーリングするため、削除は最大30秒程度で反映される
    return NextResponse.json({ success: true });
  } catch (error) {
    console.error('Admin user delete error:', error);
    return internalError();
  }
}
