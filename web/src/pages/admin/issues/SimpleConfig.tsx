// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Badge,
  Button,
  ColorSwatch,
  Group,
  Modal,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconArrowDown, IconArrowUp, IconPencil, IconPlus, IconTrash } from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { request } from '../../../api/client';
import { ErrorAlert } from '../../../components/ErrorAlert';
import { PageHeader } from '../../../components/PageHeader';
import { notifyError } from '../../../components/notify';
import { PriorityIcon, StatusBadge, TypeIcon, useIssueConfig, type IssueConfig } from '../../issues/shared';

export const themeColors = [
  'blue',
  'cyan',
  'teal',
  'green',
  'lime',
  'yellow',
  'orange',
  'red',
  'pink',
  'grape',
  'violet',
  'indigo',
  'petrol',
  'gray',
];

type Kind = 'issue-types' | 'statuses' | 'priorities' | 'resolutions' | 'link-types' | 'roles';

interface FieldDef {
  key: string;
  label: string;
  type: 'text' | 'textarea' | 'select' | 'switch' | 'color';
  options?: { value: string; label: string }[];
  required?: boolean;
  /** Only editable when creating. */
  createOnly?: boolean;
}

interface Item {
  id: number;
  name: string;
  [k: string]: unknown;
}

interface KindDef {
  title: string;
  description: string;
  items: (c: IssueConfig) => Item[];
  columns: { label: string; render: (i: Item) => ReactNode }[];
  fields: FieldDef[];
  defaults: Record<string, unknown>;
  ordered?: boolean;
}

function useKinds(): Record<Kind, KindDef> {
  const { t } = useTranslation();
  const name = { key: 'name', label: t('common.name'), type: 'text', required: true } as const;
  const description = { key: 'description', label: t('common.description'), type: 'textarea' } as const;
  const color = { key: 'color', label: t('issueAdmin.color'), type: 'color' } as const;
  const isDefault = { key: 'isDefault', label: t('issueAdmin.isDefault'), type: 'switch' } as const;
  const descriptionColumn = {
    label: t('common.description'),
    render: (i: Item) => (
      <Text size="sm" c="dimmed" lineClamp={2}>
        {String(i.description ?? '')}
      </Text>
    ),
  };
  const defaultColumn = {
    label: t('issueAdmin.isDefault'),
    render: (i: Item) => (i.isDefault ? <Badge variant="light">{t('issueAdmin.isDefault')}</Badge> : null),
  };
  return {
    'issue-types': {
      title: t('issueAdmin.issueTypes'),
      description: t('issueAdmin.issueTypesHelp'),
      items: (c) => c.issueTypes,
      ordered: true,
      columns: [
        {
          label: t('common.name'),
          render: (i) => (
            <Group gap="xs">
              <TypeIcon type={i as never} />
              <Text size="sm" fw={600}>
                {i.name}
              </Text>
            </Group>
          ),
        },
        {
          label: t('issueAdmin.level'),
          render: (i) => t(`issueAdmin.levels.${String(i.level)}`),
        },
        descriptionColumn,
      ],
      fields: [
        name,
        description,
        {
          key: 'icon',
          label: t('issueAdmin.icon'),
          type: 'select',
          options: ['epic', 'story', 'task', 'bug', 'subtask'].map((v) => ({
            value: v,
            label: t(`issueAdmin.icons.${v}`),
          })),
        },
        color,
        {
          key: 'level',
          label: t('issueAdmin.level'),
          type: 'select',
          options: ['1', '0', '-1'].map((v) => ({ value: v, label: t(`issueAdmin.levels.${v}`) })),
        },
      ],
      defaults: { name: '', description: '', icon: 'task', color: 'blue', level: 0 },
    },
    statuses: {
      title: t('issueAdmin.statuses'),
      description: t('issueAdmin.statusesHelp'),
      items: (c) => c.statuses,
      ordered: true,
      columns: [
        { label: t('common.name'), render: (i) => <StatusBadge status={i as never} /> },
        { label: t('issueAdmin.category'), render: (i) => t(`issueAdmin.categories.${String(i.category)}`) },
        descriptionColumn,
      ],
      fields: [
        name,
        description,
        {
          key: 'category',
          label: t('issueAdmin.category'),
          type: 'select',
          options: ['todo', 'inprogress', 'done'].map((v) => ({
            value: v,
            label: t(`issueAdmin.categories.${v}`),
          })),
        },
      ],
      defaults: { name: '', description: '', category: 'todo' },
    },
    priorities: {
      title: t('issueAdmin.priorities'),
      description: t('issueAdmin.prioritiesHelp'),
      items: (c) => c.priorities,
      ordered: true,
      columns: [
        { label: t('common.name'), render: (i) => <PriorityIcon priority={i as never} withName /> },
        defaultColumn,
        descriptionColumn,
      ],
      fields: [
        name,
        description,
        {
          key: 'icon',
          label: t('issueAdmin.icon'),
          type: 'select',
          options: ['highest', 'high', 'medium', 'low', 'lowest'].map((v) => ({
            value: v,
            label: t(`issueAdmin.icons.${v}`),
          })),
        },
        color,
        isDefault,
      ],
      defaults: { name: '', description: '', icon: 'medium', color: 'yellow', isDefault: false },
    },
    resolutions: {
      title: t('issueAdmin.resolutions'),
      description: t('issueAdmin.resolutionsHelp'),
      items: (c) => c.resolutions,
      ordered: true,
      columns: [
        {
          label: t('common.name'),
          render: (i) => (
            <Text size="sm" fw={600}>
              {i.name}
            </Text>
          ),
        },
        defaultColumn,
        descriptionColumn,
      ],
      fields: [name, description, isDefault],
      defaults: { name: '', description: '', isDefault: false },
    },
    'link-types': {
      title: t('issueAdmin.linkTypes'),
      description: t('issueAdmin.linkTypesHelp'),
      items: (c) => c.linkTypes,
      columns: [
        {
          label: t('common.name'),
          render: (i) => (
            <Text size="sm" fw={600}>
              {i.name}
            </Text>
          ),
        },
        { label: t('issueAdmin.outward'), render: (i) => String(i.outward) },
        { label: t('issueAdmin.inward'), render: (i) => String(i.inward) },
      ],
      fields: [
        name,
        { key: 'outward', label: t('issueAdmin.outward'), type: 'text', required: true },
        { key: 'inward', label: t('issueAdmin.inward'), type: 'text', required: true },
      ],
      defaults: { name: '', outward: '', inward: '' },
    },
    roles: {
      title: t('issueAdmin.roles'),
      description: t('issueAdmin.rolesHelp'),
      items: (c) => c.roles,
      columns: [
        {
          label: t('common.name'),
          render: (i) => (
            <Text size="sm" fw={600}>
              {i.name}
            </Text>
          ),
        },
        descriptionColumn,
      ],
      fields: [name, description],
      defaults: { name: '', description: '' },
    },
  };
}

function ItemForm({
  kind,
  def,
  item,
  onClose,
}: {
  kind: Kind;
  def: KindDef;
  item: Item | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [values, setValues] = useState<Record<string, unknown>>(() => {
    const v: Record<string, unknown> = { ...def.defaults };
    if (item) for (const k of Object.keys(def.defaults)) v[k] = item[k];
    return v;
  });
  const set = (k: string, v: unknown) => setValues((old) => ({ ...old, [k]: v }));
  const save = useMutation({
    mutationFn: () =>
      item
        ? request('PUT', `/api/v1/issue-config/${kind}/${item.id}`, values)
        : request('POST', `/api/v1/issue-config/${kind}`, values),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['issue-config'] });
      onClose();
    },
  });
  return (
    <Stack>
      {def.fields.map((f) => {
        const v = values[f.key];
        switch (f.type) {
          case 'textarea':
            return (
              <Textarea
                key={f.key}
                label={f.label}
                value={String(v ?? '')}
                onChange={(e) => set(f.key, e.currentTarget.value)}
                autosize
                minRows={2}
              />
            );
          case 'select':
            return (
              <Select
                key={f.key}
                label={f.label}
                data={f.options ?? []}
                value={v == null ? null : String(v)}
                onChange={(x) => set(f.key, f.key === 'level' ? Number(x) : x)}
                allowDeselect={false}
              />
            );
          case 'switch':
            return (
              <Switch
                key={f.key}
                label={f.label}
                checked={!!v}
                onChange={(e) => set(f.key, e.currentTarget.checked)}
              />
            );
          case 'color':
            return (
              <Stack key={f.key} gap={4}>
                <Text size="sm" fw={500}>
                  {f.label}
                </Text>
                <Group gap={6}>
                  {themeColors.map((c) => (
                    <ColorSwatch
                      key={c}
                      component="button"
                      type="button"
                      color={`var(--mantine-color-${c}-6)`}
                      onClick={() => set(f.key, c)}
                      style={{
                        outline: v === c ? '2px solid var(--mantine-color-text)' : undefined,
                        outlineOffset: 2,
                        cursor: 'pointer',
                      }}
                      aria-label={c}
                    />
                  ))}
                </Group>
              </Stack>
            );
          default:
            return (
              <TextInput
                key={f.key}
                label={f.label}
                value={String(v ?? '')}
                onChange={(e) => set(f.key, e.currentTarget.value)}
                required={f.required}
                data-autofocus={f.key === 'name' || undefined}
              />
            );
        }
      })}
      <ErrorAlert error={save.error} />
      <Group justify="flex-end">
        <Button
          onClick={() => save.mutate()}
          loading={save.isPending}
          disabled={!String(values.name ?? '').trim()}
        >
          {t('common.save')}
        </Button>
      </Group>
    </Stack>
  );
}

export function SimpleConfigPage({ kind }: { kind: Kind }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const config = useIssueConfig();
  const def = useKinds()[kind];
  const [editing, setEditing] = useState<Item | 'new' | null>(null);
  const items = config.data ? def.items(config.data) : [];
  const refresh = () => client.invalidateQueries({ queryKey: ['issue-config'] });
  const del = useMutation({
    mutationFn: (id: number) => request('DELETE', `/api/v1/issue-config/${kind}/${id}`),
    onSuccess: refresh,
    onError: notifyError,
  });
  const order = useMutation({
    mutationFn: (ids: number[]) => request('PUT', `/api/v1/issue-config/${kind}-order`, ids),
    onSuccess: refresh,
    onError: notifyError,
  });
  const move = (index: number, delta: number) => {
    const ids = items.map((i) => i.id);
    const [id] = ids.splice(index, 1);
    ids.splice(index + delta, 0, id);
    order.mutate(ids);
  };
  return (
    <>
      <PageHeader
        title={def.title}
        subtitle={def.description}
        actions={
          <Button leftSection={<IconPlus size={16} />} onClick={() => setEditing('new')}>
            {t('common.create')}
          </Button>
        }
      />
      <Table>
        <Table.Thead>
          <Table.Tr>
            {def.columns.map((c) => (
              <Table.Th key={c.label}>{c.label}</Table.Th>
            ))}
            <Table.Th w={def.ordered ? 150 : 90} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {items.map((item, index) => (
            <Table.Tr key={item.id}>
              {def.columns.map((c) => (
                <Table.Td key={c.label}>{c.render(item)}</Table.Td>
              ))}
              <Table.Td>
                <Group gap={2} wrap="nowrap" justify="flex-end">
                  {def.ordered && (
                    <>
                      <ActionIcon
                        variant="subtle"
                        disabled={index === 0}
                        onClick={() => move(index, -1)}
                        aria-label={t('issueAdmin.moveUp')}
                      >
                        <IconArrowUp size={16} />
                      </ActionIcon>
                      <ActionIcon
                        variant="subtle"
                        disabled={index === items.length - 1}
                        onClick={() => move(index, 1)}
                        aria-label={t('issueAdmin.moveDown')}
                      >
                        <IconArrowDown size={16} />
                      </ActionIcon>
                    </>
                  )}
                  <ActionIcon variant="subtle" onClick={() => setEditing(item)} aria-label={t('common.edit')}>
                    <IconPencil size={16} />
                  </ActionIcon>
                  <ActionIcon
                    variant="subtle"
                    color="red"
                    aria-label={t('common.delete')}
                    onClick={() =>
                      modals.openConfirmModal({
                        title: t('common.delete'),
                        children: <Text size="sm">{t('issueAdmin.deleteConfirm', { name: item.name })}</Text>,
                        labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                        confirmProps: { color: 'red' },
                        onConfirm: () => del.mutate(item.id),
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
      <Modal
        opened={editing !== null}
        onClose={() => setEditing(null)}
        title={editing === 'new' ? def.title : editing?.name}
      >
        {editing !== null && (
          <ItemForm
            kind={kind}
            def={def}
            item={editing === 'new' ? null : editing}
            onClose={() => setEditing(null)}
          />
        )}
      </Modal>
    </>
  );
}
