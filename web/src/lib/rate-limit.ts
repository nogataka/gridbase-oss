// 単一プロセス向けのメモリ内レート制限（固定窓）。
// コンテナ 1 台構成を前提としたベストエフォートで、再起動でリセットされる。

interface Bucket {
  count: number;
  resetAt: number;
}

const buckets = new Map<string, Bucket>();

const PURGE_THRESHOLD = 5000;
const HARD_CAP = 50000; // 窓内のキーが際限なく増えてもメモリを使い切らない

function purge(now: number): void {
  buckets.forEach((b, k) => {
    if (b.resetAt <= now) buckets.delete(k);
  });
  if (buckets.size > HARD_CAP) {
    // Map は挿入順なので、古いものから捨てる
    let excess = buckets.size - HARD_CAP * 0.8;
    for (const k of Array.from(buckets.keys())) {
      if (excess-- <= 0) break;
      buckets.delete(k);
    }
  }
}

/** 呼ぶたびに数え、limit を超えたら true を返す。 */
export function hitRateLimit(key: string, limit: number, windowMs: number): boolean {
  const now = Date.now();
  if (buckets.size > PURGE_THRESHOLD) purge(now);
  const bucket = buckets.get(key);
  if (!bucket || bucket.resetAt <= now) {
    buckets.set(key, { count: 1, resetAt: now + windowMs });
    return false;
  }
  bucket.count += 1;
  return bucket.count > limit;
}

export function resetRateLimit(key: string): void {
  buckets.delete(key);
}

export type LimitedAttempt<T> = { limited: true } | { limited: false; value: T | null };

/**
 * 「先に数えてから判定する」試行回数制限（パスワードの総当たり対策）。
 *
 * 1. attempt を実行する前に、同期的に数える（hitRateLimit は await を挟まないので、同時に届いたリクエストでも
 *    Node の単一スレッドで 1 件ずつ数えられる）。上限を超えたら attempt を実行せず limited を返す。
 * 2. attempt が null 以外を返したら成功とみなして数を戻す。null（失敗）なら数えたまま残る。
 *
 * 「bcrypt が終わってから失敗を記録する」と、並列に送られたリクエストが全部「まだ上限内」と判定されて
 * 上限を超えられてしまう。それを避けるため、数える位置を attempt の前に置く。
 */
export async function attemptWithLimit<T>(
  key: string,
  limit: number,
  windowMs: number,
  attempt: () => Promise<T | null>,
): Promise<LimitedAttempt<T>> {
  if (hitRateLimit(key, limit, windowMs)) return { limited: true };
  const value = await attempt();
  if (value !== null) resetRateLimit(key);
  return { limited: false, value };
}

/** リバースプロキシ（Caddy）が付ける X-Forwarded-For の先頭をクライアント IP とみなす。 */
export function clientIp(headers: { get(name: string): string | null } | Record<string, unknown>): string {
  const get = (name: string): string | null => {
    if (typeof (headers as { get?: unknown }).get === 'function') {
      return (headers as { get(n: string): string | null }).get(name);
    }
    const v = (headers as Record<string, unknown>)[name];
    return typeof v === 'string' ? v : null;
  };
  const xff = get('x-forwarded-for');
  if (xff) return xff.split(',')[0].trim() || 'unknown';
  return get('x-real-ip') || 'unknown';
}
