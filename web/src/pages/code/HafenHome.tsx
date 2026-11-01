// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Button,
  Card,
  Grid,
  Group,
  SegmentedControl,
  Stack,
  Tabs,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { IconBook2, IconGitPullRequest, IconSearch, IconTimeline } from '@tabler/icons-react';
import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatRelative } from '../../app/format';
import { ActivityStream } from '../../components/ActivityStream';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { ProjectAvatar, PullRequestTable } from './CodeHome';
import { repoPath, useRepositoryPage } from './hooks';

/** Repositories in the panel of the start page; the full list pages through all of them. */
const shownRepositories = 200;

type Repository = Schemas['Repository'];

/**
 * The user's repositories in a panel at the side, grouped by code project: the project pushed to last comes first,
 * and in each project the repository pushed to last.
 */
function RepositoryList() {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  const [debounced] = useDebouncedValue(query.trim(), 250);
  const repos = useRepositoryPage({
    query: debounced || undefined,
    sort: 'pushed',
    limit: shownRepositories,
  });
  const total = repos.data?.total ?? 0;
  const items = repos.data?.items ?? [];
  const groups = new Map<string, { project: Repository['project']; repos: Repository[] }>();
  for (const r of items) {
    const group = groups.get(r.project.key) ?? { project: r.project, repos: [] };
    group.repos.push(r);
    groups.set(r.project.key, group);
  }
  return (
    <Card withBorder padding={0} className="kogge-hafen-panel">
      <Group justify="space-between" px="md" pt="sm" pb="xs" wrap="nowrap">
        <Text fw={650} size="sm">
          {t('hafen.yourRepositories')}
        </Text>
        {/* The full list finds and pages through all of them; it says how many there are when not all fit here. */}
        <Anchor
          component={Link}
          to={debounced ? `/code/repos?q=${encodeURIComponent(debounced)}` : '/code/repos'}
          size="xs"
        >
          {total > items.length ? t('hafen.allRepositoriesTotal', { total }) : t('hafen.allRepositories')}
        </Anchor>
      </Group>
      <TextInput
        size="xs"
        px="md"
        pb="sm"
        leftSection={<IconSearch size={14} />}
        placeholder={t('code.searchRepositories')}
        aria-label={t('code.searchRepositories')}
        value={query}
        onChange={(e) => setQuery(e.currentTarget.value)}
      />
      {[...groups.values()].map(({ project, repos: list }) => (
        <section key={project.key} className="kogge-hafen-group" aria-label={project.name}>
          <Group gap={8} px="md" py={6} wrap="nowrap" className="kogge-hafen-group-head">
            <ProjectAvatar project={project} size={18} />
            <Anchor
              component={Link}
              to={`/code/${project.key.toLowerCase()}`}
              size="sm"
              fw={600}
              c="inherit"
              truncate
            >
              {project.name}
            </Anchor>
            <Text size="xs" c="dimmed" ml="auto">
              {list.length}
            </Text>
          </Group>
          <ul className="kogge-hafen-repos">
            {list.map((r) => (
              <li key={r.id}>
                <IconBook2 size={14} className="kogge-hafen-repo-icon" aria-hidden />
                <Anchor component={Link} to={repoPath(r)} size="sm" truncate>
                  {r.name}
                </Anchor>
                {r.pushed && (
                  <Text size="xs" c="dimmed" ml="auto" style={{ whiteSpace: 'nowrap' }}>
                    {formatRelative(r.pushed)}
                  </Text>
                )}
              </li>
            ))}
          </ul>
        </section>
      ))}
      {repos.isSuccess && total === 0 && (
        <Text size="sm" c="dimmed" px="md" pb="md">
          {debounced ? t('code.noMatchingRepositories') : t('code.noRepositories')}
        </Text>
      )}
    </Card>
  );
}

/** Pull requests in the card of the start page; one more is asked for, to know whether more wait. */
const shownPullRequests = 10;

/** Pull requests the user is asked to review, or opened. */
function PullRequestsCard() {
  const { t } = useTranslation();
  const [role, setRole] = useState<'reviewer' | 'author'>('reviewer');
  const prs = useQuery({
    queryKey: ['pull-request', 'mine', role, 'open', 'card'],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/pull-requests', {
          params: { query: { role, state: 'open', limit: shownPullRequests + 1 } },
        }),
      ),
  });
  const list = prs.data ?? [];
  return (
    <Card withBorder padding="lg">
      <Group justify="space-between" mb="sm">
        <Title order={4} style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <IconGitPullRequest size={20} />
          {t('hafen.pullRequests')}
        </Title>
        <Anchor component={Link} to="/code/pull-requests" size="sm">
          {t('hafen.allPullRequests')}
        </Anchor>
      </Group>
      <Tabs value={role} onChange={(v) => setRole((v as typeof role) ?? 'reviewer')}>
        <Tabs.List mb="sm">
          <Tabs.Tab value="reviewer">{t('code.toReview')}</Tabs.Tab>
          <Tabs.Tab value="author">{t('code.authored')}</Tabs.Tab>
        </Tabs.List>
      </Tabs>
      <PullRequestTable prs={list.slice(0, shownPullRequests)} showRepo />
      {list.length > shownPullRequests && (
        <Anchor component={Link} to={`/code/pull-requests?role=${role}`} size="sm" mt="xs" display="block">
          {t('hafen.morePullRequests')}
        </Anchor>
      )}
    </Card>
  );
}

/** What happened lately in the repositories the user can see: pushes, pull requests and their reviews. */
function ActivityCard() {
  const { t } = useTranslation();
  return (
    <Card withBorder padding="lg">
      <Title order={4} mb="sm" style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <IconTimeline size={20} />
        {t('activity.title')}
      </Title>
      <ActivityStream product="hafen" density="compact" limit={15} />
    </Card>
  );
}

/**
 * Hafen's start page: the pull requests and what happened lately, with the user's repositories by project at the
 * side (on phones first).
 */
export function HafenHome() {
  return (
    <Grid gap="xl">
      <Grid.Col span={{ base: 12, md: 8 }} order={{ base: 2, md: 1 }}>
        <Stack gap="lg">
          <PullRequestsCard />
          <ActivityCard />
        </Stack>
      </Grid.Col>
      <Grid.Col span={{ base: 12, md: 4 }} order={{ base: 1, md: 2 }}>
        <RepositoryList />
      </Grid.Col>
    </Grid>
  );
}

/** Pull requests per page of the full list; one more is asked for, to know whether another page follows. */
const pullRequestPage = 50;

const roles = ['reviewer', 'author'] as const;
const states = ['open', 'merged', 'declined', 'all'] as const;

/** The pull requests of the user across repositories, loaded a page at a time; role and state are in the address. */
export function PullRequestsHome() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const role = roles.find((r) => r === params.get('role')) ?? 'reviewer';
  const state = states.find((s) => s === params.get('state')) ?? 'open';
  const choose = (key: 'role' | 'state', value: string, fallback: string) => {
    const next = new URLSearchParams(params);
    if (value === fallback) next.delete(key);
    else next.set(key, value);
    setParams(next, { replace: true });
  };
  const prs = useInfiniteQuery({
    queryKey: ['pull-request', 'mine', role, state, 'pages'],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/api/v1/code/pull-requests', {
          params: { query: { role, state, limit: pullRequestPage + 1, offset: pageParam } },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (last, pages) =>
      last.length > pullRequestPage ? pages.length * pullRequestPage : undefined,
  });
  // A pull request updated while the next page loads moves up the list and would show twice.
  const seen = new Set<number>();
  const list = (prs.data?.pages ?? [])
    .flatMap((p) => p.slice(0, pullRequestPage))
    .filter((pr) => !seen.has(pr.id) && !!seen.add(pr.id));
  return (
    <>
      <PageHeader title={t('hafen.pullRequests')} subtitle={t('hafen.pullRequestsSubtitle')} />
      <Group mb="md">
        <SegmentedControl
          value={role}
          onChange={(v) => choose('role', v, 'reviewer')}
          data={[
            { value: 'reviewer', label: t('code.toReview') },
            { value: 'author', label: t('code.authored') },
          ]}
        />
        <SegmentedControl
          value={state}
          onChange={(v) => choose('state', v, 'open')}
          data={states.map((s) => ({ value: s, label: t(`hafen.state.${s}`) }))}
        />
      </Group>
      <ErrorAlert error={prs.error} />
      <Card withBorder padding="xs">
        <PullRequestTable prs={list} showRepo />
      </Card>
      {prs.hasNextPage && (
        <Group justify="center" mt="md">
          <Button variant="default" onClick={() => void prs.fetchNextPage()} loading={prs.isFetchingNextPage}>
            {t('code.loadMore')}
          </Button>
        </Group>
      )}
    </>
  );
}
