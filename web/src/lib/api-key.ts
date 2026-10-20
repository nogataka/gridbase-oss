import { randomBytes } from 'crypto';

/** トンネルクライアント認証用 API キー。256bit の暗号論的乱数（`gb_` + base64url）。 */
export function generateApiKey(): string {
  return `gb_${randomBytes(32).toString('base64url')}`;
}
