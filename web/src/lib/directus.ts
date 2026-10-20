// Directus REST の薄いクエリビルダ（.from().select().eq() のチェーン API。await すると { data, error } を返す）。
// - DIRECTUS_URL / DIRECTUS_TOKEN はサーバー専用（ブラウザへ出さない）。
// - 環境変数は呼び出し時に読む（ビルド時に Directus が無くても build が通る）。
// - select() は必ず明示列を渡すこと（'*' を使わない。秘密列の漏えい防止）。

export interface DbResult<T = any> {
  data: T;
  error: { message: string } | null;
}

function directusConfig(): { url: string; token: string } {
  const url = (process.env.DIRECTUS_URL || '').trim().replace(/\/+$/, '');
  const token = (process.env.DIRECTUS_TOKEN || '').trim();
  if (!url || !token) {
    throw new Error('DIRECTUS_URL and DIRECTUS_TOKEN must be set');
  }
  return { url, token };
}

async function directusFetch(path: string, options: RequestInit = {}): Promise<Response> {
  const { url, token } = directusConfig();
  return fetch(`${url}${path}`, {
    ...options,
    cache: 'no-store',
    headers: {
      Authorization: `Bearer ${token}`,
      ...(options.headers as Record<string, string> | undefined),
    },
  });
}

async function errorMessage(resp: Response): Promise<string> {
  const body = await resp.json().catch(() => ({}));
  return body?.errors?.[0]?.message || resp.statusText || `HTTP ${resp.status}`;
}

interface FilterNode {
  type: 'eq';
  field: string;
  value: unknown;
}

type Row = Record<string, unknown>;

class QueryBuilder {
  private _select = '';
  private _filters: FilterNode[] = [];
  private _orders: string[] = [];
  private _limit?: number;
  private _single = false;
  private _maybe = false;
  private _mutation?: 'insert' | 'update' | 'delete';
  private _payload?: Row;

  constructor(private table: string) {}

  select(cols: string): this {
    if (!cols || cols.trim() === '*') {
      throw new Error('select() requires an explicit column list');
    }
    this._select = cols;
    return this;
  }

  eq(field: string, value: unknown): this {
    this._filters.push({ type: 'eq', field, value });
    return this;
  }

  order(field: string, opts?: { ascending?: boolean }): this {
    this._orders.push((opts?.ascending === false ? '-' : '') + field);
    return this;
  }

  limit(n: number): this {
    this._limit = n;
    return this;
  }

  /** 先頭 1 件を返す（無ければ error）。 */
  single(): this {
    this._single = true;
    return this;
  }

  /** 先頭 1 件を返す。0 件は error ではなく data=null（実エラーは error に入る）。 */
  maybeSingle(): this {
    this._single = true;
    this._maybe = true;
    return this;
  }

  insert(data: Row): this {
    this._mutation = 'insert';
    this._payload = data;
    return this;
  }

  update(data: Row): this {
    this._mutation = 'update';
    this._payload = data;
    return this;
  }

  delete(): this {
    this._mutation = 'delete';
    return this;
  }

  then<R1 = DbResult, R2 = never>(
    onfulfilled?: ((value: DbResult) => R1 | PromiseLike<R1>) | null,
    onrejected?: ((reason: unknown) => R2 | PromiseLike<R2>) | null
  ): Promise<R1 | R2> {
    return this.execute().then(onfulfilled, onrejected);
  }

  private async execute(): Promise<DbResult> {
    try {
      if (this._mutation === 'insert') return await this.runInsert();
      if (this._mutation === 'update') return await this.runUpdate();
      if (this._mutation === 'delete') return await this.runDelete();
      return await this.runSelect();
    } catch (e) {
      return { data: null, error: { message: e instanceof Error ? e.message : String(e) } };
    }
  }

  private filterParams(params: URLSearchParams): void {
    for (const f of this._filters) {
      if (f.value === null) params.append(`filter[${f.field}][_null]`, 'true');
      else params.append(`filter[${f.field}][_eq]`, String(f.value));
    }
  }

  private async runSelect(): Promise<DbResult> {
    if (!this._select) throw new Error('select() requires an explicit column list');
    const params = new URLSearchParams();
    params.set('fields', this._select);
    this.filterParams(params);
    if (this._orders.length) params.set('sort', this._orders.join(','));
    params.set('limit', String(this._single ? 1 : this._limit ?? -1));

    const resp = await directusFetch(`/items/${this.table}?${params}`);
    if (!resp.ok) return { data: null, error: { message: await errorMessage(resp) } };

    const json = await resp.json();
    const rows: Row[] = Array.isArray(json.data) ? json.data : [json.data];
    if (this._single) {
      if (rows.length === 0) {
        return { data: null, error: this._maybe ? null : { message: 'No rows found' } };
      }
      return { data: rows[0], error: null };
    }
    return { data: rows, error: null };
  }

  private async runInsert(): Promise<DbResult> {
    const resp = await directusFetch(`/items/${this.table}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(this._payload),
    });
    if (!resp.ok) return { data: null, error: { message: await errorMessage(resp) } };
    return { data: null, error: null };
  }

  /** フィルタに合致する主キー(id)を列挙する。 */
  private async resolveIds(): Promise<string[]> {
    const params = new URLSearchParams();
    params.set('fields', 'id');
    params.set('limit', '-1');
    this.filterParams(params);
    const resp = await directusFetch(`/items/${this.table}?${params}`);
    if (!resp.ok) throw new Error(await errorMessage(resp));
    const json = await resp.json();
    const rows: Row[] = Array.isArray(json.data) ? json.data : [json.data];
    return rows.filter((r) => r?.id).map((r) => String(r.id));
  }

  private async runUpdate(): Promise<DbResult> {
    if (this._filters.length === 0) throw new Error('update() requires a filter');
    const ids = await this.resolveIds();
    if (ids.length === 0) return { data: null, error: { message: 'No matching records to update' } };
    const resp = await directusFetch(`/items/${this.table}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ keys: ids, data: this._payload }),
    });
    if (!resp.ok) return { data: null, error: { message: await errorMessage(resp) } };
    return { data: null, error: null };
  }

  private async runDelete(): Promise<DbResult> {
    if (this._filters.length === 0) throw new Error('delete() requires a filter');
    const ids = await this.resolveIds();
    if (ids.length === 0) return { data: null, error: null };
    const resp = await directusFetch(`/items/${this.table}`, {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(ids),
    });
    if (!resp.ok) return { data: null, error: { message: await errorMessage(resp) } };
    return { data: null, error: null };
  }
}

export const db = {
  from(table: string): QueryBuilder {
    return new QueryBuilder(table);
  },
};
