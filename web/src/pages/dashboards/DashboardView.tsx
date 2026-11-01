// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Button,
  Card,
  Group,
  Menu,
  SegmentedControl,
  SimpleGrid,
  Stack,
  Text,
  Title,
} from '@mantine/core';
import {
  IconArrowDown,
  IconArrowLeft,
  IconArrowRight,
  IconArrowUp,
  IconDots,
  IconPencil,
  IconPlus,
  IconTrash,
} from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, api, unwrap, type Schemas } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError } from '../../components/notify';
import { WidgetBody, type Widget } from './widgets';
import { WidgetEditor } from './WidgetEditor';

export type Dashboard = Schemas['Dashboard'];

function normalise(widgets: Widget[], columns: number): Widget[] {
  const out: Widget[] = [];
  for (let c = 0; c < columns; c++) {
    widgets
      .filter((w) => Math.min(w.column, columns - 1) === c)
      .sort((a, b) => a.position - b.position)
      .forEach((w, i) => out.push({ ...w, column: c, position: i }));
  }
  return out;
}

/** Shows a dashboard; its owner can arrange and configure the widgets. */
export function DashboardView({ dashboard, editing }: { dashboard: Dashboard; editing: boolean }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [edit, setEdit] = useState<Widget | null>(null);
  const save = useMutation({
    mutationFn: (v: { widgets: Widget[]; columns?: number }) =>
      unwrap(
        api.PUT('/api/v1/dashboards/{id}', {
          params: { path: { id: dashboard.id }, header: { 'If-Match': `"${dashboard.version}"` } },
          body: {
            name: dashboard.name,
            description: dashboard.description,
            columns: v.columns ?? dashboard.columns,
            widgets: normalise(v.widgets, v.columns ?? dashboard.columns),
            shares: dashboard.shares,
          },
        }),
      ),
    onSuccess: (d) => {
      client.setQueryData(['dashboard', dashboard.id], d);
      void client.invalidateQueries({ queryKey: ['dashboard'] });
    },
    onError: (e) => {
      notifyError(e);
      if (e instanceof ApiError && e.status === 412)
        void client.invalidateQueries({ queryKey: ['dashboard'] });
    },
  });
  const widgets = dashboard.widgets;
  const update = (list: Widget[], columns?: number) => save.mutate({ widgets: list, columns });
  const move = (w: Widget, dc: number, dp: number) => {
    const list = widgets.map((x) =>
      x.id === w.id ? { ...x, column: x.column + dc, position: x.position + dp * 1.5 } : x,
    );
    update(list);
  };
  const add = (column: number) =>
    setEdit({
      id: `w${Math.max(0, ...widgets.map((w) => Number(w.id.replace(/\D/g, '')) || 0)) + 1}`,
      type: 'assigned-to-me',
      column,
      position: widgets.filter((w) => w.column === column).length,
      config: {},
    });
  return (
    <Stack>
      {editing && (
        <Group>
          <SegmentedControl
            size="xs"
            value={String(dashboard.columns)}
            onChange={(v) => update(widgets, Number(v))}
            data={[
              { value: '1', label: t('dashboards.columns', { count: 1 }) },
              { value: '2', label: t('dashboards.columns', { count: 2 }) },
              { value: '3', label: t('dashboards.columns', { count: 3 }) },
            ]}
          />
        </Group>
      )}
      <ErrorAlert error={save.error instanceof ApiError && save.error.status !== 412 ? save.error : null} />
      <SimpleGrid cols={{ base: 1, md: dashboard.columns }} spacing="md">
        {Array.from({ length: dashboard.columns }, (_, col) => (
          <Stack key={col} gap="md">
            {widgets
              .filter((w) => w.column === col)
              .map((w, i, inColumn) => (
                <Card key={w.id} withBorder padding="md">
                  <Group justify="space-between" mb="sm" wrap="nowrap">
                    <Title order={5} lineClamp={1}>
                      {w.title || t(`dashboards.types.${w.type}`)}
                    </Title>
                    {editing && (
                      <Menu position="bottom-end">
                        <Menu.Target>
                          <ActionIcon variant="subtle" aria-label={t('issues.more')}>
                            <IconDots size={16} />
                          </ActionIcon>
                        </Menu.Target>
                        <Menu.Dropdown>
                          <Menu.Item leftSection={<IconPencil size={14} />} onClick={() => setEdit(w)}>
                            {t('common.edit')}
                          </Menu.Item>
                          {i > 0 && (
                            <Menu.Item leftSection={<IconArrowUp size={14} />} onClick={() => move(w, 0, -1)}>
                              {t('issueAdmin.moveUp')}
                            </Menu.Item>
                          )}
                          {i < inColumn.length - 1 && (
                            <Menu.Item
                              leftSection={<IconArrowDown size={14} />}
                              onClick={() => move(w, 0, 1)}
                            >
                              {t('issueAdmin.moveDown')}
                            </Menu.Item>
                          )}
                          {col > 0 && (
                            <Menu.Item
                              leftSection={<IconArrowLeft size={14} />}
                              onClick={() => move(w, -1, 0)}
                            >
                              {t('agile.moveLeft')}
                            </Menu.Item>
                          )}
                          {col < dashboard.columns - 1 && (
                            <Menu.Item
                              leftSection={<IconArrowRight size={14} />}
                              onClick={() => move(w, 1, 0)}
                            >
                              {t('agile.moveRight')}
                            </Menu.Item>
                          )}
                          <Menu.Divider />
                          <Menu.Item
                            color="red"
                            leftSection={<IconTrash size={14} />}
                            onClick={() => update(widgets.filter((x) => x.id !== w.id))}
                          >
                            {t('common.remove')}
                          </Menu.Item>
                        </Menu.Dropdown>
                      </Menu>
                    )}
                  </Group>
                  <WidgetBody widget={w} />
                </Card>
              ))}
            {editing && (
              <Button variant="default" leftSection={<IconPlus size={16} />} onClick={() => add(col)}>
                {t('dashboards.addWidget')}
              </Button>
            )}
            {!editing && widgets.length === 0 && col === 0 && (
              <Text c="dimmed" size="sm">
                {t('dashboards.empty')}
              </Text>
            )}
          </Stack>
        ))}
      </SimpleGrid>
      {edit && (
        <WidgetEditor
          widget={edit}
          onClose={() => setEdit(null)}
          onSave={(w) =>
            update(
              widgets.some((x) => x.id === w.id)
                ? widgets.map((x) => (x.id === w.id ? w : x))
                : [...widgets, w],
            )
          }
        />
      )}
    </Stack>
  );
}
