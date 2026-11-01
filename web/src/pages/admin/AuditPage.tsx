// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Alert,
  Badge,
  Button,
  Card,
  Code,
  Group,
  Stack,
  Table,
  Text,
  TextInput,
  ThemeIcon,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import {
  IconAlertTriangle,
  IconShieldCheck,
  IconShieldOff,
  IconShieldPause,
  IconShieldX,
} from '@tabler/icons-react';
import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { formatDateTime, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';

export function AuditPage() {
  const { t } = useTranslation();
  const [action, setAction] = useState('');
  const [debounced] = useDebouncedValue(action, 300);
  const events = useInfiniteQuery({
    queryKey: ['admin', 'audit', debounced],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/api/v1/admin/audit', {
          params: { query: { action: debounced, before: pageParam, limit: 100 } },
        }),
      ),
    getNextPageParam: (last) => (last.length === 100 ? last[last.length - 1].id : undefined),
  });
  const rows = (events.data?.pages ?? []).flat();
  return (
    <>
      <PageHeader title={t('admin.audit')} />
      <Forwarding />
      <Group mb="md">
        <TextInput
          placeholder={t('admin.filterAction')}
          aria-label={t('admin.filterAction')}
          value={action}
          onChange={(e) => setAction(e.currentTarget.value)}
          w={280}
        />
      </Group>
      <ErrorAlert error={events.error} />
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('admin.time')}</Table.Th>
            <Table.Th>{t('admin.action')}</Table.Th>
            <Table.Th>{t('admin.actor')}</Table.Th>
            <Table.Th>{t('admin.target')}</Table.Th>
            <Table.Th>{t('admin.address')}</Table.Th>
            <Table.Th>{t('admin.details')}</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {rows.map((e) => (
            <Table.Tr key={e.id}>
              <Table.Td style={{ whiteSpace: 'nowrap' }}>{formatDateTime(e.at)}</Table.Td>
              <Table.Td>
                <Code>{e.action}</Code>
              </Table.Td>
              <Table.Td>{e.actorName ?? (e.actorId ? `#${e.actorId}` : '—')}</Table.Td>
              <Table.Td>{e.targetType ? `${e.targetType}:${e.targetId ?? ''}` : '—'}</Table.Td>
              <Table.Td>{e.actorIp ?? ''}</Table.Td>
              <Table.Td>
                <Text size="xs" ff="monospace" lineClamp={2}>
                  {Object.keys(e.details ?? {}).length ? JSON.stringify(e.details) : ''}
                </Text>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      {events.hasNextPage && (
        <Button
          mt="md"
          variant="light"
          onClick={() => events.fetchNextPage()}
          loading={events.isFetchingNextPage}
        >
          {t('admin.loadMore')}
        </Button>
      )}
    </>
  );
}

/** The state of the forwarding to a SIEM. */
function Forwarding() {
  const { t } = useTranslation();
  const state = useQuery({
    queryKey: ['admin', 'audit', 'forwarding'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/audit/forwarding')),
    refetchInterval: 15_000,
  });
  const s = state.data;
  if (!s) return <ErrorAlert error={state.error} />;
  if (!s.configured) {
    return (
      <Group gap="xs" mb="md" c="dimmed" wrap="nowrap">
        <IconShieldOff size={16} style={{ flexShrink: 0 }} />
        <Text size="sm">{t('admin.forwardingOff')}</Text>
      </Group>
    );
  }
  const failing = !!s.failingSince;
  const color = !s.active ? 'yellow' : failing ? 'red' : 'teal';
  const Icon = !s.active ? IconShieldPause : failing ? IconShieldX : IconShieldCheck;
  const facts = [
    t('admin.forwarded', { count: s.forwarded }),
    s.forwardedAt ? t('admin.lastDelivery', { when: formatRelative(s.forwardedAt) }) : null,
    t('admin.waiting', { count: s.waiting }),
    s.waiting > 0 && s.oldestWaiting
      ? t('admin.oldestWaiting', { when: formatDateTime(s.oldestWaiting) })
      : null,
  ].filter(Boolean);
  return (
    <Card withBorder mb="md" padding="md">
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <Group gap="sm" align="flex-start" wrap="nowrap" style={{ minWidth: 0 }}>
          <ThemeIcon variant="light" color={color} size="lg">
            <Icon size={20} />
          </ThemeIcon>
          <Stack gap={2} style={{ minWidth: 0 }}>
            <Text fw={600}>{t('admin.forwarding')}</Text>
            <Text size="sm" ff="monospace" style={{ overflowWrap: 'anywhere' }}>
              {s.target}
            </Text>
            <Text size="sm" c="dimmed">
              {facts.join(' · ')}
            </Text>
          </Stack>
        </Group>
        <Badge color={color} variant="light" style={{ flexShrink: 0 }}>
          {!s.active
            ? t('admin.forwardingIdle')
            : failing
              ? t('admin.forwardingFailing')
              : t('admin.forwardingActive')}
        </Badge>
      </Group>
      {!s.active && (
        <Alert color="yellow" mt="sm" icon={<IconAlertTriangle size={16} />}>
          {t('admin.forwardingNoNode')}
        </Alert>
      )}
      {failing && (
        <Alert color="red" mt="sm" icon={<IconAlertTriangle size={16} />}>
          {t('admin.forwardingFails', {
            since: formatDateTime(s.failingSince),
            error: s.error ?? '',
          })}
        </Alert>
      )}
    </Card>
  );
}
