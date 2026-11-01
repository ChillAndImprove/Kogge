// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Button,
  Card,
  Group,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconCopy, IconPlus, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../../api/client';
import { ErrorAlert } from '../../../components/ErrorAlert';
import { PageHeader } from '../../../components/PageHeader';
import { notifyError, notifySuccess } from '../../../components/notify';
import { useIssueConfig } from '../../issues/shared';
import { AddHolder, HolderBadge } from './Holders';

type Grant = Schemas['IssuesGrant'];
type Scheme = Schemas['PermissionScheme'];

const holderTypes = ['role', 'group', 'user', 'reporter', 'assignee', 'project-lead', 'logged-in', 'anyone'];

function usePermissionSchemes() {
  return useQuery({
    queryKey: ['issue-config', 'permission-schemes'],
    queryFn: () => unwrap(api.GET('/api/v1/issue-config/permission-schemes')),
  });
}

export function PermissionSchemesPage() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const schemes = usePermissionSchemes();
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/issue-config/permission-schemes/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['issue-config'] }),
    onError: notifyError,
  });
  const copy = useMutation({
    mutationFn: (s: Scheme) =>
      unwrap(
        api.POST('/api/v1/issue-config/permission-schemes', {
          body: {
            name: t('issueAdmin.copyOf', { name: s.name }),
            description: s.description,
            grants: s.grants,
          },
        }),
      ),
    onSuccess: (r) => {
      void client.invalidateQueries({ queryKey: ['issue-config'] });
      navigate(`/issues/admin/permission-schemes/${r.id}`);
    },
    onError: notifyError,
  });
  return (
    <>
      <PageHeader
        title={t('issueAdmin.permissionSchemes')}
        subtitle={t('issueAdmin.permissionSchemesHelp')}
        actions={
          <Button
            leftSection={<IconPlus size={16} />}
            component={Link}
            to="/issues/admin/permission-schemes/new"
          >
            {t('common.create')}
          </Button>
        }
      />
      <ErrorAlert error={schemes.error} />
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('common.name')}</Table.Th>
            <Table.Th>{t('issueAdmin.grants')}</Table.Th>
            <Table.Th>{t('issueAdmin.usedBy')}</Table.Th>
            <Table.Th w={90} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(schemes.data ?? []).map((s) => (
            <Table.Tr key={s.id}>
              <Table.Td>
                <Anchor component={Link} to={`/issues/admin/permission-schemes/${s.id}`} fw={600} size="sm">
                  {s.name}
                </Anchor>
                <Text size="xs" c="dimmed" lineClamp={1}>
                  {s.description}
                </Text>
              </Table.Td>
              <Table.Td>{s.grants.length}</Table.Td>
              <Table.Td>{t('issueAdmin.nProjects', { count: s.projects })}</Table.Td>
              <Table.Td>
                <Group gap={2} wrap="nowrap" justify="flex-end">
                  <ActionIcon
                    variant="subtle"
                    onClick={() => copy.mutate(s)}
                    aria-label={t('issueAdmin.copy')}
                  >
                    <IconCopy size={16} />
                  </ActionIcon>
                  <ActionIcon
                    variant="subtle"
                    color="red"
                    disabled={s.projects > 0}
                    aria-label={t('common.delete')}
                    onClick={() =>
                      modals.openConfirmModal({
                        title: t('common.delete'),
                        children: <Text size="sm">{t('issueAdmin.deleteConfirm', { name: s.name })}</Text>,
                        labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                        confirmProps: { color: 'red' },
                        onConfirm: () => del.mutate(s.id),
                      })
                    }
                  >
                    <IconTrash size={16} />
                  </ActionIcon>
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </>
  );
}

function SchemeEditor({ scheme }: { scheme: Scheme | null }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const config = useIssueConfig();
  const [name, setName] = useState(scheme?.name ?? '');
  const [description, setDescription] = useState(scheme?.description ?? '');
  const [grants, setGrants] = useState<Grant[]>(scheme?.grants ?? []);
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        description,
        grants: grants.map(({ permission, holderType, holderId }) => ({ permission, holderType, holderId })),
      };
      return scheme
        ? unwrap(
            api.PUT('/api/v1/issue-config/permission-schemes/{id}', {
              params: { path: { id: scheme.id } },
              body,
            }),
          )
        : unwrap(api.POST('/api/v1/issue-config/permission-schemes', { body }));
    },
    onSuccess: (r) => {
      void client.invalidateQueries({ queryKey: ['issue-config'] });
      notifySuccess(t('common.saved'));
      if (!scheme) navigate(`/issues/admin/permission-schemes/${r.id}`, { replace: true });
    },
  });
  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <Stack gap={4}>
          <Anchor component={Link} to="/issues/admin/permission-schemes" size="sm">
            ← {t('issueAdmin.permissionSchemes')}
          </Anchor>
          <Title order={2}>{name || t('issueAdmin.newScheme')}</Title>
        </Stack>
        <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name.trim()}>
          {t('common.save')}
        </Button>
      </Group>
      <ErrorAlert error={save.error} />
      <Card withBorder>
        <Group grow align="flex-start">
          <TextInput
            label={t('common.name')}
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
            required
          />
          <Textarea
            label={t('common.description')}
            value={description}
            onChange={(e) => setDescription(e.currentTarget.value)}
            autosize
          />
        </Group>
      </Card>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th w="40%">{t('issueAdmin.permission')}</Table.Th>
            <Table.Th>{t('issueAdmin.grantedTo')}</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(config.data?.permissions ?? []).map((p) => (
            <Table.Tr key={p}>
              <Table.Td>
                <Text size="sm" fw={600}>
                  {t(`issuePermissions.${p}`, { defaultValue: p })}
                </Text>
                <Text size="xs" c="dimmed">
                  {p}
                </Text>
              </Table.Td>
              <Table.Td>
                <Group gap={6}>
                  {grants
                    .filter((g) => g.permission === p)
                    .map((g) => (
                      <HolderBadge
                        key={`${g.holderType}-${g.holderId ?? 0}`}
                        holder={{ type: g.holderType, id: g.holderId, name: g.holderName }}
                        onRemove={() => setGrants((gs) => gs.filter((x) => x !== g))}
                      />
                    ))}
                  <AddHolder
                    types={holderTypes}
                    onAdd={(h) =>
                      setGrants((gs) =>
                        gs.some(
                          (x) =>
                            x.permission === p &&
                            x.holderType === h.type &&
                            (x.holderId ?? 0) === (h.id ?? 0),
                        )
                          ? gs
                          : [
                              ...gs,
                              {
                                permission: p,
                                holderType: h.type as Grant['holderType'],
                                holderId: h.id,
                                holderName: h.name,
                              },
                            ],
                      )
                    }
                  />
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

export function PermissionSchemeEditorPage() {
  const { id } = useParams();
  const schemes = usePermissionSchemes();
  if (id === 'new') return <SchemeEditor scheme={null} />;
  if (schemes.error) return <ErrorAlert error={schemes.error} />;
  const s = schemes.data?.find((x) => String(x.id) === id);
  if (!s) return null;
  return <SchemeEditor key={s.id} scheme={s} />;
}
