// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Badge, Group, Text, ThemeIcon, Tooltip } from '@mantine/core';
import {
  IconArrowBadgeDown,
  IconArrowBadgeUp,
  IconBolt,
  IconBookmark,
  IconBug,
  IconCheckbox,
  IconChevronsDown,
  IconChevronsUp,
  IconEqual,
  IconSubtask,
} from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { api, unwrap, type Schemas } from '../../api/client';
import { PersonAvatar } from '../../components/PersonAvatar';

export type IssueConfig = Schemas['Config'];
export type Issue = Schemas['Issue'];
export type IssueType = Schemas['IssueType'];
export type Status = Schemas['Status'];
export type Priority = Schemas['Priority'];
export type UserRef = Schemas['UserRef'];

export function useIssueConfig() {
  return useQuery({
    queryKey: ['issue-config'],
    queryFn: () => unwrap(api.GET('/api/v1/issue-config')),
    staleTime: 5 * 60_000,
  });
}

export function useProject(key: string | undefined) {
  return useQuery({
    queryKey: ['project', key],
    queryFn: () => unwrap(api.GET('/api/v1/projects/{key}', { params: { path: { key: key! } } })),
    enabled: !!key,
  });
}

const typeIcons: Record<string, typeof IconBug> = {
  epic: IconBolt,
  story: IconBookmark,
  task: IconCheckbox,
  bug: IconBug,
  subtask: IconSubtask,
};

/** Kogge's own issue type icons: a coloured rounded square with a symbol. */
export function TypeIcon({
  type,
  size = 18,
}: {
  type?: Pick<IssueType, 'name' | 'icon' | 'color'> | null;
  size?: number;
}) {
  if (!type) return null;
  const Icon = typeIcons[type.icon] ?? IconCheckbox;
  return (
    <Tooltip label={type.name} openDelay={400}>
      <ThemeIcon size={size} radius={4} color={type.color} variant="filled" role="img" aria-label={type.name}>
        <Icon size={size * 0.72} stroke={2.2} />
      </ThemeIcon>
    </Tooltip>
  );
}

const priorityIcons: Record<string, typeof IconBug> = {
  highest: IconChevronsUp,
  high: IconArrowBadgeUp,
  medium: IconEqual,
  low: IconArrowBadgeDown,
  lowest: IconChevronsDown,
};

export function PriorityIcon({ priority, withName }: { priority?: Priority | null; withName?: boolean }) {
  if (!priority) return null;
  const Icon = priorityIcons[priority.icon] ?? IconEqual;
  const color = `var(--mantine-color-${priority.color}-6)`;
  if (withName) {
    // The name is written next to the icon, which is decoration then.
    return (
      <Group gap={4} wrap="nowrap">
        <Icon size={18} color={color} aria-hidden />
        <Text size="sm">{priority.name}</Text>
      </Group>
    );
  }
  const icon = <Icon size={18} color={color} role="img" aria-label={priority.name} />;
  return (
    <Tooltip label={priority.name} openDelay={400}>
      <span style={{ display: 'inline-flex' }}>{icon}</span>
    </Tooltip>
  );
}

export function StatusBadge({ status }: { status?: Pick<Status, 'name' | 'category'> | null }) {
  if (!status) return null;
  // Shades dark enough for white text.
  const color = status.category === 'done' ? 'teal.9' : status.category === 'inprogress' ? 'blue.8' : 'gray';
  return (
    <Badge color={color} variant={status.category === 'todo' ? 'light' : 'filled'} radius="sm" tt="none">
      {status.name}
    </Badge>
  );
}

export function UserBadge({ user, size = 'sm' }: { user?: UserRef | null; size?: 'xs' | 'sm' | 'md' }) {
  if (!user) return null;
  return (
    <Group gap={6} wrap="nowrap">
      <PersonAvatar person={user} size={size === 'xs' ? 20 : 24} />
      <Text size={size} c={user.active ? undefined : 'dimmed'} truncate>
        {user.displayName}
      </Text>
    </Group>
  );
}

export function formatDuration(seconds: number | null | undefined, hoursPerDay = 8, daysPerWeek = 5): string {
  if (!seconds) return '0m';
  const units: [number, string][] = [
    [3600 * hoursPerDay * daysPerWeek, 'w'],
    [3600 * hoursPerDay, 'd'],
    [3600, 'h'],
    [60, 'm'],
  ];
  let rest = seconds;
  const parts: string[] = [];
  for (const [n, u] of units) {
    if (rest >= n) {
      parts.push(`${Math.floor(rest / n)}${u}`);
      rest %= n;
    }
  }
  return parts.join(' ') || '0m';
}
