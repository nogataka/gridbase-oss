// /api/dashboard/subdomains — ログインユーザー自身のサブドメイン CRUD
// 書き込み可能な項目は許可リスト（lib/subdomain-input.ts）のみ。userId はセッション由来。
import { NextResponse } from 'next/server';
import { randomInt } from 'crypto';
import { db } from '@/lib/directus';
import { requireUser } from '@/lib/guards';
import { RECORD_ID_PATTERN } from '@/lib/users';
import { SUBDOMAIN_FIELDS, parseSubdomainWritable } from '@/lib/subdomain-input';
import {
  SUBDOMAIN_FORMAT_MESSAGE,
  isReservedSubdomain,
  isValidSubdomainFormat,
  normalizeSubdomain,
} from '@/lib/subdomain-rules';

export const dynamic = 'force-dynamic';

const RANDOM_CHARS = 'abcdefghijklmnopqrstuvwxyz0123456789';

const internalError = () => NextResponse.json({ error: 'Internal server error' }, { status: 500 });

async function isSubdomainTaken(subdomain: string): Promise<boolean> {
  // 重複チェックは fail-closed: DB エラーは「空き」とみなさず例外にする（呼び出し側で 500）。
  // 最終的な一意性は Directus の subdomains.subdomain の UNIQUE 制約で担保する。
  const { data, error } = await db.from('subdomains').select('id').eq('subdomain', subdomain).maybeSingle();
  if (error) throw new Error(error.message);
  return Boolean(data);
}

async function generateUniqueSubdomain(): Promise<string> {
  for (let attempt = 0; attempt < 10; attempt++) {
    let candidate = '';
    for (let i = 0; i < 8; i++) candidate += RANDOM_CHARS[randomInt(RANDOM_CHARS.length)];
    if (!isReservedSubdomain(candidate) && isValidSubdomainFormat(candidate) && !(await isSubdomainTaken(candidate))) {
      return candidate;
    }
  }
  throw new Error('一意のサブドメインを生成できませんでした');
}

// GET: 自分のサブドメイン一覧
export async function GET(req: Request) {
  try {
    const actor = await requireUser(req);
    if (actor instanceof NextResponse) return actor;

    const { data, error } = await db
      .from('subdomains')
      .select(SUBDOMAIN_FIELDS)
      .eq('userId', actor.userId)
      .order('created_at', { ascending: false });
    if (error) throw new Error(error.message);

    return NextResponse.json({ subdomains: data || [] });
  } catch (error) {
    console.error('dashboard/subdomains GET error:', error);
    return internalError();
  }
}

// POST: サブドメインを作成（subdomain 省略時は自動生成）
export async function POST(req: Request) {
  try {
    const actor = await requireUser(req);
    if (actor instanceof NextResponse) return actor;

    const body = await req.json().catch(() => null);
    const parsed = parseSubdomainWritable(body);
    if (!parsed.ok) return NextResponse.json({ error: parsed.error }, { status: 400 });

    let subdomain: string;
    const desired = typeof body?.subdomain === 'string' ? normalizeSubdomain(body.subdomain) : '';
    if (desired) {
      if (!isValidSubdomainFormat(desired)) {
        return NextResponse.json({ error: SUBDOMAIN_FORMAT_MESSAGE }, { status: 400 });
      }
      if (isReservedSubdomain(desired)) {
        return NextResponse.json({ error: '指定したサブドメインは利用できません。別の値を選択してください。' }, { status: 409 });
      }
      if (await isSubdomainTaken(desired)) {
        return NextResponse.json({ error: '指定したサブドメインは既に使用されています。別の値を選択してください。' }, { status: 409 });
      }
      subdomain = desired;
    } else {
      subdomain = await generateUniqueSubdomain();
    }

    const { error } = await db.from('subdomains').insert({
      ...parsed.value,
      subdomain,
      userId: actor.userId,
      created_at: new Date().toISOString(),
    });
    if (error) {
      // subdomains.subdomain の UNIQUE 制約に競合した場合もここに来る
      console.error('dashboard/subdomains insert error:', error.message);
      return NextResponse.json({ error: 'サブドメインの作成に失敗しました（既に使用されている可能性があります）' }, { status: 409 });
    }

    return NextResponse.json({ success: true, subdomain });
  } catch (error) {
    console.error('dashboard/subdomains POST error:', error);
    return internalError();
  }
}

// PATCH: 自分のサブドメインを更新（subdomain 名・userId は変更不可）
export async function PATCH(req: Request) {
  try {
    const actor = await requireUser(req);
    if (actor instanceof NextResponse) return actor;

    const body = await req.json().catch(() => null);
    const id = typeof body?.id === 'string' ? body.id : '';
    if (!RECORD_ID_PATTERN.test(id)) {
      return NextResponse.json({ error: 'id is required' }, { status: 400 });
    }

    const parsed = parseSubdomainWritable(body);
    if (!parsed.ok) return NextResponse.json({ error: parsed.error }, { status: 400 });

    const { data: existing, error: existingError } = await db
      .from('subdomains')
      .select('id')
      .eq('id', id)
      .eq('userId', actor.userId)
      .maybeSingle();
    if (existingError) throw new Error(existingError.message);
    if (!existing) return NextResponse.json({ error: 'Not found or forbidden' }, { status: 403 });

    const { error } = await db.from('subdomains').update(parsed.value).eq('id', id);
    if (error) throw new Error(error.message);

    return NextResponse.json({ success: true });
  } catch (error) {
    console.error('dashboard/subdomains PATCH error:', error);
    return internalError();
  }
}

// DELETE: 自分のサブドメインを削除
export async function DELETE(req: Request) {
  try {
    const actor = await requireUser(req);
    if (actor instanceof NextResponse) return actor;

    const id = new URL(req.url).searchParams.get('id') || '';
    if (!RECORD_ID_PATTERN.test(id)) {
      return NextResponse.json({ error: 'id is required' }, { status: 400 });
    }

    const { data: existing, error: existingError } = await db
      .from('subdomains')
      .select('id')
      .eq('id', id)
      .eq('userId', actor.userId)
      .maybeSingle();
    if (existingError) throw new Error(existingError.message);
    if (!existing) return NextResponse.json({ error: 'Not found or forbidden' }, { status: 403 });

    const { error } = await db.from('subdomains').delete().eq('id', id);
    if (error) throw new Error(error.message);

    return NextResponse.json({ success: true });
  } catch (error) {
    console.error('dashboard/subdomains DELETE error:', error);
    return internalError();
  }
}
