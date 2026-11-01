// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import '@mantine/charts/styles.css';

import { AreaChart, BarChart, LineChart } from '@mantine/charts';
import {
  Anchor,
  Badge,
  Card,
  Group,
  SegmentedControl,
  Select,
  Stack,
  Table,
  Tabs,
  Text,
  Title,
} from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams, useSearchParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatDate, formatDateTime } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { StatusBadge, TypeIcon } from '../issues/shared';
import { BoardNav } from './BoardNav';
import { formatEstimate, useBoard, useSprints, type Board } from './hooks';

const columnColors = ['gray.5', 'blue.5', 'violet.5', 'orange.5', 'cyan.5', 'grape.5', 'teal.5'];

function SprintSelect({
  sprints,
  value,
  onChange,
}: {
  sprints: Schemas['Sprint'][];
  value: number | null;
  onChange: (id: number) => void;
}) {
  const { t } = useTranslation();
  return (
    <Select
      label={t('agile.sprint')}
      w={280}
      data={sprints.map((s) => ({
        value: String(s.id),
        label: `${s.name}${s.state === 'active' ? ` (${t('agile.active')})` : ''}`,
      }))}
      value={value ? String(value) : null}
      onChange={(v) => v && onChange(Number(v))}
      allowDeselect={false}
      placeholder={t('agile.noSprints')}
    />
  );
}

function BurndownReport({ board, sprintId }: { board: Board; sprintId: number }) {
  const { t } = useTranslation();
  const data = useQuery({
    queryKey: ['board', board.id, 'burndown', sprintId],
    queryFn: () => unwrap(api.GET('/api/v1/sprints/{id}/burndown', { params: { path: { id: sprintId } } })),
  });
  if (data.error) return <ErrorAlert error={data.error} />;
  if (!data.data) return null;
  const { points, sprint, statistic } = data.data;
  const start = new Date(sprint.start!).getTime();
  const end = new Date(
    sprint.end ?? sprint.completed ?? points[points.length - 1]?.at ?? sprint.start!,
  ).getTime();
  const initial = points[0]?.remaining ?? 0;
  const rows: Record<string, number | null>[] = points.map((p) => ({
    t: new Date(p.at).getTime(),
    remaining: p.remaining,
    guideline: null,
  }));
  rows.push({ t: start, remaining: null, guideline: initial }, { t: end, remaining: null, guideline: 0 });
  rows.sort((a, b) => (a.t ?? 0) - (b.t ?? 0));
  const fmt = (v: number) => formatEstimate(v, statistic) || '0';
  return (
    <Stack>
      <LineChart
        h={340}
        data={rows}
        dataKey="t"
        series={[
          { name: 'remaining', label: t('agile.remaining'), color: 'red.6', curveType: 'stepAfter' },
          {
            name: 'guideline',
            label: t('agile.guideline'),
            color: 'gray.5',
            strokeDasharray: '5 5',
            curveType: 'linear',
          },
        ]}
        connectNulls
        withLegend
        withDots={false}
        valueFormatter={fmt}
        lineChartProps={{ margin: { right: 40, left: 10 } }}
        xAxisProps={{
          type: 'number',
          scale: 'time',
          domain: [start, Math.max(end, rows[rows.length - 1]?.t ?? end)],
          tickFormatter: (v: number) => formatDate(new Date(v).toISOString()),
        }}
      />
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('agile.time')}</Table.Th>
            <Table.Th>{t('issues.issueKey')}</Table.Th>
            <Table.Th>{t('agile.event')}</Table.Th>
            <Table.Th>{t('agile.change')}</Table.Th>
            <Table.Th>{t('agile.remaining')}</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {points.map((p, i) => (
            <Table.Tr key={i}>
              <Table.Td>{formatDateTime(p.at)}</Table.Td>
              <Table.Td>
                {p.key && (
                  <Anchor component={Link} to={`/browse/${p.key}`} size="sm">
                    {p.key}
                  </Anchor>
                )}
              </Table.Td>
              <Table.Td>{t(`agile.events.${p.event}`)}</Table.Td>
              <Table.Td>{p.delta ? (p.delta > 0 ? '+' : '−') + fmt(Math.abs(p.delta)) : ''}</Table.Td>
              <Table.Td>{fmt(p.remaining)}</Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

function IssueTable({
  title,
  issues,
  statistic,
}: {
  title: string;
  issues: Schemas['ReportIssue'][];
  statistic: Schemas['Estimation'];
}) {
  const { t } = useTranslation();
  return (
    <Card withBorder>
      <Title order={5} mb="xs">
        {title} ({issues.length})
      </Title>
      {issues.length === 0 ? (
        <Text size="sm" c="dimmed">
          —
        </Text>
      ) : (
        <Table>
          <Table.Tbody>
            {issues.map((i) => (
              <Table.Tr key={i.key}>
                <Table.Td w={30}>
                  <TypeIcon type={i.type} size={16} />
                </Table.Td>
                <Table.Td w={110}>
                  <Anchor component={Link} to={`/browse/${i.key}`} size="sm">
                    {i.key}
                  </Anchor>
                  {i.added && (
                    <Text span c="orange" title={t('agile.addedAfterStart')}>
                      {' '}
                      *
                    </Text>
                  )}
                </Table.Td>
                <Table.Td>
                  <Text size="sm">{i.summary}</Text>
                </Table.Td>
                <Table.Td w={140}>
                  <StatusBadge status={i.status} />
                </Table.Td>
                <Table.Td w={70}>{formatEstimate(i.estimate, statistic)}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Card>
  );
}

function SprintReportView({ board, sprintId }: { board: Board; sprintId: number }) {
  const { t } = useTranslation();
  const data = useQuery({
    queryKey: ['board', board.id, 'sprint-report', sprintId],
    queryFn: () => unwrap(api.GET('/api/v1/sprints/{id}/report', { params: { path: { id: sprintId } } })),
  });
  if (data.error) return <ErrorAlert error={data.error} />;
  if (!data.data) return null;
  const r = data.data;
  const sum = (list: Schemas['ReportIssue'][]) => list.reduce((s, i) => s + (i.estimate ?? 0), 0);
  return (
    <Stack>
      <Group gap="xl">
        <Text size="sm">
          {t('agile.committed')}: <b>{formatEstimate(r.committed, r.statistic) || '0'}</b>
        </Text>
        <Text size="sm">
          {t('agile.completedWork')}: <b>{formatEstimate(sum(r.completed), r.statistic) || '0'}</b>
        </Text>
        {r.sprint.start && (
          <Text size="sm" c="dimmed">
            {formatDateTime(r.sprint.start)} – {formatDateTime(r.sprint.completed ?? r.sprint.end ?? '')}
          </Text>
        )}
      </Group>
      <IssueTable title={t('agile.completedIssues')} issues={r.completed} statistic={r.statistic} />
      <IssueTable title={t('agile.notCompletedIssues')} issues={r.notCompleted} statistic={r.statistic} />
      <IssueTable title={t('agile.removedIssues')} issues={r.removed} statistic={r.statistic} />
      <Text size="xs" c="dimmed">
        * {t('agile.addedAfterStart')}
      </Text>
    </Stack>
  );
}

function VelocityReport({ board }: { board: Board }) {
  const { t } = useTranslation();
  const data = useQuery({
    queryKey: ['board', board.id, 'velocity'],
    queryFn: () => unwrap(api.GET('/api/v1/boards/{id}/velocity', { params: { path: { id: board.id } } })),
  });
  if (data.error) return <ErrorAlert error={data.error} />;
  if (!data.data) return null;
  if (data.data.sprints.length === 0) return <Text c="dimmed">{t('agile.noClosedSprints')}</Text>;
  const rows = data.data.sprints.map((s) => ({
    sprint: s.sprint.name,
    committed: s.committed,
    completed: s.completed,
  }));
  const avg = rows.reduce((s, r) => s + r.completed, 0) / rows.length;
  return (
    <Stack>
      <BarChart
        h={320}
        data={rows}
        dataKey="sprint"
        series={[
          { name: 'committed', label: t('agile.committed'), color: 'gray.5' },
          { name: 'completed', label: t('agile.completedWork'), color: 'teal.6' },
        ]}
        withLegend
        valueFormatter={(v) => formatEstimate(v, data.data!.statistic) || '0'}
      />
      <Text size="sm">
        {t('agile.averageVelocity')}: <b>{formatEstimate(avg, data.data.statistic) || '0'}</b>
      </Text>
    </Stack>
  );
}

function CumulativeFlowReport({ board }: { board: Board }) {
  const { t } = useTranslation();
  const [days, setDays] = useState('30');
  const data = useQuery({
    queryKey: ['board', board.id, 'cfd', days],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/boards/{id}/cumulative-flow', {
          params: { path: { id: board.id }, query: { days: Number(days) } },
        }),
      ),
  });
  if (data.error) return <ErrorAlert error={data.error} />;
  const cols = data.data?.columns ?? [];
  const rows = (data.data?.days ?? []).map((d) => {
    const row: Record<string, string | number> = { date: formatDate(d.date) };
    cols.forEach((_c, i) => (row[`c${i}`] = d.counts[i]));
    return row;
  });
  return (
    <Stack>
      <SegmentedControl
        w={260}
        size="xs"
        value={days}
        onChange={setDays}
        data={[
          { value: '14', label: t('agile.lastDays', { count: 14 }) },
          { value: '30', label: t('agile.lastDays', { count: 30 }) },
          { value: '90', label: t('agile.lastDays', { count: 90 }) },
        ]}
      />
      <AreaChart
        h={340}
        data={rows}
        dataKey="date"
        type="stacked"
        withLegend
        series={[...cols.keys()]
          .reverse()
          .map((i) => ({ name: `c${i}`, label: cols[i], color: columnColors[i % columnColors.length] }))}
        withDots={false}
      />
      {data.data?.truncated && (
        <Text size="xs" c="orange">
          {t('agile.flowTruncated')}
        </Text>
      )}
    </Stack>
  );
}

export function CreatedResolvedReport({ query, height = 320 }: { query: string; height?: number }) {
  const { t } = useTranslation();
  const [days, setDays] = useState('30');
  const [period, setPeriod] = useState<'day' | 'week'>('day');
  const data = useQuery({
    queryKey: ['issue', 'created-resolved', query, days, period],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/reports/created-vs-resolved', {
          params: { query: { q: query, days: Number(days), period } },
        }),
      ),
  });
  if (data.error) return <ErrorAlert error={data.error} />;
  const rows = (data.data?.periods ?? []).map((p) => ({
    start: formatDate(p.start),
    created: p.created,
    resolved: p.resolved,
  }));
  return (
    <Stack>
      <Group>
        <SegmentedControl
          size="xs"
          value={days}
          onChange={setDays}
          data={[
            { value: '30', label: t('agile.lastDays', { count: 30 }) },
            { value: '90', label: t('agile.lastDays', { count: 90 }) },
            { value: '365', label: t('agile.lastDays', { count: 365 }) },
          ]}
        />
        <SegmentedControl
          size="xs"
          value={period}
          onChange={(v) => setPeriod(v as 'day' | 'week')}
          data={[
            { value: 'day', label: t('agile.perDay') },
            { value: 'week', label: t('agile.perWeek') },
          ]}
        />
      </Group>
      <BarChart
        h={height}
        data={rows}
        dataKey="start"
        withLegend
        series={[
          { name: 'created', label: t('agile.createdIssues'), color: 'red.5' },
          { name: 'resolved', label: t('agile.resolvedIssues'), color: 'teal.6' },
        ]}
      />
    </Stack>
  );
}

export function ReportsPage() {
  const { t } = useTranslation();
  const { key = '', boardId = '' } = useParams();
  const id = Number(boardId);
  const board = useBoard(id);
  const [params, setParams] = useSearchParams();
  const [sprintId, setSprintId] = useState<number | null>(null);
  const sprints = useSprints(id);
  const started = (sprints.data ?? []).filter((s) => s.state !== 'future');
  const effectiveSprint = sprintId ?? started[0]?.id ?? null;
  const scrum = board.data?.type === 'scrum';
  const tab = params.get('report') ?? (scrum ? 'burndown' : 'cfd');
  if (board.error) return <ErrorAlert error={board.error} />;
  if (!board.data) return null;
  const b = board.data;
  return (
    <Stack gap="md">
      <BoardNav projectKey={key} board={b} active="reports" />
      <Tabs
        value={tab}
        onChange={(v) => v && setParams({ report: v }, { replace: true })}
        keepMounted={false}
      >
        <Tabs.List mb="md">
          {scrum && <Tabs.Tab value="burndown">{t('agile.burndown')}</Tabs.Tab>}
          {scrum && <Tabs.Tab value="sprint">{t('agile.sprintReport')}</Tabs.Tab>}
          {scrum && <Tabs.Tab value="velocity">{t('agile.velocity')}</Tabs.Tab>}
          <Tabs.Tab value="cfd">{t('agile.cumulativeFlow')}</Tabs.Tab>
          <Tabs.Tab value="created">{t('agile.createdVsResolved')}</Tabs.Tab>
        </Tabs.List>
        {(tab === 'burndown' || tab === 'sprint') && (
          <Group mb="md">
            <SprintSelect sprints={started} value={effectiveSprint} onChange={setSprintId} />
            <Badge variant="light" mt="lg">
              {t(`agile.statistics.${b.config.estimation.type}`)}
            </Badge>
          </Group>
        )}
        <Tabs.Panel value="burndown">
          {effectiveSprint && <BurndownReport board={b} sprintId={effectiveSprint} />}
        </Tabs.Panel>
        <Tabs.Panel value="sprint">
          {effectiveSprint && <SprintReportView board={b} sprintId={effectiveSprint} />}
        </Tabs.Panel>
        <Tabs.Panel value="velocity">
          <VelocityReport board={b} />
        </Tabs.Panel>
        <Tabs.Panel value="cfd">
          <CumulativeFlowReport board={b} />
        </Tabs.Panel>
        <Tabs.Panel value="created">
          <CreatedResolvedReport query={b.query} />
        </Tabs.Panel>
      </Tabs>
    </Stack>
  );
}
