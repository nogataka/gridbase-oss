"use client";

import { useCallback, useEffect, useState } from 'react';
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
  Select,
  ActionIcon,
  Tooltip,
  CopyButton,
  Box,
  Card,
  MediaQuery,
} from '@mantine/core';
import { useForm } from '@mantine/form';
import { notifications } from '@mantine/notifications';
import { useSession } from 'next-auth/react';
import { IconCheck, IconCopy } from '@tabler/icons-react';
import { SimpleLayout } from '@/shared/components/SimpleLayout';
import { usePublicConfig } from '@/app/PublicConfig';
import {
  RESERVED_SUBDOMAINS,
  SUBDOMAIN_FORMAT_MESSAGE,
  isValidSubdomainFormat,
  normalizeSubdomain,
} from '@/lib/subdomain-rules';

type TunnelMode = 'http' | 'tcp';

// トンネルサーバーは TCP の外部待受ポートを持たないため、TCP は選べない（API も 400 を返す）
const TUNNEL_MODE_OPTIONS = [
  { value: 'http', label: 'HTTP (ウェブ) トンネル' },
  { value: 'tcp', label: 'TCP トンネル（未対応: サーバー側未実装）', disabled: true },
];

interface OwnUser {
  userId: string;
  api_key: string | null;
  status: string;
  created_at: string;
  last_login_at: string | null;
}

interface Subdomain {
  id: string;
  subdomain: string;
  mode: TunnelMode;
  status: string;
  created_at: string;
  description: string | null;
  local_server_url: string;
  local_host: string | null;
  local_port: number | null;
}

interface SubdomainFormValues {
  mode: TunnelMode;
  local_server_url: string;
  status: string;
  description: string;
  customSubdomain: string;
}

const formatDate = (value: string | null | undefined) => (value ? new Date(value).toLocaleDateString() : '-');

const normalizeSubdomainRecord = (domain: Record<string, unknown>): Subdomain => ({
  id: String(domain.id ?? ''),
  subdomain: typeof domain.subdomain === 'string' ? domain.subdomain : '',
  mode: domain.mode === 'tcp' ? 'tcp' : 'http',
  status: typeof domain.status === 'string' ? domain.status : 'inactive',
  created_at: typeof domain.created_at === 'string' ? domain.created_at : '',
  description: typeof domain.description === 'string' ? domain.description : null,
  local_server_url: typeof domain.local_server_url === 'string' ? domain.local_server_url.trim() : '',
  local_host: typeof domain.local_host === 'string' ? domain.local_host.trim() : null,
  local_port: typeof domain.local_port === 'number' ? domain.local_port : null,
});

const buildEndpointLabel = (domain: Subdomain) => {
  if (domain.mode === 'tcp') {
    const host = (domain.local_host ?? '').trim();
    const port = domain.local_port ?? '';
    if (!host && !port) return '-';
    if (!host) return String(port);
    if (!port) return host;
    return `${host}:${port}`;
  }
  return domain.local_server_url || '-';
};

const getModeBadgeColor = (mode: TunnelMode) => (mode === 'tcp' ? 'violet' : 'blue');

const CopyIcon = ({ value }: { value: string }) => (
  <CopyButton value={value} timeout={2000}>
    {({ copied, copy }) => (
      <Tooltip label={copied ? 'コピーしました' : 'クリップボードにコピー'}>
        <ActionIcon color={copied ? 'teal' : 'gray'} onClick={copy} size="sm">
          {copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
        </ActionIcon>
      </Tooltip>
    )}
  </CopyButton>
);

const DashBoard = () => {
  const { data: session, status } = useSession();
  const { baseDomain, appUrl } = usePublicConfig();
  const [user, setUser] = useState<OwnUser | null>(null);
  const [subdomains, setSubdomains] = useState<Subdomain[]>([]);
  const [loading, setLoading] = useState(true);
  const [modalOpen, setModalOpen] = useState(false);
  const [editingSubdomain, setEditingSubdomain] = useState<Subdomain | null>(null);

  const scheme = appUrl.startsWith('https://') ? 'https' : 'http';
  const publicUrl = (sub: string) => (baseDomain ? `${scheme}://${sub}.${baseDomain}` : '');

  // サーバー側の許可リスト(lib/subdomain-input.ts)に合わせた送信項目のみを組み立てる
  const buildPayload = (values: SubdomainFormValues) => ({
    mode: 'http' as const,
    status: values.status.trim() || 'active',
    description: values.description.trim() || null,
    local_server_url: values.local_server_url.trim(),
  });

  const form = useForm<SubdomainFormValues>({
    initialValues: {
      mode: 'http',
      local_server_url: '',
      status: 'active',
      description: '',
      customSubdomain: '',
    },
    validate: {
      mode: (value) => (value === 'http' ? null : 'TCP トンネルは現在サポートされていません'),
      local_server_url: (value) => (value ? null : 'ローカルサーバーURLは必須です'),
      customSubdomain: (value) => {
        if (!value) return null;
        const normalized = normalizeSubdomain(value);
        if (!isValidSubdomainFormat(normalized)) return SUBDOMAIN_FORMAT_MESSAGE;
        if (RESERVED_SUBDOMAINS.has(normalized)) return '指定したサブドメインは使用できません';
        return null;
      },
    },
  });

  const fetchData = useCallback(async () => {
    try {
      const userRes = await fetch('/api/dashboard/user');
      if (!userRes.ok) throw new Error('Failed to fetch user data');
      const { user: userData } = await userRes.json();
      setUser(userData || null);

      const subRes = await fetch('/api/dashboard/subdomains');
      if (!subRes.ok) throw new Error('Failed to fetch subdomains');
      const { subdomains: subdomainData } = await subRes.json();
      setSubdomains((subdomainData || []).map(normalizeSubdomainRecord));
    } catch {
      notifications.show({ title: 'エラー', message: 'データの取得に失敗しました', color: 'red' });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (session?.user?.uid) fetchData();
  }, [session?.user?.uid, fetchData]);

  const handleGenerateApiKey = async () => {
    const message = user?.api_key
      ? 'API Key を再生成します。現在のキーは、トンネルサーバーが変更を読み込んだ時点（最大30秒ほど）で使えなくなり、接続中のクライアントは新しいキーで再起動が必要です。よろしいですか？'
      : 'API Key を生成してもよろしいですか？';
    if (!window.confirm(message)) return;

    try {
      const res = await fetch('/api/dashboard/api-key', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
      });
      if (!res.ok) throw new Error('API Key generation failed');
      notifications.show({ title: '成功', message: 'API Key を生成しました', color: 'green' });
      await fetchData();
    } catch {
      notifications.show({ title: 'エラー', message: 'API Key の生成に失敗しました', color: 'red' });
    }
  };

  const handleEdit = (domain: Subdomain) => {
    setEditingSubdomain(domain);
    form.reset();
    form.setValues({
      // 既存の TCP レコードを編集するときは HTTP に切り替える（TCP は未対応）
      mode: 'http',
      local_server_url: domain.local_server_url || '',
      status: domain.status,
      description: domain.description || '',
      customSubdomain: domain.subdomain,
    });
    setModalOpen(true);
  };

  const handleCloseModal = () => {
    setModalOpen(false);
    setEditingSubdomain(null);
    form.reset();
  };

  const openCreateModal = () => {
    setEditingSubdomain(null);
    form.reset();
    setModalOpen(true);
  };

  const handleSubmit = async (values: SubdomainFormValues) => {
    const editing = editingSubdomain;
    try {
      const payload = buildPayload(values);
      const body = editing
        ? { id: editing.id, ...payload }
        : { ...payload, subdomain: normalizeSubdomain(values.customSubdomain) || undefined };

      const res = await fetch('/api/dashboard/subdomains', {
        method: editing ? 'PATCH' : 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        throw new Error(data?.error || (editing ? 'サブドメインの更新に失敗しました' : 'サブドメインの作成に失敗しました'));
      }

      notifications.show({
        title: '成功',
        message: `サブドメインを${editing ? '更新' : '作成'}しました（モード: ${values.mode.toUpperCase()}）。トンネルへの反映には最大30秒ほどかかります。`,
        color: 'green',
      });
      handleCloseModal();
      await fetchData();
    } catch (error) {
      notifications.show({
        title: 'エラー',
        message: error instanceof Error ? error.message : '保存に失敗しました',
        color: 'red',
      });
    }
  };

  const handleDelete = async (id: string) => {
    if (!window.confirm('このサブドメインを削除してもよろしいですか？')) return;
    try {
      const res = await fetch(`/api/dashboard/subdomains?id=${encodeURIComponent(id)}`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
      });
      if (!res.ok) throw new Error('Delete failed');
      notifications.show({ title: '成功', message: 'サブドメインが削除されました', color: 'green' });
      await fetchData();
    } catch {
      notifications.show({ title: 'エラー', message: 'サブドメインの削除に失敗しました', color: 'red' });
    }
  };

  const apiKeyButtonLabel = user?.api_key ? 'API Key再生成' : 'API Key生成';

  const SubdomainActions = ({ domain }: { domain: Subdomain }) => (
    <Group spacing="xs">
      <Button size="xs" variant="outline" onClick={() => handleEdit(domain)}>
        編集
      </Button>
      <Button size="xs" variant="outline" color="red" onClick={() => handleDelete(domain.id)}>
        削除
      </Button>
    </Group>
  );

  const ApiKeyCard = ({ apiUser }: { apiUser: OwnUser }) => (
    <Card shadow="sm" p="md" radius="md" withBorder>
      <Stack spacing="xs">
        <Group position="apart" noWrap>
          <Text size="sm" weight={500}>API Key:</Text>
          <Group spacing={4}>
            <Text size="sm" style={{ maxWidth: '200px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {apiUser.api_key || '-'}
            </Text>
            {apiUser.api_key && <CopyIcon value={apiUser.api_key} />}
          </Group>
        </Group>
        <Group position="apart">
          <Text size="sm">ステータス:</Text>
          <Badge color={apiUser.status === 'active' ? 'green' : 'red'}>{apiUser.status}</Badge>
        </Group>
        <Group position="apart">
          <Text size="sm">作成日:</Text>
          <Text size="sm">{formatDate(apiUser.created_at)}</Text>
        </Group>
        <Group position="apart">
          <Text size="sm">最終ログイン:</Text>
          <Text size="sm">{formatDate(apiUser.last_login_at)}</Text>
        </Group>
      </Stack>
    </Card>
  );

  const SubdomainCard = ({ domain }: { domain: Subdomain }) => (
    <Card shadow="sm" p="md" radius="md" withBorder>
      <Stack spacing="xs">
        <Group position="apart" noWrap>
          <Text size="sm" weight={500}>サブドメイン:</Text>
          <Group spacing={4}>
            <Text size="sm">{domain.subdomain}</Text>
            <CopyIcon value={publicUrl(domain.subdomain) || domain.subdomain} />
          </Group>
        </Group>
        <Group position="apart">
          <Text size="sm">モード:</Text>
          <Badge color={getModeBadgeColor(domain.mode)}>{domain.mode.toUpperCase()}</Badge>
        </Group>
        <Group position="apart">
          <Text size="sm">接続先:</Text>
          <Text size="sm" style={{ maxWidth: '200px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {buildEndpointLabel(domain)}
          </Text>
        </Group>
        <Group position="apart">
          <Text size="sm">ステータス:</Text>
          <Badge color={domain.status === 'active' ? 'green' : 'red'}>{domain.status}</Badge>
        </Group>
        <Group position="apart">
          <Text size="sm">作成日:</Text>
          <Text size="sm">{formatDate(domain.created_at)}</Text>
        </Group>
        <Group spacing="xs" position="right" mt="sm">
          <SubdomainActions domain={domain} />
        </Group>
      </Stack>
    </Card>
  );

  const DesktopView = () => (
    <>
      <Paper shadow="xs" p="md" mb="xl">
        <Group position="apart" mb="md">
          <Title order={3}>API Key 情報</Title>
          <Button onClick={handleGenerateApiKey} variant="outline">{apiKeyButtonLabel}</Button>
        </Group>
        <Table striped highlightOnHover>
          <thead>
            <tr>
              <th>API Key</th>
              <th></th>
              <th>ステータス</th>
              <th>作成日</th>
              <th>最終ログイン</th>
            </tr>
          </thead>
          <tbody>
            {user && (
              <tr>
                <td style={{ maxWidth: '400px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', paddingRight: 0 }}>
                  {user.api_key || '-'}
                </td>
                <td style={{ width: '40px', paddingLeft: 0 }}>
                  {user.api_key && <CopyIcon value={user.api_key} />}
                </td>
                <td>
                  <Badge color={user.status === 'active' ? 'green' : 'red'}>{user.status}</Badge>
                </td>
                <td>{formatDate(user.created_at)}</td>
                <td>{formatDate(user.last_login_at)}</td>
              </tr>
            )}
          </tbody>
        </Table>
      </Paper>

      <Paper shadow="xs" p="md" mb="xl">
        <Group position="apart" mb="md">
          <Title order={3}>サブドメイン情報</Title>
          <Button onClick={openCreateModal}>新規登録</Button>
        </Group>
        <Table striped highlightOnHover>
          <thead>
            <tr>
              <th>サブドメイン名</th>
              <th>モード</th>
              <th>接続先</th>
              <th>ステータス</th>
              <th>作成日</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {subdomains.map((domain) => (
              <tr key={domain.id}>
                <td>
                  <Group spacing={8}>
                    {domain.subdomain}
                    <CopyIcon value={publicUrl(domain.subdomain) || domain.subdomain} />
                  </Group>
                </td>
                <td>
                  <Badge color={getModeBadgeColor(domain.mode)}>{domain.mode.toUpperCase()}</Badge>
                </td>
                <td>{buildEndpointLabel(domain)}</td>
                <td>
                  <Badge color={domain.status === 'active' ? 'green' : 'red'}>{domain.status}</Badge>
                </td>
                <td>{formatDate(domain.created_at)}</td>
                <td>
                  <SubdomainActions domain={domain} />
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
        {baseDomain && (
          <Text size="xs" color="dimmed" mt="sm">
            公開URL: {scheme}://&lt;サブドメイン&gt;.{baseDomain}（コピーボタンで公開URLをコピーできます）
          </Text>
        )}
        <Text size="xs" color="dimmed" mt={4}>
          変更はトンネルサーバーが定期的（最大30秒程度）に読み込んで反映します。
        </Text>
      </Paper>
    </>
  );

  const MobileView = () => (
    <Stack spacing="xl">
      <Box>
        <Group position="apart" mb="md">
          <Title order={2}>API Key 情報</Title>
          <Button onClick={handleGenerateApiKey} variant="outline" size="sm">{apiKeyButtonLabel}</Button>
        </Group>
        <Stack spacing="md">{user && <ApiKeyCard apiUser={user} />}</Stack>
      </Box>

      <Box>
        <Group position="apart" mb="md">
          <Title order={2}>サブドメイン情報</Title>
          <Button onClick={openCreateModal} size="sm">新規登録</Button>
        </Group>
        <Stack spacing="md">
          {subdomains.map((domain) => (
            <SubdomainCard key={domain.id} domain={domain} />
          ))}
        </Stack>
        <Text size="xs" color="dimmed" mt="sm">
          変更はトンネルサーバーが定期的（最大30秒程度）に読み込んで反映します。
        </Text>
      </Box>
    </Stack>
  );

  if (status === 'loading' || (status === 'authenticated' && loading)) {
    return (
      <SimpleLayout session={session}>
        <Group position="center">
          <Loader size="xl" />
        </Group>
      </SimpleLayout>
    );
  }

  if (!session || !session.user) {
    return (
      <SimpleLayout session={session}>
        <Text align="center" size="xl" weight={700} color="red">
          ユーザーが認証されていません
        </Text>
      </SimpleLayout>
    );
  }

  return (
    <SimpleLayout session={session}>
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

      <Modal
        opened={modalOpen}
        onClose={handleCloseModal}
        title={editingSubdomain ? 'サブドメインを編集' : 'サブドメインを新規登録'}
        size="md"
      >
        <form onSubmit={form.onSubmit(handleSubmit)}>
          <Stack>
            <Select
              required
              label="トンネルモード"
              description="現在は HTTP のみ利用できます（TCP はトンネルサーバー側が未実装です）"
              data={TUNNEL_MODE_OPTIONS}
              {...form.getInputProps('mode')}
            />
            <TextInput
              required
              label="ローカルサーバーURL"
              placeholder="http://localhost:3000"
              {...form.getInputProps('local_server_url')}
            />
            <TextInput
              label="任意のサブドメイン (3〜16文字)"
              placeholder="my-subdomain"
              description="英小文字・数字・ハイフンのみ使用可能。未入力の場合は自動生成されます。"
              disabled={Boolean(editingSubdomain)}
              {...form.getInputProps('customSubdomain')}
            />
            <Select
              label="ステータス"
              data={[
                { value: 'active', label: 'アクティブ' },
                { value: 'inactive', label: '非アクティブ' },
              ]}
              {...form.getInputProps('status')}
            />
            <TextInput label="説明" placeholder="説明を入力" {...form.getInputProps('description')} />
            <Group position="right">
              <Button variant="outline" onClick={handleCloseModal}>
                キャンセル
              </Button>
              <Button type="submit">{editingSubdomain ? '更新' : '作成'}</Button>
            </Group>
          </Stack>
        </form>
      </Modal>
    </SimpleLayout>
  );
};

export default DashBoard;
