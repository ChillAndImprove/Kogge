// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import '@mantine/charts/styles.css';
import { BarChart } from '@mantine/charts';
import {
  Anchor,
  Badge,
  Card,
  Group,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Table,
  Tabs,
  Text,
  TextInput,
  ThemeIcon,
  Tooltip,
  UnstyledButton,
} from '@mantine/core';
import {
  IconActivity,
  IconClockHour4,
  IconGitBranch,
  IconHourglass,
  IconSearch,
  IconServer2,
  IconTrendingUp,
} from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { Pagination } from '../../components/Pagination';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatDate, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { Duration, RunTable, StateIcon, TimeAgo } from './components';
import { formatDuration, pipelinesPath, runColor, runKey, runPath, type RunState } from './hooks';
import './builds.css';

const pageSize = 30;

type Health = Schemas['Health'];

/** A figure of the dashboard with its icon. */
function Stat({
  icon,
  color,
  label,
  value,
  hint,
}: {
  icon: ReactNode;
  color: string;
  label: string;
  value: ReactNode;
  hint?: ReactNode;
}) {
  return (
    <Card withBorder padding="md" radius="md">
      <Group gap="sm" wrap="nowrap" align="flex-start">
        <ThemeIcon variant="light" color={color} size={36} radius="md">
          {icon}
        </ThemeIcon>
        <Stack gap={0} style={{ minWidth: 0 }}>
          <Text size="xs" c="dimmed" fw={600} tt="uppercase">
            {label}
          </Text>
          <Text fz={22} fw={700} lh={1.3}>
            {value}
          </Text>
          {hint && (
            <Text size="xs" c="dimmed" truncate>
              {hint}
            </Text>
          )}
        </Stack>
      </Group>
    </Card>
  );
}

/** The last runs of a pipeline as small bars, oldest first: green, red, grey or blue. */
function History({ health }: { health: Health }) {
  const { t } = useTranslation();
  const longest = Math.max(1, ...health.history.map((h) => h.seconds ?? 0));
  return (
    <Group
      gap={3}
      wrap="nowrap"
      align="flex-end"
      h={26}
      role="group"
      aria-label={`${t('builds.history')}: ${Object.entries(
        health.history.reduce<Record<string, number>>(
          (n, h) => ({ ...n, [h.state]: (n[h.state] ?? 0) + 1 }),
          {},
        ),
      )
        .map(([s, n]) => `${n} ${t(`builds.state.${s}` as 'builds.state.queued', { defaultValue: s })}`)
        .join(', ')}`}
    >
      {health.history.map((h) => (
        <Tooltip
          key={h.number}
          withinPortal
          label={`#${h.number} · ${t(`builds.state.${h.state}` as 'builds.state.queued', { defaultValue: h.state })}${
            h.seconds ? ` · ${formatDuration(h.seconds)}` : ''
          }`}
        >
          <UnstyledButton
            component={Link}
            to={runPath(health.repository, h.number)}
            className="kogge-history-bar"
            data-state={h.state}
            aria-label={`#${h.number}: ${t(`builds.state.${h.state}` as 'builds.state.queued', { defaultValue: h.state })}`}
            style={{
              height: `${Math.max(30, Math.round(((h.seconds ?? longest * 0.6) / longest) * 100))}%`,
              background: `var(--mantine-color-${runColor[h.state] ?? 'gray'}-${h.state === 'running' ? 4 : 6})`,
            }}
          />
        </Tooltip>
      ))}
    </Group>
  );
}

/** The pipelines of the default branches: the newest run of each and how the runs before went. */
function Pipelines({ pipelines }: { pipelines: Health[] }) {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const show = params.get('show') ?? 'all';
  const [search, setSearch] = useState('');
  const needle = search.trim().toLowerCase();
  const failing = (h: Health) => h.latest.state === 'failed' || h.latest.state === 'error';
  const list = pipelines.filter(
    (h) =>
      (show === 'all' ||
        (show === 'failing' && failing(h)) ||
        (show === 'running' && (h.latest.state === 'running' || h.latest.state === 'queued'))) &&
      (!needle || `${h.repository.projectKey} ${h.repository.name} ${h.name}`.toLowerCase().includes(needle)),
  );
  const counts = {
    failing: pipelines.filter(failing).length,
    running: pipelines.filter((h) => h.latest.state === 'running' || h.latest.state === 'queued').length,
  };
  return (
    <Stack gap="sm">
      <Group justify="space-between" wrap="wrap">
        <SegmentedControl
          value={show}
          onChange={(v) =>
            setParams(
              (p) => {
                if (v === 'all') p.delete('show');
                else p.set('show', v);
                return p;
              },
              { replace: true },
            )
          }
          data={[
            { value: 'all', label: t('builds.allPipelines', { count: pipelines.length }) },
            { value: 'failing', label: t('builds.failingPipelines', { count: counts.failing }) },
            { value: 'running', label: t('builds.runningPipelines', { count: counts.running }) },
          ]}
        />
        <TextInput
          w={260}
          leftSection={<IconSearch size={14} />}
          placeholder={t('builds.findPipeline')}
          aria-label={t('builds.findPipeline')}
          value={search}
          onChange={(e) => setSearch(e.currentTarget.value)}
        />
      </Group>
      <Card withBorder padding={0} radius="md">
        {list.length === 0 ? (
          <Text c="dimmed" ta="center" py="xl" size="sm">
            {pipelines.length === 0 ? t('builds.noPipelinesYet') : t('builds.noPipelineMatches')}
          </Text>
        ) : (
          <Table verticalSpacing="sm" highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th w={40} />
                <Table.Th>{t('builds.pipeline')}</Table.Th>
                <Table.Th visibleFrom="md">{t('builds.history')}</Table.Th>
                <Table.Th>{t('builds.latestRun')}</Table.Th>
                <Table.Th visibleFrom="sm">{t('builds.duration')}</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {list.map((h) => (
                <Table.Tr key={`${h.repository.projectKey}/${h.repository.slug}/${h.pipeline}`}>
                  <Table.Td>
                    <StateIcon state={h.latest.state} />
                  </Table.Td>
                  <Table.Td>
                    <Stack gap={2}>
                      <Anchor component={Link} to={pipelinesPath(h.repository)} size="sm" fw={600}>
                        {h.repository.projectKey} / {h.repository.name}
                      </Anchor>
                      <Group gap={6} wrap="nowrap">
                        <Text size="sm">{h.name}</Text>
                        <Badge
                          size="xs"
                          variant="default"
                          radius="sm"
                          tt="none"
                          leftSection={<IconGitBranch size={10} />}
                        >
                          {h.branch}
                        </Badge>
                      </Group>
                    </Stack>
                  </Table.Td>
                  <Table.Td visibleFrom="md">
                    <History health={h} />
                  </Table.Td>
                  <Table.Td>
                    <Stack gap={2}>
                      <Anchor component={Link} to={runPath(h.repository, h.latest.number)} size="sm" fw={600}>
                        #{h.latest.number}
                      </Anchor>
                      <Text size="xs" c="dimmed" lineClamp={1} title={h.latest.subject}>
                        {h.latest.subject} · <TimeAgo at={h.latest.created} />
                      </Text>
                    </Stack>
                  </Table.Td>
                  <Table.Td visibleFrom="sm">
                    <Text size="sm" style={{ whiteSpace: 'nowrap' }}>
                      <Duration started={h.latest.started} finished={h.latest.finished} />
                    </Text>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Card>
    </Stack>
  );
}

/** The runs of all readable repositories, newest first. */
function RecentRuns() {
  const { t } = useTranslation();
  const [mine, setMine] = useState('all');
  const [state, setState] = useState<string | null>(null);
  const [page, setPage] = useState(1);
  const runs = useQuery({
    queryKey: runKey('recent', mine, state, page),
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/builds/runs', {
          params: {
            query: {
              mine: mine === 'mine',
              state: (state ?? undefined) as RunState | undefined,
              limit: pageSize,
              offset: (page - 1) * pageSize,
            },
          },
        }),
      ),
    placeholderData: (p) => p,
  });
  return (
    <Stack>
      <Group>
        <SegmentedControl
          value={mine}
          onChange={(v) => {
            setMine(v);
            setPage(1);
          }}
          data={[
            { value: 'all', label: t('builds.allRuns') },
            { value: 'mine', label: t('builds.myRuns') },
          ]}
        />
        <Select
          placeholder={t('builds.allStates')}
          aria-label={t('builds.stateFilter')}
          clearable
          value={state}
          onChange={(v) => {
            setState(v);
            setPage(1);
          }}
          data={(['queued', 'running', 'succeeded', 'failed', 'cancelled', 'error'] as const).map((s) => ({
            value: s,
            label: t(`builds.state.${s}`),
          }))}
          w={180}
        />
      </Group>
      <ErrorAlert error={runs.error} />
      <Card withBorder padding="xs" radius="md">
        <RunTable runs={runs.data?.items ?? []} showRepo />
      </Card>
      {(runs.data?.total ?? 0) > pageSize && (
        <Group justify="center">
          <Pagination total={Math.ceil((runs.data?.total ?? 0) / pageSize)} value={page} onChange={setPage} />
        </Group>
      )}
    </Stack>
  );
}

/** Helling's start page: what runs and waits, the last two weeks, the pipelines and the runs. */
export function BuildsHome() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') === 'runs' ? 'runs' : 'pipelines';
  const dashboard = useQuery({
    queryKey: runKey('dashboard'),
    queryFn: () => unwrap(api.GET('/api/v1/builds/dashboard')),
    refetchInterval: 15_000,
  });
  const d = dashboard.data;
  const week = d?.week;
  const rate =
    week && week.succeeded + week.failed > 0 ? week.succeeded / (week.succeeded + week.failed) : null;
  const days = useMemo(
    () =>
      (d?.days ?? []).map((day) => ({
        day: formatDate(`${day.day}T12:00:00Z`).replace(/,? \d{4}$/, ''),
        [t('builds.state.succeeded')]: day.succeeded,
        [t('builds.state.failed')]: day.failed,
        [t('builds.state.cancelled')]: day.other,
      })),
    [d?.days, t],
  );
  return (
    <>
      <PageHeader title={t('builds.title')} subtitle={t('builds.subtitle')} />
      <Stack gap="lg">
        <ErrorAlert error={dashboard.error} />
        {d && (
          <>
            <SimpleGrid cols={{ base: 1, xs: 2, md: 3, lg: 5 }}>
              <Stat
                icon={<IconActivity size={20} />}
                color="blue"
                label={t('builds.runningNow')}
                value={d.running}
                hint={t('builds.queuedRuns', { count: d.queued })}
              />
              <Stat
                icon={<IconHourglass size={20} />}
                color={d.pendingJobs > 0 && d.agentsOnline === 0 ? 'red' : 'yellow'}
                label={t('builds.waitingJobs')}
                value={d.pendingJobs}
                hint={
                  d.oldestPending
                    ? t('builds.waitingSince', { since: formatRelative(d.oldestPending) })
                    : undefined
                }
              />
              <Stat
                icon={<IconServer2 size={20} />}
                color={d.agentsEnabled > 0 && d.agentsOnline === 0 ? 'red' : 'teal'}
                label={t('builds.agentsOnline')}
                value={`${d.agentsOnline} / ${d.agentsEnabled}`}
                hint={
                  <Anchor component={Link} to="/builds/agents" size="xs">
                    {t('builds.manageAgents')}
                  </Anchor>
                }
              />
              <Stat
                icon={<IconTrendingUp size={20} />}
                color={rate === null ? 'gray' : rate >= 0.9 ? 'teal' : rate >= 0.7 ? 'yellow' : 'red'}
                label={t('builds.successRate')}
                value={rate === null ? '—' : `${Math.round(rate * 100)} %`}
                hint={t('builds.runsThisWeek', { count: week?.runs ?? 0 })}
              />
              <Stat
                icon={<IconClockHour4 size={20} />}
                color="grape"
                label={t('builds.medianDuration')}
                value={week?.medianSeconds ? formatDuration(week.medianSeconds) : '—'}
                hint={t('builds.lastSevenDays')}
              />
            </SimpleGrid>
            <Card withBorder padding="md" radius="md">
              <Text fw={600} size="sm" mb="sm">
                {t('builds.finishedPerDay')}
              </Text>
              <BarChart
                h={180}
                data={days}
                dataKey="day"
                type="stacked"
                withLegend
                legendProps={{ verticalAlign: 'bottom', height: 30 }}
                gridAxis="y"
                tickLine="none"
                series={[
                  { name: t('builds.state.succeeded'), color: 'teal.6' },
                  { name: t('builds.state.failed'), color: 'red.6' },
                  { name: t('builds.state.cancelled'), color: 'gray.5' },
                ]}
              />
            </Card>
          </>
        )}
        <Tabs
          value={tab}
          onChange={(v) =>
            setParams(
              (p) => {
                if (v === 'runs') p.set('tab', 'runs');
                else p.delete('tab');
                return p;
              },
              { replace: true },
            )
          }
          keepMounted={false}
        >
          <Tabs.List mb="md">
            <Tabs.Tab value="pipelines">{t('builds.pipelines')}</Tabs.Tab>
            <Tabs.Tab value="runs">{t('builds.allRunsTab')}</Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="pipelines">
            <Pipelines pipelines={d?.pipelines ?? []} />
          </Tabs.Panel>
          <Tabs.Panel value="runs">
            <RecentRuns />
          </Tabs.Panel>
        </Tabs>
      </Stack>
    </>
  );
}
