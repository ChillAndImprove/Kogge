// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Avatar, Badge, Group, Text } from '@mantine/core';
import {
  IconBook2,
  IconFileText,
  IconFolderCode,
  IconFolders,
  IconGitPullRequest,
  IconLayoutKanban,
  IconNews,
} from '@tabler/icons-react';
import type { ComponentType } from 'react';
import { useTranslation } from 'react-i18next';
import type { Schemas } from '../api/client';
import { StateIcon } from '../pages/builds/status';
import { StatusBadge, TypeIcon } from '../pages/issues/shared';

/** Things found by the quick search and the search page, as both show them. */

export type Hit = Schemas['Hit'];

const kindIcons: Partial<Record<Hit['kind'], ComponentType<{ size?: number }>>> = {
  project: IconLayoutKanban,
  page: IconFileText,
  blog: IconNews,
  space: IconBook2,
  'code-project': IconFolders,
  repository: IconFolderCode,
  'pull-request': IconGitPullRequest,
};

function initials(name: string) {
  return name
    .split(/\s+/)
    .map((p) => p[0])
    .join('')
    .slice(0, 2)
    .toUpperCase();
}

/** Whether the hit's icon says something (the type of an issue, the state of a run) rather than decorating. */
export function iconSpeaks(hit: Hit) {
  return hit.kind === 'issue' || ((hit.kind === 'pipelines' || hit.kind === 'run') && !!hit.state);
}

/** The icon of a hit: the type of an issue, the state of a run, a person's initials, or the icon of its kind. */
export function HitIcon({ hit, size = 18 }: { hit: Hit; size?: number }) {
  if (hit.kind === 'issue') return <TypeIcon type={hit.type} size={size} />;
  if ((hit.kind === 'pipelines' || hit.kind === 'run') && hit.state)
    return <StateIcon state={hit.state} size={size + 2} />;
  if (hit.kind === 'user') {
    return (
      <Avatar size={size + 2} radius="xl" color="initials" name={hit.title} aria-hidden>
        {initials(hit.title)}
      </Avatar>
    );
  }
  const Icon = kindIcons[hit.kind];
  return Icon ? <Icon size={size} /> : null;
}

/** Whether the hit's key is shown before its title (keys of issues, numbers of pull requests and runs). */
export function showsKey(hit: Hit) {
  return !!hit.key && (hit.kind === 'issue' || hit.kind === 'pull-request' || hit.kind === 'run');
}

/** The person a hit names on the search page: the assignee of an issue, who changed a page last, the author of a
 * pull request; with a small avatar. */
export function HitPerson({ hit }: { hit: Hit }) {
  const { t } = useTranslation();
  const p = hit.person;
  if (!p) return null;
  const text =
    hit.kind === 'issue'
      ? t('searchAll.assignedTo', { name: p.displayName })
      : hit.kind === 'pull-request'
        ? t('searchAll.openedBy', { name: p.displayName })
        : t('searchAll.changedBy', { name: p.displayName });
  return (
    <Group gap="xs" wrap="nowrap" miw={0}>
      <Avatar size={20} radius="xl" color="initials" name={p.displayName} aria-hidden>
        {initials(p.displayName)}
      </Avatar>
      <Text size="xs" c="dimmed" truncate>
        {text}
      </Text>
    </Group>
  );
}

/** The number of hits of a filter of a search: a small neutral pill. */
export function HitCount({ n }: { n: number }) {
  return (
    <Badge
      size="sm"
      variant="light"
      color="gray"
      radius="xl"
      tt="none"
      style={{ fontVariantNumeric: 'tabular-nums' }}
    >
      {n}
    </Badge>
  );
}

/** The state of a hit: the status of an issue, the state of a pull request. */
export function HitBadges({ hit }: { hit: Hit }) {
  const { t } = useTranslation();
  if (hit.status) return <StatusBadge status={hit.status} />;
  if (hit.kind === 'pull-request' && hit.state) {
    return (
      <Badge
        size="sm"
        variant="light"
        color={hit.state === 'merged' ? 'violet' : hit.state === 'open' ? 'blue' : 'gray'}
      >
        {t(`code.pr.${hit.state}` as 'code.pr.open')}
      </Badge>
    );
  }
  return null;
}
