// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Badge,
  Button,
  Group,
  Modal,
  SegmentedControl,
  Stack,
  Table,
  Text,
  TextInput,
} from '@mantine/core';
import { IconPlus } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { Pagination } from '../../components/Pagination';
import { useProjectContext } from '../issues/ProjectLayout';
import { UserBadge } from '../issues/shared';

const pageSize = 50;

export function BoardsPage() {
  const { t } = useTranslation();
  const project = useProjectContext();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [page, setPage] = useState(1);
  const boards = useQuery({
    queryKey: ['board', 'list', project.key, 'page', page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/boards', {
          params: { query: { project: project.key, limit: pageSize, offset: (page - 1) * pageSize } },
        }),
      ),
    placeholderData: (prev) => prev,
  });
  const pages = Math.ceil((boards.data?.total ?? 0) / pageSize);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const [type, setType] = useState<'scrum' | 'kanban'>('scrum');
  const admin = project.permissions.includes('project.admin');
  const create = useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/boards', { body: { name, type, project: project.key } })),
    onSuccess: (b) => {
      void client.invalidateQueries({ queryKey: ['board'] });
      navigate(`/projects/${project.key}/boards/${b.id}${b.type === 'scrum' ? '/backlog' : ''}`);
    },
  });
  return (
    <>
      <PageHeader
        title={t('agile.boards')}
        actions={
          admin && (
            <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>
              {t('agile.createBoard')}
            </Button>
          )
        }
      />
      <ErrorAlert error={boards.error} />
      {(boards.data?.items ?? []).length === 0 ? (
        <Text c="dimmed">{t('agile.noBoards')}</Text>
      ) : (
        <Table>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.name')}</Table.Th>
              <Table.Th>{t('agile.boardType')}</Table.Th>
              <Table.Th>{t('agile.owner')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(boards.data?.items ?? []).map((b) => (
              <Table.Tr key={b.id}>
                <Table.Td>
                  <Anchor component={Link} to={`/projects/${project.key}/boards/${b.id}`} fw={600}>
                    {b.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>
                  <Badge variant="light">{b.type === 'scrum' ? 'Scrum' : 'Kanban'}</Badge>
                </Table.Td>
                <Table.Td>{b.owner ? <UserBadge user={b.owner} size="xs" /> : '—'}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      {pages > 1 && (
        <Group justify="center" mt="md">
          <Pagination total={pages} value={Math.min(page, pages)} onChange={setPage} />
        </Group>
      )}
      <Modal opened={creating} onClose={() => setCreating(false)} title={t('agile.createBoard')}>
        <Stack>
          <TextInput
            label={t('common.name')}
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
            required
            data-autofocus
          />
          <SegmentedControl
            value={type}
            onChange={(v) => setType(v as 'scrum' | 'kanban')}
            data={[
              { value: 'scrum', label: 'Scrum' },
              { value: 'kanban', label: 'Kanban' },
            ]}
          />
          <Text size="xs" c="dimmed">
            {type === 'scrum' ? t('agile.scrumHelp') : t('agile.kanbanHelp')}
          </Text>
          <ErrorAlert error={create.error} />
          <Group justify="flex-end">
            <Button onClick={() => create.mutate()} loading={create.isPending} disabled={!name.trim()}>
              {t('common.create')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}
