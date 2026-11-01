// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Button, Group, Popover, Select, Stack, Text } from '@mantine/core';
import { IconArrowDown, IconArrowUp, IconColumns, IconX } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import type { Column } from './columns';

/** Chooses and orders the columns of the issue navigator. */
export function ColumnChooser({
  catalog,
  value,
  onChange,
  onReset,
}: {
  catalog: Column[];
  value: Column[];
  onChange: (keys: string[]) => void;
  onReset: () => void;
}) {
  const { t } = useTranslation();
  const keys = value.map((c) => c.key);
  const move = (i: number, by: number) => {
    const next = [...keys];
    [next[i], next[i + by]] = [next[i + by], next[i]];
    onChange(next);
  };
  const available = catalog
    .filter((c) => !keys.includes(c.key))
    .map((c) => ({ value: c.key, label: c.label }))
    .sort((a, b) => a.label.localeCompare(b.label));
  return (
    <Popover position="bottom-end" width={320} shadow="md" trapFocus>
      <Popover.Target>
        <Button variant="default" size="xs" leftSection={<IconColumns size={14} />}>
          {t('navigator.columns')}
        </Button>
      </Popover.Target>
      <Popover.Dropdown>
        <Stack gap="xs">
          <Text size="sm" fw={600}>
            {t('navigator.columnsTitle')}
          </Text>
          <Stack gap={2} role="list" aria-label={t('navigator.columnsTitle')}>
            {value.map((c, i) => (
              <Group key={c.key} gap={4} wrap="nowrap" role="listitem">
                <Text size="sm" style={{ flex: 1 }} truncate>
                  {c.label}
                </Text>
                <ActionIcon
                  size="sm"
                  variant="subtle"
                  disabled={i === 0}
                  onClick={() => move(i, -1)}
                  aria-label={t('navigator.moveUp', { name: c.label })}
                >
                  <IconArrowUp size={14} />
                </ActionIcon>
                <ActionIcon
                  size="sm"
                  variant="subtle"
                  disabled={i === value.length - 1}
                  onClick={() => move(i, 1)}
                  aria-label={t('navigator.moveDown', { name: c.label })}
                >
                  <IconArrowDown size={14} />
                </ActionIcon>
                <ActionIcon
                  size="sm"
                  variant="subtle"
                  color="red"
                  disabled={value.length === 1}
                  onClick={() => onChange(keys.filter((k) => k !== c.key))}
                  aria-label={t('navigator.remove', { name: c.label })}
                >
                  <IconX size={14} />
                </ActionIcon>
              </Group>
            ))}
          </Stack>
          <Select
            size="xs"
            placeholder={t('navigator.addColumn')}
            aria-label={t('navigator.addColumn')}
            data={available}
            value={null}
            onChange={(k) => k && onChange([...keys, k])}
            searchable
            comboboxProps={{ withinPortal: false }}
          />
          <Group justify="flex-end">
            <Button size="xs" variant="subtle" onClick={onReset}>
              {t('navigator.restore')}
            </Button>
          </Group>
        </Stack>
      </Popover.Dropdown>
    </Popover>
  );
}
