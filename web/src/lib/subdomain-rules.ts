// サブドメイン規則。サーバー（API 検証）とブラウザ（フォーム検証）の両方から使うため、
// サーバー専用モジュールを import しないこと。

export const SUBDOMAIN_PATTERN = /^(?!-)[a-z0-9-]{3,16}(?<!-)$/;

export const RESERVED_SUBDOMAINS: ReadonlySet<string> = new Set([
  'admin',
  'root',
  'www',
  'mail',
  'ftp',
  'ssh',
  'api',
  'directus',
  'gridbase',
  'tunnels',
  'tunnel',
  'conn',
  'support',
  'system',
  'status',
  'test',
  'example',
  'subdomain',
]);

export const SUBDOMAIN_FORMAT_MESSAGE =
  'サブドメインは3〜16文字の英小文字・数字・ハイフンで、ハイフンで始まらず終わらない必要があります（連続したハイフンも不可）';

export const normalizeSubdomain = (value: string): string => value.trim().toLowerCase();

export const isReservedSubdomain = (value: string): boolean => RESERVED_SUBDOMAINS.has(value);

export const isValidSubdomainFormat = (value: string): boolean =>
  SUBDOMAIN_PATTERN.test(value) && !value.includes('--');
