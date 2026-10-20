// GET /api/admin/users/[userId]/subdomains — 指定ユーザーのサブドメイン一覧（管理者のみ）
import { NextResponse } from 'next/server';
import { db } from '@/lib/directus';
import { requireAdmin } from '@/lib/guards';
import { RECORD_ID_PATTERN } from '@/lib/users';
import { SUBDOMAIN_FIELDS } from '@/lib/subdomain-input';

export const dynamic = 'force-dynamic';

export async function GET(req: Request, { params }: { params: { userId: string } }) {
  try {
    const actor = await requireAdmin(req);
    if (actor instanceof NextResponse) return actor;

    const { userId } = params;
    if (!RECORD_ID_PATTERN.test(userId)) {
      return NextResponse.json({ error: 'Invalid userId' }, { status: 400 });
    }

    const { data, error } = await db
      .from('subdomains')
      .select(SUBDOMAIN_FIELDS)
      .eq('userId', userId)
      .order('created_at', { ascending: false });
    if (error) throw new Error(error.message);

    return NextResponse.json({ subdomains: data || [] });
  } catch (error) {
    console.error('Admin user subdomains error:', error);
    return NextResponse.json({ error: 'Internal server error' }, { status: 500 });
  }
}
