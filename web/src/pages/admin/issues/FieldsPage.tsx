// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Badge,
  Button,
  Code,
  Group,
  Modal,
  MultiSelect,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconPencil, IconPlus, IconTrash, IconX } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../../api/client';
import { ErrorAlert } from '../../../components/ErrorAlert';
import { PageHeader } from '../../../components/PageHeader';
import { notifyError } from '../../../components/notify';
import { useIssueConfig } from '../../issues/shared';

type FieldInfo = Schemas['FieldInfo'];
type Option = Schemas['Option'];
type FieldType = Schemas['CustomFieldInput']['type'];

const fieldTypes: FieldType[] = [
  'text',
  'textarea',
  'number',
  'date',
  'datetime',
  'select',
  'multiselect',
  'checkboxes',
  'radio',
  'cascading',
  'user',
  'multiuser',
  'group',
  'labels',
  'url',
  'version',
  'multiversion',
];

const withOptions = new Set(['select', 'multiselect', 'checkboxes', 'radio', 'cascading']);

export const fieldId = (key: string) => Number(key.replace(/^cf_/, ''));

let tempId = 0;
const nextTempId = () => --tempId;

function OptionsEditor({
  type,
  options,
  onChange,
}: {
  type: string;
  options: Option[];
  onChange: (o: Option[]) => void;
}) {
  const { t } = useTranslation();
  const cascading = type === 'cascading';
  const update = (id: number, patch: Partial<Option>) =>
    onChange(options.map((o) => (o.id === id ? { ...o, ...patch } : o)));
  const remove = (id: number) => onChange(options.filter((o) => o.id !== id && o.parentId !== id));
  const row = (o: Option, child: boolean) => (
    <Group key={o.id} gap="xs" wrap="nowrap" pl={child ? 32 : 0}>
      <TextInput
        size="xs"
        style={{ flex: 1 }}
        value={o.value}
        onChange={(e) => update(o.id, { value: e.currentTarget.value })}
        aria-label={t('issueAdmin.option')}
        styles={{ input: { textDecoration: o.disabled ? 'line-through' : undefined } }}
      />
      {o.id > 0 ? (
        <Switch
          size="xs"
          label={t('issueAdmin.optionEnabled')}
          checked={!o.disabled}
          onChange={(e) => update(o.id, { disabled: !e.currentTarget.checked })}
        />
      ) : (
        <ActionIcon size="sm" variant="subtle" onClick={() => remove(o.id)} aria-label={t('common.remove')}>
          <IconX size={14} />
        </ActionIcon>
      )}
      {cascading && !child && (
        <Button
          size="compact-xs"
          variant="subtle"
          onClick={() => onChange([...options, { id: nextTempId(), value: '', parentId: o.id }])}
        >
          {t('issueAdmin.addChildOption')}
        </Button>
      )}
    </Group>
  );
  const parents = options.filter((o) => !o.parentId);
  return (
    <Stack gap={6}>
      <Text size="sm" fw={500}>
        {t('issueAdmin.options')}
      </Text>
      {parents.map((p) => (
        <Stack key={p.id} gap={6}>
          {row(p, false)}
          {cascading && options.filter((o) => o.parentId === p.id).map((c) => row(c, true))}
        </Stack>
      ))}
      <Group>
        <Button
          size="xs"
          variant="light"
          leftSection={<IconPlus size={14} />}
          onClick={() => onChange([...options, { id: nextTempId(), value: '' }])}
        >
          {t('issueAdmin.addOption')}
        </Button>
      </Group>
    </Stack>
  );
}

function FieldForm({ field, onClose }: { field: FieldInfo | null; onClose: () => void }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const config = useIssueConfig();
  const projects = useQuery({ queryKey: ['projects'], queryFn: () => unwrap(api.GET('/api/v1/projects')) });
  const [name, setName] = useState(field?.name ?? '');
  const [description, setDescription] = useState(field?.description ?? '');
  const [type, setType] = useState<FieldType>((field?.type as FieldType) ?? 'text');
  const [options, setOptions] = useState<Option[]>(field?.options ?? []);
  const [projectIds, setProjectIds] = useState<string[]>((field?.projectIds ?? []).map(String));
  const [typeIds, setTypeIds] = useState<string[]>((field?.issueTypeIds ?? []).map(String));
  const save = useMutation({
    mutationFn: () => {
      const body: Schemas['CustomFieldInput'] = {
        name,
        description,
        type,
        options: withOptions.has(type) ? options : undefined,
        projectIds: projectIds.map(Number),
        issueTypeIds: typeIds.map(Number),
      };
      return field
        ? unwrap(
            api.PUT('/api/v1/issue-config/fields/{id}', {
              params: { path: { id: fieldId(field.key) } },
              body,
            }),
          )
        : unwrap(api.POST('/api/v1/issue-config/fields', { body }));
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['issue-config'] });
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
      <Select
        label={t('issueAdmin.fieldType')}
        data={fieldTypes.map((ft) => ({ value: ft, label: t(`issueAdmin.fieldTypes.${ft}`) }))}
        value={type}
        onChange={(v) => v && setType(v as FieldType)}
        disabled={!!field}
        description={field ? t('issueAdmin.fieldTypeFixed') : undefined}
        allowDeselect={false}
      />
      {withOptions.has(type) && <OptionsEditor type={type} options={options} onChange={setOptions} />}
      <MultiSelect
        label={t('issueAdmin.fieldProjects')}
        placeholder={projectIds.length ? undefined : t('issueAdmin.allProjects')}
        data={(projects.data ?? []).map((p) => ({ value: String(p.id), label: `${p.name} (${p.key})` }))}
        value={projectIds}
        onChange={setProjectIds}
        searchable
      />
      <MultiSelect
        label={t('issueAdmin.fieldIssueTypes')}
        placeholder={typeIds.length ? undefined : t('issueAdmin.allIssueTypes')}
        data={(config.data?.issueTypes ?? []).map((ty) => ({ value: String(ty.id), label: ty.name }))}
        value={typeIds}
        onChange={setTypeIds}
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

export function FieldsPage() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const config = useIssueConfig();
  const [editing, setEditing] = useState<FieldInfo | 'new' | null>(null);
  const del = useMutation({
    mutationFn: (key: string) =>
      unwrap(api.DELETE('/api/v1/issue-config/fields/{id}', { params: { path: { id: fieldId(key) } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['issue-config'] }),
    onError: notifyError,
  });
  const fields = (config.data?.fields ?? []).filter((f) => f.custom);
  return (
    <>
      <PageHeader
        title={t('issueAdmin.fields')}
        subtitle={t('issueAdmin.fieldsHelp')}
        actions={
          <Button leftSection={<IconPlus size={16} />} onClick={() => setEditing('new')}>
            {t('common.create')}
          </Button>
        }
      />
      {fields.length === 0 ? (
        <Text c="dimmed">{t('issueAdmin.noFields')}</Text>
      ) : (
        <Table>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.name')}</Table.Th>
              <Table.Th>{t('issueAdmin.fieldType')}</Table.Th>
              <Table.Th>{t('issueAdmin.kqlName')}</Table.Th>
              <Table.Th>{t('issueAdmin.context')}</Table.Th>
              <Table.Th w={90} />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {fields.map((f) => (
              <Table.Tr key={f.key}>
                <Table.Td>
                  <Text size="sm" fw={600}>
                    {f.name}
                  </Text>
                  {f.description && (
                    <Text size="xs" c="dimmed" lineClamp={1}>
                      {f.description}
                    </Text>
                  )}
                </Table.Td>
                <Table.Td>{t(`issueAdmin.fieldTypes.${f.type}`, { defaultValue: f.type })}</Table.Td>
                <Table.Td>
                  <Code>{`cf[${fieldId(f.key)}]`}</Code>
                </Table.Td>
                <Table.Td>
                  <Group gap={4}>
                    <Badge variant="light" tt="none">
                      {f.projectIds?.length
                        ? t('issueAdmin.nProjects', { count: f.projectIds.length })
                        : t('issueAdmin.allProjects')}
                    </Badge>
                    <Badge variant="light" color="gray" tt="none">
                      {f.issueTypeIds?.length
                        ? t('issueAdmin.nIssueTypes', { count: f.issueTypeIds.length })
                        : t('issueAdmin.allIssueTypes')}
                    </Badge>
                  </Group>
                </Table.Td>
                <Table.Td>
                  <Group gap={2} wrap="nowrap" justify="flex-end">
                    <ActionIcon variant="subtle" onClick={() => setEditing(f)} aria-label={t('common.edit')}>
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
                            <Text size="sm">{t('issueAdmin.deleteFieldConfirm', { name: f.name })}</Text>
                          ),
                          labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                          confirmProps: { color: 'red' },
                          onConfirm: () => del.mutate(f.key),
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
      )}
      <Modal
        opened={editing !== null}
        onClose={() => setEditing(null)}
        title={editing === 'new' ? t('issueAdmin.newField') : editing?.name}
        size="lg"
      >
        {editing !== null && (
          <FieldForm field={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />
        )}
      </Modal>
    </>
  );
}
