"use client";

import { useEffect, useState, useCallback } from 'react';
import {
  Title,
  Paper,
  Table,
  Text,
  Loader,
  Stack,
  Badge,
  Group,
  Button,
  Modal,
  TextInput,
  ActionIcon,
  Tooltip,
  Box,
  Card,
  MediaQuery,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { SimpleLayout } from "@/shared/components/SimpleLayout";
import { useSession } from 'next-auth/react';
import {
  IconSearch,
  IconTrash,
  IconEye,
  IconToggleLeft,
  IconToggleRight,
  IconShield,
  IconShieldOff,
} from '@tabler/icons-react';

interface AdminUser {
  userId: string;
  status: string;
  role: string;
  display_name: string | null;
  created_at: string;
  last_login_at: string | null;
  email: string | null;
}

interface SubdomainInfo {
  id: string;
  subdomain: string;
  mode: string;
  status: string;
  local_server_url: string;
  local_host: string | null;
  local_port: number | null;
  created_at: string;
}

const AdminPage = () => {
  const { data: session, status } = useSession();
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [loading, setLoading] = useState(true);
  const [searchQuery, setSearchQuery] = useState('');
  const [deleteModalUser, setDeleteModalUser] = useState<AdminUser | null>(null);
  const [subdomainModalUser, setSubdomainModalUser] = useState<AdminUser | null>(null);
  const [subdomains, setSubdomains] = useState<SubdomainInfo[]>([]);
  const [subdomainsLoading, setSubdomainsLoading] = useState(false);

  const fetchUsers = useCallback(async (query?: string) => {
    try {
      const params = new URLSearchParams();
      if (query) params.set('q', query);
      const res = await fetch(`/api/admin/users?${params.toString()}`);
      if (!res.ok) throw new Error('Failed to fetch users');
      const data = await res.json();
      setUsers(data.users);
    } catch (error) {
      notifications.show({
        title: 'エラー',
        message: 'ユーザー一覧の取得に失敗しました',
        color: 'red',
      });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (session?.user?.role === 'admin') {
      fetchUsers();
    }
  }, [session, fetchUsers]);

  const handleSearch = () => {
    setLoading(true);
    fetchUsers(searchQuery);
  };

  const handleSearchKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      handleSearch();
    }
  };

  const handleToggleStatus = async (user: AdminUser) => {
    const newStatus = user.status === 'active' ? 'inactive' : 'active';
    const label = newStatus === 'active' ? 'アクティブ' : '非アクティブ';
    if (!window.confirm(`${user.email || user.userId} のステータスを「${label}」に変更しますか？`)) return;

    try {
      const res = await fetch(`/api/admin/users/${encodeURIComponent(user.userId)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: newStatus }),
      });
      if (!res.ok) throw new Error('Failed to update user');
      notifications.show({
        title: '成功',
        message: `ステータスを「${label}」に変更しました`,
        color: 'green',
      });
      fetchUsers(searchQuery);
    } catch (error) {
      notifications.show({
        title: 'エラー',
        message: 'ステータスの変更に失敗しました',
        color: 'red',
      });
    }
  };

  const handleToggleRole = async (user: AdminUser) => {
    const newRole = user.role === 'admin' ? 'user' : 'admin';
    const label = newRole === 'admin' ? '管理者' : '一般ユーザー';
    if (!window.confirm(`${user.email || user.userId} のロールを「${label}」に変更しますか？`)) return;

    try {
      const res = await fetch(`/api/admin/users/${encodeURIComponent(user.userId)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: newRole }),
      });
      if (!res.ok) throw new Error('Failed to update role');
      notifications.show({ title: '成功', message: `ロールを「${label}」に変更しました`, color: 'green' });
      fetchUsers(searchQuery);
    } catch (error) {
      notifications.show({ title: 'エラー', message: 'ロールの変更に失敗しました', color: 'red' });
    }
  };

  const handleDelete = async () => {
    if (!deleteModalUser) return;
    try {
      const res = await fetch(`/api/admin/users/${encodeURIComponent(deleteModalUser.userId)}`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
      });
      if (!res.ok) {
        const data = await res.json();
        throw new Error(data.error || 'Failed to delete user');
      }
      notifications.show({
        title: '成功',
        message: 'ユーザーを削除しました',
        color: 'green',
      });
      setDeleteModalUser(null);
      fetchUsers(searchQuery);
    } catch (error) {
      notifications.show({
        title: 'エラー',
        message: error instanceof Error ? error.message : 'ユーザーの削除に失敗しました',
        color: 'red',
      });
    }
  };

  const handleViewSubdomains = async (user: AdminUser) => {
    setSubdomainModalUser(user);
    setSubdomainsLoading(true);
    try {
      const res = await fetch(`/api/admin/users/${encodeURIComponent(user.userId)}/subdomains`);
      if (!res.ok) throw new Error('Failed to fetch subdomains');
      const data = await res.json();
      setSubdomains(data.subdomains);
    } catch (error) {
      notifications.show({
        title: 'エラー',
        message: 'サブドメイン情報の取得に失敗しました',
        color: 'red',
      });
    } finally {
      setSubdomainsLoading(false);
    }
  };

  const formatDate = (dateStr: string | null) => {
    if (!dateStr) return '-';
    return new Date(dateStr).toLocaleDateString('ja-JP');
  };

  // 自分自身の降格・無効化・削除はサーバー側でも拒否される（UI では非表示）
  const UserActions = ({ user }: { user: AdminUser }) => (
    <>
      <Tooltip label={user.role === 'admin' ? '管理者を解除' : '管理者にする'}>
        <ActionIcon variant="outline" color="violet" onClick={() => handleToggleRole(user)}>
          {user.role === 'admin' ? <IconShieldOff size={16} /> : <IconShield size={16} />}
        </ActionIcon>
      </Tooltip>
      <Tooltip label={user.status === 'active' ? '非アクティブにする' : 'アクティブにする'}>
        <ActionIcon
          variant="outline"
          color={user.status === 'active' ? 'orange' : 'green'}
          onClick={() => handleToggleStatus(user)}
        >
          {user.status === 'active' ? <IconToggleRight size={16} /> : <IconToggleLeft size={16} />}
        </ActionIcon>
      </Tooltip>
      <Tooltip label="削除">
        <ActionIcon variant="outline" color="red" onClick={() => setDeleteModalUser(user)}>
          <IconTrash size={16} />
        </ActionIcon>
      </Tooltip>
    </>
  );

  const UserCard = ({ user }: { user: AdminUser }) => (
    <Card shadow="sm" p="md" radius="md" withBorder>
      <Stack spacing="xs">
        <Group position="apart">
          <Text size="sm" weight={500}>メール:</Text>
          <Text size="sm" lineClamp={1}>{user.email || '-'}</Text>
        </Group>
        <Group position="apart">
          <Text size="sm" weight={500}>表示名:</Text>
          <Text size="sm">{user.display_name || '-'}</Text>
        </Group>
        <Group position="apart">
          <Text size="sm" weight={500}>ロール:</Text>
          <Badge color={user.role === 'admin' ? 'violet' : 'blue'} variant="light">
            {user.role}
          </Badge>
        </Group>
        <Group position="apart">
          <Text size="sm" weight={500}>ステータス:</Text>
          <Badge color={user.status === 'active' ? 'green' : 'red'} variant="light">
            {user.status}
          </Badge>
        </Group>
        <Group position="apart">
          <Text size="sm" weight={500}>作成日:</Text>
          <Text size="sm">{formatDate(user.created_at)}</Text>
        </Group>
        <Group position="apart">
          <Text size="sm" weight={500}>最終ログイン:</Text>
          <Text size="sm">{formatDate(user.last_login_at)}</Text>
        </Group>
        <Group spacing="xs" position="right" mt="sm">
          <Tooltip label="サブドメイン確認">
            <ActionIcon variant="outline" color="blue" onClick={() => handleViewSubdomains(user)}>
              <IconEye size={16} />
            </ActionIcon>
          </Tooltip>
          {user.userId !== session?.user?.uid && <UserActions user={user} />}
        </Group>
      </Stack>
    </Card>
  );

  const DesktopView = () => (
    <Paper shadow="xs" p="md">
      <Table striped highlightOnHover>
        <thead>
          <tr>
            <th>メールアドレス</th>
            <th>表示名</th>
            <th>ロール</th>
            <th>ステータス</th>
            <th>作成日</th>
            <th>最終ログイン</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          {users.map((user) => (
            <tr key={user.userId}>
              <td style={{ maxWidth: '250px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {user.email || '-'}
              </td>
              <td>{user.display_name || '-'}</td>
              <td>
                <Badge color={user.role === 'admin' ? 'violet' : 'blue'} variant="light">
                  {user.role}
                </Badge>
              </td>
              <td>
                <Badge color={user.status === 'active' ? 'green' : 'red'} variant="light">
                  {user.status}
                </Badge>
              </td>
              <td>{formatDate(user.created_at)}</td>
              <td>{formatDate(user.last_login_at)}</td>
              <td>
                <Group spacing="xs">
                  <Tooltip label="サブドメイン確認">
                    <ActionIcon variant="outline" color="blue" onClick={() => handleViewSubdomains(user)}>
                      <IconEye size={16} />
                    </ActionIcon>
                  </Tooltip>
                  {user.userId !== session?.user?.uid && <UserActions user={user} />}
                </Group>
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
    </Paper>
  );

  const MobileView = () => (
    <Stack spacing="md">
      {users.map((user) => (
        <UserCard key={user.userId} user={user} />
      ))}
    </Stack>
  );

  if (status === "loading" || loading) {
    return (
      <SimpleLayout session={session}>
        <Group position="center">
          <Loader size="xl" />
        </Group>
      </SimpleLayout>
    );
  }

  if (!session?.user || session.user.role !== 'admin') {
    return (
      <SimpleLayout session={session}>
        <Text align="center" size="xl" weight={700} color="red">
          アクセス権限がありません
        </Text>
      </SimpleLayout>
    );
  }

  return (
    <SimpleLayout session={session}>
      <Title order={2} mb="lg">ユーザー管理</Title>

      {/* 検索バー */}
      <Paper shadow="xs" p="md" mb="lg">
        <Group>
          <TextInput
            placeholder="メールアドレス・表示名で検索"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.currentTarget.value)}
            onKeyDown={handleSearchKeyDown}
            icon={<IconSearch size={16} />}
            style={{ flex: 1 }}
          />
          <Button onClick={handleSearch}>検索</Button>
        </Group>
      </Paper>

      {/* ユーザー数 */}
      <Text size="sm" color="dimmed" mb="md">
        {users.length} 件のユーザー
      </Text>

      {users.length === 0 ? (
        <Text align="center" color="dimmed" py="xl">
          ユーザーが見つかりません
        </Text>
      ) : (
        <>
          <MediaQuery smallerThan="md" styles={{ display: 'none' }}>
            <Box>
              <DesktopView />
            </Box>
          </MediaQuery>
          <MediaQuery largerThan="md" styles={{ display: 'none' }}>
            <Box>
              <MobileView />
            </Box>
          </MediaQuery>
        </>
      )}

      {/* 削除確認モーダル */}
      <Modal
        opened={!!deleteModalUser}
        onClose={() => setDeleteModalUser(null)}
        title="ユーザー削除の確認"
        size="sm"
      >
        <Stack>
          <Text>
            以下のユーザーを削除しますか？この操作は取り消せません。
          </Text>
          <Text weight={500}>
            {deleteModalUser?.email || deleteModalUser?.userId}
          </Text>
          <Text size="sm" color="dimmed">
            関連するサブドメインとAPI Keyも全て削除されます。
          </Text>
          <Group position="right" mt="md">
            <Button variant="outline" onClick={() => setDeleteModalUser(null)}>
              キャンセル
            </Button>
            <Button color="red" onClick={handleDelete}>
              削除する
            </Button>
          </Group>
        </Stack>
      </Modal>

      {/* サブドメイン確認モーダル */}
      <Modal
        opened={!!subdomainModalUser}
        onClose={() => { setSubdomainModalUser(null); setSubdomains([]); }}
        title={`サブドメイン情報 - ${subdomainModalUser?.email || subdomainModalUser?.userId || ''}`}
        size="lg"
      >
        {subdomainsLoading ? (
          <Group position="center" py="xl">
            <Loader />
          </Group>
        ) : subdomains.length === 0 ? (
          <Text align="center" color="dimmed" py="xl">
            サブドメインが登録されていません
          </Text>
        ) : (
          <Stack spacing="md">
            {subdomains.map((sd) => (
              <Card key={sd.id} shadow="xs" p="sm" radius="md" withBorder>
                <Stack spacing="xs">
                  <Group position="apart">
                    <Text size="sm" weight={500}>サブドメイン:</Text>
                    <Text size="sm">{sd.subdomain}</Text>
                  </Group>
                  <Group position="apart">
                    <Text size="sm" weight={500}>モード:</Text>
                    <Badge color={sd.mode === 'tcp' ? 'violet' : 'blue'} variant="light">
                      {sd.mode.toUpperCase()}
                    </Badge>
                  </Group>
                  <Group position="apart">
                    <Text size="sm" weight={500}>接続先:</Text>
                    <Text size="sm">
                      {sd.mode === 'tcp'
                        ? `${sd.local_host || ''}:${sd.local_port || ''}`
                        : sd.local_server_url || '-'}
                    </Text>
                  </Group>
                  <Group position="apart">
                    <Text size="sm" weight={500}>ステータス:</Text>
                    <Badge color={sd.status === 'active' ? 'green' : 'red'} variant="light">
                      {sd.status}
                    </Badge>
                  </Group>
                  <Group position="apart">
                    <Text size="sm" weight={500}>作成日:</Text>
                    <Text size="sm">{formatDate(sd.created_at)}</Text>
                  </Group>
                </Stack>
              </Card>
            ))}
          </Stack>
        )}
      </Modal>
    </SimpleLayout>
  );
};

export default AdminPage;
