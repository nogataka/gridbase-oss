'use client';
import { Button } from '@mantine/core';
import { signOut } from 'next-auth/react';

export const SignOutButton = () => (
  <Button
    onClick={() => signOut({ callbackUrl: '/signin' })}
    style={{ color: '#808080', backgroundColor: 'transparent', borderColor: '#808080' }}
  >
    ログアウト
  </Button>
);
