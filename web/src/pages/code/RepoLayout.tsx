// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Alert,
  Anchor,
  Badge,
  Button,
  Group,
  Loader,
  PasswordInput,
  Stack,
  Tabs,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import {
  IconBook2,
  IconCode,
  IconGitBranch,
  IconGitCommit,
  IconGitPullRequest,
  IconSettings,
  IconTimeline,
} from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, Outlet, useLocation, useNavigate, useParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { useRecordVisit } from '../../app/visits';
import { ActivityStream } from '../../components/ActivityStream';
import { ErrorAlert } from '../../components/ErrorAlert';
import { products } from '../../products';
import { pipelinesPath } from '../builds/hooks';
import { RepoSearchBox } from './CodeSearch';
import { CloneButton } from './components';
import { RepoContext, repoPath, useRepo, useRepository } from './hooks';

/** What happened recently in the repository: pushes, pull requests, runs and deployments. */
export function RepoActivity() {
  const r = useRepo();
  return <ActivityStream repository={`${r.project.key}/${r.slug}`} />;
}

/** The frame of a repository: name, clone addresses and the sections. */
export function RepoLayout() {
  const { t } = useTranslation();
  const { key = '', repo: slug = '', number } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const repo = useRepository(key, slug);
  // A pull request on screen is the visit, not its repository.
  useRecordVisit('repository', number ? undefined : repo.data?.id);
  if (repo.error) return <ErrorAlert error={repo.error} />;
  if (!repo.data) return null;
  const r = repo.data;
  const base = repoPath(r);
  const Helling = products.helling.Logo;
  const rest = location.pathname.slice(base.length).split('/')[1] ?? '';
  const tab = ['commits', 'branches', 'tags', 'pull-requests', 'activity', 'settings', 'compare'].includes(
    rest,
  )
    ? rest === 'tags'
      ? 'branches'
      : rest === 'compare'
        ? 'pull-requests'
        : rest
    : 'src';
  return (
    <RepoContext.Provider value={r}>
      <div className="kogge-repo-head">
        <Stack gap={2} miw={0}>
          {/* Project / repository on one line, as code hosts name them; the project leads to its page. */}
          <Group gap="xs" wrap="nowrap" miw={0}>
            <IconBook2 size={22} className="kogge-repo-icon" aria-hidden />
            <Title order={2} size="h3" fw={400} className="kogge-repo-title">
              <Anchor component={Link} to={`/code/${r.project.key.toLowerCase()}`} inherit>
                {r.project.name}
              </Anchor>
              {' / '}
              <Text span inherit fw={650}>
                {r.name}
              </Text>
            </Title>
            {r.archived && (
              <Badge variant="outline" color="gray">
                {t('code.archived')}
              </Badge>
            )}
          </Group>
          {r.description && (
            <Text size="sm" c="dimmed">
              {r.description}
            </Text>
          )}
        </Stack>
        <Group gap="xs" wrap="nowrap" className="kogge-repo-actions">
          <RepoSearchBox repo={r} />
          <CloneButton repo={r} />
        </Group>
      </div>
      {r.import && r.import.state !== 'done' && <ImportState repo={r} />}
      <Tabs
        value={tab}
        onChange={(v) => navigate(v === 'helling' ? pipelinesPath(r) : v === 'src' ? base : `${base}/${v}`)}
        mb="md"
      >
        <Tabs.List>
          <Tabs.Tab value="src" leftSection={<IconCode size={14} />}>
            {t('code.source')}
          </Tabs.Tab>
          <Tabs.Tab value="commits" leftSection={<IconGitCommit size={14} />}>
            {t('code.commits')}
          </Tabs.Tab>
          <Tabs.Tab value="branches" leftSection={<IconGitBranch size={14} />}>
            {t('code.branches')}
          </Tabs.Tab>
          <Tabs.Tab value="pull-requests" leftSection={<IconGitPullRequest size={14} />}>
            {t('code.pullRequests')}
          </Tabs.Tab>
          <Tabs.Tab value="helling" leftSection={<Helling size={14} />}>
            {t('hafen.builds')}
          </Tabs.Tab>
          <Tabs.Tab value="activity" leftSection={<IconTimeline size={14} />}>
            {t('activity.title')}
          </Tabs.Tab>
          {r.permission === 'admin' && (
            <Tabs.Tab value="settings" leftSection={<IconSettings size={14} />}>
              {t('code.settings')}
            </Tabs.Tab>
          )}
        </Tabs.List>
      </Tabs>
      <Outlet />
    </RepoContext.Provider>
  );
}

/** A running or failed import from another Git server; administrators start a failed one again. */
function ImportState({ repo }: { repo: Schemas['Repository'] }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const retry = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/code/repos/{key}/{slug}/import', {
          params: { path: { key: repo.project.key, slug: repo.slug } },
          body: { username, password },
        }),
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: ['repository'] }),
  });
  const imp = repo.import!;
  if (imp.state === 'running') {
    return (
      <Alert color="blue" mb="md" icon={<Loader size={16} />}>
        {t('code.importRunning', { url: imp.url })}
      </Alert>
    );
  }
  return (
    <Alert color="red" mb="md" title={t('code.importFailed', { url: imp.url })}>
      <Stack gap="xs">
        <Text size="sm">{imp.error}</Text>
        {repo.permission === 'admin' && (
          <Group align="flex-end">
            <TextInput
              size="xs"
              label={t('code.importUsername')}
              value={username}
              onChange={(e) => setUsername(e.currentTarget.value)}
              autoComplete="off"
            />
            <PasswordInput
              size="xs"
              label={t('code.importPassword')}
              value={password}
              onChange={(e) => setPassword(e.currentTarget.value)}
              autoComplete="new-password"
            />
            <Button size="xs" onClick={() => retry.mutate()} loading={retry.isPending}>
              {t('code.importRetry')}
            </Button>
          </Group>
        )}
        <ErrorAlert error={retry.error} />
      </Stack>
    </Alert>
  );
}
