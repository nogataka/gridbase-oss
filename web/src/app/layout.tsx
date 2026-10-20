// app/layout.tsx
import '@/styles/globals.css';
import type { Metadata, Viewport } from 'next';
import Providers from './Providers';
import { getPublicConfig } from '@/lib/config';

// 設定は実行時の環境変数から読むため、ビルド時に静的化しない
export const dynamic = 'force-dynamic';

export const metadata: Metadata = {
  title: {
    default: 'GridBase',
    template: '%s | GridBase',
  },
  description: 'GridBase admin console',
  // セルフホストの管理画面なので検索エンジンには載せない
  robots: { index: false, follow: false },
  icons: {
    icon: '/images/logo_new.png',
    apple: [{ url: '/images/logo_new.png' }],
  },
};

export const viewport: Viewport = {
  width: 'device-width',
  initialScale: 1,
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  const config = getPublicConfig();
  return (
    <html lang="ja" suppressHydrationWarning>
      <body suppressHydrationWarning>
        <Providers config={config}>{children}</Providers>
      </body>
    </html>
  );
}
