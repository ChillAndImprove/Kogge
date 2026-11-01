// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Badge, Box, Button, Group, Modal, Stack, Text, TextInput, Title } from '@mantine/core';
import { IconGitBranch, IconGitCommit, IconGitPullRequest, IconRocket } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { RepositoryPicker } from '../../components/Pickers';
import { runPath } from '../builds/hooks';
import { useRepository } from '../code/hooks';
import { StateIcon } from '../builds/status';

type Issue = Schemas['Issue'];

function repoUrl(r: Schemas['RepoRef'], ...rest: string[]) {
  return ['/code', r.projectKey.toLowerCase(), r.slug, ...rest].join('/');
}

function branchSlug(text: string) {
  return text
    .toLowerCase()
    .replace(/ä/g, 'ae')
    .replace(/ö/g, 'oe')
    .replace(/ü/g, 'ue')
    .replace(/ß/g, 'ss')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '')
    .slice(0, 40)
    .replace(/-$/, '');
}

function CreateBranch({
  issue,
  only,
  onClose,
}: {
  issue: Issue;
  /** The one repository the user may create branches in, if there is just one. */
  only?: Schemas['Repository'];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  // KEY/slug of the chosen repository.
  const [chosen, setChosen] = useState<string | null>(only ? `${only.project.key}/${only.slug}` : null);
  const [key, slug] = chosen?.split('/') ?? [];
  const repo = useRepository(key, slug).data;
  const kind = issue.type?.icon === 'bug' ? 'bugfix' : 'feature';
  const [name, setName] = useState(`${kind}/${issue.key}-${branchSlug(issue.summary)}`);
  const [from, setFrom] = useState('');
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/code/repos/{key}/{slug}/branches', {
          params: { path: { key: key!, slug: slug! } },
          body: { name, from: from || undefined },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['issue', issue.key] });
      onClose();
      navigate(`/code/${key!.toLowerCase()}/${slug!}/src?at=${encodeURIComponent(name)}`);
    },
  });
  return (
    <Modal opened onClose={onClose} title={t('code.createBranchFor', { key: issue.key })}>
      <Stack>
        <RepositoryPicker
          label={t('code.repository')}
          permission="write"
          active
          value={chosen}
          onChange={setChosen}
          data-autofocus
        />
        <TextInput
          label={t('code.branchFrom')}
          placeholder={repo?.defaultBranch}
          value={from}
          onChange={(e) => setFrom(e.currentTarget.value)}
        />
        <TextInput
          label={t('code.branchName')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value.replace(/\s+/g, '-'))}
        />
        <ErrorAlert error={create.error} />
        <Group justify="flex-end">
          <Button
            onClick={() => create.mutate()}
            loading={create.isPending}
            disabled={!key || !slug || !name.trim()}
          >
            {t('code.createBranch')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

const prColor = { open: 'teal', merged: 'grape', declined: 'red' } as const;

/** Branches, commits and pull requests of the issue. */
export function Development({ issue }: { issue: Issue }) {
  const { t } = useTranslation();
  const [creating, setCreating] = useState(false);
  const [allCommits, setAllCommits] = useState(false);
  const allowed = issue.permissions.development;
  const dev = useQuery({
    queryKey: ['issue', issue.key, 'development'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/issues/{key}/development', { params: { path: { key: issue.key } } })),
    enabled: allowed,
  });
  // Whether the user may create a branch somewhere: the repositories they may push to, counted by the server; the
  // dialog searches them.
  const writable = useQuery({
    queryKey: ['repository', 'list', 'writable'],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos', {
          params: { query: { permission: 'write', archived: 'false', limit: 1 } },
        }),
      ),
    staleTime: 60_000,
    enabled: allowed,
  });
  const d = dev.data;
  const canBranch = (writable.data?.total ?? 0) > 0;
  if (!allowed) return null;
  const empty = !d || d.branches.length + d.commits.length + d.pullRequests.length + d.builds.length === 0;
  if (empty && !canBranch) return null;
  const commits = allCommits ? (d?.commits ?? []) : (d?.commits ?? []).slice(0, 5);
  return (
    <Box>
      <Group justify="space-between" mb="xs">
        <Title order={5}>{t('code.development')}</Title>
        {canBranch && (
          <Button
            size="compact-xs"
            variant="default"
            leftSection={<IconGitBranch size={12} />}
            onClick={() => setCreating(true)}
          >
            {t('code.createBranch')}
          </Button>
        )}
      </Group>
      {d && (
        <Stack gap={6}>
          {d.pullRequests.map((pr) => (
            <Group key={pr.id} gap="xs" wrap="nowrap">
              <IconGitPullRequest size={14} />
              <Anchor
                component={Link}
                to={repoUrl(pr.repository, 'pull-requests', String(pr.number))}
                size="sm"
                truncate
              >
                {pr.title}
              </Anchor>
              <Badge size="xs" color={prColor[pr.state]} variant="light">
                {t(`code.pr.${pr.state}`)}
              </Badge>
              <Text size="xs" c="dimmed" truncate>
                {pr.repository.slug}
              </Text>
            </Group>
          ))}
          {d.branches.map((b) => (
            <Group key={`${b.repository.id}:${b.name}`} gap="xs" wrap="nowrap">
              <IconGitBranch size={14} />
              <Anchor
                component={Link}
                to={`${repoUrl(b.repository, 'src')}?at=${encodeURIComponent(b.name)}`}
                size="sm"
                ff="monospace"
                truncate
              >
                {b.name}
              </Anchor>
              <Text size="xs" c="dimmed" truncate>
                {b.repository.slug}
              </Text>
              {!d.pullRequests.some((pr) => pr.source === b.name && pr.repository.id === b.repository.id) && (
                <Anchor
                  component={Link}
                  to={`${repoUrl(b.repository, 'pull-requests', 'new')}?source=${encodeURIComponent(b.name)}`}
                  size="xs"
                >
                  {t('code.openPullRequest')}
                </Anchor>
              )}
            </Group>
          ))}
          {commits.map((c) => (
            <Group key={`${c.repository.id}:${c.sha}`} gap="xs" wrap="nowrap">
              <IconGitCommit size={14} />
              <Anchor component={Link} to={repoUrl(c.repository, 'commits', c.sha)} size="sm" truncate>
                {c.subject}
              </Anchor>
              <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
                {c.author?.displayName ?? c.authorName} · {formatRelative(c.when)}
              </Text>
            </Group>
          ))}
          {!allCommits && d.commitsTotal > 5 && (
            <Anchor size="xs" onClick={() => setAllCommits(true)}>
              {t('common.more')} ({d.commitsTotal - 5})
            </Anchor>
          )}
          {allCommits && d.commitsTotal > d.commits.length && (
            <Text size="xs" c="dimmed">
              {t('code.newestCommits', { shown: d.commits.length, total: d.commitsTotal })}
            </Text>
          )}
          {d.builds.map((b) => (
            <Group key={`${b.repository.id}:${b.run.id}`} gap="xs" wrap="nowrap">
              <StateIcon state={b.run.state} size={14} />
              <Anchor component={Link} to={runPath(b.repository, b.run.number)} size="sm" truncate>
                {b.run.name} #{b.run.number}
              </Anchor>
              <Text size="xs" c="dimmed" truncate>
                {b.repository.slug}
                {b.run.branch ? ` · ${b.run.branch}` : ''}
              </Text>
            </Group>
          ))}
          {d.deployments.map((dep) => (
            <Group key={`${dep.repository.id}:${dep.environment}`} gap="xs" wrap="nowrap">
              <IconRocket size={14} />
              <Badge size="xs" variant="light" color="teal" tt="none">
                {dep.environment}
              </Badge>
              <Anchor component={Link} to={runPath(dep.repository, dep.runNumber)} size="sm">
                #{dep.runNumber}
              </Anchor>
              <Text size="xs" c="dimmed" truncate>
                {t('code.deployedIn', { repository: dep.repository.slug })} · {formatRelative(dep.finished)}
              </Text>
            </Group>
          ))}
          {d.commands.length > 0 && (
            <Text size="xs" c="dimmed">
              {t('code.commitCommands')}:{' '}
              {d.commands.map((c) => `${c.sha.slice(0, 8)} ${c.result}`).join(' · ')}
            </Text>
          )}
        </Stack>
      )}
      {creating && (
        <CreateBranch
          issue={issue}
          only={writable.data?.total === 1 ? writable.data.items[0] : undefined}
          onClose={() => setCreating(false)}
        />
      )}
    </Box>
  );
}
