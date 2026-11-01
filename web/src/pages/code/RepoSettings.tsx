// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Box,
  ActionIcon,
  Button,
  Card,
  Checkbox,
  Code,
  Group,
  MultiSelect,
  NumberInput,
  Progress,
  Stack,
  Table,
  Tabs,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatBytes, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError, notifySuccess } from '../../components/notify';
import { CodeProjectPicker, UsersPicker } from '../../components/Pickers';
import { BranchPicker } from './components';
import { repoParams, repoPath, useRepo, type Repository } from './hooks';
import { WebhooksEditor } from '../../components/Webhooks';
import { CodeOwnersSummary } from './CodeOwners';
import { BranchRulesEditor, GrantsEditor } from './Permissions';

/** What the repository takes on the server, and its quota. */
function Storage({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const total = repo.size + repo.lfsSize;
  const quota = repo.quota ?? 0;
  const share = quota > 0 ? Math.min(100, Math.round((total / quota) * 100)) : 0;
  const ofQuota = t('code.storageOfQuota', { used: formatBytes(total), quota: formatBytes(quota) });
  const row = (label: string, value: string) => (
    <Group justify="space-between" gap="xs">
      <Text size="sm" c="dimmed">
        {label}
      </Text>
      <Text size="sm" fw={500}>
        {value}
      </Text>
    </Group>
  );
  return (
    <Card withBorder mt="xl">
      <Stack gap="xs">
        <Text fw={500}>{t('code.storage')}</Text>
        {row(t('code.storageGit'), formatBytes(repo.size))}
        {row(t('code.storageLfs'), formatBytes(repo.lfsSize))}
        {row(t('code.storageQuota'), quota > 0 ? ofQuota : t('code.storageNoQuota'))}
        {quota > 0 && (
          <Progress
            value={share}
            color={share >= 100 ? 'red' : share > 90 ? 'orange' : 'teal'}
            aria-label={ofQuota}
          />
        )}
        <Text size="xs" c="dimmed">
          {t('code.storageHelp')}
        </Text>
      </Stack>
    </Card>
  );
}

function General({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState(repo.name);
  const [slug, setSlug] = useState(repo.slug);
  const [description, setDescription] = useState(repo.description);
  const [defaultBranch, setDefaultBranch] = useState(repo.defaultBranch);
  const [project, setProject] = useState<string | null>(repo.project.key);
  const save = useMutation({
    mutationFn: (body: Schemas['RepositoryUpdate']) =>
      unwrap(api.PATCH('/api/v1/code/repos/{key}/{slug}', { params: { path: repoParams(repo) }, body })),
    onSuccess: (r) => {
      void client.invalidateQueries({ queryKey: ['repository'] });
      notifySuccess(t('common.saved'));
      if (r.slug !== repo.slug || r.project.key !== repo.project.key) navigate(repoPath(r, 'settings'));
    },
  });
  const remove = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE('/api/v1/code/repos/{key}/{slug}', { params: { path: repoParams(repo) } })),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['repository'] });
      navigate(`/code/${repo.project.key.toLowerCase()}`);
    },
    onError: notifyError,
  });
  return (
    <Stack maw={640}>
      <TextInput label={t('common.name')} value={name} onChange={(e) => setName(e.currentTarget.value)} />
      <TextInput
        label={t('code.slug')}
        description={t('code.slugHelp')}
        value={slug}
        onChange={(e) => setSlug(e.currentTarget.value.toLowerCase())}
      />
      <Textarea
        label={t('common.description')}
        value={description}
        onChange={(e) => setDescription(e.currentTarget.value)}
        autosize
      />
      <BranchPicker
        repo={repo}
        label={t('code.defaultBranch')}
        value={defaultBranch}
        onChange={(v) => setDefaultBranch(v ?? defaultBranch)}
      />
      <CodeProjectPicker
        label={t('code.moveTo')}
        admin
        clearable={false}
        value={project}
        onChange={(v) => setProject(v ?? repo.project.key)}
      />
      <ErrorAlert error={save.error} />
      <Group justify="flex-end">
        <Button
          onClick={() =>
            save.mutate({
              name,
              slug,
              description,
              defaultBranch: defaultBranch !== repo.defaultBranch ? defaultBranch : undefined,
              project: project && project !== repo.project.key ? project : undefined,
            })
          }
          loading={save.isPending}
        >
          {t('common.save')}
        </Button>
      </Group>
      <Storage repo={repo} />
      <Card withBorder mt="xl">
        <Stack>
          <Group justify="space-between">
            <Stack gap={0}>
              <Text fw={500}>{repo.archived ? t('code.unarchive') : t('code.archive')}</Text>
              <Text size="xs" c="dimmed">
                {t('code.archiveHelp')}
              </Text>
            </Stack>
            <Button variant="default" onClick={() => save.mutate({ archived: !repo.archived })}>
              {repo.archived ? t('code.unarchive') : t('code.archive')}
            </Button>
          </Group>
          <Group justify="space-between">
            <Text fw={500}>{t('code.deleteRepository')}</Text>
            <Button
              color="red"
              variant="light"
              onClick={() =>
                modals.openConfirmModal({
                  title: t('code.deleteRepository'),
                  children: (
                    <Text size="sm">
                      {t('code.deleteRepositoryConfirm', { name: `${repo.project.key}/${repo.slug}` })}
                    </Text>
                  ),
                  labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                  confirmProps: { color: 'red' },
                  onConfirm: () => remove.mutate(),
                })
              }
            >
              {t('code.deleteRepository')}
            </Button>
          </Group>
        </Stack>
      </Card>
    </Stack>
  );
}

function PullRequestSettings({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [s, setS] = useState(repo.settings);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/api/v1/code/repos/{key}/{slug}', {
          params: { path: repoParams(repo) },
          body: { settings: s },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['repository'] });
      notifySuccess(t('common.saved'));
    },
  });
  return (
    <Stack maw={640}>
      <MultiSelect
        label={t('code.mergeStrategies')}
        description={t('code.mergeStrategiesHelp')}
        value={s.mergeStrategies}
        onChange={(v) => setS({ ...s, mergeStrategies: v as typeof s.mergeStrategies })}
        data={(['merge', 'squash', 'fast-forward', 'rebase', 'rebase-merge'] as const).map((m) => ({
          value: m,
          label: t(`code.pr.strategies.${m}`),
        }))}
      />
      <NumberInput
        label={t('code.requiredApprovals')}
        min={0}
        max={20}
        value={s.requiredApprovals}
        onChange={(v) => setS({ ...s, requiredApprovals: Number(v) || 0 })}
      />
      <NumberInput
        label={t('code.requiredBuilds')}
        min={0}
        max={20}
        value={s.requiredBuilds}
        onChange={(v) => setS({ ...s, requiredBuilds: Number(v) || 0 })}
      />
      <Checkbox
        label={t('code.externalStatuses')}
        description={t('code.externalStatusesHelp')}
        checked={!!s.externalStatuses}
        onChange={(e) => setS({ ...s, externalStatuses: e.currentTarget.checked })}
      />
      <Checkbox
        label={t('code.noNeedsWork')}
        checked={s.noNeedsWork}
        onChange={(e) => setS({ ...s, noNeedsWork: e.currentTarget.checked })}
      />
      <Checkbox
        label={t('code.tasksResolved')}
        checked={s.tasksResolved}
        onChange={(e) => setS({ ...s, tasksResolved: e.currentTarget.checked })}
      />
      <Checkbox
        label={t('code.resetApprovals')}
        checked={s.resetApprovals}
        onChange={(e) => setS({ ...s, resetApprovals: e.currentTarget.checked })}
      />
      <Stack gap={6}>
        <Checkbox
          label={t('code.codeOwnerApproval')}
          description={t('code.codeOwnerApprovalHelp')}
          checked={!!s.codeOwnerApproval}
          onChange={(e) => setS({ ...s, codeOwnerApproval: e.currentTarget.checked })}
        />
        <Box pl={32}>
          <CodeOwnersSummary repo={repo} />
        </Box>
      </Stack>
      <Checkbox
        label={t('code.deleteSourceBranch')}
        checked={s.deleteSourceBranch}
        onChange={(e) => setS({ ...s, deleteSourceBranch: e.currentTarget.checked })}
      />
      <UsersPicker
        label={t('code.defaultReviewers')}
        value={s.defaultReviewers}
        onChange={(ids) => setS({ ...s, defaultReviewers: ids })}
      />
      <ErrorAlert error={save.error} />
      <Group justify="flex-end">
        <Button
          onClick={() => save.mutate()}
          loading={save.isPending}
          disabled={s.mergeStrategies.length === 0}
        >
          {t('common.save')}
        </Button>
      </Group>
    </Stack>
  );
}

function DeployKeys({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [name, setName] = useState('');
  const [key, setKey] = useState('');
  const [write, setWrite] = useState(false);
  const keys = useQuery({
    queryKey: ['repository', repo.id, 'deploy-keys'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/code/repos/{key}/{slug}/deploy-keys', { params: { path: repoParams(repo) } })),
  });
  const add = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/code/repos/{key}/{slug}/deploy-keys', {
          params: { path: repoParams(repo) },
          body: { name, key, write },
        }),
      ),
    onSuccess: () => {
      setName('');
      setKey('');
      setWrite(false);
      void client.invalidateQueries({ queryKey: ['repository', repo.id, 'deploy-keys'] });
    },
  });
  const remove = useMutation({
    mutationFn: (id: number) =>
      unwrap(
        api.DELETE('/api/v1/code/repos/{key}/{slug}/deploy-keys/{id}', {
          params: { path: { ...repoParams(repo), id } },
        }),
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: ['repository', repo.id, 'deploy-keys'] }),
    onError: notifyError,
  });
  return (
    <Stack>
      <Text size="sm" c="dimmed">
        {t('code.deployKeysHelp')}
      </Text>
      {(keys.data ?? []).length === 0 ? (
        <Text c="dimmed">{t('code.noDeployKeys')}</Text>
      ) : (
        <Table>
          <Table.Tbody>
            {(keys.data ?? []).map((k) => (
              <Table.Tr key={k.id}>
                <Table.Td>
                  <Text size="sm" fw={500}>
                    {k.name}
                  </Text>
                  <Code fz="xs">{k.fingerprint}</Code>
                </Table.Td>
                <Table.Td>{k.write ? t('code.writeAccess') : t('code.readOnly')}</Table.Td>
                <Table.Td>
                  <Text size="xs" c="dimmed">
                    {k.lastUsed ? formatRelative(k.lastUsed) : t('common.never')}
                  </Text>
                </Table.Td>
                <Table.Td>
                  <ActionIcon
                    variant="subtle"
                    color="red"
                    onClick={() => remove.mutate(k.id)}
                    aria-label={t('common.remove')}
                  >
                    <IconTrash size={14} />
                  </ActionIcon>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      <Card withBorder>
        <Stack>
          <TextInput label={t('common.name')} value={name} onChange={(e) => setName(e.currentTarget.value)} />
          <Textarea
            label={t('code.publicKey')}
            value={key}
            onChange={(e) => setKey(e.currentTarget.value)}
            autosize
            minRows={2}
            placeholder="ssh-ed25519 AAAA…"
          />
          <Checkbox
            label={t('code.writeAccess')}
            checked={write}
            onChange={(e) => setWrite(e.currentTarget.checked)}
          />
          <ErrorAlert error={add.error} />
          <Group justify="flex-end">
            <Button onClick={() => add.mutate()} loading={add.isPending} disabled={!key.trim()}>
              {t('code.addDeployKey')}
            </Button>
          </Group>
        </Stack>
      </Card>
    </Stack>
  );
}

/** Settings of a repository (administrators). */
export function RepoSettings() {
  const { t } = useTranslation();
  const repo = useRepo();
  const client = useQueryClient();
  const [search, setSearch] = useSearchParams();
  const tab = search.get('tab') ?? 'general';
  const grants = useQuery({
    queryKey: ['repository', repo.id, 'permissions'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/code/repos/{key}/{slug}/permissions', { params: { path: repoParams(repo) } })),
    enabled: tab === 'permissions',
  });
  const rules = useQuery({
    queryKey: ['repository', repo.id, 'branch-permissions'],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/branch-permissions', { params: { path: repoParams(repo) } }),
      ),
    enabled: tab === 'branches',
  });
  if (repo.permission !== 'admin') return <ErrorAlert error={new Error(t('errors.forbidden'))} />;
  return (
    <Tabs
      value={tab}
      onChange={(v) => setSearch({ tab: v ?? 'general' })}
      orientation="vertical"
      keepMounted={false}
    >
      <Tabs.List mr="lg">
        <Tabs.Tab value="general">{t('code.settingsTabs.general')}</Tabs.Tab>
        <Tabs.Tab value="permissions">{t('code.settingsTabs.permissions')}</Tabs.Tab>
        <Tabs.Tab value="branches">{t('code.settingsTabs.branchPermissions')}</Tabs.Tab>
        <Tabs.Tab value="pull-requests">{t('code.settingsTabs.pullRequests')}</Tabs.Tab>
        <Tabs.Tab value="deploy-keys">{t('code.settingsTabs.deployKeys')}</Tabs.Tab>
        <Tabs.Tab value="webhooks">{t('webhooks.title')}</Tabs.Tab>
      </Tabs.List>
      <Tabs.Panel value="general">
        <General repo={repo} />
      </Tabs.Panel>
      <Tabs.Panel value="permissions">
        <Stack>
          <Text size="sm" c="dimmed">
            {t('code.repositoryPermissions')}
          </Text>
          {grants.data && (
            <GrantsEditor
              grants={grants.data}
              project={false}
              onSave={(g) =>
                unwrap(
                  api.PUT('/api/v1/code/repos/{key}/{slug}/permissions', {
                    params: { path: repoParams(repo) },
                    body: g,
                  }),
                ).then(() => client.invalidateQueries({ queryKey: ['repository'] }))
              }
            />
          )}
        </Stack>
      </Tabs.Panel>
      <Tabs.Panel value="branches">
        {rules.data && (
          <BranchRulesEditor
            rules={rules.data}
            onSave={(r) =>
              unwrap(
                api.PUT('/api/v1/code/repos/{key}/{slug}/branch-permissions', {
                  params: { path: repoParams(repo) },
                  body: r,
                }),
              ).then(() =>
                client.invalidateQueries({ queryKey: ['repository', repo.id, 'branch-permissions'] }),
              )
            }
          />
        )}
      </Tabs.Panel>
      <Tabs.Panel value="pull-requests">
        <PullRequestSettings repo={repo} />
      </Tabs.Panel>
      <Tabs.Panel value="deploy-keys">
        <DeployKeys repo={repo} />
      </Tabs.Panel>
      <Tabs.Panel value="webhooks">
        <WebhooksEditor scope="repository" scopeKey={`${repo.project.key}/${repo.slug}`} />
      </Tabs.Panel>
    </Tabs>
  );
}
