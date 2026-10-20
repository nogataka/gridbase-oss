import { AppHeader } from '@/shared/components/AppHeader';
import { AppShell, Container } from '@mantine/core';
import { ReactNode } from 'react';
import type { Session } from 'next-auth';

type Props = {
  children: ReactNode;
  session: Session | null;
};

export const SimpleLayout = ({ children, session }: Props) => {
  return (
    <AppShell
      header={<AppHeader session={session?.user?.uid ? session : null} />}
      padding="md"
      styles={{
        main: {
          paddingTop: '90px', // ヘッダーの高さ(70px) + 余白(20px)
          minHeight: '100vh',
          backgroundColor: 'white',
        },
      }}
    >
      <Container size="lg" px="xl">
        {children}
      </Container>
    </AppShell>
  );
};
