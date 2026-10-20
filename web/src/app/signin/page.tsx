// app/signin/page.tsx
'use client';
import { Suspense, useEffect, useState } from 'react';
import { Button, Card, Container, Flex, PasswordInput, Text, TextInput } from '@mantine/core';
import { useForm } from '@mantine/form';
import { notifications } from '@mantine/notifications';
import { signIn, useSession } from 'next-auth/react';
import { useRouter, useSearchParams } from 'next/navigation';
import Link from 'next/link';
import { SimpleLayout } from '@/shared/components/SimpleLayout';
import { usePublicConfig } from '@/app/PublicConfig';
import { safeCallbackPath } from '@/lib/callback-url';

function SignInForm() {
  const [isLoading, setIsLoading] = useState(false);
  const router = useRouter();
  const searchParams = useSearchParams();
  const { signupEnabled, passwordResetEnabled } = usePublicConfig();

  const form = useForm({
    initialValues: { email: '', password: '' },
    validate: {
      email: (value) => (/^\S+@\S+$/.test(value) ? null : '無効なメールアドレスです'),
      password: (value) => (value ? null : '入力してください'),
    },
  });

  const handleSubmit = async (values: { email: string; password: string }) => {
    setIsLoading(true);
    try {
      const result = await signIn('credentials', {
        email: values.email,
        password: values.password,
        redirect: false,
      });

      if (result?.error) {
        notifications.show({
          color: 'red',
          message:
            result.error === 'RATE_LIMITED'
              ? '試行回数が多すぎます。しばらく待ってから再度お試しください。'
              : 'ログインに失敗しました。メールアドレスとパスワードを確認してください。',
        });
        return;
      }
      if (result?.ok) {
        // 遷移先は、URL として解決した結果が同一オリジンのパスのときだけ使う（オープンリダイレクト防止）
        router.push(safeCallbackPath(searchParams?.get('callbackUrl'), window.location.origin));
      }
    } catch {
      notifications.show({ color: 'red', message: 'エラーが発生しました。しばらく待ってから再度お試しください。' });
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <>
      <Card w={{ base: 300, sm: 320 }} withBorder shadow="sm" radius="md" style={{ marginBottom: '20px' }}>
        <Card.Section withBorder inheritPadding py="xs">
          <form onSubmit={form.onSubmit(handleSubmit)}>
            <TextInput label="メールアドレス" mb="lg" required type="text" {...form.getInputProps('email')} />
            <PasswordInput label="パスワード" mb="lg" required {...form.getInputProps('password')} />
            <Flex justify="center" align="center">
              <Button w="100%" type="submit" loading={isLoading}>
                ログイン
              </Button>
            </Flex>
          </form>
        </Card.Section>
      </Card>

      {passwordResetEnabled ? (
        <Link href="/reset-password">パスワードを忘れた場合</Link>
      ) : (
        <Text size="sm" color="dimmed" align="center">
          パスワードを忘れた場合は、管理者に連絡してください。
        </Text>
      )}
      {signupEnabled && (
        <Text size="sm" mt="sm" align="center">
          アカウントをお持ちでない場合は <Link href="/signup">新規登録</Link>
        </Text>
      )}
    </>
  );
}

export default function SignIn() {
  const { data: session } = useSession();
  const router = useRouter();

  useEffect(() => {
    if (session?.user?.uid) router.push('/dashboard');
  }, [session, router]);

  return (
    <SimpleLayout session={session}>
      <Container pt={48} pb="xl">
        <Flex direction="column" align="center">
          <Suspense fallback={null}>
            <SignInForm />
          </Suspense>
        </Flex>
      </Container>
    </SimpleLayout>
  );
}
