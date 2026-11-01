// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Card,
  Group,
  MultiSelect,
  NumberInput,
  Select,
  Stack,
  Switch,
  Table,
  Tabs,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconArrowLeft, IconArrowRight, IconPlus, IconTrash } from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router';
import { ApiError, api, unwrap, type Schemas } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError, notifySuccess } from '../../components/notify';
import { themeColors } from '../admin/issues/SimpleConfig';
import { StatusBadge, useIssueConfig } from '../issues/shared';
import { BoardNav } from './BoardNav';
import { useBoard, type Board, type BoardConfig } from './hooks';

type NamedQuery = Schemas['NamedQuery'];

function nextId(list: NamedQuery[]) {
  return Math.max(0, ...list.map((q) => q.id)) + 1;
}

function QueriesEditor({
  value,
  onChange,
  withColor,
  addLabel,
}: {
  value: NamedQuery[];
  onChange: (v: NamedQuery[]) => void;
  withColor?: boolean;
  addLabel: string;
}) {
  const { t } = useTranslation();
  const set = (id: number, patch: Partial<NamedQuery>) =>
    onChange(value.map((q) => (q.id === id ? { ...q, ...patch } : q)));
  return (
    <Stack gap="xs">
      {value.map((q) => (
        <Group key={q.id} align="flex-start" wrap="nowrap">
          <TextInput
            w={200}
            value={q.name}
            onChange={(e) => set(q.id, { name: e.currentTarget.value })}
            aria-label={t('common.name')}
            placeholder={t('common.name')}
          />
          <TextInput
            style={{ flex: 1 }}
            value={q.query}
            onChange={(e) => set(q.id, { query: e.currentTarget.value })}
            aria-label={t('issues.query')}
            placeholder="assignee = currentUser()"
            ff="monospace"
          />
          {withColor && (
            <Select
              w={130}
              data={themeColors}
              value={q.color ?? null}
              onChange={(v) => set(q.id, { color: v ?? undefined })}
              aria-label={t('issueAdmin.color')}
            />
          )}
          <ActionIcon
            variant="subtle"
            color="red"
            mt={6}
            onClick={() => onChange(value.filter((x) => x.id !== q.id))}
            aria-label={t('common.remove')}
          >
            <IconTrash size={16} />
          </ActionIcon>
        </Group>
      ))}
      <Group>
        <Button
          size="xs"
          variant="light"
          leftSection={<IconPlus size={14} />}
          onClick={() => onChange([...value, { id: nextId(value), name: '', query: '' }])}
        >
          {addLabel}
        </Button>
      </Group>
    </Stack>
  );
}

function ColumnsEditor({ config, onChange }: { config: BoardConfig; onChange: (c: BoardConfig) => void }) {
  const { t } = useTranslation();
  const issueConfig = useIssueConfig();
  const statuses = issueConfig.data?.statuses ?? [];
  const mapped = new Set(config.columns.flatMap((c) => c.statuses));
  const unmapped = statuses.filter((s) => !mapped.has(s.id));
  const setColumn = (i: number, patch: Partial<BoardConfig['columns'][number]>) =>
    onChange({ ...config, columns: config.columns.map((c, j) => (j === i ? { ...c, ...patch } : c)) });
  const moveColumn = (i: number, d: number) => {
    const cols = [...config.columns];
    [cols[i], cols[i + d]] = [cols[i + d], cols[i]];
    onChange({ ...config, columns: cols });
  };
  return (
    <Stack>
      <Text size="sm" c="dimmed">
        {t('agile.columnsHelp')}
      </Text>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('common.name')}</Table.Th>
            <Table.Th>{t('agile.statuses')}</Table.Th>
            <Table.Th w={90}>{t('agile.min')}</Table.Th>
            <Table.Th w={90}>{t('agile.max')}</Table.Th>
            <Table.Th w={110} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {config.columns.map((c, i) => (
            <Table.Tr key={i}>
              <Table.Td>
                <TextInput
                  value={c.name}
                  onChange={(e) => setColumn(i, { name: e.currentTarget.value })}
                  aria-label={t('common.name')}
                />
              </Table.Td>
              <Table.Td miw={260}>
                <MultiSelect
                  data={statuses
                    .filter((s) => c.statuses.includes(s.id) || !mapped.has(s.id))
                    .map((s) => ({ value: String(s.id), label: s.name }))}
                  value={c.statuses.map(String)}
                  onChange={(v) => setColumn(i, { statuses: v.map(Number) })}
                  aria-label={t('agile.statuses')}
                />
              </Table.Td>
              <Table.Td>
                <NumberInput
                  min={0}
                  value={c.min ?? ''}
                  onChange={(v) => setColumn(i, { min: v === '' ? undefined : Number(v) })}
                  aria-label={t('agile.min')}
                />
              </Table.Td>
              <Table.Td>
                <NumberInput
                  min={0}
                  value={c.max ?? ''}
                  onChange={(v) => setColumn(i, { max: v === '' ? undefined : Number(v) })}
                  aria-label={t('agile.max')}
                />
              </Table.Td>
              <Table.Td>
                <Group gap={2} wrap="nowrap">
                  <ActionIcon
                    variant="subtle"
                    disabled={i === 0}
                    onClick={() => moveColumn(i, -1)}
                    aria-label={t('agile.moveLeft')}
                  >
                    <IconArrowLeft size={16} />
                  </ActionIcon>
                  <ActionIcon
                    variant="subtle"
                    disabled={i === config.columns.length - 1}
                    onClick={() => moveColumn(i, 1)}
                    aria-label={t('agile.moveRight')}
                  >
                    <IconArrowRight size={16} />
                  </ActionIcon>
                  <ActionIcon
                    variant="subtle"
                    color="red"
                    disabled={config.columns.length === 1}
                    onClick={() => onChange({ ...config, columns: config.columns.filter((_, j) => j !== i) })}
                    aria-label={t('common.remove')}
                  >
                    <IconTrash size={16} />
                  </ActionIcon>
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <Group>
        <Button
          size="xs"
          variant="light"
          leftSection={<IconPlus size={14} />}
          onClick={() =>
            onChange({
              ...config,
              columns: [...config.columns, { name: t('agile.newColumn'), statuses: [] }],
            })
          }
        >
          {t('agile.addColumn')}
        </Button>
      </Group>
      {unmapped.length > 0 && (
        <Card withBorder>
          <Text size="sm" fw={600} mb="xs">
            {t('agile.unmappedStatuses')}
          </Text>
          <Group gap="xs">
            {unmapped.map((s) => (
              <StatusBadge key={s.id} status={s} />
            ))}
          </Group>
        </Card>
      )}
    </Stack>
  );
}

function Editor({ board }: { board: Board }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const issueConfig = useIssueConfig();
  const [name, setName] = useState(board.name);
  const [query, setQuery] = useState(board.query);
  const [config, setConfig] = useState<BoardConfig>(board.config);
  const numberFields = (issueConfig.data?.fields ?? []).filter((f) => f.custom && f.type === 'number');
  const cardFields = (issueConfig.data?.fields ?? []).filter(
    (f) =>
      f.custom ||
      [
        'dueDate',
        'labels',
        'components',
        'fixVersions',
        'reporter',
        'originalEstimate',
        'remainingEstimate',
        'timeSpent',
        'created',
        'updated',
      ].includes(f.key),
  );
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/boards/{id}', {
          params: { path: { id: board.id }, header: { 'If-Match': `"${board.version}"` } },
          body: { name, query, config },
        }),
      ),
    onSuccess: (b) => {
      client.setQueryData(['board', board.id], b);
      void client.invalidateQueries({ queryKey: ['board'] });
      notifySuccess(t('common.saved'));
    },
    onError: (e) => {
      if (e instanceof ApiError && e.status === 412)
        void client.invalidateQueries({ queryKey: ['board', board.id] });
    },
  });
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE('/api/v1/boards/{id}', { params: { path: { id: board.id } } })),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['board'] });
      navigate(`/projects/${board.project.key}/boards`);
    },
    onError: notifyError,
  });
  const set = (patch: Partial<BoardConfig>) => setConfig((c) => ({ ...c, ...patch }));
  return (
    <Stack>
      <Group justify="flex-end">
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          {t('common.save')}
        </Button>
      </Group>
      <ErrorAlert error={save.error} />
      <Tabs defaultValue="general" keepMounted={false}>
        <Tabs.List mb="md">
          <Tabs.Tab value="general">{t('agile.general')}</Tabs.Tab>
          <Tabs.Tab value="columns">{t('agile.columns')}</Tabs.Tab>
          <Tabs.Tab value="quick">{t('agile.quickFilters')}</Tabs.Tab>
          <Tabs.Tab value="lanes">{t('agile.swimlanes')}</Tabs.Tab>
          <Tabs.Tab value="cards">{t('agile.cards')}</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="general">
          <Stack maw={760}>
            <TextInput
              label={t('common.name')}
              value={name}
              onChange={(e) => setName(e.currentTarget.value)}
              required
            />
            <Textarea
              label={t('agile.boardQuery')}
              description={t('agile.boardQueryHelp')}
              value={query}
              onChange={(e) => setQuery(e.currentTarget.value)}
              autosize
              minRows={2}
              styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
            />
            <Group grow align="flex-start">
              <Select
                label={t('agile.estimation')}
                data={[
                  { value: 'count', label: t('agile.statistics.count') },
                  { value: 'originalEstimate', label: t('agile.statistics.originalEstimate') },
                  { value: 'field', label: t('agile.statistics.field') },
                ]}
                value={config.estimation.type}
                onChange={(v) =>
                  set({
                    estimation: {
                      type: (v ?? 'count') as BoardConfig['estimation']['type'],
                      field: config.estimation.field,
                    },
                  })
                }
                allowDeselect={false}
              />
              {config.estimation.type === 'field' && (
                <Select
                  label={t('agile.estimationField')}
                  data={numberFields.map((f) => ({ value: f.key, label: f.name }))}
                  value={config.estimation.field ?? null}
                  onChange={(v) => set({ estimation: { type: 'field', field: v ?? undefined } })}
                />
              )}
            </Group>
            {board.type === 'scrum' ? (
              <Switch
                label={t('agile.parallelSprints')}
                checked={!!config.parallelSprints}
                onChange={(e) => set({ parallelSprints: e.currentTarget.checked })}
              />
            ) : (
              <>
                <TextInput
                  label={t('agile.subQuery')}
                  description={t('agile.subQueryHelp')}
                  value={config.subQuery ?? ''}
                  onChange={(e) => set({ subQuery: e.currentTarget.value })}
                  ff="monospace"
                />
                <NumberInput
                  label={t('agile.hideDoneAfterDays')}
                  min={0}
                  value={config.hideDoneAfterDays ?? 0}
                  onChange={(v) => set({ hideDoneAfterDays: Number(v) || 0 })}
                />
              </>
            )}
            <Card withBorder mt="xl" style={{ borderColor: 'var(--mantine-color-red-4)' }}>
              <Group justify="space-between">
                <Text size="sm">{t('agile.deleteBoardText')}</Text>
                <Button
                  color="red"
                  variant="light"
                  onClick={() =>
                    modals.openConfirmModal({
                      title: t('agile.deleteBoard'),
                      children: <Text size="sm">{t('agile.deleteBoardConfirm', { name: board.name })}</Text>,
                      labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                      confirmProps: { color: 'red' },
                      onConfirm: () => del.mutate(),
                    })
                  }
                >
                  {t('agile.deleteBoard')}
                </Button>
              </Group>
            </Card>
          </Stack>
        </Tabs.Panel>
        <Tabs.Panel value="columns">
          <ColumnsEditor config={config} onChange={setConfig} />
        </Tabs.Panel>
        <Tabs.Panel value="quick">
          <Stack>
            <Text size="sm" c="dimmed">
              {t('agile.quickFiltersHelp')}
            </Text>
            <QueriesEditor
              value={config.quickFilters}
              onChange={(v) => set({ quickFilters: v })}
              addLabel={t('agile.addQuickFilter')}
            />
          </Stack>
        </Tabs.Panel>
        <Tabs.Panel value="lanes">
          <Stack maw={900}>
            <Select
              label={t('agile.swimlanesBy')}
              w={300}
              data={['none', 'assignee', 'epic', 'parent', 'queries'].map((v) => ({
                value: v,
                label: t(`agile.lanes.${v}`),
              }))}
              value={config.swimlanes.type}
              onChange={(v) =>
                set({
                  swimlanes: { ...config.swimlanes, type: (v ?? 'none') as BoardConfig['swimlanes']['type'] },
                })
              }
              allowDeselect={false}
            />
            {config.swimlanes.type === 'queries' && (
              <QueriesEditor
                value={config.swimlanes.queries ?? []}
                onChange={(v) => set({ swimlanes: { ...config.swimlanes, queries: v } })}
                addLabel={t('agile.addSwimlane')}
              />
            )}
          </Stack>
        </Tabs.Panel>
        <Tabs.Panel value="cards">
          <Stack maw={900}>
            <MultiSelect
              label={t('agile.cardFields')}
              description={t('agile.cardFieldsHelp')}
              data={cardFields.map((f) => ({ value: f.key, label: f.name }))}
              value={config.card.fields}
              onChange={(v) => set({ card: { ...config.card, fields: v.slice(0, 3) } })}
              maxValues={3}
            />
            <Select
              label={t('agile.cardColors')}
              w={300}
              data={['none', 'issuetype', 'priority', 'queries'].map((v) => ({
                value: v,
                label: t(`agile.colors.${v}`),
              }))}
              value={config.card.colors}
              onChange={(v) =>
                set({ card: { ...config.card, colors: (v ?? 'none') as BoardConfig['card']['colors'] } })
              }
              allowDeselect={false}
            />
            {config.card.colors === 'queries' && (
              <QueriesEditor
                value={config.card.colorQueries ?? []}
                onChange={(v) => set({ card: { ...config.card, colorQueries: v } })}
                withColor
                addLabel={t('agile.addColorQuery')}
              />
            )}
          </Stack>
        </Tabs.Panel>
      </Tabs>
      {config.columns.length > 0 && (
        <Alert variant="light" color="gray">
          <Group gap="xs">
            <Text size="sm">{t('agile.lastColumnDone')}</Text>
            <Badge variant="light">{config.columns[config.columns.length - 1].name}</Badge>
          </Group>
        </Alert>
      )}
    </Stack>
  );
}

export function BoardSettingsPage() {
  const { key = '', boardId = '' } = useParams();
  const board = useBoard(Number(boardId));
  if (board.error) return <ErrorAlert error={board.error} />;
  if (!board.data) return null;
  return (
    <Stack gap="md">
      <BoardNav projectKey={key} board={board.data} active="settings" />
      <Editor key={board.data.version} board={board.data} />
    </Stack>
  );
}
