// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Badge,
  Button,
  Card,
  Collapse,
  Divider,
  Group,
  Modal,
  NumberInput,
  Stack,
  Table,
  TagsInput,
  Text,
  Textarea,
  TextInput,
  Title,
} from '@mantine/core';
import { IconChevronDown, IconChevronRight, IconPlus, IconRocket, IconSettings } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { Pagination } from '../../components/Pagination';
import { GroupsPicker, UsersPicker } from '../../components/Pickers';
import { repoParams, repoPath, shortSha, useRepo } from '../code/hooks';
import { StateBadge, StateIcon, TimeAgo } from './components';
import { runKey, runPath, shortRef, useEnvironments, type Deployment, type Environment } from './hooks';
import { VariablesEditor } from './Variables';

/** The environments of a repository with their deployments. */
export function Environments() {
  const { t } = useTranslation();
  const repo = useRepo();
  const envs = useEnvironments(repo);
  const [editing, setEditing] = useState<Environment | 'new' | null>(null);
  const admin = repo.permission === 'admin';
  return (
    <Stack>
      <Group justify="space-between">
        <Text c="dimmed" size="sm">
          {t('builds.environmentsHelp')}
        </Text>
        {admin && (
          <Button leftSection={<IconPlus size={14} />} variant="light" onClick={() => setEditing('new')}>
            {t('builds.newEnvironment')}
          </Button>
        )}
      </Group>
      <ErrorAlert error={envs.error} />
      {envs.data?.length === 0 && (
        <Text c="dimmed" ta="center" py="lg">
          {t('builds.noEnvironments')}
        </Text>
      )}
      {(envs.data ?? []).map((e) => (
        <EnvironmentCard key={e.id} env={e} admin={admin} onEdit={() => setEditing(e)} />
      ))}
      {editing && (
        <EnvironmentModal env={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />
      )}
    </Stack>
  );
}

function EnvironmentCard({ env, admin, onEdit }: { env: Environment; admin: boolean; onEdit: () => void }) {
  const { t } = useTranslation();
  const repo = useRepo();
  const [open, setOpen] = useState(false);
  const current = env.current;
  return (
    <Card withBorder>
      <Group justify="space-between" align="flex-start">
        <Stack gap={4}>
          <Group gap="xs">
            <IconRocket size={18} />
            <Title order={4}>{env.name}</Title>
            {env.approvals > 0 && (
              <Badge variant="light" color="orange" size="sm">
                {t('builds.requiresApprovals', { count: env.approvals })}
              </Badge>
            )}
            {env.refs.length > 0 && (
              <Badge variant="outline" color="gray" size="sm" tt="none">
                {env.refs.join(', ')}
              </Badge>
            )}
          </Group>
          {env.description && (
            <Text size="sm" c="dimmed">
              {env.description}
            </Text>
          )}
          {current ? (
            <Group gap="xs">
              <StateIcon state="succeeded" size={16} />
              <Text size="sm">
                {t('builds.currentDeployment')}{' '}
                <Anchor component={Link} to={runPath(repo, current.runNumber)}>
                  #{current.runNumber}
                </Anchor>{' '}
                · {shortRef(current.ref)} ·{' '}
                <Anchor component={Link} to={repoPath(repo, 'commits', current.commit)} ff="monospace">
                  {shortSha(current.commit)}
                </Anchor>{' '}
                · {current.createdBy?.displayName ?? ''} ·{' '}
                <TimeAgo at={current.finished ?? current.created} />
              </Text>
            </Group>
          ) : (
            <Text size="sm" c="dimmed">
              {t('builds.neverDeployed')}
            </Text>
          )}
          {env.last && env.last.id !== current?.id && (
            <Group gap="xs">
              <StateIcon state={env.last.state} size={16} />
              <Text size="sm" c="dimmed">
                {env.last.state === 'waiting' ? t('builds.waitingDeployment') : t('builds.lastDeployment')}{' '}
                <Anchor component={Link} to={`${runPath(repo, env.last.runNumber)}?job=${env.last.jobId}`}>
                  #{env.last.runNumber}
                </Anchor>{' '}
                · {shortRef(env.last.ref)}
              </Text>
            </Group>
          )}
        </Stack>
        <Group gap="xs">
          <Button
            variant="subtle"
            size="xs"
            leftSection={open ? <IconChevronDown size={14} /> : <IconChevronRight size={14} />}
            onClick={() => setOpen(!open)}
          >
            {t('builds.history')}
          </Button>
          {admin && (
            <Button variant="default" size="xs" leftSection={<IconSettings size={14} />} onClick={onEdit}>
              {t('builds.configure')}
            </Button>
          )}
        </Group>
      </Group>
      <Collapse expanded={open} keepMounted={false}>
        <DeploymentHistory env={env} />
      </Collapse>
    </Card>
  );
}

/** Deployments of an environment per page of its history. */
const historyPage = 20;

function DeploymentHistory({ env }: { env: Environment }) {
  const { t } = useTranslation();
  const repo = useRepo();
  const client = useQueryClient();
  const canRun = repo.permission === 'write' || repo.permission === 'admin';
  const [page, setPage] = useState(1);
  const deps = useQuery({
    queryKey: runKey(repo.id, 'deployments', env.name, page),
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/environments/{name}/deployments', {
          params: {
            path: { ...repoParams(repo), name: env.name },
            query: { limit: historyPage, offset: (page - 1) * historyPage },
          },
        }),
      ),
    placeholderData: (p) => p,
  });
  const pages = Math.ceil((deps.data?.total ?? 0) / historyPage);
  const redeploy = useMutation({
    mutationFn: (d: Deployment) =>
      unwrap(api.POST('/api/v1/builds/deployments/{id}/redeploy', { params: { path: { id: d.id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: runKey(repo.id) }),
  });
  return (
    <Stack mt="md" gap="xs">
      <Divider />
      <ErrorAlert error={deps.error ?? redeploy.error} />
      <Table verticalSpacing={4}>
        <Table.Tbody>
          {(deps.data?.items ?? []).map((d) => (
            <Table.Tr key={d.id}>
              <Table.Td w={120}>
                <StateBadge state={d.state} />
              </Table.Td>
              <Table.Td>
                <Anchor component={Link} to={`${runPath(repo, d.runNumber)}?job=${d.jobId}`}>
                  #{d.runNumber}
                </Anchor>{' '}
                · {shortRef(d.ref)} ·{' '}
                <Text span ff="monospace" size="sm">
                  {shortSha(d.commit)}
                </Text>
              </Table.Td>
              <Table.Td>{d.createdBy?.displayName ?? ''}</Table.Td>
              <Table.Td>
                <TimeAgo at={d.created} />
              </Table.Td>
              <Table.Td w={120}>
                {canRun && (d.state === 'succeeded' || d.state === 'failed' || d.state === 'cancelled') && (
                  <Button
                    size="compact-xs"
                    variant="light"
                    onClick={() => redeploy.mutate(d)}
                    loading={redeploy.isPending && redeploy.variables?.id === d.id}
                  >
                    {t('builds.redeploy')}
                  </Button>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      {deps.data?.total === 0 && (
        <Text c="dimmed" size="sm">
          {t('builds.neverDeployed')}
        </Text>
      )}
      {pages > 1 && (
        <Group justify="center">
          <Pagination size="sm" total={pages} value={Math.min(page, pages)} onChange={setPage} />
        </Group>
      )}
    </Stack>
  );
}

function EnvironmentModal({ env, onClose }: { env: Environment | null; onClose: () => void }) {
  const { t } = useTranslation();
  const repo = useRepo();
  const client = useQueryClient();
  const [name, setName] = useState(env?.name ?? '');
  const [description, setDescription] = useState(env?.description ?? '');
  const [refs, setRefs] = useState<string[]>(env?.refs ?? []);
  const [deployerUsers, setDeployerUsers] = useState<number[]>(env?.deployerUsers ?? []);
  const [deployerGroups, setDeployerGroups] = useState<number[]>(env?.deployerGroups ?? []);
  const [approvals, setApprovals] = useState(env?.approvals ?? 0);
  const [approverUsers, setApproverUsers] = useState<number[]>(env?.approverUsers ?? []);
  const [approverGroups, setApproverGroups] = useState<number[]>(env?.approverGroups ?? []);
  const knownUsers = (env?.users ?? []).map((u) => ({ id: u.id, label: u.displayName }));
  const knownGroups = (env?.groups ?? []).map((g) => ({ id: g.id, label: g.name }));
  const invalidate = () => client.invalidateQueries({ queryKey: runKey(repo.id) });
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/code/repos/{key}/{slug}/environments/{name}', {
          params: { path: { ...repoParams(repo), name } },
          body: {
            description,
            refs,
            deployerUsers,
            deployerGroups,
            approvals,
            approverUsers,
            approverGroups,
          },
        }),
      ),
    onSuccess: () => {
      void invalidate();
      onClose();
    },
  });
  const remove = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/api/v1/code/repos/{key}/{slug}/environments/{name}', {
          params: { path: { ...repoParams(repo), name } },
        }),
      ),
    onSuccess: () => {
      void invalidate();
      onClose();
    },
  });
  return (
    <Modal
      opened
      onClose={onClose}
      title={env ? t('builds.configureEnvironment', { name: env.name }) : t('builds.newEnvironment')}
      size="lg"
    >
      <Stack>
        {!env && (
          <TextInput
            label={t('builds.environmentName')}
            description={t('builds.environmentNameHelp')}
            value={name}
            onChange={(e) => setName(e.currentTarget.value.toLowerCase())}
            required
          />
        )}
        <Textarea
          label={t('common.description')}
          value={description}
          onChange={(e) => setDescription(e.currentTarget.value)}
          autosize
        />
        <TagsInput
          label={t('builds.allowedRefs')}
          description={t('builds.allowedRefsHelp')}
          value={refs}
          onChange={setRefs}
        />
        <UsersPicker
          label={t('builds.deployers')}
          description={t('builds.deployersHelp')}
          value={deployerUsers}
          onChange={setDeployerUsers}
          known={knownUsers}
        />
        <GroupsPicker
          label={t('builds.deployerGroups')}
          value={deployerGroups}
          onChange={setDeployerGroups}
          known={knownGroups}
        />
        <NumberInput
          label={t('builds.requiredApprovals')}
          min={0}
          max={10}
          value={approvals}
          onChange={(v) => setApprovals(Number(v) || 0)}
        />
        {approvals > 0 && (
          <>
            <UsersPicker
              label={t('builds.approvers')}
              value={approverUsers}
              onChange={setApproverUsers}
              known={knownUsers}
            />
            <GroupsPicker
              label={t('builds.approverGroups')}
              value={approverGroups}
              onChange={setApproverGroups}
              known={knownGroups}
            />
          </>
        )}
        {env && (
          <>
            <Divider label={t('builds.environmentVariables')} labelPosition="left" />
            <VariablesEditor
              scope={{
                scope: 'environment',
                project: repo.project.key,
                repository: repo.slug,
                environment: env.name,
              }}
            />
          </>
        )}
        <ErrorAlert error={save.error ?? remove.error} />
        <Group justify="space-between">
          {env ? (
            <Button color="red" variant="subtle" onClick={() => remove.mutate()} loading={remove.isPending}>
              {t('common.delete')}
            </Button>
          ) : (
            <span />
          )}
          <Group>
            <Button variant="default" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name}>
              {t('common.save')}
            </Button>
          </Group>
        </Group>
      </Stack>
    </Modal>
  );
}
