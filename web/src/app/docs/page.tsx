'use client';
import { Code, List, Paper, Text, Title } from '@mantine/core';
import { useSession } from 'next-auth/react';
import { SimpleLayout } from '@/shared/components/SimpleLayout';
import { usePublicConfig } from '@/app/PublicConfig';

export default function Docs() {
  const { data: session } = useSession();
  const { baseDomain, tunnelServer, appUrl } = usePublicConfig();
  const scheme = appUrl.startsWith('https://') ? 'https' : 'http';
  const example = baseDomain ? `${scheme}://<サブドメイン>.${baseDomain}` : '（管理者が GRIDBASE_BASE_DOMAIN を設定すると表示されます）';

  return (
    <SimpleLayout session={session}>
      <Title order={2} mb="lg">
        使い方
      </Title>
      <Paper withBorder p="md" mb="md">
        <List type="ordered" spacing="sm">
          <List.Item>
            <Text>
              ダッシュボードで <b>API Key</b> を確認します（再発行すると、トンネルサーバーが変更を読み込んだ時点＝最大30秒ほどで旧キーは使えなくなります）。
            </Text>
          </List.Item>
          <List.Item>
            <Text>
              ダッシュボードの「サブドメイン情報」から<b>新規登録</b>します。公開したいローカルの URL
              （例 <Code>http://localhost:3000</Code>）を指定します。TCP トンネルは現在サポートされていません。
            </Text>
          </List.Item>
          <List.Item>
            <Text>
              トンネルクライアントを、接続先{' '}
              {tunnelServer ? <Code>{tunnelServer}</Code> : <Code>（管理者が GRIDBASE_TUNNEL_SERVER を設定すると表示されます）</Code>}{' '}
              と API Key を指定して起動します。起動オプションはクライアントの README を参照してください。
            </Text>
          </List.Item>
          <List.Item>
            <Text>
              公開 URL は <Code>{example}</Code> です。
            </Text>
          </List.Item>
        </List>
      </Paper>
      <Text size="sm" color="dimmed">
        サブドメインや API Key の変更は、トンネルサーバーが定期的に（最大30秒程度ごとに）読み込んで反映します。
        サブドメインは3〜16文字の英小文字・数字・ハイフンで、ハイフンで始まらず終わらず、連続したハイフンも使えません。
      </Text>
    </SimpleLayout>
  );
}
