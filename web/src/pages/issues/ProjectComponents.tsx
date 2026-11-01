// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Button,
  Group,
  Modal,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconPencil, IconPlus, IconTrash } from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { UserPicker } from '../../components/Pickers';
import { notifyError } from '../../components/notify';
import { useProjectContext } from './ProjectLayout';
import { kqlString } from './ProjectReleases';
import { UserBadge } from './shared';

type Component = Schemas['Component'];
type DefaultAssignee = Component['defaultAssignee'];

function ComponentForm({
  projectKey,
  component,
  onClose,
}: {
  projectKey: string;
  component: Component | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [name, setName] = useState(component?.name ?? '');
  const [description, setDescription] = useState(component?.description ?? '');
  const [lead, setLead] = useState<number | null>(component?.lead?.id ?? null);
  const [assignee, setAssignee] = useState<DefaultAssignee>(component?.defaultAssignee ?? 'none');
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        description,
        leadId: lead ?? 0,
        defaultAssignee: assignee,
        archived: component?.archived,
      };
      return component
        ? unwrap(
            api.PUT('/api/v1/projects/{key}/components/{id}', {
              params: { path: { key: projectKey, id: component.id } },
              body,
            }),
          )
        : unwrap(
            api.POST('/api/v1/projects/{key}/components', { params: { path: { key: projectKey } }, body }),
          );
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['project', projectKey] });
      onClose();
    },
  });
  return (
    <Stack>
      <TextInput
        label={t('common.name')}
        value={name}
        onChange={(e) => setName(e.currentTarget.value)}
        required
        data-autofocus
      />
      <Textarea
        label={t('common.description')}
        value={description}
        onChange={(e) => setDescription(e.currentTarget.value)}
        autosize
        minRows={2}
      />
      <UserPicker label={t('issues.lead')} value={lead} onChange={setLead} />
      <Select
        label={t('issues.defaultAssignee')}
        data={[
          { value: 'none', label: t('issues.assigneeNone') },
          { value: 'component-lead', label: t('issues.assigneeComponentLead') },
          { value: 'project-lead', label: t('issues.assigneeProjectLead') },
        ]}
        value={assignee}
        onChange={(v) => setAssignee((v ?? 'none') as DefaultAssignee)}
        allowDeselect={false}
      />
      <ErrorAlert error={save.error} />
      <Group justify="flex-end">
        <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name.trim()}>
          {t('common.save')}
        </Button>
      </Group>
    </Stack>
  );
}

export function ProjectComponents() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const project = useProjectContext();
  const admin = project.permissions.includes('project.admin');
  const [editing, setEditing] = useState<Component | 'new' | null>(null);
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(
        api.DELETE('/api/v1/projects/{key}/components/{id}', { params: { path: { key: project.key, id } } }),
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: ['project', project.key] }),
    onError: notifyError,
  });
  return (
    <>
      <PageHeader
        title={t('issues.components')}
        actions={
          admin && (
            <Button leftSection={<IconPlus size={16} />} onClick={() => setEditing('new')}>
              {t('issues.newComponent')}
            </Button>
          )
        }
      />
      {project.components.length === 0 ? (
        <Text c="dimmed">{t('issues.noComponents')}</Text>
      ) : (
        <Table>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.name')}</Table.Th>
              <Table.Th>{t('issues.lead')}</Table.Th>
              <Table.Th>{t('issues.defaultAssignee')}</Table.Th>
              <Table.Th>{t('issues.issuesNav')}</Table.Th>
              <Table.Th>{t('common.description')}</Table.Th>
              {admin && <Table.Th w={80} />}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {project.components.map((c) => (
              <Table.Tr key={c.id}>
                <Table.Td>
                  <Anchor
                    component={Link}
                    to={`/issues/search?q=${encodeURIComponent(`project = ${project.key} AND component = ${kqlString(c.name)}`)}`}
                    fw={600}
                  >
                    {c.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>{c.lead ? <UserBadge user={c.lead} size="xs" /> : '—'}</Table.Td>
                <Table.Td>
                  {c.defaultAssignee === 'component-lead'
                    ? t('issues.assigneeComponentLead')
                    : c.defaultAssignee === 'project-lead'
                      ? t('issues.assigneeProjectLead')
                      : t('issues.assigneeNone')}
                </Table.Td>
                <Table.Td>{c.issues}</Table.Td>
                <Table.Td>
                  <Text size="sm" lineClamp={2}>
                    {c.description}
                  </Text>
                </Table.Td>
                {admin && (
                  <Table.Td>
                    <Group gap={4} wrap="nowrap">
                      <ActionIcon
                        variant="subtle"
                        onClick={() => setEditing(c)}
                        aria-label={t('common.edit')}
                      >
                        <IconPencil size={16} />
                      </ActionIcon>
                      <ActionIcon
                        variant="subtle"
                        color="red"
                        aria-label={t('common.delete')}
                        onClick={() =>
                          modals.openConfirmModal({
                            title: t('common.delete'),
                            children: (
                              <Text size="sm">{t('issues.deleteComponentText', { name: c.name })}</Text>
                            ),
                            labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                            confirmProps: { color: 'red' },
                            onConfirm: () => del.mutate(c.id),
                          })
                        }
                      >
                        <IconTrash size={16} />
                      </ActionIcon>
                    </Group>
                  </Table.Td>
                )}
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      <Modal
        opened={editing !== null}
        onClose={() => setEditing(null)}
        title={editing === 'new' ? t('issues.newComponent') : t('common.edit')}
      >
        {editing !== null && (
          <ComponentForm
            projectKey={project.key}
            component={editing === 'new' ? null : editing}
            onClose={() => setEditing(null)}
          />
        )}
      </Modal>
    </>
  );
}
