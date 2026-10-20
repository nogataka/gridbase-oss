// app/account/page.tsx — ログイン中のユーザーが自分のパスワードを変更する。
// 変更に成功すると、他の端末・ブラウザのセッションはすべて無効になる。
// この画面のセッションも古い扱いになるため、新しいパスワードで自動的に再ログインして継続する。
'use client';
import { useState } from 'react';
import { Alert, Button, Container, Paper, PasswordInput, Text, Title } from '@mantine/core';
import { IconAlertCircle, IconCheck } from '@tabler/icons-react';
import { signIn, useSession } from 'next-auth/react';
import { SimpleLayout } from '@/shared/components/SimpleLayout';

export default function AccountPage() {
  const { data: session } = useSession();
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setMessage('');
    setError('');

    if (newPassword !== confirmPassword) {
      setError('新しいパスワードが一致しません。');
      return;
    }
    const email = session?.user?.email;
    if (!email) {
      setError('セッションを確認できません。ログインし直してください。');
      return;
    }

    setLoading(true);
    try {
      const res = await fetch('/api/account/password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ currentPassword, newPassword }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        setError(data.error || 'パスワードの変更に失敗しました。');
        return;
      }

      // このブラウザのセッションも変更前のものなので、新しいパスワードで入り直して継続する
      const relogin = await signIn('credentials', { email, password: newPassword, redirect: false });
      setCurrentPassword('');
      setNewPassword('');
      setConfirmPassword('');
      if (relogin?.ok) {
        setMessage(data.message || 'パスワードを変更しました。');
      } else {
        setMessage('パスワードを変更しました。新しいパスワードでログインし直してください。');
      }
    } catch {
      setError('パスワードの変更に失敗しました。');
    } finally {
      setLoading(false);
    }
  };

  return (
    <SimpleLayout session={session}>
      <Container size="xs" px="xs">
        <Paper radius="md" p="xl" withBorder mt="md">
          <Title order={2} align="center" mb="xs">
            パスワードの変更
          </Title>
          <Text size="sm" color="dimmed" align="center" mb="lg">
            {session?.user?.email}
          </Text>

          <form onSubmit={handleSubmit}>
            <PasswordInput
              label="現在のパスワード"
              required
              autoComplete="current-password"
              value={currentPassword}
              onChange={(e) => setCurrentPassword(e.target.value)}
              mb="md"
            />
            <PasswordInput
              label="新しいパスワード"
              description="8文字以上で、英字と数字を含めてください"
              required
              autoComplete="new-password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              mb="md"
              minLength={8}
            />
            <PasswordInput
              label="新しいパスワード（確認）"
              required
              autoComplete="new-password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              mb="md"
              minLength={8}
            />

            <Button fullWidth type="submit" loading={loading} loaderPosition="center">
              パスワードを変更
            </Button>

            {message && (
              <Alert icon={<IconCheck size={16} />} title="成功" color="green" mt="md">
                {message}
              </Alert>
            )}
            {error && (
              <Alert icon={<IconAlertCircle size={16} />} title="エラー" color="red" mt="md">
                {error}
              </Alert>
            )}
          </form>

          <Text size="xs" color="dimmed" mt="lg">
            変更すると、他の端末・ブラウザでログイン中のセッションはすべて無効になります。
          </Text>
        </Paper>
      </Container>
    </SimpleLayout>
  );
}
