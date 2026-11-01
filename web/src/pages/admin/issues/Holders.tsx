// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Badge, Button, Group, Popover, Select, Stack } from '@mantine/core';
import { IconPlus, IconX } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { GroupPicker, UserPicker } from '../../../components/Pickers';
import { useIssueConfig } from '../../issues/shared';

export interface Holder {
  type: string;
  id?: number;
  name?: string;
}

/** Shows a holder (role, group, user or a special holder) as a removable badge. */
export function HolderBadge({ holder, onRemove }: { holder: Holder; onRemove?: () => void }) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  let label = t(`issueAdmin.holders.${holder.type}`, { defaultValue: holder.type });
  if (holder.type === 'role')
    label += `: ${holder.name || config.data?.roles.find((r) => r.id === holder.id)?.name || holder.id}`;
  else if (holder.id) label += `: ${holder.name || `#${holder.id}`}`;
  return (
    <Badge
      variant={holder.type === 'role' ? 'light' : 'outline'}
      color={holder.type === 'anyone' ? 'red' : undefined}
      tt="none"
      rightSection={
        onRemove && (
          <ActionIcon size="xs" variant="transparent" onClick={onRemove} aria-label={t('common.remove')}>
            <IconX size={10} />
          </ActionIcon>
        )
      }
    >
      {label}
    </Badge>
  );
}

/** A small popover to add a holder of one of the allowed types. */
export function AddHolder({ types, onAdd }: { types: string[]; onAdd: (h: Holder) => void }) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  const [opened, setOpened] = useState(false);
  const [type, setType] = useState<string | null>(types[0]);
  const [id, setId] = useState<number | null>(null);
  const [name, setName] = useState('');
  const needsId = type === 'role' || type === 'group' || type === 'user';
  return (
    <Popover opened={opened} onChange={setOpened} position="bottom-start" withArrow trapFocus>
      <Popover.Target>
        <Button
          size="compact-xs"
          variant="subtle"
          leftSection={<IconPlus size={12} />}
          onClick={() => setOpened((o) => !o)}
        >
          {t('issueAdmin.add')}
        </Button>
      </Popover.Target>
      <Popover.Dropdown>
        <Stack gap="xs" w={260}>
          <Select
            size="xs"
            data={types.map((v) => ({ value: v, label: t(`issueAdmin.holders.${v}`) }))}
            value={type}
            onChange={(v) => {
              setType(v);
              setId(null);
            }}
            allowDeselect={false}
            comboboxProps={{ withinPortal: false }}
          />
          {type === 'role' && (
            <Select
              size="xs"
              data={(config.data?.roles ?? []).map((r) => ({ value: String(r.id), label: r.name }))}
              value={id ? String(id) : null}
              onChange={(v) => {
                setId(v ? Number(v) : null);
                setName(config.data?.roles.find((r) => String(r.id) === v)?.name ?? '');
              }}
              comboboxProps={{ withinPortal: false }}
            />
          )}
          {type === 'group' && (
            <GroupPicker size="xs" value={id} onChange={setId} comboboxProps={{ withinPortal: false }} />
          )}
          {type === 'user' && (
            <UserPicker size="xs" value={id} onChange={setId} comboboxProps={{ withinPortal: false }} />
          )}
          <Group justify="flex-end">
            <Button
              size="xs"
              disabled={!type || (needsId && !id)}
              onClick={() => {
                onAdd({
                  type: type!,
                  id: needsId ? id! : undefined,
                  name: type === 'role' ? name : undefined,
                });
                setId(null);
                setOpened(false);
              }}
            >
              {t('issueAdmin.add')}
            </Button>
          </Group>
        </Stack>
      </Popover.Dropdown>
    </Popover>
  );
}
