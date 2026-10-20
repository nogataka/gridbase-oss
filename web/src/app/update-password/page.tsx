// app/update-password/page.tsx
// URL クエリパラメータ ?token=... でリセットトークンを受け取り、/api/update-password へ POST する。
'use client';
import { useState, useEffect } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { PasswordInput, Button, Paper, Title, Container, Alert } from '@mantine/core';
import { IconAlertCircle, IconCheck } from '@tabler/icons-react';
import { SimpleLayout } from "@/shared/components/SimpleLayout";
import { useSession } from 'next-auth/react';
import { Suspense } from 'react';

function UpdatePasswordForm() {
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [token, setToken] = useState('');
  const router = useRouter();
  const searchParams = useSearchParams();

  useEffect(() => {
    const t = searchParams.get('token') || '';
    if (!t) {
      setError('リセットトークンが見つかりません。パスワードリセットメールのリンクから再度アクセスしてください。');
    }
    setToken(t);
  }, [searchParams]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);
    setMessage('');
    setError('');

    if (password !== confirmPassword) {
      setError('パスワードが一致しません。');
      setLoading(false);
      return;
    }

    if (!token) {
      setError('リセットトークンが見つかりません。');
      setLoading(false);
      return;
    }

    try {
      const res = await fetch('/api/update-password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token, password }),
      });

      const data = await res.json();

      if (!res.ok) {
        setError(data.error || 'パスワードの更新に失敗しました。');
        return;
      }

      setMessage('パスワードを更新しました。ログインページへ移動します。');
      setTimeout(() => router.push('/signin'), 2000);
    } catch {
      setError('パスワードの更新に失敗しました。');
    } finally {
      setLoading(false);
    }
  };

  return (
    <Container size="xs" px="xs">
      <Paper radius="md" p="xl" withBorder mt={50}>
        <Title order={2} align="center" mb="lg">
          新しいパスワードの設定
        </Title>

        <form onSubmit={handleSubmit}>
          <PasswordInput
            label="新しいパスワード"
            placeholder="新しいパスワード"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            mb="md"
            minLength={8}
          />

          <PasswordInput
            label="パスワードの確認"
            placeholder="パスワードを再入力"
            required
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            mb="md"
            minLength={8}
          />

          <Button
            fullWidth
            type="submit"
            loading={loading}
            loaderPosition="center"
            disabled={!token}
          >
            パスワードを更新
          </Button>

          {message && (
            <Alert
              icon={<IconCheck size={16} />}
              title="成功"
              color="green"
              mt="md"
            >
              {message}
            </Alert>
          )}

          {error && (
            <Alert
              icon={<IconAlertCircle size={16} />}
              title="エラー"
              color="red"
              mt="md"
            >
              {error}
            </Alert>
          )}
        </form>
      </Paper>
    </Container>
  );
}

export default function UpdatePassword() {
  const { data: session } = useSession();

  return (
    <SimpleLayout session={session}>
      <Suspense fallback={null}>
        <UpdatePasswordForm />
      </Suspense>
    </SimpleLayout>
  );
}
