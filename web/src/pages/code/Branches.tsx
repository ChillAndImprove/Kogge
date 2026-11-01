// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Badge,
  Button,
  Group,
  Modal,
  Stack,
  Table,
  Tabs,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { modals } from '@mantine/modals';
import { IconGitPullRequest, IconSearch, IconTrash } from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useLocation, useNavigate } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError } from '../../components/notify';
import { ListPages } from '../../components/Pagination';
import { useStatuses } from '../builds/hooks';
import { CommitStatusIcon } from '../builds/status';
import { PersonLabel, RefSelect } from './components';
import {
  repoParams,
  repoPath,
  shortSha,
  srcPath,
  useBranches,
  useRepo,
  useTags,
  type Repository,
} from './hooks';

export function CreateBranchModal({
  repo,
  initialName = '',
  onClose,
}: {
  repo: Repository;
  initialName?: string;
  onClose: (name?: string) => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [name, setName] = useState(initialName);
  const [from, setFrom] = useState(repo.defaultBranch);
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/code/repos/{key}/{slug}/branches', {
          params: { path: repoParams(repo) },
          body: { name, from },
        }),
      ),
    onSuccess: (b) => {
      void client.invalidateQueries({ queryKey: ['repository', repo.id] });
      onClose(b.name);
    },
  });
  return (
    <Modal opened onClose={() => onClose()} title={t('code.newBranch')}>
      <Stack>
        <RefSelect repo={repo} value={from} onChange={setFrom} label={t('code.branchFrom')} />
        <TextInput
          label={t('code.branchName')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value.replace(/\s+/g, '-'))}
          required
          data-autofocus
        />
        <ErrorAlert error={create.error} />
        <Group justify="flex-end">
          <Button onClick={() => create.mutate()} loading={create.isPending} disabled={!name.trim()}>
            {t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** Branches and tags per page; the server counts the distances of a page's branches only. */
const pageSize = 50;

function BranchList({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const [filter, setFilter] = useState('');
  const [debounced] = useDebouncedValue(filter.trim(), 250);
  const [page, setPage] = useState(1);
  const [creating, setCreating] = useState(false);
  const branches = useBranches(repo, debounced, pageSize, (page - 1) * pageSize);
  const statuses = useStatuses(
    repo,
    (branches.data?.items ?? []).map((b) => b.commit.sha),
  );
  const remove = useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.DELETE('/api/v1/code/repos/{key}/{slug}/branches', {
          params: { path: repoParams(repo), query: { name } },
        }),
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: ['repository', repo.id] }),
    onError: notifyError,
  });
  const writable = repo.permission === 'write' || repo.permission === 'admin';
  return (
    <Stack>
      <Group justify="space-between">
        <Group>
          <TextInput
            leftSection={<IconSearch size={14} />}
            placeholder={t('code.filterBranches')}
            value={filter}
            onChange={(e) => {
              setFilter(e.currentTarget.value);
              setPage(1);
            }}
            w={280}
            aria-label={t('code.filterBranches')}
          />
          <Text size="sm" c="dimmed" aria-live="polite">
            {!!branches.data?.total && t('code.branchCount', { count: branches.data.total })}
          </Text>
        </Group>
        {writable && !repo.archived && (
          <Button onClick={() => setCreating(true)}>{t('code.newBranch')}</Button>
        )}
      </Group>
      <ErrorAlert error={branches.error} />
      {branches.data?.total === 0 && debounced && <Text c="dimmed">{t('code.noMatchingBranches')}</Text>}
      <Table>
        <Table.Tbody>
          {(branches.data?.items ?? []).map((b) => (
            <Table.Tr key={b.name}>
              <Table.Td>
                <Group gap="xs">
                  <Anchor component={Link} to={srcPath(repo, b.name, '')} ff="monospace" size="sm">
                    {b.name}
                  </Anchor>
                  {b.default && (
                    <Badge size="xs" variant="light">
                      {t('code.default')}
                    </Badge>
                  )}
                </Group>
                <Group gap="xs" mt={2}>
                  <CommitStatusIcon statuses={statuses.data?.[b.commit.sha]} />
                  <PersonLabel person={b.commit.author} size="xs" />
                  <Text size="xs" c="dimmed" truncate maw={360}>
                    {b.commit.subject} · {shortSha(b.commit.sha)} · {formatRelative(b.commit.committer.when)}
                  </Text>
                </Group>
              </Table.Td>
              <Table.Td>
                {!b.default && (
                  <Group gap={4}>
                    <Badge size="sm" variant="outline" color="teal">
                      {t('code.ahead', { count: b.ahead })}
                    </Badge>
                    <Badge size="sm" variant="outline" color="gray">
                      {t('code.behind', { count: b.behind })}
                    </Badge>
                  </Group>
                )}
              </Table.Td>
              <Table.Td>
                <Group gap={4} justify="flex-end" wrap="nowrap">
                  {!b.default && (
                    <Tooltip label={t('code.openPullRequest')}>
                      <ActionIcon
                        variant="subtle"
                        onClick={() =>
                          navigate(
                            `${repoPath(repo, 'pull-requests', 'new')}?source=${encodeURIComponent(b.name)}`,
                          )
                        }
                        aria-label={t('code.openPullRequest')}
                      >
                        <IconGitPullRequest size={16} />
                      </ActionIcon>
                    </Tooltip>
                  )}
                  {writable && !b.default && !repo.archived && (
                    <Tooltip label={t('code.deleteBranch')}>
                      <ActionIcon
                        variant="subtle"
                        color="red"
                        aria-label={t('code.deleteBranch')}
                        onClick={() =>
                          modals.openConfirmModal({
                            title: t('code.deleteBranch'),
                            children: (
                              <Text size="sm">{t('code.deleteBranchConfirm', { name: b.name })}</Text>
                            ),
                            labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                            confirmProps: { color: 'red' },
                            onConfirm: () => remove.mutate(b.name),
                          })
                        }
                      >
                        <IconTrash size={16} />
                      </ActionIcon>
                    </Tooltip>
                  )}
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <ListPages total={branches.data?.total ?? 0} size={pageSize} page={page} onChange={setPage} />
      {creating && (
        <CreateBranchModal
          repo={repo}
          onClose={(name) => {
            setCreating(false);
            if (name) navigate(srcPath(repo, name, ''));
          }}
        />
      )}
    </Stack>
  );
}

function TagList({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState('');
  const [debounced] = useDebouncedValue(filter.trim(), 250);
  const [page, setPage] = useState(1);
  const tags = useTags(repo, debounced, pageSize, (page - 1) * pageSize);
  return (
    <Stack>
      <Group>
        <TextInput
          leftSection={<IconSearch size={14} />}
          placeholder={t('code.filterTags')}
          value={filter}
          onChange={(e) => {
            setFilter(e.currentTarget.value);
            setPage(1);
          }}
          w={280}
          aria-label={t('code.filterTags')}
        />
        <Text size="sm" c="dimmed" aria-live="polite">
          {!!tags.data?.total && t('code.tagCount', { count: tags.data.total })}
        </Text>
      </Group>
      <ErrorAlert error={tags.error} />
      {tags.data?.total === 0 && (
        <Text c="dimmed">{debounced ? t('code.noMatchingTags') : t('code.noTags')}</Text>
      )}
      <Table>
        <Table.Tbody>
          {(tags.data?.items ?? []).map((tg) => (
            <Table.Tr key={tg.name}>
              <Table.Td>
                <Group gap="xs">
                  <Anchor component={Link} to={srcPath(repo, tg.name, '')} ff="monospace" size="sm">
                    {tg.name}
                  </Anchor>
                  {tg.annotated && (
                    <Badge size="xs" variant="light">
                      {t('code.annotated')}
                    </Badge>
                  )}
                </Group>
                {tg.message && (
                  <Text size="xs" c="dimmed">
                    {tg.message}
                  </Text>
                )}
              </Table.Td>
              <Table.Td>
                <Anchor
                  component={Link}
                  to={repoPath(repo, 'commits', tg.commit.sha)}
                  size="xs"
                  ff="monospace"
                >
                  {shortSha(tg.commit.sha)}
                </Anchor>
              </Table.Td>
              <Table.Td>
                <Text size="xs" c="dimmed">
                  {formatRelative(tg.commit.committer.when)}
                </Text>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <ListPages total={tags.data?.total ?? 0} size={pageSize} page={page} onChange={setPage} />
    </Stack>
  );
}

/** Branches and tags. */
export function Branches() {
  const { t } = useTranslation();
  const repo = useRepo();
  const location = useLocation();
  const navigate = useNavigate();
  const tab = location.pathname.endsWith('/tags') ? 'tags' : 'branches';
  return (
    <Tabs value={tab} onChange={(v) => navigate(repoPath(repo, v ?? 'branches'))} variant="pills">
      <Tabs.List mb="md">
        <Tabs.Tab value="branches">{t('code.branches')}</Tabs.Tab>
        <Tabs.Tab value="tags">{t('code.tags')}</Tabs.Tab>
      </Tabs.List>
      <Tabs.Panel value="branches">
        <BranchList repo={repo} />
      </Tabs.Panel>
      <Tabs.Panel value="tags">
        <TagList repo={repo} />
      </Tabs.Panel>
    </Tabs>
  );
}
