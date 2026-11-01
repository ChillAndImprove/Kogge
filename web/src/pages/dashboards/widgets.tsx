// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Group, Loader, Progress, Stack, Table, Text } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { lazy, Suspense } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { ActivityStream } from '../../components/ActivityStream';
import { ErrorAlert } from '../../components/ErrorAlert';
import { formatEstimate } from '../agile/hooks';
import { PriorityIcon, StatusBadge, TypeIcon } from '../issues/shared';

export type Widget = Schemas['Widget'];

const ChartWidgets = lazy(() => import('./ChartWidgets'));

export const searchLink = (q: string) => `/issues/search?q=${encodeURIComponent(q)}`;

export function str(v: unknown, fallback = ''): string {
  return typeof v === 'string' ? v : fallback;
}

function IssueList({ query, limit }: { query: string; limit: number }) {
  const { t } = useTranslation();
  const res = useQuery({
    queryKey: ['issue', 'search', query, 'widget', limit],
    queryFn: () => unwrap(api.GET('/api/v1/search', { params: { query: { q: query, limit } } })),
    retry: false,
  });
  if (res.error) return <ErrorAlert error={res.error} />;
  if (!res.data) return <Loader size="sm" />;
  if (res.data.issues.length === 0)
    return (
      <Text size="sm" c="dimmed">
        {t('issues.noResults')}
      </Text>
    );
  return (
    <Stack gap={4}>
      <Table>
        <Table.Tbody>
          {res.data.issues.map((i) => (
            <Table.Tr key={i.id}>
              <Table.Td w={24}>
                <TypeIcon type={i.type} size={16} />
              </Table.Td>
              <Table.Td w={100}>
                <Anchor component={Link} to={`/browse/${i.key}`} size="sm" fw={600}>
                  {i.key}
                </Anchor>
              </Table.Td>
              <Table.Td>
                <Text size="sm" lineClamp={1}>
                  {i.summary}
                </Text>
              </Table.Td>
              <Table.Td w={24}>
                <PriorityIcon priority={i.priority} />
              </Table.Td>
              <Table.Td w={120}>
                <StatusBadge status={i.status} />
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      {res.data.total > res.data.issues.length && (
        <Anchor component={Link} to={searchLink(query)} size="xs">
          {t('dashboards.showAll', { count: res.data.total })}
        </Anchor>
      )}
    </Stack>
  );
}

function MatrixWidget({ query, x, y }: { query: string; x: string; y: string }) {
  const { t } = useTranslation();
  const res = useQuery({
    queryKey: ['issue', 'matrix', query, x, y],
    queryFn: () => unwrap(api.GET('/api/v1/reports/matrix', { params: { query: { q: query, x, y } } })),
    retry: false,
  });
  if (res.error) return <ErrorAlert error={res.error} />;
  if (!res.data) return <Loader size="sm" />;
  const m = res.data;
  const label = (it: Schemas['DistributionItem'], field: string) =>
    it.label || (field === 'statusCategory' ? t(`issueAdmin.categories.${it.key}`) : t('dashboards.none'));
  const and = (a: string, b: string) =>
    [query.replace(/\s+ORDER BY.*$/i, ''), a, b]
      .filter(Boolean)
      .map((c) => `(${c})`)
      .join(' AND ');
  return (
    <Stack gap={4}>
      <Table.ScrollContainer minWidth={300}>
        <Table withColumnBorders>
          <Table.Thead>
            <Table.Tr>
              <Table.Th />
              {m.x.map((xi) => (
                <Table.Th key={xi.key}>{label(xi, x)}</Table.Th>
              ))}
              <Table.Th>{t('dashboards.total')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {m.y.map((yi, r) => (
              <Table.Tr key={yi.key}>
                <Table.Td fw={600}>{label(yi, y)}</Table.Td>
                {m.x.map((xi, c) => (
                  <Table.Td key={xi.key}>
                    {m.counts[r][c] ? (
                      <Anchor component={Link} to={searchLink(and(xi.query, yi.query))} size="sm">
                        {m.counts[r][c]}
                      </Anchor>
                    ) : (
                      ''
                    )}
                  </Table.Td>
                ))}
                <Table.Td fw={600}>{yi.count}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      {m.truncated && (
        <Text size="xs" c="orange">
          {t('dashboards.truncatedCombinations')}
        </Text>
      )}
    </Stack>
  );
}

function SprintHealthWidget({ boardId }: { boardId: number }) {
  const { t } = useTranslation();
  const res = useQuery({
    queryKey: ['board', boardId, 'health'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/boards/{id}/sprint-health', { params: { path: { id: boardId } } })),
    enabled: boardId > 0,
    retry: false,
  });
  if (!boardId)
    return (
      <Text size="sm" c="dimmed">
        {t('dashboards.chooseBoard')}
      </Text>
    );
  if (res.error) return <ErrorAlert error={res.error} />;
  if (!res.data) return <Loader size="sm" />;
  if (res.data.sprints.length === 0)
    return (
      <Text size="sm" c="dimmed">
        {t('agile.noActiveSprint')}
      </Text>
    );
  return (
    <Stack>
      {res.data.sprints.map((h) => {
        const total = h.todo + h.inProgress + h.done || 1;
        return (
          <Stack key={h.sprint.id} gap={6}>
            <Group justify="space-between">
              <Anchor
                component={Link}
                to={`/projects/${res.data!.board.key}/boards/${res.data!.board.id}`}
                fw={600}
                size="sm"
              >
                {h.sprint.name}
              </Anchor>
              <Text size="xs" c="dimmed">
                {t('dashboards.timeElapsed', { percent: Math.round(h.timeElapsed * 100) })}
              </Text>
            </Group>
            <Progress.Root size="lg">
              <Progress.Section
                value={(100 * h.done) / total}
                color="teal"
                aria-label={`${t('dashboards.sectionDone')}: ${h.done}`}
              >
                <Progress.Label>{h.done}</Progress.Label>
              </Progress.Section>
              <Progress.Section
                value={(100 * h.inProgress) / total}
                color="blue"
                aria-label={`${t('dashboards.sectionInProgress')}: ${h.inProgress}`}
              >
                <Progress.Label>{h.inProgress}</Progress.Label>
              </Progress.Section>
              <Progress.Section
                value={(100 * h.todo) / total}
                color="gray"
                aria-label={`${t('dashboards.sectionTodo')}: ${h.todo}`}
              >
                <Progress.Label>{h.todo}</Progress.Label>
              </Progress.Section>
            </Progress.Root>
            <Progress
              value={h.timeElapsed * 100}
              size="xs"
              color="orange"
              aria-label={t('dashboards.timeElapsed', { percent: Math.round(h.timeElapsed * 100) })}
            />
            {h.statistic.type !== 'count' && (
              <Text size="xs" c="dimmed">
                {formatEstimate(h.estimateDone, h.statistic) || '0'} /{' '}
                {formatEstimate(h.estimate, h.statistic) || '0'}
              </Text>
            )}
          </Stack>
        );
      })}
      {res.data.truncated && (
        <Text size="xs" c="orange">
          {t('dashboards.truncatedSprints')}
        </Text>
      )}
    </Stack>
  );
}

/** What happened in Kontor (or one project): the activity stream as everywhere, in its compact density. */
function ActivityWidget({ project, limit }: { project: string; limit: number }) {
  return project ? (
    <ActivityStream project={project} limit={limit} density="compact" />
  ) : (
    <ActivityStream product="kontor" limit={limit} density="compact" />
  );
}

/** Renders the content of a widget. */
export function WidgetBody({ widget }: { widget: Widget }) {
  const c = widget.config ?? {};
  const query = str(c.query);
  switch (widget.type) {
    case 'filter-results':
      return <IssueList query={query} limit={Number(c.limit ?? 10)} />;
    case 'assigned-to-me':
      return (
        <IssueList
          query="assignee = currentUser() AND resolution = Unresolved ORDER BY priority DESC, updated DESC"
          limit={Number(c.limit ?? 10)}
        />
      );
    case 'matrix':
      return <MatrixWidget query={query} x={str(c.x, 'status')} y={str(c.y, 'assignee')} />;
    case 'sprint-health':
      return <SprintHealthWidget boardId={Number(c.boardId ?? 0)} />;
    case 'activity':
      return <ActivityWidget project={str(c.project)} limit={Number(c.limit ?? 15)} />;
    case 'text':
      return (
        <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>
          {str(c.text)}
        </Text>
      );
    case 'distribution':
    case 'created-vs-resolved':
      return (
        <Suspense fallback={<Loader size="sm" />}>
          <ChartWidgets widget={widget} />
        </Suspense>
      );
  }
  return null;
}
