// サブドメイン作成/更新の入力を許可リストで検証する（サーバー専用）。
// userId・id・created_at 等はここで一切受け取らない。

/** API が返す subdomains の列（明示列。'*' は使わない）。 */
export const SUBDOMAIN_FIELDS =
  'id,subdomain,mode,status,description,local_server_url,local_host,local_port,created_at';

export type SubdomainWritable = {
  mode: 'http';
  status: 'active' | 'inactive';
  description: string | null;
  local_server_url: string;
  local_host: null;
  local_port: null;
};

type Result<T> = { ok: true; value: T } | { ok: false; error: string };

export const TCP_UNSUPPORTED_MESSAGE =
  'TCP トンネルは現在サポートされていません（サーバー側に外部待受ポートが未実装です）。HTTP を指定してください';

function optionalString(value: unknown, max: number): string | null | undefined {
  if (value === undefined) return undefined;
  if (value === null || value === '') return null;
  if (typeof value !== 'string' || value.length > max) return undefined;
  return value.trim() || null;
}

/** body から書き込み可能な項目だけを取り出して検証する。 */
export function parseSubdomainWritable(body: unknown): Result<SubdomainWritable> {
  if (typeof body !== 'object' || body === null) return { ok: false, error: 'invalid body' };
  const b = body as Record<string, unknown>;

  // トンネルサーバーは TCP の公開リスナーを持たない。作っても動かないデータを残さないよう 400 で拒否する
  if (b.mode === 'tcp') return { ok: false, error: TCP_UNSUPPORTED_MESSAGE };
  if (b.mode !== undefined && b.mode !== 'http') {
    return { ok: false, error: 'mode は http を指定してください' };
  }

  const status = b.status === undefined ? 'active' : b.status;
  if (status !== 'active' && status !== 'inactive') {
    return { ok: false, error: 'status は active か inactive を指定してください' };
  }

  const description = optionalString(b.description, 200);
  if (description === undefined && b.description !== undefined) {
    return { ok: false, error: '説明は200文字以内で指定してください' };
  }

  const raw = typeof b.local_server_url === 'string' ? b.local_server_url.trim() : '';
  let url: URL | null = null;
  try {
    url = raw && raw.length <= 2048 ? new URL(raw) : null;
  } catch {
    url = null;
  }
  if (!/^https?:\/\//i.test(raw) || !url || (url.protocol !== 'http:' && url.protocol !== 'https:')) {
    return { ok: false, error: 'ローカルサーバーURLは http:// または https:// で始まる URL を指定してください' };
  }
  return {
    ok: true,
    value: {
      mode: 'http',
      status,
      description: description ?? null,
      local_server_url: raw,
      local_host: null,
      local_port: null,
    },
  };
}
