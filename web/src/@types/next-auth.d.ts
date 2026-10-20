import { DefaultSession } from 'next-auth';

declare module 'next-auth' {
  interface User {
    role: string;
    /** password_changed_at（エポック ms。未変更なら 0）。ログイン時点の値 */
    pwdAt: number;
  }
  interface Session {
    user: {
      uid: string;
      role: string;
      pwdAt: number;
    } & DefaultSession['user'];
  }
}

declare module 'next-auth/jwt' {
  interface JWT {
    uid: string;
    role: string;
    pwdAt: number;
    checkedAt?: number;
    inactive?: boolean;
  }
}
