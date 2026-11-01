// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Alert, Anchor, Badge, Group, Progress, Stack, Table, Text } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatDateTime, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { useFieldLabel } from './fields';
import { UserBadge, useIssueConfig } from './shared';

type BulkChange = Schemas['BulkChange'];

const resultColor: Record<string, string> = { done: 'teal', skipped: 'gray', failed: 'red', pending: 'blue' };

/** What a bulk change does, in words of the interface. */
export function BulkDescription({ change }: { change: BulkChange }) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  const label = useFieldLabel();
  const name = (key: string) => {
    const def = config.data?.fields.find((f) => f.key === key);
    return def ? label(def) : key;
  };
  const parts: string[] = [];
  const e = change.edit;
  if (e) {
    for (const key of Object.keys(e.fields ?? {})) parts.push(name(key));
    if (e.labels)
      parts.push(`${name('labels')} (${t(`bulk.modes.${e.labels.mode}`)}: ${e.labels.values.join(', ')})`);
    for (const key of ['components', 'fixVersions', 'affectsVersions'] as const) {
      const c = e[key];
      if (c) parts.push(`${name(key)} (${t(`bulk.modes.${c.mode}`)})`);
    }
  }
  if (change.status) parts.push(`${t('issues.status')} → ${change.status.name}`);
  if (change.target) parts.push(`${t('issues.project')} → ${change.target.name} (${change.target.key})`);
  return (
    <Text size="sm">
      {t(`bulk.actions.${change.action}`)}
      {parts.length > 0 && `: ${parts.join('; ')}`}
    </Text>
  );
}

/** The progress and results of a bulk change; it follows a running one live. */
function BulkChangeView({ id }: { id: number }) {
  const { t } = useTranslation();
  const change = useQuery({
    queryKey: ['bulk-change', id],
    queryFn: () => unwrap(api.GET('/api/v1/issues/bulk/{id}', { params: { path: { id } } })),
    // Live messages tell of progress; polling covers a lost connection.
    refetchInterval: (q) =>
      q.state.data && (q.state.data.state === 'queued' || q.state.data.state === 'running') ? 3000 : false,
    retry: false,
  });
  if (change.error) return <ErrorAlert error={change.error} />;
  const b = change.data;
  if (!b) return null;
  const running = b.state === 'queued' || b.state === 'running';
  const percent = b.total ? Math.round((b.processed / b.total) * 100) : 0;
  return (
    <Stack gap="md">
      <Group gap="sm" justify="space-between">
        <Stack gap={2}>
          <BulkDescription change={b} />
          <Group gap="xs">
            {b.actor && <UserBadge user={b.actor} size="xs" />}
            <Text size="xs" c="dimmed" title={formatDateTime(b.created)}>
              {formatRelative(b.created)}
            </Text>
          </Group>
        </Stack>
        <Badge
          color={running ? 'blue' : b.state === 'failed' ? 'red' : 'teal'}
          variant="light"
          data-testid="bulk-state"
        >
          {t(`bulk.states.${b.state}`)}
        </Badge>
      </Group>
      {b.mine && (
        <Stack gap={4}>
          <Progress value={percent} animated={running} aria-label={t('bulk.progress')} />
          <Text size="sm" data-testid="bulk-progress">
            {t('bulk.processed', { processed: b.processed, total: b.total })} ·{' '}
            {t('bulk.summary', { done: b.done, skipped: b.skipped, failed: b.failed })}
          </Text>
        </Stack>
      )}
      {b.error && <Alert color="red">{b.error}</Alert>}
      {!b.mine && (
        <Text size="sm" c="dimmed">
          {t('bulk.othersView')}
        </Text>
      )}
      <Table withTableBorder verticalSpacing={4} data-testid="bulk-results">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('issues.key')}</Table.Th>
            <Table.Th>{t('issues.summary')}</Table.Th>
            <Table.Th>{t('bulk.result')}</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {b.items.map((it) => (
            <Table.Tr key={it.key}>
              <Table.Td style={{ whiteSpace: 'nowrap' }}>
                {b.action === 'delete' && it.result === 'done' ? (
                  <Text size="sm" td="line-through">
                    {it.key}
                  </Text>
                ) : (
                  <Anchor component={Link} to={`/browse/${it.newKey || it.key}`} size="sm" fw={600}>
                    {it.newKey ? `${it.key} → ${it.newKey}` : it.key}
                  </Anchor>
                )}
              </Table.Td>
              <Table.Td>
                <Text size="sm" lineClamp={1}>
                  {it.summary}
                </Text>
              </Table.Td>
              <Table.Td>
                <Group gap="xs" wrap="nowrap">
                  <Badge size="sm" variant="light" color={resultColor[it.result]}>
                    {t(`bulk.results.${it.result}`)}
                  </Badge>
                  {it.message && (
                    <Text size="xs" c="dimmed">
                      {it.message}
                    </Text>
                  )}
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

/** The user's recent bulk changes. */
function RecentBulkChanges() {
  const { t } = useTranslation();
  const list = useQuery({
    queryKey: ['bulk-change', 'list'],
    queryFn: () => unwrap(api.GET('/api/v1/issues/bulk')),
  });
  if (list.error) return <ErrorAlert error={list.error} />;
  if (!list.data) return null;
  if (list.data.length === 0) return <Text c="dimmed">{t('bulk.none')}</Text>;
  return (
    <Table withTableBorder verticalSpacing={4}>
      <Table.Tbody>
        {list.data.map((b) => (
          <Table.Tr key={b.id}>
            <Table.Td>
              <Anchor component={Link} to={`/issues/bulk/${b.id}`} size="sm">
                <BulkDescription change={b} />
              </Anchor>
            </Table.Td>
            <Table.Td>
              <Text size="xs" c="dimmed">
                {formatRelative(b.created)}
              </Text>
            </Table.Td>
            <Table.Td>
              <Text size="xs">
                {t('bulk.summary', { done: b.done, skipped: b.skipped, failed: b.failed })}
              </Text>
            </Table.Td>
            <Table.Td>
              <Badge size="sm" variant="light">
                {t(`bulk.states.${b.state}`)}
              </Badge>
            </Table.Td>
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  );
}

/** A bulk change (/issues/bulk/:id), or the user's recent ones (/issues/bulk). */
export function BulkPage() {
  const { t } = useTranslation();
  const { id } = useParams();
  return (
    <>
      <PageHeader title={id ? t('bulk.pageTitle') : t('bulk.recent')} />
      <Stack gap="xl">
        {id && <BulkChangeView id={Number(id)} />}
        {!id && <RecentBulkChanges />}
      </Stack>
    </>
  );
}
