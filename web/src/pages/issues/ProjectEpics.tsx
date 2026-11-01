// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Badge,
  Button,
  Card,
  Group,
  Progress,
  SegmentedControl,
  SimpleGrid,
  Skeleton,
  Stack,
  Text,
} from '@mantine/core';
import { IconListDetails, IconPlus, IconStack2 } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { EmptyState } from '../../components/EmptyState';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { epicColor } from '../agile/hooks';
import { useCreateIssue } from './CreateIssue';
import { useProjectContext } from './ProjectLayout';
import { kqlString } from './ProjectReleases';
import { StatusBadge } from './shared';

type IssueRow = Schemas['IssueRow'];

/** Issues per request; a project's epics and their issues are read page by page. */
const searchPage = 1000;

/** All issues a query finds, page by page. */
async function searchAll(q: string): Promise<IssueRow[]> {
  const all: IssueRow[] = [];
  for (;;) {
    const page = await unwrap(
      api.GET('/api/v1/search', { params: { query: { q, limit: searchPage, offset: all.length } } }),
    );
    all.push(...page.issues);
    if (page.issues.length === 0 || all.length >= page.total) return all;
  }
}

interface Epic {
  issue: IssueRow;
  issues: number;
  done: number;
}

const filters = ['open', 'done', 'all'] as const;
type Filter = (typeof filters)[number];

/** The epics of a project with the progress of their issues, open ones first in the order of their rank. */
function useEpics(projectKey: string, epicType: string | undefined) {
  return useQuery({
    queryKey: ['issue', 'epics', projectKey],
    enabled: !!epicType,
    queryFn: async (): Promise<Epic[]> => {
      const epics = await searchAll(
        `project = ${projectKey} AND issuetype = ${kqlString(epicType!)} ORDER BY rank`,
      );
      if (epics.length === 0) return [];
      // Their issues may be in other projects too.
      const children = await searchAll(`parent IN (${epics.map((e) => e.key).join(', ')})`);
      const counts = new Map(epics.map((e) => [e.key, { issues: 0, done: 0 }]));
      for (const c of children) {
        const n = c.parentKey ? counts.get(c.parentKey) : undefined;
        if (!n) continue;
        n.issues++;
        if (c.status.category === 'done') n.done++;
      }
      return epics.map((issue) => ({ issue, ...counts.get(issue.key)! }));
    },
  });
}

function EpicCard({ epic }: { epic: Epic }) {
  const { t } = useTranslation();
  const { issue, issues, done } = epic;
  const color = epicColor(issue.id);
  const percent = issues ? Math.round((100 * done) / issues) : 0;
  return (
    <Card
      withBorder
      padding="md"
      radius="md"
      className="kogge-epic-card"
      style={{ borderLeftColor: `var(--mantine-color-${color}-6)` }}
    >
      <Group justify="space-between" wrap="nowrap" gap="xs" mb={6}>
        <Anchor component={Link} to={`/browse/${issue.key}`} size="xs" c="dimmed">
          {issue.key}
        </Anchor>
        <StatusBadge status={issue.status} />
      </Group>
      <Anchor component={Link} to={`/browse/${issue.key}`} fw={650} c="inherit" lineClamp={2} mb="sm">
        {issue.summary}
      </Anchor>
      <Group justify="space-between" gap="xs" mb={4}>
        <Text size="xs" c="dimmed">
          {issues ? t('agile.epicProgress', { done, total: issues }) : t('issues.epicNoIssues')}
        </Text>
        {issues > 0 && (
          <Text size="xs" fw={650}>
            {percent} %
          </Text>
        )}
      </Group>
      <Progress
        value={percent}
        color={color}
        size="sm"
        radius="xl"
        aria-label={t('agile.epicProgress', { done, total: issues })}
      />
      <Group justify="space-between" mt="sm" gap="xs">
        <Badge variant="light" color={color} radius="sm">
          {t('issues.epicOpenIssues', { count: issues - done })}
        </Badge>
        {issues > 0 && (
          <Anchor
            component={Link}
            to={`/issues/search?q=${encodeURIComponent(`parent = ${issue.key} ORDER BY status, rank`)}`}
            size="xs"
          >
            <Group gap={4} wrap="nowrap">
              <IconListDetails size={14} />
              {t('issues.epicShowIssues')}
            </Group>
          </Anchor>
        )}
      </Group>
    </Card>
  );
}

/** A project's epics: what each larger goal holds and how far it is, like the backlog's panel but with all of them. */
export function ProjectEpics() {
  const { t } = useTranslation();
  const project = useProjectContext();
  const createIssue = useCreateIssue();
  const epicType = project.issueTypes.find((ty) => ty.level === 1);
  const epics = useEpics(project.key, epicType?.name);
  const [filter, setFilter] = useState<Filter>('open');
  const list = epics.data ?? [];
  const isDone = (e: Epic) => e.issue.status.category === 'done';
  const shown = list.filter((e) => filter === 'all' || (filter === 'done') === isDone(e));
  const count = {
    open: list.filter((e) => !isDone(e)).length,
    done: list.filter(isDone).length,
    all: list.length,
  };
  const canCreate = !!epicType && project.permissions.includes('issue.create') && !project.archived;
  const create = () => createIssue({ project: project.key, typeId: epicType!.id });
  return (
    <>
      <PageHeader
        title={t('agile.epics')}
        subtitle={t('issues.epicsSubtitle', { name: project.name })}
        actions={
          canCreate && (
            <Button leftSection={<IconPlus size={16} />} onClick={create}>
              {t('agile.createEpic')}
            </Button>
          )
        }
      />
      <ErrorAlert error={epics.error} />
      {epics.isPending && epicType ? (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} h={150} radius="md" />
          ))}
        </SimpleGrid>
      ) : list.length === 0 ? (
        <EmptyState
          icon={IconStack2}
          title={t('issues.noEpicsTitle')}
          description={t('issues.noEpics')}
          action={
            canCreate && (
              <Button leftSection={<IconPlus size={16} />} onClick={create}>
                {t('agile.createEpic')}
              </Button>
            )
          }
        />
      ) : (
        <Stack gap="md">
          <SegmentedControl
            value={filter}
            onChange={(v) => setFilter(v as Filter)}
            w="fit-content"
            data={filters.map((f) => ({ value: f, label: `${t(`issues.epicFilter.${f}`)} (${count[f]})` }))}
          />
          {shown.length === 0 ? (
            <Text c="dimmed" size="sm">
              {t('issues.noEpicsHere')}
            </Text>
          ) : (
            <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
              {shown.map((e) => (
                <EpicCard key={e.issue.id} epic={e} />
              ))}
            </SimpleGrid>
          )}
        </Stack>
      )}
    </>
  );
}
