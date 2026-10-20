// /api/dashboard/user — ログインユーザー本人の情報（api_key は本人にだけ返す）
import { NextResponse } from 'next/server';
import { db } from '@/lib/directus';
import { requireUser } from '@/lib/guards';
import { OWN_USER_FIELDS } from '@/lib/users';

export const dynamic = 'force-dynamic';

// GET: 自分の users レコード
export async function GET(req: Request) {
  try {
    const actor = await requireUser(req);
    if (actor instanceof NextResponse) return actor;

    const { data, error } = await db.from('users').select(OWN_USER_FIELDS).eq('userId', actor.userId).single();
    if (error || !data) throw new Error(error?.message || 'user not found');

    return NextResponse.json({ user: data });
  } catch (error) {
    console.error('dashboard/user GET error:', error);
    return NextResponse.json({ error: 'Internal server error' }, { status: 500 });
  }
}

// PATCH: 本人が変更できるのは表示名と説明のみ（role/status/password/api_key/... は書けない）
export async function PATCH(req: Request) {
  try {
    const actor = await requireUser(req);
    if (actor instanceof NextResponse) return actor;

    const body = await req.json().catch(() => null);
    // JSON が文字列・数値・配列・null のときは `in` 演算子が TypeError になるので、オブジェクトだけ受け付ける
    if (typeof body !== 'object' || body === null || Array.isArray(body)) {
      return NextResponse.json({ error: 'invalid body' }, { status: 400 });
    }
    const updates: Record<string, string | null> = {};

    if ('display_name' in body) {
      const v = body.display_name;
      if (v !== null && (typeof v !== 'string' || v.trim().length > 50)) {
        return NextResponse.json({ error: '表示名は50文字以内で指定してください' }, { status: 400 });
      }
      updates.display_name = typeof v === 'string' && v.trim() ? v.trim() : null;
    }
    if ('description' in body) {
      const v = body.description;
      if (v !== null && (typeof v !== 'string' || v.length > 200)) {
        return NextResponse.json({ error: '説明は200文字以内で指定してください' }, { status: 400 });
      }
      updates.description = typeof v === 'string' && v.trim() ? v.trim() : null;
    }

    if (Object.keys(updates).length === 0) {
      return NextResponse.json({ error: 'No valid fields to update' }, { status: 400 });
    }

    const { error } = await db.from('users').update(updates).eq('userId', actor.userId);
    if (error) throw new Error(error.message);

    return NextResponse.json({ success: true });
  } catch (error) {
    console.error('dashboard/user PATCH error:', error);
    return NextResponse.json({ error: 'Internal server error' }, { status: 500 });
  }
}
