// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Avatar,
  Badge,
  Button,
  Card,
  Group,
  Modal,
  Radio,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { IconBook2, IconGitPullRequest, IconSearch } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { avatarColor } from '../../components/avatarColor';
import { api, unwrap } from '../../api/client';
import { formatRelative } from '../../app/format';
import { useSession } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { AutoMergeBadge } from './components';
import { repoPath, useRepositoryPage, type CodeProject, type PullRequest, type Repository } from './hooks';

export function ProjectAvatar({
  project,
  size = 36,
}: {
  project: Pick<CodeProject, 'key' | 'name'>;
  size?: number;
}) {
  return (
    <Avatar size={size} radius="md" color={avatarColor(project.key)} variant="filled">
      {project.key.slice(0, 3)}
    </Avatar>
  );
}

export function CreateProject({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [name, setName] = useState('');
  const [key, setKey] = useState('');
  const [touched, setTouched] = useState(false);
  const [description, setDescription] = useState('');
  const [visibility, setVisibility] = useState<'private' | 'internal' | 'public'>('private');
  const suggested = name
    .toUpperCase()
    .replace(/[^A-Z0-9 ]/g, '')
    .split(/\s+/)
    .filter(Boolean)
    .map((w, _i, all) => (all.length > 1 ? w[0] : w.slice(0, 6)))
    .join('')
    .slice(0, 10);
  const effective = touched ? key : suggested;
  const create = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/api/v1/code/projects', { body: { key: effective, name, description, visibility } })),
    onSuccess: (p) => {
      void client.invalidateQueries({ queryKey: ['code-project'] });
      onClose();
      navigate(`/code/${p.key.toLowerCase()}`);
    },
  });
  return (
    <Modal opened onClose={onClose} title={t('code.createProject')} size="lg">
      <Stack>
        <TextInput
          label={t('common.name')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
          data-autofocus
        />
        <TextInput
          label={t('code.projectKey')}
          description={t('code.projectKeyHelp')}
          value={effective}
          onChange={(e) => {
            setTouched(true);
            setKey(e.currentTarget.value.toUpperCase().replace(/[^A-Z0-9_]/g, ''));
          }}
          required
        />
        <Textarea
          label={t('common.description')}
          value={description}
          onChange={(e) => setDescription(e.currentTarget.value)}
          autosize
        />
        <Radio.Group
          label={t('code.visibility')}
          value={visibility}
          onChange={(v) => setVisibility(v as typeof visibility)}
        >
          <Stack gap={6} mt={4}>
            <Radio value="private" label={t('code.visibilityPrivate')} />
            <Radio value="internal" label={t('code.visibilityInternal')} />
            <Radio value="public" label={t('code.visibilityPublic')} />
          </Stack>
        </Radio.Group>
        <ErrorAlert error={create.error} />
        <Group justify="flex-end">
          <Button
            onClick={() => create.mutate()}
            loading={create.isPending}
            disabled={!name.trim() || !effective}
          >
            {t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** A compact list of pull requests (with their repositories). */
export function PullRequestTable({ prs, showRepo = false }: { prs: PullRequest[]; showRepo?: boolean }) {
  const { t } = useTranslation();
  if (prs.length === 0) return <Text c="dimmed">{t('code.pr.none')}</Text>;
  return (
    <Table>
      <Table.Tbody>
        {prs.map((pr) => (
          <Table.Tr key={pr.id}>
            <Table.Td>
              <Group gap="xs" wrap="nowrap">
                <IconGitPullRequest
                  size={16}
                  color={
                    pr.state === 'merged'
                      ? 'var(--mantine-color-grape-6)'
                      : pr.state === 'declined'
                        ? 'var(--mantine-color-red-6)'
                        : 'var(--mantine-color-teal-6)'
                  }
                />
                <Stack gap={0} style={{ minWidth: 0 }}>
                  <Anchor
                    component={Link}
                    to={repoPath(
                      { projectKey: pr.repository.projectKey, slug: pr.repository.slug },
                      'pull-requests',
                      String(pr.number),
                    )}
                    fw={500}
                    size="sm"
                  >
                    {pr.title}
                  </Anchor>
                  <Text size="xs" c="dimmed">
                    {showRepo && `${pr.repository.projectKey}/${pr.repository.slug} · `}
                    {t('code.pr.byAuthor', {
                      number: pr.number,
                      author: pr.author?.displayName ?? t('code.pr.someone'),
                      when: formatRelative(pr.updated),
                    })}
                  </Text>
                </Stack>
              </Group>
            </Table.Td>
            <Table.Td>
              <Text size="xs" c="dimmed" ff="monospace">
                {t('code.pr.intoFrom', { source: pr.source, target: pr.target })}
              </Text>
            </Table.Td>
            <Table.Td>
              <Group gap={4} justify="flex-end">
                {pr.autoMerge && <AutoMergeBadge size="xs" blocked={!!pr.autoMerge.blocked} />}
                {pr.draft && (
                  <Badge size="xs" variant="outline">
                    {t('code.pr.draft')}
                  </Badge>
                )}
                {pr.reviewers.map((r) => (
                  <Badge
                    key={r.user.id}
                    size="xs"
                    variant="light"
                    color={r.state === 'approved' ? 'teal' : r.state === 'needs-work' ? 'orange' : 'gray'}
                  >
                    {r.user.displayName}
                  </Badge>
                ))}
              </Group>
            </Table.Td>
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  );
}

/** Repositories the start page lists at most, grouped by project; more are found through the search. */
const shownRepositories = 500;

/**
 * The code module's start page: every code project as a group with its repositories, the project pushed to last
 * first and in each the repository pushed to last, as on Hafen's dashboard. The search is part of the address, so
 * that the header's search lands here and going back returns to the same list.
 */
export function CodeHome() {
  const { t } = useTranslation();
  const { can } = useSession();
  const [params, setParams] = useSearchParams();
  const [creating, setCreating] = useState(false);
  const query = params.get('q') ?? '';
  const [debounced] = useDebouncedValue(query.trim(), 250);
  const setQuery = (q: string) => {
    const next = new URLSearchParams(params);
    if (q) next.set('q', q);
    else next.delete('q');
    setParams(next, { replace: true });
  };
  const projects = useQuery({
    queryKey: ['code-project', 'list', 'home'],
    queryFn: () => unwrap(api.GET('/api/v1/code/projects', { params: { query: { limit: 200 } } })),
  });
  const repos = useRepositoryPage({
    query: debounced || undefined,
    sort: 'pushed',
    limit: shownRepositories,
  });
  const items = repos.data?.items ?? [];
  const byProject = new Map<string, Repository[]>();
  for (const r of items) byProject.set(r.project.key, [...(byProject.get(r.project.key) ?? []), r]);
  // Projects in the order of their latest push; without a search, empty projects follow by name.
  const pushed = [...byProject.keys()];
  const groups = [
    ...pushed.map(
      (key) =>
        (projects.data?.items ?? []).find((p) => p.key === key) ??
        items.find((r) => r.project.key === key)!.project,
    ),
    ...(debounced ? [] : (projects.data?.items ?? []).filter((p) => !byProject.has(p.key))),
  ];
  const total = repos.data?.total ?? 0;
  return (
    <>
      <PageHeader
        title={t('code.title')}
        subtitle={t('code.subtitle')}
        actions={
          can('codeproject.create') && (
            <Button onClick={() => setCreating(true)}>{t('code.createProject')}</Button>
          )
        }
      />
      <Group justify="space-between" mb="md">
        <Text size="sm" c="dimmed" aria-live="polite">
          {projects.data && t('code.projectCount', { count: projects.data.total })}
          {repos.data && ` · ${t('code.repositoryCount', { count: total })}`}
        </Text>
        <TextInput
          leftSection={<IconSearch size={14} />}
          placeholder={t('code.searchRepositories')}
          value={query}
          onChange={(e) => setQuery(e.currentTarget.value)}
          w={280}
          aria-label={t('code.searchRepositories')}
        />
      </Group>
      <ErrorAlert error={projects.error ?? repos.error} />
      {projects.data?.total === 0 && <Text c="dimmed">{t('code.noProjects')}</Text>}
      {repos.isSuccess && debounced && total === 0 && (
        <Text c="dimmed">{t('code.noMatchingRepositories')}</Text>
      )}
      <Stack gap="md">
        {groups.map((p) => (
          <ProjectGroup key={p.key} project={p} repos={byProject.get(p.key) ?? []} />
        ))}
      </Stack>
      {total > items.length && (
        <Text size="sm" c="dimmed" mt="md">
          {t('code.moreRepositories', { shown: items.length, total })}
        </Text>
      )}
      {creating && <CreateProject onClose={() => setCreating(false)} />}
    </>
  );
}

/** A code project with its repositories: its name, what it holds, and each repository with its last push. */
function ProjectGroup({
  project,
  repos,
}: {
  project: Pick<CodeProject, 'key' | 'name'> & Partial<Pick<CodeProject, 'description' | 'repositories'>>;
  repos: Repository[];
}) {
  const { t } = useTranslation();
  return (
    <Card withBorder padding={0} component="section" aria-label={project.name}>
      <Group gap="sm" px="md" py="sm" wrap="nowrap" className="kogge-hafen-group-head">
        <ProjectAvatar project={project} size={28} />
        <Stack gap={0} style={{ minWidth: 0, flex: 1 }}>
          <Group gap="xs" wrap="nowrap">
            <Anchor component={Link} to={`/code/${project.key.toLowerCase()}`} fw={650} c="inherit" truncate>
              {project.name}
            </Anchor>
            <Text size="xs" c="dimmed">
              {project.key}
            </Text>
          </Group>
          {project.description && (
            <Text size="xs" c="dimmed" truncate>
              {project.description}
            </Text>
          )}
        </Stack>
        <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
          {t('code.repositoryCount', { count: project.repositories ?? repos.length })}
        </Text>
      </Group>
      {repos.length === 0 ? (
        <Text size="sm" c="dimmed" px="md" py="sm">
          {t('code.noRepositories')}
        </Text>
      ) : (
        <ul className="kogge-hafen-repos kogge-hafen-repos-wide">
          {repos.map((r) => (
            <li key={r.id}>
              <IconBook2 size={16} className="kogge-hafen-repo-icon" aria-hidden />
              <Stack gap={0} style={{ minWidth: 0, flex: 1 }}>
                <Group gap="xs" wrap="nowrap">
                  <Anchor component={Link} to={repoPath(r)} fw={550} size="sm" truncate>
                    {r.name}
                  </Anchor>
                  {r.archived && (
                    <Badge size="xs" variant="outline" color="gray">
                      {t('code.archived')}
                    </Badge>
                  )}
                </Group>
                {r.description && (
                  <Text size="xs" c="dimmed" truncate>
                    {r.description}
                  </Text>
                )}
              </Stack>
              <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
                {formatRelative(r.pushed ?? r.created)}
              </Text>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

export function RepositoryTable({
  repos,
  showProject = false,
  empty,
}: {
  repos: Repository[];
  showProject?: boolean;
  /** What to say when there are none (default: no repositories yet). */
  empty?: string;
}) {
  const { t } = useTranslation();
  if (repos.length === 0) return <Text c="dimmed">{empty ?? t('code.noRepositories')}</Text>;
  return (
    <Table>
      <Table.Tbody>
        {repos.map((r) => (
          <Table.Tr key={r.id}>
            <Table.Td>
              <Group gap="xs">
                <Anchor component={Link} to={repoPath(r)} fw={500}>
                  {showProject ? `${r.project.key} / ${r.name}` : r.name}
                </Anchor>
                {r.archived && (
                  <Badge size="xs" variant="outline" color="gray">
                    {t('code.archived')}
                  </Badge>
                )}
              </Group>
              {r.description && (
                <Text size="xs" c="dimmed" lineClamp={1}>
                  {r.description}
                </Text>
              )}
            </Table.Td>
            <Table.Td>
              <Text size="xs" c="dimmed" ta="right">
                {r.pushed ? formatRelative(r.pushed) : formatRelative(r.created)}
              </Text>
            </Table.Td>
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  );
}
