// GET /api/health — コンテナのヘルスチェック用（DB に触れない・認証不要）
import { NextResponse } from 'next/server';

export const dynamic = 'force-dynamic';

export function GET() {
  return NextResponse.json({ status: 'ok' });
}
