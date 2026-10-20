'use client';

import { createContext, useContext } from 'react';
import type { PublicConfig } from '@/lib/config';

// server component(app/layout.tsx)が実行時に読んだ設定を、ブラウザ側コンポーネントへ渡す。
// NEXT_PUBLIC_* を使わないので、ビルド後に環境変数で変更できる。
const defaults: PublicConfig = {
  signupEnabled: false,
  passwordResetEnabled: false,
  baseDomain: '',
  tunnelServer: '',
  appUrl: '',
};

const PublicConfigContext = createContext<PublicConfig>(defaults);

export const PublicConfigProvider = PublicConfigContext.Provider;

export const usePublicConfig = (): PublicConfig => useContext(PublicConfigContext);
