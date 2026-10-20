// POST /api/dashboard/api-key — 自分の API キーを再発行。
// 旧キーが使えなくなるのは、トンネルサーバーが次に Directus を読み込んだ時点（最大 30 秒）。
// すでに接続中のクライアントは、その反映後に切断される。
import { NextResponse } from 'next/server';
import { db } from '@/lib/directus';
import { requireUser } from '@/lib/guards';
import { generateApiKey } from '@/lib/api-key';

export const dynamic = 'force-dynamic';

export async function POST(req: Request) {
  try {
    const actor = await requireUser(req);
    if (actor instanceof NextResponse) return actor;

    const apiKey = generateApiKey();
    const { error } = await db.from('users').update({ api_key: apiKey }).eq('userId', actor.userId);
    if (error) throw new Error(error.message);

    return NextResponse.json({ apiKey });
  } catch (error) {
    console.error('dashboard/api-key POST error:', error);
    return NextResponse.json({ error: 'Internal server error' }, { status: 500 });
  }
}
