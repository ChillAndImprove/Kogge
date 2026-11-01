// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Badge, Button, Group, Select, Stack, Text } from '@mantine/core';
import { IconX } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../api/client';
import { GroupPicker, UserPicker } from './Pickers';

export type Share = Schemas['Share'];

/** Returns a function that names a share ("Group: team"). */
export function useShareLabel(): (s: Share) => string {
  const { t } = useTranslation();
  const projects = useQuery({ queryKey: ['projects'], queryFn: () => unwrap(api.GET('/api/v1/projects')) });
  const projectName = (pid?: number) => projects.data?.find((p) => p.id === pid)?.name ?? `#${pid}`;
  return (s: Share) => {
    switch (s.type) {
      case 'project':
        return `${t('sharing.project')}: ${projectName(s.projectId)}`;
      case 'group':
        return `${t('sharing.group')}: ${s.name ?? s.id}`;
      case 'user':
        return `${t('sharing.user')}: ${s.name ?? s.id}`;
      default:
        return t(`sharing.${s.type}`);
    }
  };
}

/** Edits who a filter or dashboard is shared with. */
export function ShareEditor({ value, onChange }: { value: Share[]; onChange: (s: Share[]) => void }) {
  const { t } = useTranslation();
  const [type, setType] = useState<Share['type']>('project');
  const [id, setId] = useState<number | null>(null);
  const [name, setName] = useState<string | undefined>();
  const projects = useQuery({ queryKey: ['projects'], queryFn: () => unwrap(api.GET('/api/v1/projects')) });
  const label = useShareLabel();
  const pick = (v: number | null, l?: string) => {
    setId(v);
    setName(l);
  };
  const needsId = type === 'project' || type === 'group' || type === 'user';
  return (
    <Stack gap="xs">
      <Group gap={6}>
        {value.length === 0 && (
          <Text size="sm" c="dimmed">
            {t('sharing.private')}
          </Text>
        )}
        {value.map((s, i) => (
          <Badge
            key={i}
            variant="light"
            tt="none"
            color={s.type === 'anyone' ? 'red' : undefined}
            rightSection={
              <ActionIcon
                size="xs"
                variant="transparent"
                onClick={() => onChange(value.filter((_, j) => j !== i))}
                aria-label={t('common.remove')}
              >
                <IconX size={10} />
              </ActionIcon>
            }
          >
            {label(s)}
          </Badge>
        ))}
      </Group>
      <Group align="flex-end" gap="xs">
        <Select
          size="xs"
          w={170}
          data={(['project', 'group', 'user', 'logged-in', 'anyone'] as const).map((v) => ({
            value: v,
            label: t(`sharing.${v}`),
          }))}
          value={type}
          onChange={(v) => {
            setType((v ?? 'project') as Share['type']);
            setId(null);
          }}
          allowDeselect={false}
          aria-label={t('sharing.shareWith')}
        />
        {type === 'project' && (
          <Select
            size="xs"
            w={220}
            data={(projects.data ?? []).map((p) => ({ value: String(p.id), label: p.name }))}
            value={id ? String(id) : null}
            onChange={(v) => setId(v ? Number(v) : null)}
            searchable
            aria-label={t('sharing.project')}
          />
        )}
        {type === 'group' && (
          <GroupPicker size="xs" w={220} value={id} onChange={pick} aria-label={t('sharing.group')} />
        )}
        {type === 'user' && (
          <UserPicker size="xs" w={220} value={id} onChange={pick} aria-label={t('sharing.user')} />
        )}
        <Button
          size="xs"
          variant="light"
          disabled={needsId && !id}
          onClick={() => {
            const s: Share =
              type === 'project' ? { type, projectId: id! } : needsId ? { type, id: id!, name } : { type };
            onChange([...value, s]);
            setId(null);
          }}
        >
          {t('sharing.add')}
        </Button>
      </Group>
    </Stack>
  );
}
