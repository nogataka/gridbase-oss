// ログイン後の遷移先（callbackUrl）を、同一オリジンのパスだけに絞る。
// ブラウザ・サーバーのどちらからも使えるよう、DOM や Node 固有の API には依存しない。

const FALLBACK = '/dashboard';

/**
 * `value` を `origin` を基準に解決し、結果のオリジンが一致するときだけ「パス + クエリ + ハッシュ」を返す。
 * それ以外（外部 URL・`//evil.com`・`/\evil.com`・`javascript:` など）は fallback。
 *
 * 単純な「/ で始まり // で始まらない」判定は `/\evil.com` を通してしまう。
 * WHATWG URL ではバックスラッシュがスラッシュ扱いになり `https://evil.com/` に解決されるため、
 * 文字列の見た目ではなく、実際に URL として解決した結果のオリジンで判定する。
 */
export function safeCallbackPath(
  value: string | null | undefined,
  origin: string,
  fallback: string = FALLBACK,
): string {
  if (!value) return fallback;
  try {
    const base = new URL(origin).origin;
    const resolved = new URL(value, base);
    if (resolved.origin !== base) return fallback;

    const path = `${resolved.pathname}${resolved.search}${resolved.hash}`;
    // `/..//evil.com` のように、正規化後に `//` で始まるパスは、そのまま渡すとプロトコル相対 URL になる
    if (!path.startsWith('/') || path.startsWith('//') || path.startsWith('/\\')) return fallback;
    // 念のため、組み立てたパスをもう一度解決して同一オリジンであることを確かめる
    if (new URL(path, base).origin !== base) return fallback;
    return path;
  } catch {
    return fallback;
  }
}
