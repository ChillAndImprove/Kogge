// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Badge,
  Button,
  Drawer,
  Group,
  Modal,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { modals } from '@mantine/modals';
import { IconPencil, IconSearch, IconTrash, IconUsers, IconX } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError } from '../../components/notify';
import { PageHeader } from '../../components/PageHeader';
import { Pagination } from '../../components/Pagination';
import { UserPicker } from '../../components/Pickers';

type GroupT = Schemas['Group'];

/** Groups and members per page: groups synchronised from a directory can be thousands, and so can members. */
const pageSize = 25;

function Members({ group }: { group: GroupT }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [user, setUser] = useState<number | null>(null);
  const [page, setPage] = useState(1);
  const members = useQuery({
    queryKey: ['admin', 'groups', group.id, 'members', page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/admin/groups/{id}/members', {
          params: { path: { id: group.id }, query: { limit: pageSize, offset: (page - 1) * pageSize } },
        }),
      ),
    placeholderData: (p) => p,
  });
  const pages = Math.ceil((members.data?.total ?? 0) / pageSize);
  const refresh = () => client.invalidateQueries({ queryKey: ['admin', 'groups'] });
  const add = useMutation({
    mutationFn: (userId: number) =>
      unwrap(
        api.PUT('/api/v1/admin/groups/{id}/members/{userId}', { params: { path: { id: group.id, userId } } }),
      ),
    onSuccess: () => {
      setUser(null);
      void refresh();
    },
    onError: notifyError,
  });
  const remove = useMutation({
    mutationFn: (userId: number) =>
      unwrap(
        api.DELETE('/api/v1/admin/groups/{id}/members/{userId}', {
          params: { path: { id: group.id, userId } },
        }),
      ),
    onSuccess: refresh,
    onError: notifyError,
  });
  const local = group.source === 'local';
  return (
    <Stack>
      {local && (
        <Group align="flex-end">
          <UserPicker label={t('admin.addMember')} value={user} onChange={setUser} style={{ flex: 1 }} />
          <Button
            disabled={user === null}
            onClick={() => user !== null && add.mutate(user)}
            loading={add.isPending}
          >
            {t('common.add')}
          </Button>
        </Group>
      )}
      <Table>
        <Table.Tbody>
          {(members.data?.items ?? []).map((m) => (
            <Table.Tr key={m.id}>
              <Table.Td>
                <Text size="sm">{m.displayName}</Text>
                <Text size="xs" c="dimmed">
                  {m.username}
                </Text>
              </Table.Td>
              <Table.Td w={40}>
                {local && (
                  <ActionIcon
                    variant="subtle"
                    color="red"
                    onClick={() => remove.mutate(m.id)}
                    aria-label={t('common.remove')}
                  >
                    <IconX size={16} />
                  </ActionIcon>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      {pages > 1 && <Pagination size="sm" total={pages} value={Math.min(page, pages)} onChange={setPage} />}
    </Stack>
  );
}

export function GroupsPage() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [query, setQuery] = useState('');
  const [debounced] = useDebouncedValue(query, 250);
  const [page, setPage] = useState(1);
  const groups = useQuery({
    queryKey: ['admin', 'groups', 'list', debounced, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/admin/groups', {
          params: { query: { query: debounced, limit: pageSize, offset: (page - 1) * pageSize } },
        }),
      ),
    placeholderData: (p) => p,
  });
  const pages = Math.ceil((groups.data?.total ?? 0) / pageSize);
  const [editing, setEditing] = useState<GroupT | 'new' | null>(null);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [membersOf, setMembersOf] = useState<GroupT | null>(null);
  const refresh = () => client.invalidateQueries({ queryKey: ['admin', 'groups'] });
  const save = useMutation({
    mutationFn: () =>
      editing === 'new'
        ? unwrap(api.POST('/api/v1/admin/groups', { body: { name, description } }))
        : unwrap(
            api.PATCH('/api/v1/admin/groups/{id}', {
              params: { path: { id: (editing as GroupT).id } },
              body: { name, description },
            }),
          ),
    onSuccess: () => {
      setEditing(null);
      void refresh();
    },
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/api/v1/admin/groups/{id}', { params: { path: { id } } })),
    onSuccess: refresh,
    onError: notifyError,
  });
  const open = (g: GroupT | 'new') => {
    setName(g === 'new' ? '' : g.name);
    setDescription(g === 'new' ? '' : g.description);
    save.reset();
    setEditing(g);
  };
  return (
    <>
      <PageHeader
        title={t('admin.groups')}
        actions={<Button onClick={() => open('new')}>{t('admin.newGroup')}</Button>}
      />
      <TextInput
        leftSection={<IconSearch size={16} />}
        placeholder={t('common.search')}
        aria-label={t('common.search')}
        value={query}
        onChange={(e) => {
          setQuery(e.currentTarget.value);
          setPage(1);
        }}
        w={320}
        mb="md"
      />
      <ErrorAlert error={groups.error} />
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('common.name')}</Table.Th>
            <Table.Th>{t('admin.source')}</Table.Th>
            <Table.Th>{t('admin.members')}</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(groups.data?.items ?? []).map((g) => (
            <Table.Tr key={g.id}>
              <Table.Td>
                <Text fw={550}>{g.name}</Text>
                {g.description && (
                  <Text size="xs" c="dimmed">
                    {g.description}
                  </Text>
                )}
              </Table.Td>
              <Table.Td>
                <Badge variant="light" color={g.source === 'local' ? 'petrol' : 'amber'}>
                  {g.source === 'local' ? t('admin.sourceLocal') : t('admin.sourceOidc')}
                </Badge>
              </Table.Td>
              <Table.Td>{g.members}</Table.Td>
              <Table.Td>
                <Group gap={4} justify="flex-end">
                  <ActionIcon
                    variant="subtle"
                    onClick={() => setMembersOf(g)}
                    aria-label={t('admin.members')}
                  >
                    <IconUsers size={16} />
                  </ActionIcon>
                  <ActionIcon variant="subtle" onClick={() => open(g)} aria-label={t('common.edit')}>
                    <IconPencil size={16} />
                  </ActionIcon>
                  <ActionIcon
                    variant="subtle"
                    color="red"
                    aria-label={t('common.delete')}
                    onClick={() =>
                      modals.openConfirmModal({
                        title: t('common.delete'),
                        children: <Text size="sm">{t('admin.deleteGroupConfirm', { name: g.name })}</Text>,
                        labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                        confirmProps: { color: 'red' },
                        onConfirm: () => del.mutate(g.id),
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
      {pages > 1 && <Pagination mt="md" total={pages} value={Math.min(page, pages)} onChange={setPage} />}
      <Modal
        opened={editing !== null}
        onClose={() => setEditing(null)}
        title={editing === 'new' ? t('admin.newGroup') : t('common.edit')}
      >
        <Stack>
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
          />
          <ErrorAlert error={save.error} />
          <Group justify="flex-end">
            <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name.trim()}>
              {t('common.save')}
            </Button>
          </Group>
        </Stack>
      </Modal>
      <Drawer
        opened={membersOf !== null}
        onClose={() => setMembersOf(null)}
        position="right"
        title={membersOf?.name}
        size="md"
      >
        {membersOf && <Members group={membersOf} />}
      </Drawer>
    </>
  );
}
