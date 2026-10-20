"use client";

import { useState } from 'react';
import { 
  TextInput, 
  PasswordInput, 
  Paper, 
  Title, 
  Container, 
  Button, 
  Stack,
  Text,
  Box,
  Alert
} from '@mantine/core';
import { useForm } from '@mantine/form';
import { notifications } from '@mantine/notifications';
import { useRouter } from 'next/navigation';
import { useSession } from 'next-auth/react';
import { SimpleLayout } from "@/shared/components/SimpleLayout";
import { usePublicConfig } from '@/app/PublicConfig';

interface SignupForm {
  email: string;
  password: string;
  confirmPassword: string;
  name: string;
}

const Signup = () => {
  const [loading, setLoading] = useState(false);
  const router = useRouter();
  const { data: session } = useSession();
  const { signupEnabled } = usePublicConfig();
  const form = useForm<SignupForm>({
    initialValues: {
      email: '',
      password: '',
      confirmPassword: '',
      name: '',
    },
    validate: {
      email: (value) => (/^\S+@\S+$/.test(value) ? null : '有効なメールアドレスを入力してください'),
      password: (value) => (
        value.length < 8 
          ? 'パスワードは8文字以上である必要があります'
          : !value.match(/^(?=.*[A-Za-z])(?=.*\d)/)
          ? 'パスワードは少なくとも1つの文字と数字を含む必要があります'
          : null
      ),
      confirmPassword: (value, values) => 
        value !== values.password ? 'パスワードが一致しません' : null,
      name: (value) => 
        value.length < 2 || value.length > 50 ? '名前は2〜50文字で入力してください' : null,
    },
  });

  const handleSubmit = async (values: SignupForm) => {
    setLoading(true);
    try {
      const response = await fetch('/api/signup', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({
          email: values.email,
          password: values.password,
          name: values.name,
        }),
      });
  
      const result = await response.json();
  
      if (response.ok) {
        notifications.show({
          title: '成功',
          message: 'アカウントが作成されました',
          color: 'green',
        });
        form.reset();
        router.push('/signin');
      } else {
        throw new Error(result.error);
      }
    } catch (error) {
      notifications.show({
        title: 'エラー',
        message: error instanceof Error ? error.message : '登録に失敗しました',
        color: 'red',
      });
    } finally {
      setLoading(false);
    }
  };

  if (!signupEnabled) {
    return (
      <SimpleLayout session={session}>
        <Container size={420} my={40}>
          <Title align="center" order={2}>
            新規登録は受け付けていません
          </Title>
          <Text color="dimmed" size="sm" align="center" mt="md">
            このサーバーでは現在アカウントの新規登録が無効です。管理者にアカウント作成を依頼してください。
          </Text>
          <Text size="sm" align="center" mt="md">
            <Text component="a" href="/signin" size="sm" color="blue">
              ログインへ戻る
            </Text>
          </Text>
        </Container>
      </SimpleLayout>
    );
  }

  return (
    <SimpleLayout session={session}>
    <Container size={420} my={40}>
      <Title
        align="center"
        sx={(theme) => ({ 
          fontFamily: `Greycliff CF, ${theme.fontFamily}`,
          fontWeight: 900 
        })}
      >
        新規アカウント登録
      </Title>
      <Text color="dimmed" size="sm" align="center" mt={5}>
        既にアカウントをお持ちですか?{' '}
        <Text component="a" href="/signin" size="sm" color="blue">
          ログイン
        </Text>
      </Text>

      <Paper withBorder shadow="md" p={30} mt={30} radius="md">
        <form onSubmit={form.onSubmit(handleSubmit)}>
          <Stack>
          <TextInput
            label="表示名"
            placeholder="Your name"
            required
            {...form.getInputProps('name')}
          />

            <TextInput
              label="メールアドレス"
              placeholder="you@example.com"
              required
              {...form.getInputProps('email')}
            />

            <PasswordInput
              label="パスワード"
              placeholder="Your password"
              required
              {...form.getInputProps('password')}
            />

            <PasswordInput
              label="パスワード（確認）"
              placeholder="Confirm your password"
              required
              {...form.getInputProps('confirmPassword')}
            />

            <Box mt="md">
              <Alert title="パスワードの要件" color="blue" radius="md">
                <Text size="sm">
                  パスワードは以下の要件を満たす必要があります：
                  <ul style={{ margin: '8px 0', paddingLeft: '20px' }}>
                    <li>8文字以上</li>
                    <li>少なくとも1つの文字</li>
                    <li>少なくとも1つの数字</li>
                  </ul>
                </Text>
              </Alert>
            </Box>

            <Button type="submit" loading={loading}>
              登録
            </Button>
          </Stack>
        </form>
      </Paper>
    </Container>
    </SimpleLayout>
  );
};

export default Signup;