// app/Providers.tsx
'use client';
import { Notifications } from '@mantine/notifications';
import { SessionProvider } from 'next-auth/react';
import RootStyleRegistry from './emotion';
import { PublicConfigProvider } from './PublicConfig';
import type { PublicConfig } from '@/lib/config';

export default function Providers({
  children,
  config,
}: {
  children: React.ReactNode;
  config: PublicConfig;
}) {
  return (
    <RootStyleRegistry>
      <Notifications position="top-right" />
      <PublicConfigProvider value={config}>
        <SessionProvider>{children}</SessionProvider>
      </PublicConfigProvider>
    </RootStyleRegistry>
  );
}
