// API ルート用の認可ヘルパー。
// セッション(JWT)の role/status は信用せず、毎回 DB で再確認する
// （無効化・降格・パスワード変更がログイン中のセッションにも即時反映されるようにする）。

import { NextResponse } from 'next/server';
import { getServerSession } from 'next-auth';
import { authOptions } from '@/lib/auth';
import { db } from '@/lib/directus';
import { rejectUnsafeMutation } from '@/lib/request-guard';
import { ACTOR_FIELDS, toEpoch } from '@/lib/users';

export interface Actor {
  userId: string;
  email: string;
  role: 'user' | 'admin';
}

const json = (body: Record<string, unknown>, status: number) => NextResponse.json(body, { status });

/** ログイン済みかつ status=active のユーザーを要求する。 */
export async function requireUser(req: Request): Promise<Actor | NextResponse> {
  const rejected = rejectUnsafeMutation(req);
  if (rejected) return rejected;

  const session = await getServerSession(authOptions);
  const uid = session?.user?.uid;
  if (!uid) return json({ error: 'Unauthorized' }, 401);

  // DB 障害時も認可は通さない（fail-closed）
  const { data: user, error } = await db
    .from('users')
    .select(ACTOR_FIELDS)
    .eq('userId', uid)
    .maybeSingle();
  if (error) return json({ error: 'Service unavailable' }, 503);
  if (!user) return json({ error: 'Unauthorized' }, 401);
  if (user.status !== 'active') return json({ error: 'Forbidden' }, 403);
  // パスワードを変更（またはリセット）した後は、それ以前に発行されたセッションを受け付けない
  if (toEpoch(user.password_changed_at) !== (session?.user?.pwdAt ?? 0)) {
    return json({ error: 'Unauthorized' }, 401);
  }

  return {
    userId: String(user.userId),
    email: String(user.email ?? ''),
    role: user.role === 'admin' ? 'admin' : 'user',
  };
}

/** 管理者(role=admin, status=active)を要求する。 */
export async function requireAdmin(req: Request): Promise<Actor | NextResponse> {
  const actor = await requireUser(req);
  if (actor instanceof NextResponse) return actor;
  if (actor.role !== 'admin') return json({ error: 'Forbidden' }, 403);
  return actor;
}
