// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Group, Modal, NumberInput, Select, Stack, Textarea, TextInput } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { useIssueConfig } from '../issues/shared';
import { str, type Widget } from './widgets';

export const widgetTypes: Widget['type'][] = [
  'assigned-to-me',
  'filter-results',
  'distribution',
  'matrix',
  'created-vs-resolved',
  'sprint-health',
  'activity',
  'text',
];

const systemStatisticFields = [
  'status',
  'statusCategory',
  'priority',
  'assignee',
  'reporter',
  'issuetype',
  'resolution',
  'project',
  'labels',
  'components',
  'fixVersions',
];

/** Finds a saved filter as the user types; choosing one hands its query on. */
function FilterQueryPicker({ onPick }: { onPick: (query: string) => void }) {
  const { t } = useTranslation();
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search.trim(), 200);
  const filters = useQuery({
    queryKey: ['filters', 'list', 'picker', debounced],
    queryFn: () =>
      unwrap(api.GET('/api/v1/filters', { params: { query: { query: debounced || undefined, limit: 30 } } })),
    placeholderData: (prev) => prev,
  });
  const items = filters.data?.items ?? [];
  // Who has no saved filters at all needs no picker.
  if (!debounced && filters.data?.total === 0) return null;
  return (
    <Select
      size="xs"
      searchable
      placeholder={t('dashboards.fromFilter')}
      aria-label={t('dashboards.fromFilter')}
      data={items.map((f) => ({ value: String(f.id), label: f.name }))}
      value={null}
      onChange={(v) => {
        const f = items.find((x) => String(x.id) === v);
        if (f) onPick(f.query);
        setSearch('');
      }}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
    />
  );
}

/** Finds a Scrum board the user sees as they type; the chosen board keeps its name while the search shows others. */
function ScrumBoardPicker({
  value,
  onChange,
}: {
  value: number | null;
  onChange: (id: number | null) => void;
}) {
  const { t } = useTranslation();
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search.trim(), 200);
  const chosen = useQuery({
    queryKey: ['board', value],
    queryFn: () => unwrap(api.GET('/api/v1/boards/{id}', { params: { path: { id: value! } } })),
    enabled: !!value,
    retry: false,
  });
  const label = (b: Schemas['Board']) => `${b.name} (${b.project.key})`;
  // The input shows the chosen board's name; that is no search: offer the other boards too.
  const query = chosen.data && debounced === label(chosen.data) ? '' : debounced;
  const boards = useQuery({
    queryKey: ['board', 'list', 'picker', query],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/boards', {
          params: { query: { query: query || undefined, type: 'scrum', limit: 30 } },
        }),
      ),
    placeholderData: (prev) => prev,
  });
  const data = (boards.data?.items ?? []).map((b) => ({ value: String(b.id), label: label(b) }));
  if (value && !data.some((d) => d.value === String(value))) {
    data.unshift({ value: String(value), label: chosen.data ? label(chosen.data) : `#${value}` });
  }
  return (
    <Select
      label={t('agile.board')}
      searchable
      clearable
      data={data}
      value={value ? String(value) : null}
      onChange={(v) => onChange(v ? Number(v) : null)}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
    />
  );
}

/** Adds a widget or changes its title and settings. */
export function WidgetEditor({
  widget,
  onSave,
  onClose,
}: {
  widget: Widget;
  onSave: (w: Widget) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  const [type, setType] = useState<Widget['type']>(widget.type);
  const [title, setTitle] = useState(widget.title ?? '');
  const [values, setValues] = useState<Record<string, unknown>>(widget.config ?? {});
  const set = (k: string, v: unknown) => setValues((o) => ({ ...o, [k]: v }));
  const projects = useQuery({ queryKey: ['projects'], queryFn: () => unwrap(api.GET('/api/v1/projects')) });
  const fieldOptions = [
    ...systemStatisticFields.map((f) => ({ value: f, label: t(`dashboards.fields.${f}`) })),
    ...(config.data?.fields ?? [])
      .filter((f) => f.custom && (f.type === 'select' || f.type === 'radio'))
      .map((f) => ({ value: f.key, label: f.name })),
  ];
  const queryInput = (
    <Stack gap={4}>
      <Textarea
        label={t('issues.query')}
        value={str(values.query)}
        onChange={(e) => set('query', e.currentTarget.value)}
        autosize
        minRows={2}
        styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
      />
      <FilterQueryPicker onPick={(q) => set('query', q)} />
    </Stack>
  );
  return (
    <Modal opened onClose={onClose} title={t('dashboards.widget')} size="lg">
      <Stack>
        <Select
          label={t('dashboards.widgetType')}
          data={widgetTypes.map((w) => ({ value: w, label: t(`dashboards.types.${w}`) }))}
          value={type}
          onChange={(v) => v && setType(v as Widget['type'])}
          allowDeselect={false}
        />
        <TextInput
          label={t('dashboards.widgetTitle')}
          placeholder={t(`dashboards.types.${type}`)}
          value={title}
          onChange={(e) => setTitle(e.currentTarget.value)}
        />
        {(type === 'filter-results' ||
          type === 'distribution' ||
          type === 'matrix' ||
          type === 'created-vs-resolved') &&
          queryInput}
        {(type === 'filter-results' || type === 'assigned-to-me' || type === 'activity') && (
          <NumberInput
            label={t('dashboards.limit')}
            min={1}
            max={50}
            value={Number(values.limit ?? 10)}
            onChange={(v) => set('limit', Number(v) || 10)}
          />
        )}
        {type === 'distribution' && (
          <Select
            label={t('dashboards.field')}
            data={fieldOptions}
            value={str(values.field, 'status')}
            onChange={(v) => set('field', v)}
            allowDeselect={false}
          />
        )}
        {type === 'matrix' && (
          <Group grow>
            <Select
              label={t('dashboards.xField')}
              data={fieldOptions}
              value={str(values.x, 'status')}
              onChange={(v) => set('x', v)}
              allowDeselect={false}
            />
            <Select
              label={t('dashboards.yField')}
              data={fieldOptions}
              value={str(values.y, 'assignee')}
              onChange={(v) => set('y', v)}
              allowDeselect={false}
            />
          </Group>
        )}
        {type === 'sprint-health' && (
          <ScrumBoardPicker
            value={values.boardId ? Number(values.boardId) : null}
            onChange={(id) => set('boardId', id ?? undefined)}
          />
        )}
        {type === 'activity' && (
          <Select
            label={t('issues.project')}
            placeholder={t('dashboards.allProjects')}
            clearable
            data={(projects.data ?? []).map((p) => ({ value: p.key, label: `${p.name} (${p.key})` }))}
            value={str(values.project) || null}
            onChange={(v) => set('project', v ?? '')}
          />
        )}
        {type === 'text' && (
          <Textarea
            label={t('dashboards.text')}
            value={str(values.text)}
            onChange={(e) => set('text', e.currentTarget.value)}
            autosize
            minRows={4}
          />
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={() => {
              onSave({ ...widget, type, title: title.trim() || undefined, config: values });
              onClose();
            }}
          >
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
