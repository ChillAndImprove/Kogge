// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Button,
  Checkbox,
  Group,
  Modal,
  PasswordInput,
  Stack,
  Tabs,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { modals } from '@mantine/modals';
import { IconSearch } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { useRecordVisit } from '../../app/visits';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError } from '../../components/notify';
import { PageHeader } from '../../components/PageHeader';
import { ListPages } from '../../components/Pagination';
import { ProjectAvatar, RepositoryTable } from './CodeHome';
import { repoPath, useRepositoryPage } from './hooks';

/** Repositories per page of a code project. */
const repoPageSize = 50;
import { WebhooksEditor } from '../../components/Webhooks';
import { BranchRulesEditor, GrantsEditor } from './Permissions';

export function CreateRepository({ projectKey, onClose }: { projectKey: string; onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [name, setName] = useState('');
  const [slug, setSlug] = useState('');
  const [touched, setTouched] = useState(false);
  const [description, setDescription] = useState('');
  const [branch, setBranch] = useState('main');
  const [readme, setReadme] = useState(true);
  const [importing, setImporting] = useState(false);
  const [importUrl, setImportUrl] = useState('');
  const [importUsername, setImportUsername] = useState('');
  const [importPassword, setImportPassword] = useState('');
  const suggested = name
    .toLowerCase()
    .replace(/[^a-z0-9._]+/g, '-')
    .replace(/^[-.]+|[-.]+$/g, '');
  const effective = touched ? slug : suggested;
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/code/projects/{key}/repos', {
          params: { path: { key: projectKey } },
          body: {
            name,
            slug: effective,
            description,
            defaultBranch: branch,
            readme: readme && !importing,
            ...(importing ? { importUrl: importUrl.trim(), importUsername, importPassword } : {}),
          },
        }),
      ),
    onSuccess: (r) => {
      void client.invalidateQueries({ queryKey: ['repository'] });
      void client.invalidateQueries({ queryKey: ['code-project'] });
      onClose();
      navigate(repoPath(r));
    },
  });
  return (
    <Modal opened onClose={onClose} title={t('code.createRepository')} size="lg">
      <Stack>
        <TextInput
          label={t('common.name')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
          data-autofocus
        />
        <TextInput
          label={t('code.slug')}
          description={t('code.slugHelp')}
          value={effective}
          onChange={(e) => {
            setTouched(true);
            setSlug(e.currentTarget.value.toLowerCase().replace(/[^a-z0-9._-]/g, ''));
          }}
          required
        />
        <Textarea
          label={t('common.description')}
          value={description}
          onChange={(e) => setDescription(e.currentTarget.value)}
          autosize
        />
        <TextInput
          label={t('code.defaultBranch')}
          value={branch}
          onChange={(e) => setBranch(e.currentTarget.value)}
        />
        <Checkbox
          label={t('code.importFromServer')}
          description={t('code.importHelp')}
          checked={importing}
          onChange={(e) => setImporting(e.currentTarget.checked)}
        />
        {importing ? (
          <>
            <TextInput
              label={t('code.importUrl')}
              placeholder="https://git.example.com/team/project.git"
              value={importUrl}
              onChange={(e) => setImportUrl(e.currentTarget.value)}
              required
            />
            <Group grow>
              <TextInput
                label={t('code.importUsername')}
                value={importUsername}
                onChange={(e) => setImportUsername(e.currentTarget.value)}
                autoComplete="off"
              />
              <PasswordInput
                label={t('code.importPassword')}
                value={importPassword}
                onChange={(e) => setImportPassword(e.currentTarget.value)}
                autoComplete="new-password"
              />
            </Group>
          </>
        ) : (
          <Checkbox
            label={t('code.withReadme')}
            checked={readme}
            onChange={(e) => setReadme(e.currentTarget.checked)}
          />
        )}
        <ErrorAlert error={create.error} />
        <Group justify="flex-end">
          <Button
            onClick={() => create.mutate()}
            loading={create.isPending}
            disabled={!name.trim() || !effective || (importing && !importUrl.trim())}
          >
            {t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** A code project: its repositories and, for administrators, its permissions. */
export function ProjectPage() {
  const { t } = useTranslation();
  const { key = '' } = useParams();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [query, setQuery] = useState('');
  const [debounced] = useDebouncedValue(query.trim(), 250);
  const [page, setPage] = useState(1);
  const project = useQuery({
    queryKey: ['code-project', key.toUpperCase()],
    queryFn: () => unwrap(api.GET('/api/v1/code/projects/{key}', { params: { path: { key } } })),
    retry: false,
  });
  useRecordVisit('code-project', project.data?.id);
  const repos = useRepositoryPage({
    project: key,
    query: debounced || undefined,
    limit: repoPageSize,
    offset: (page - 1) * repoPageSize,
  });
  const admin = project.data?.canAdmin ?? false;
  const grants = useQuery({
    queryKey: ['code-project', key.toUpperCase(), 'permissions'],
    queryFn: () => unwrap(api.GET('/api/v1/code/projects/{key}/permissions', { params: { path: { key } } })),
    enabled: admin,
  });
  const rules = useQuery({
    queryKey: ['code-project', key.toUpperCase(), 'branch-permissions'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/code/projects/{key}/branch-permissions', { params: { path: { key } } })),
    enabled: admin,
  });
  const remove = useMutation({
    mutationFn: () => unwrap(api.DELETE('/api/v1/code/projects/{key}', { params: { path: { key } } })),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['code-project'] });
      navigate('/code');
    },
    onError: notifyError,
  });
  if (project.error) return <ErrorAlert error={project.error} />;
  if (!project.data) return null;
  const p = project.data;
  return (
    <>
      <PageHeader
        title={
          <Group gap="sm">
            <ProjectAvatar project={p} />
            {p.name}
          </Group>
        }
        subtitle={p.description || p.key}
        actions={admin && <Button onClick={() => setCreating(true)}>{t('code.createRepository')}</Button>}
      />
      <Tabs defaultValue="repositories" keepMounted={false}>
        <Tabs.List mb="md">
          <Tabs.Tab value="repositories">{t('code.repositories')}</Tabs.Tab>
          {admin && <Tabs.Tab value="permissions">{t('code.settingsTabs.permissions')}</Tabs.Tab>}
          {admin && <Tabs.Tab value="branches">{t('code.settingsTabs.branchPermissions')}</Tabs.Tab>}
          {admin && <Tabs.Tab value="webhooks">{t('webhooks.title')}</Tabs.Tab>}
        </Tabs.List>
        <Tabs.Panel value="repositories">
          <Stack gap="sm">
            <Group justify="space-between">
              <Text size="sm" c="dimmed" aria-live="polite">
                {!!repos.data?.total && t('code.repositoryCount', { count: repos.data.total })}
              </Text>
              <TextInput
                leftSection={<IconSearch size={14} />}
                placeholder={t('code.searchRepositories')}
                aria-label={t('code.searchRepositories')}
                value={query}
                onChange={(e) => {
                  setQuery(e.currentTarget.value);
                  setPage(1);
                }}
                w={280}
              />
            </Group>
            <ErrorAlert error={repos.error} />
            {repos.isSuccess && (
              <RepositoryTable
                repos={repos.data.items}
                empty={debounced ? t('code.noMatchingRepositories') : undefined}
              />
            )}
            <ListPages total={repos.data?.total ?? 0} size={repoPageSize} page={page} onChange={setPage} />
          </Stack>
        </Tabs.Panel>
        <Tabs.Panel value="permissions">
          {grants.data && (
            <Stack>
              <GrantsEditor
                grants={grants.data}
                project
                onSave={(g) =>
                  unwrap(
                    api.PUT('/api/v1/code/projects/{key}/permissions', {
                      params: { path: { key } },
                      body: g,
                    }),
                  ).then(() => client.invalidateQueries({ queryKey: ['code-project', key.toUpperCase()] }))
                }
              />
              {repos.data?.total === 0 && !debounced && (
                <Group justify="flex-end">
                  <Button
                    color="red"
                    variant="light"
                    onClick={() =>
                      modals.openConfirmModal({
                        title: t('code.deleteProject'),
                        labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                        confirmProps: { color: 'red' },
                        onConfirm: () => remove.mutate(),
                      })
                    }
                  >
                    {t('code.deleteProject')}
                  </Button>
                </Group>
              )}
            </Stack>
          )}
        </Tabs.Panel>
        <Tabs.Panel value="branches">
          {rules.data && (
            <BranchRulesEditor
              rules={rules.data}
              onSave={(r) =>
                unwrap(
                  api.PUT('/api/v1/code/projects/{key}/branch-permissions', {
                    params: { path: { key } },
                    body: r,
                  }),
                ).then(() => client.invalidateQueries({ queryKey: ['code-project', key.toUpperCase()] }))
              }
            />
          )}
        </Tabs.Panel>
        <Tabs.Panel value="webhooks">
          <WebhooksEditor scope="code-project" scopeKey={p.key} />
        </Tabs.Panel>
      </Tabs>
      {creating && <CreateRepository projectKey={p.key} onClose={() => setCreating(false)} />}
    </>
  );
}
