// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Anchor, Badge, Button, Group, Table, Text, Tooltip } from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconCopy, IconPlus, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { api, unwrap } from '../../../api/client';
import { ErrorAlert } from '../../../components/ErrorAlert';
import { PageHeader } from '../../../components/PageHeader';
import { notifyError } from '../../../components/notify';

export function useWorkflows() {
  return useQuery({
    queryKey: ['issue-config', 'workflows'],
    queryFn: () => unwrap(api.GET('/api/v1/issue-config/workflows')),
  });
}

export function WorkflowsPage() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const workflows = useWorkflows();
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/issue-config/workflows/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['issue-config'] }),
    onError: notifyError,
  });
  const copy = useMutation({
    mutationFn: (id: number) => {
      const w = workflows.data!.find((x) => x.id === id)!;
      return unwrap(
        api.POST('/api/v1/issue-config/workflows', {
          body: {
            name: t('issueAdmin.copyOf', { name: w.name }),
            description: w.description,
            definition: w.definition,
          },
        }),
      );
    },
    onSuccess: (r) => {
      void client.invalidateQueries({ queryKey: ['issue-config'] });
      navigate(`/issues/admin/workflows/${r.id}`);
    },
    onError: notifyError,
  });
  return (
    <>
      <PageHeader
        title={t('issueAdmin.workflows')}
        subtitle={t('issueAdmin.workflowsHelp')}
        actions={
          <Button leftSection={<IconPlus size={16} />} component={Link} to="/issues/admin/workflows/new">
            {t('common.create')}
          </Button>
        }
      />
      <ErrorAlert error={workflows.error} />
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('common.name')}</Table.Th>
            <Table.Th>{t('issueAdmin.statuses')}</Table.Th>
            <Table.Th>{t('issueAdmin.transitions')}</Table.Th>
            <Table.Th>{t('issueAdmin.usedBy')}</Table.Th>
            <Table.Th w={90} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(workflows.data ?? []).map((w) => (
            <Table.Tr key={w.id}>
              <Table.Td>
                <Anchor component={Link} to={`/issues/admin/workflows/${w.id}`} fw={600} size="sm">
                  {w.name}
                </Anchor>
                {w.description && (
                  <Text size="xs" c="dimmed" lineClamp={1}>
                    {w.description}
                  </Text>
                )}
              </Table.Td>
              <Table.Td>{w.definition.statuses.length}</Table.Td>
              <Table.Td>{w.definition.transitions.length}</Table.Td>
              <Table.Td>
                <Group gap={4}>
                  {w.projects.length === 0 && (
                    <Text size="sm" c="dimmed">
                      —
                    </Text>
                  )}
                  {w.projects.map((k) => (
                    <Badge key={k} variant="light" tt="none">
                      {k}
                    </Badge>
                  ))}
                </Group>
              </Table.Td>
              <Table.Td>
                <Group gap={2} wrap="nowrap" justify="flex-end">
                  <Tooltip label={t('issueAdmin.copy')}>
                    <ActionIcon
                      variant="subtle"
                      onClick={() => copy.mutate(w.id)}
                      aria-label={t('issueAdmin.copy')}
                    >
                      <IconCopy size={16} />
                    </ActionIcon>
                  </Tooltip>
                  <ActionIcon
                    variant="subtle"
                    color="red"
                    disabled={w.projects.length > 0}
                    aria-label={t('common.delete')}
                    onClick={() =>
                      modals.openConfirmModal({
                        title: t('common.delete'),
                        children: <Text size="sm">{t('issueAdmin.deleteConfirm', { name: w.name })}</Text>,
                        labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                        confirmProps: { color: 'red' },
                        onConfirm: () => del.mutate(w.id),
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
