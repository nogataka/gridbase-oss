'use client';

import {
  Button,
  Container,
  Group,
  Header,
  MediaQuery,
  Burger,
  useMantineTheme,
  Drawer,
  Stack,
  Text,
  Divider,
  UnstyledButton,
  Box,
} from "@mantine/core";
import Link from 'next/link';
import type { Session } from 'next-auth';
import { useDisclosure } from '@mantine/hooks';
import { SignOutButton } from "@/shared/components/SignOutButton";
import { usePublicConfig } from "@/app/PublicConfig";

type Props = {
  session: Session | null;
};

export const AppHeader = ({ session }: Props) => {
  const theme = useMantineTheme();
  const [opened, { toggle, close }] = useDisclosure(false);
  const { signupEnabled } = usePublicConfig();

  const navigationItems = [
    ...(session ? [{ label: 'ダッシュボード', href: '/dashboard' }, { label: 'アカウント', href: '/account' }] : []),
    ...(session?.user?.role === 'admin' ? [{ label: '管理画面', href: '/admin' }] : []),
    { label: '使い方', href: '/docs' },
  ];

  // PCナビゲーション用のリンクコンポーネント
  const NavLink = ({ href, label }: { href: string; label: string }) => (
    <Link
      href={href}
      prefetch={false}
      style={{ 
        textDecoration: 'none',
        color: 'white',
        fontSize: '0.9rem',
        fontWeight: 500,
        whiteSpace: 'nowrap',
      }}
    >
      {label}
    </Link>
  );

  return (
    <>
      <Header 
        height={70} 
        sx={{
          backgroundColor: '#00023e',
          borderBottom: 'none',
        }}
      >
        <Container size="lg" h="100%">
          <Group position="apart" h="100%" noWrap>
            {/* 左側のグループ（ロゴとナビゲーション） */}
            <Group spacing="xl" noWrap>
              {/* ロゴとサイト名 */}
              <Group spacing="0" noWrap>
                <Link href="/" style={{ display: "flex", alignItems: "center" }}>
                  <img 
                    src="/images/logo_new.png" 
                    alt="gridbase" 
                    style={{ height: "16px", width: "auto", marginBottom: "2.5px", marginRight: "6px" }} 
                  />
                </Link>
                <Link 
                  href="/" 
                  style={{ 
                    textDecoration: 'none',
                    color: 'white',
                  }}
                >
                  <Text
                    weight={700}
                    size="lg"
                    sx={{
                      whiteSpace: 'nowrap',
                    }}
                  >
                    gridbase
                  </Text>
                </Link>
              </Group>
              
              {/* PCナビゲーション */}
              <MediaQuery smallerThan="md" styles={{ display: 'none' }}>
                <Group spacing={40} noWrap>
                  {navigationItems.map((item) => (
                    <NavLink key={item.href} {...item} />
                  ))}
                </Group>
              </MediaQuery>
            </Group>

            {/* 右側のグループ（ユーザー情報/ボタン） */}
            <Group noWrap>
              <MediaQuery smallerThan="md" styles={{ display: 'none' }}>
                <Group spacing="sm" noWrap>
                  {session ? (
                    <Group spacing="sm" noWrap>
                      <Text 
                        size="sm"
                        color="white"
                        lineClamp={1}
                      >
                        {session.user.email}
                      </Text>
                      <SignOutButton />
                    </Group>
                  ) : (
                    <Group spacing="sm" noWrap>
                      <Button
                        variant="outline"
                        radius="md"
                        sx={{ 
                          color: 'white',
                          borderColor: 'white',
                          '&:hover': {
                            backgroundColor: 'rgba(255, 255, 255, 0.1)',
                          }
                        }}
                        component={Link}
                        href="/signin"
                      >
                        ログイン
                      </Button>
                      {signupEnabled && (
                        <Button
                          variant="filled"
                          radius="md"
                          sx={{ 
                            backgroundColor: 'white',
                            color: theme.colors.dark,
                            '&:hover': {
                              backgroundColor: theme.colors.gray[0],
                            }
                          }}
                          component={Link}
                          href="/signup"
                        >
                          新規登録
                        </Button>
                      )}
                    </Group>
                  )}
                </Group>
              </MediaQuery>

              {/* ハンバーガーメニュー */}
              <MediaQuery largerThan="md" styles={{ display: 'none' }}>
                <Burger
                  opened={opened}
                  onClick={toggle}
                  size="sm"
                  color="white"
                />
              </MediaQuery>
            </Group>
          </Group>
        </Container>
      </Header>

      {/* モバイルドロワー */}
      <Drawer
        opened={opened}
        onClose={close}
        size="100%"
        padding="md"
        title={
          <Group spacing="xs" noWrap>
            <img 
              src="/images/logo_new.png" 
              alt="gridbase" 
              style={{ height: "32px", width: "auto" }} 
            />
            <Text weight={700} size="lg">gridbase</Text>
          </Group>
        }
        zIndex={1000}
      >
        <Stack spacing="xs">
          {/* ユーザー情報 */}
          {session && (
            <Box mb="md">
              <Text size="sm" color="dimmed">
                {session.user.email}
              </Text>
              <SignOutButton />
            </Box>
          )}

          <Divider />

          {/* ナビゲーションリンク */}
          {navigationItems.map((item) => (
            <UnstyledButton
              key={item.href}
              sx={(theme) => ({
                display: 'block',
                width: '100%',
                padding: theme.spacing.md,
                borderRadius: theme.radius.sm,
                color: theme.colors.gray[7],
                '&:hover': {
                  backgroundColor: theme.colors.gray[0],
                },
              })}
              onClick={close}
            >
              <Link 
                href={item.href}
                style={{ 
                  textDecoration: 'none',
                  color: 'inherit',
                  display: 'block',
                  width: '100%',
                }}
              >
                {item.label}
              </Link>
            </UnstyledButton>
          ))}

          {/* 未ログイン時のボタン */}
          {!session && (
            <>
              <Divider my="md" />
              <Button
                variant="subtle"
                fullWidth
                component={Link}
                href="/signin"
                onClick={close}
                color="dark"
              >
                ログイン
              </Button>
              {signupEnabled && (
                <Button
                  fullWidth
                  component={Link}
                  href="/signup"
                  onClick={close}
                >
                  新規登録
                </Button>
              )}
            </>
          )}
        </Stack>
      </Drawer>
    </>
  );
};