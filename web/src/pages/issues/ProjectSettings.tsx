// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  Group,
  SegmentedControl,
  Select,
  Stack,
  Switch,
  Table,
  Tabs,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconX } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { useSession } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { GroupPicker, SpacePicker, UserPicker } from '../../components/Pickers';
import { notifyError, notifySuccess } from '../../components/notify';
import { useProjectContext, type ProjectView } from './ProjectLayout';
import { TypeIcon, useIssueConfig } from './shared';
import { WebhooksEditor } from '../../components/Webhooks';
import { Automation } from './Automation';

/** The wiki space of the project: release notes and retrospectives are created there. */
function ProjectSpace({ project }: { project: ProjectView }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const linked = useQuery({
    queryKey: ['project', project.key, 'space'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/projects/{key}/space', { params: { path: { key: project.key } } })),
  });
  const save = useMutation({
    mutationFn: (space: string | null) =>
      unwrap(
        api.PUT('/api/v1/projects/{key}/space', {
          params: { path: { key: project.key } },
          body: { space: space ?? '' },
        }),
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: ['project', project.key, 'space'] }),
  });
  return (
    <Stack maw={640} mt="xl" gap="xs">
      <Title order={5}>{t('linkedPage.projectSpace')}</Title>
      <Text size="sm" c="dimmed">
        {t('linkedPage.projectSpaceHelp')}
      </Text>
      {linked.data && (
        <SpacePicker
          value={linked.data.space?.key ?? null}
          onChange={(v) => save.mutate(v)}
          aria-label={t('linkedPage.projectSpace')}
        />
      )}
      <ErrorAlert error={save.error ?? linked.error} />
    </Stack>
  );
}

function Details({ project }: { project: ProjectView }) {
  const { t } = useTranslation();
  const { can } = useSession();
  const client = useQueryClient();
  const navigate = useNavigate();
  const isAdmin = can('admin');
  const [name, setName] = useState(project.name);
  const [key, setKey] = useState(project.key);
  const [description, setDescription] = useState(project.description);
  const [category, setCategory] = useState(project.category);
  const [lead, setLead] = useState<number | null>(project.lead?.id ?? null);
  const [defaultAssignee, setDefaultAssignee] = useState(project.defaultAssignee);
  const [permissionScheme, setPermissionScheme] = useState(String(project.permissionSchemeId));
  const [notificationScheme, setNotificationScheme] = useState(
    project.notificationSchemeId ? String(project.notificationSchemeId) : null,
  );
  const [securityScheme, setSecurityScheme] = useState(
    project.securitySchemeId ? String(project.securitySchemeId) : null,
  );
  const schemes = useQuery({
    queryKey: ['issue-config', 'schemes'],
    queryFn: async () => ({
      permission: await unwrap(api.GET('/api/v1/issue-config/permission-schemes')),
      notification: await unwrap(api.GET('/api/v1/issue-config/notification-schemes')),
      security: await unwrap(api.GET('/api/v1/issue-config/security-schemes')),
    }),
    enabled: isAdmin,
  });
  const save = useMutation({
    mutationFn: (patch?: Schemas['ProjectUpdate']) =>
      unwrap(
        api.PATCH('/api/v1/projects/{key}', {
          params: { path: { key: project.key } },
          body: patch ?? {
            name,
            key,
            description,
            category,
            leadId: lead ?? 0,
            defaultAssignee,
            ...(isAdmin
              ? {
                  permissionSchemeId: Number(permissionScheme),
                  notificationSchemeId: notificationScheme ? Number(notificationScheme) : 0,
                  securitySchemeId: securityScheme ? Number(securityScheme) : 0,
                }
              : {}),
          },
        }),
      ),
    onSuccess: (p) => {
      void client.invalidateQueries({ queryKey: ['project'] });
      void client.invalidateQueries({ queryKey: ['projects'] });
      notifySuccess(t('common.saved'));
      if (p.key !== project.key) navigate(`/projects/${p.key}/settings`, { replace: true });
    },
  });
  const del = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE('/api/v1/projects/{key}', { params: { path: { key: project.key } } })),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['projects'] });
      navigate('/issues');
    },
    onError: notifyError,
  });
  return (
    <Stack maw={720}>
      <TextInput
        label={t('common.name')}
        value={name}
        onChange={(e) => setName(e.currentTarget.value)}
        required
      />
      <TextInput
        label={t('issues.key')}
        description={t('issues.keyChangeHelp')}
        value={key}
        onChange={(e) => setKey(e.currentTarget.value.toUpperCase())}
        required
      />
      <Textarea
        label={t('common.description')}
        value={description}
        onChange={(e) => setDescription(e.currentTarget.value)}
        autosize
        minRows={2}
      />
      <TextInput
        label={t('issues.category')}
        value={category}
        onChange={(e) => setCategory(e.currentTarget.value)}
      />
      <UserPicker label={t('issues.lead')} value={lead} onChange={setLead} />
      <Select
        label={t('issues.defaultAssignee')}
        data={[
          { value: 'unassigned', label: t('issues.unassigned') },
          { value: 'lead', label: t('issues.assigneeProjectLead') },
        ]}
        value={defaultAssignee}
        onChange={(v) => setDefaultAssignee((v ?? 'unassigned') as typeof defaultAssignee)}
        allowDeselect={false}
      />
      {isAdmin && (
        <Card withBorder>
          <Title order={5} mb="sm">
            {t('issues.schemes')}
          </Title>
          <Stack>
            <Select
              label={t('issues.permissionScheme')}
              data={(schemes.data?.permission ?? []).map((s) => ({ value: String(s.id), label: s.name }))}
              value={permissionScheme}
              onChange={(v) => v && setPermissionScheme(v)}
              allowDeselect={false}
            />
            <Select
              label={t('issues.notificationScheme')}
              data={(schemes.data?.notification ?? []).map((s) => ({ value: String(s.id), label: s.name }))}
              value={notificationScheme}
              onChange={setNotificationScheme}
              clearable
            />
            <Select
              label={t('issues.securityScheme')}
              data={(schemes.data?.security ?? []).map((s) => ({ value: String(s.id), label: s.name }))}
              value={securityScheme}
              onChange={setSecurityScheme}
              clearable
            />
          </Stack>
        </Card>
      )}
      <ErrorAlert error={save.error} />
      <Group>
        <Button onClick={() => save.mutate(undefined)} loading={save.isPending}>
          {t('common.save')}
        </Button>
      </Group>
      <Card withBorder mt="xl" style={{ borderColor: 'var(--mantine-color-red-4)' }}>
        <Title order={5} mb="xs">
          {t('issues.dangerZone')}
        </Title>
        <Stack gap="sm">
          <Switch
            label={t('issues.archiveProject')}
            description={t('issues.archiveProjectHelp')}
            checked={project.archived}
            onChange={(e) => save.mutate({ archived: e.currentTarget.checked })}
          />
          {isAdmin && (
            <Group>
              <Button
                color="red"
                variant="light"
                onClick={() =>
                  modals.openConfirmModal({
                    title: t('issues.deleteProject'),
                    children: (
                      <Text size="sm">
                        {t('issues.deleteProjectText', { key: project.key, count: project.issues })}
                      </Text>
                    ),
                    labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                    confirmProps: { color: 'red' },
                    onConfirm: () => del.mutate(),
                  })
                }
              >
                {t('issues.deleteProject')}
              </Button>
            </Group>
          )}
        </Stack>
      </Card>
    </Stack>
  );
}

function Members({ project }: { project: ProjectView }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const roles = useQuery({
    queryKey: ['project', project.key, 'roles'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/projects/{key}/roles', { params: { path: { key: project.key } } })),
  });
  const [kind, setKind] = useState<'user' | 'group'>('user');
  const [member, setMember] = useState<number | null>(null);
  const [role, setRole] = useState<string | null>(null);
  const refresh = () => client.invalidateQueries({ queryKey: ['project', project.key] });
  const add = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/projects/{key}/roles/{roleId}/members', {
          params: { path: { key: project.key, roleId: Number(role) } },
          body: { type: kind, id: member! },
        }),
      ),
    onSuccess: () => {
      setMember(null);
      void refresh();
    },
  });
  const remove = useMutation({
    mutationFn: (v: { roleId: number; type: 'user' | 'group'; id: number }) =>
      unwrap(
        api.DELETE('/api/v1/projects/{key}/roles/{roleId}/members', {
          params: { path: { key: project.key, roleId: v.roleId }, query: { type: v.type, id: v.id } },
        }),
      ),
    onSuccess: refresh,
    onError: notifyError,
  });
  return (
    <Stack>
      <Card withBorder>
        <Group align="flex-end">
          <SegmentedControl
            value={kind}
            onChange={(v) => {
              setKind(v as 'user' | 'group');
              setMember(null);
            }}
            data={[
              { value: 'user', label: t('issues.user') },
              { value: 'group', label: t('issues.group') },
            ]}
          />
          {kind === 'user' ? (
            <UserPicker label={t('issues.user')} value={member} onChange={setMember} w={260} />
          ) : (
            <GroupPicker label={t('issues.group')} value={member} onChange={setMember} w={260} />
          )}
          <Select
            label={t('issues.role')}
            data={(roles.data ?? []).map((r) => ({ value: String(r.role.id), label: r.role.name }))}
            value={role}
            onChange={setRole}
            w={220}
          />
          <Button onClick={() => add.mutate()} disabled={!member || !role} loading={add.isPending}>
            {t('issues.addMember')}
          </Button>
        </Group>
        <ErrorAlert error={add.error} />
      </Card>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th w={220}>{t('issues.role')}</Table.Th>
            <Table.Th>{t('issues.members')}</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(roles.data ?? []).map((r) => (
            <Table.Tr key={r.role.id}>
              <Table.Td>
                <Text fw={600} size="sm">
                  {r.role.name}
                </Text>
                <Text size="xs" c="dimmed">
                  {r.role.description}
                </Text>
              </Table.Td>
              <Table.Td>
                <Group gap={6}>
                  {r.members.length === 0 && (
                    <Text size="sm" c="dimmed">
                      —
                    </Text>
                  )}
                  {r.members.map((m) => (
                    <Badge
                      key={`${m.type}-${m.id}`}
                      variant={m.type === 'group' ? 'outline' : 'light'}
                      tt="none"
                      rightSection={
                        <ActionIcon
                          size="xs"
                          variant="transparent"
                          onClick={() => remove.mutate({ roleId: r.role.id, type: m.type, id: m.id })}
                          aria-label={t('common.remove')}
                        >
                          <IconX size={10} />
                        </ActionIcon>
                      }
                    >
                      {m.type === 'group' ? `${t('issues.group')}: ` : ''}
                      {m.name}
                    </Badge>
                  ))}
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

function IssueTypes({ project }: { project: ProjectView }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const config = useIssueConfig();
  const current = useQuery({
    queryKey: ['project', project.key, 'issue-types'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/projects/{key}/issue-types', { params: { path: { key: project.key } } })),
  });
  const workflows = useQuery({
    queryKey: ['issue-config', 'workflows'],
    queryFn: () => unwrap(api.GET('/api/v1/issue-config/workflows')),
  });
  const [draft, setDraft] = useState<Schemas['IssuesProjectType'][] | null>(null);
  const rows = draft ?? current.data ?? [];
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/projects/{key}/issue-types', {
          params: { path: { key: project.key } },
          body: rows.map((r, i) => ({ ...r, position: i })),
        }),
      ),
    onSuccess: () => {
      setDraft(null);
      void client.invalidateQueries({ queryKey: ['project', project.key] });
      notifySuccess(t('common.saved'));
    },
  });
  const wfData = [
    { value: '0', label: t('issues.defaultWorkflow') },
    ...(workflows.data ?? []).map((w) => ({ value: String(w.id), label: w.name })),
  ];
  return (
    <Stack maw={820}>
      <Text size="sm" c="dimmed">
        {t('issues.issueTypesHelp')}
      </Text>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th w={40} />
            <Table.Th>{t('issues.type')}</Table.Th>
            <Table.Th>{t('issues.workflow')}</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(config.data?.issueTypes ?? []).map((ty) => {
            const row = rows.find((r) => r.issueTypeId === ty.id);
            return (
              <Table.Tr key={ty.id}>
                <Table.Td>
                  <Checkbox
                    checked={!!row}
                    aria-label={ty.name}
                    onChange={(e) =>
                      setDraft(
                        e.currentTarget.checked
                          ? [...rows, { issueTypeId: ty.id, workflowId: 0 }]
                          : rows.filter((r) => r.issueTypeId !== ty.id),
                      )
                    }
                  />
                </Table.Td>
                <Table.Td>
                  <Group gap="xs">
                    <TypeIcon type={ty} size={18} />
                    <Text size="sm">{ty.name}</Text>
                    {ty.level === -1 && (
                      <Badge size="xs" variant="light">
                        {t('issues.subtaskType')}
                      </Badge>
                    )}
                    {ty.level === 1 && (
                      <Badge size="xs" variant="light" color="grape">
                        {t('issues.epic')}
                      </Badge>
                    )}
                  </Group>
                </Table.Td>
                <Table.Td>
                  {row && (
                    <Select
                      size="xs"
                      data={wfData}
                      value={String(row.workflowId ?? 0)}
                      onChange={(v) =>
                        setDraft(
                          rows.map((r) =>
                            r.issueTypeId === ty.id ? { ...r, workflowId: Number(v ?? 0) } : r,
                          ),
                        )
                      }
                      allowDeselect={false}
                      aria-label={t('issues.workflow')}
                    />
                  )}
                </Table.Td>
              </Table.Tr>
            );
          })}
        </Table.Tbody>
      </Table>
      {draft && (
        <Alert color="blue" variant="light">
          {t('issues.issueTypesMigrationHint')}
        </Alert>
      )}
      <ErrorAlert error={save.error} />
      <Group>
        <Button onClick={() => save.mutate()} disabled={!draft} loading={save.isPending}>
          {t('common.save')}
        </Button>
        {draft && (
          <Button variant="subtle" onClick={() => setDraft(null)}>
            {t('common.cancel')}
          </Button>
        )}
      </Group>
    </Stack>
  );
}

export function ProjectSettings() {
  const { t } = useTranslation();
  const project = useProjectContext();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'details';
  return (
    <>
      <PageHeader title={t('issues.settings')} />
      <Tabs
        value={tab}
        onChange={(v) => setParams(v && v !== 'details' ? { tab: v } : {}, { replace: true })}
        keepMounted={false}
      >
        <Tabs.List mb="lg">
          <Tabs.Tab value="details">{t('issues.projectDetails')}</Tabs.Tab>
          <Tabs.Tab value="members">{t('issues.members')}</Tabs.Tab>
          <Tabs.Tab value="types">{t('issues.issueTypesWorkflows')}</Tabs.Tab>
          <Tabs.Tab value="automation">{t('automation.title')}</Tabs.Tab>
          <Tabs.Tab value="webhooks">{t('webhooks.title')}</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="details">
          <Details key={project.version} project={project} />
          <ProjectSpace project={project} />
        </Tabs.Panel>
        <Tabs.Panel value="members">
          <Members project={project} />
        </Tabs.Panel>
        <Tabs.Panel value="types">
          <IssueTypes project={project} />
        </Tabs.Panel>
        <Tabs.Panel value="automation">
          <Automation project={project} />
        </Tabs.Panel>
        <Tabs.Panel value="webhooks">
          <WebhooksEditor scope="project" scopeKey={project.key} />
        </Tabs.Panel>
      </Tabs>
    </>
  );
}
