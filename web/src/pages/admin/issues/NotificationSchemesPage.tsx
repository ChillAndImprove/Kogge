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
import { IconPlus, IconTrash } from '@tabler/icons-react';
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

type Scheme = Schemas['NotificationScheme'];
type Entry = Schemas['NotificationEntry'];

const recipientTypes = [
  'reporter',
  'assignee',
  'watchers',
  'project-lead',
  'component-lead',
  'role',
  'group',
  'user',
];

function useNotificationSchemes() {
  return useQuery({
    queryKey: ['issue-config', 'notification-schemes'],
    queryFn: () => unwrap(api.GET('/api/v1/issue-config/notification-schemes')),
  });
}

export function NotificationSchemesPage() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const schemes = useNotificationSchemes();
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/issue-config/notification-schemes/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['issue-config'] }),
    onError: notifyError,
  });
  return (
    <>
      <PageHeader
        title={t('issueAdmin.notificationSchemes')}
        subtitle={t('issueAdmin.notificationSchemesHelp')}
        actions={
          <Button
            leftSection={<IconPlus size={16} />}
            component={Link}
            to="/issues/admin/notification-schemes/new"
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
            <Table.Th>{t('issueAdmin.entries')}</Table.Th>
            <Table.Th w={50} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(schemes.data ?? []).map((s) => (
            <Table.Tr key={s.id}>
              <Table.Td>
                <Anchor component={Link} to={`/issues/admin/notification-schemes/${s.id}`} fw={600} size="sm">
                  {s.name}
                </Anchor>
                <Text size="xs" c="dimmed" lineClamp={1}>
                  {s.description}
                </Text>
              </Table.Td>
              <Table.Td>{s.entries.length}</Table.Td>
              <Table.Td>
                <ActionIcon
                  variant="subtle"
                  color="red"
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
  const [entries, setEntries] = useState<Entry[]>(scheme?.entries ?? []);
  const save = useMutation({
    mutationFn: () => {
      const body = { name, description, entries };
      return scheme
        ? unwrap(
            api.PUT('/api/v1/issue-config/notification-schemes/{id}', {
              params: { path: { id: scheme.id } },
              body,
            }),
          )
        : unwrap(api.POST('/api/v1/issue-config/notification-schemes', { body }));
    },
    onSuccess: (r) => {
      void client.invalidateQueries({ queryKey: ['issue-config'] });
      notifySuccess(t('common.saved'));
      if (!scheme) navigate(`/issues/admin/notification-schemes/${r.id}`, { replace: true });
    },
  });
  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <Stack gap={4}>
          <Anchor component={Link} to="/issues/admin/notification-schemes" size="sm">
            ← {t('issueAdmin.notificationSchemes')}
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
            <Table.Th w="30%">{t('issueAdmin.event')}</Table.Th>
            <Table.Th>{t('issueAdmin.recipients')}</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(config.data?.events ?? []).map((ev) => (
            <Table.Tr key={ev}>
              <Table.Td>
                <Text size="sm" fw={600}>
                  {t(`issueAdmin.events.${ev}`, { defaultValue: ev })}
                </Text>
              </Table.Td>
              <Table.Td>
                <Group gap={6}>
                  {entries
                    .filter((e) => e.event === ev)
                    .map((e) => (
                      <HolderBadge
                        key={`${e.recipientType}-${e.recipientId ?? 0}`}
                        holder={{ type: e.recipientType, id: e.recipientId }}
                        onRemove={() => setEntries((es) => es.filter((x) => x !== e))}
                      />
                    ))}
                  <AddHolder
                    types={recipientTypes}
                    onAdd={(h) =>
                      setEntries((es) =>
                        es.some(
                          (x) =>
                            x.event === ev &&
                            x.recipientType === h.type &&
                            (x.recipientId ?? 0) === (h.id ?? 0),
                        )
                          ? es
                          : [
                              ...es,
                              {
                                event: ev,
                                recipientType: h.type as Entry['recipientType'],
                                recipientId: h.id,
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

export function NotificationSchemeEditorPage() {
  const { id } = useParams();
  const schemes = useNotificationSchemes();
  if (id === 'new') return <SchemeEditor scheme={null} />;
  if (schemes.error) return <ErrorAlert error={schemes.error} />;
  const s = schemes.data?.find((x) => String(x.id) === id);
  if (!s) return null;
  return <SchemeEditor key={s.id} scheme={s} />;
}
