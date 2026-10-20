'use client';
import { useState } from 'react';
import Link from 'next/link';
import {
  TextInput,
  Button,
  Paper,
  Title,
  Container,
  Alert,
} from '@mantine/core';
import { IconAlertCircle, IconCheck } from '@tabler/icons-react';
import { usePublicConfig } from '@/app/PublicConfig';

export default function ResetPassword() {
  const [email, setEmail] = useState('');
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const { passwordResetEnabled } = usePublicConfig();

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);
    setMessage('');
    setError('');

    try {
      const res = await fetch('/api/reset-password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email }),
      });

      if (res.ok) {
        // ユーザー列挙防止のため、登録の有無にかかわらず同じメッセージを表示
        setMessage('該当するアカウントがある場合、パスワードリセットのメールを送信しました。メールをご確認ください。');
      } else if (res.status === 503) {
        setError('メール送信(SMTP)が設定されていないため、パスワードリセットは利用できません。管理者に連絡してください。');
      } else if (res.status === 429) {
        setError('リクエストが多すぎます。しばらくしてから再度お試しください。');
      } else {
        setError('パスワードリセットメールの送信に失敗しました。');
      }
    } catch {
      setError('パスワードリセットメールの送信に失敗しました。');
    } finally {
      setLoading(false);
    }
  };

  if (!passwordResetEnabled) {
    return (
      <Container size="xs" px="xs">
        <Paper radius="md" p="xl" withBorder mt={50}>
          <Title order={2} align="center" mb="lg">
            パスワードリセット
          </Title>
          <Alert icon={<IconAlertCircle size={16} />} title="利用できません" color="yellow">
            このサーバーではメール送信(SMTP)が設定されていないため、パスワードリセットは利用できません。
            管理者に連絡してパスワードを再設定してもらってください。
          </Alert>
          <Link href="/signin" style={{ display: 'block', marginTop: 16 }}>
            ログインへ戻る
          </Link>
        </Paper>
      </Container>
    );
  }

  return (
    <Container size="xs" px="xs">
      <Paper radius="md" p="xl" withBorder mt={50}>
        <Title order={2} align="center" mb="lg">
          パスワードリセット
        </Title>

        <form onSubmit={handleSubmit}>
          <TextInput
            label="メールアドレス"
            placeholder="your@email.com"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            mb="md"
          />

          <Button
            fullWidth
            type="submit"
            loading={loading}
            loaderPosition="center"
          >
            リセットメールを送信
          </Button>

          {message && (
            <Alert
              icon={<IconCheck size={16} />}
              title="送信完了"
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
