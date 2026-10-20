// GET /api/admin/users — ユーザー一覧（管理者のみ。api_key・password 等は返さない）
import { NextResponse } from 'next/server';
import { db } from '@/lib/directus';
import { requireAdmin } from '@/lib/guards';
import { ADMIN_USER_LIST_FIELDS } from '@/lib/users';

export const dynamic = 'force-dynamic';

export async function GET(req: Request) {
  try {
    const actor = await requireAdmin(req);
    if (actor instanceof NextResponse) return actor;

    const query = (new URL(req.url).searchParams.get('q') || '').trim().toLowerCase();

    const { data, error } = await db
      .from('users')
      .select(ADMIN_USER_LIST_FIELDS)
      .order('created_at', { ascending: false });
    if (error) throw new Error(error.message);

    let users: Record<string, unknown>[] = data || [];
    if (query) {
      users = users.filter(
        (u) =>
          String(u.email ?? '').toLowerCase().includes(query) ||
          String(u.display_name ?? '').toLowerCase().includes(query)
      );
    }

    return NextResponse.json({ users });
  } catch (error) {
    console.error('Admin users list error:', error);
    return NextResponse.json({ error: 'Internal server error' }, { status: 500 });
  }
}
