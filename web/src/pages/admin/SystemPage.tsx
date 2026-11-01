// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Badge, Button, Card, Group, SimpleGrid, Table, Text } from '@mantine/core';
import { useMutation, useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { formatBytes, formatDateTime } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError, notifySuccess } from '../../components/notify';
import { PageHeader } from '../../components/PageHeader';

export function SystemPage() {
  const { t } = useTranslation();
  const info = useQuery({
    queryKey: ['admin', 'system'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/system')),
    refetchInterval: 30_000,
  });
  const d = info.data;
  const testMail = useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/admin/mail/test')),
    onSuccess: () => notifySuccess(t('admin.testMailSent')),
    onError: notifyError,
  });
  const row = (label: string, value: React.ReactNode) => (
    <Table.Tr>
      <Table.Th w={220}>{label}</Table.Th>
      <Table.Td>{value}</Table.Td>
    </Table.Tr>
  );
  const flag = (on: boolean) => (
    <Badge variant="light" color={on ? 'teal' : 'gray'}>
      {on ? t('admin.configured') : t('admin.notConfigured')}
    </Badge>
  );
  return (
    <>
      <PageHeader title={t('admin.system')} />
      <ErrorAlert error={info.error} />
      {d && (
        <SimpleGrid cols={{ base: 1, lg: 2 }}>
          <Card withBorder>
            <Table>
              <Table.Tbody>
                {row(t('admin.version'), d.version)}
                {d.commit &&
                  row(
                    t('admin.commit'),
                    <Text size="sm" ff="monospace" title={d.commit}>
                      {d.commit.slice(0, 12)}
                    </Text>,
                  )}
                {d.buildDate && row(t('admin.built'), formatDateTime(d.buildDate))}
                {row('Go', d.goVersion)}
                {row(t('admin.database'), <Text size="sm">{d.database}</Text>)}
                {row(t('admin.roles'), (d.roles ?? []).join(', '))}
                {row(t('admin.started'), formatDateTime(d.startedAt))}
                {row('URL', d.publicUrl)}
              </Table.Tbody>
            </Table>
          </Card>
          <Card withBorder>
            <Table>
              <Table.Tbody>
                {row(t('admin.sso'), flag(d.sso))}
                {row(
                  t('admin.mail'),
                  <Group gap="xs">
                    {flag(d.mail)}
                    {d.mail && (
                      <Button
                        size="compact-xs"
                        variant="light"
                        onClick={() => testMail.mutate()}
                        loading={testMail.isPending}
                      >
                        {t('admin.sendTestMail')}
                      </Button>
                    )}
                  </Group>,
                )}
                {row(t('admin.activeUsers'), d.users)}
                {row(t('admin.dataDir'), d.dataDir)}
                {row(t('admin.freeSpace'), formatBytes(d.dataFreeBytes))}
                {row(
                  t('admin.jobs'),
                  <Group gap={4}>
                    {Object.entries(d.jobs ?? {}).map(([state, n]) => (
                      <Badge key={state} variant="outline">
                        {state}: {n}
                      </Badge>
                    ))}
                  </Group>,
                )}
              </Table.Tbody>
            </Table>
          </Card>
        </SimpleGrid>
      )}
    </>
  );
}
