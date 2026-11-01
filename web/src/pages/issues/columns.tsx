// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Badge, Group, Text, Tooltip } from '@mantine/core';
import type { TFunction } from 'i18next';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import type { Schemas } from '../../api/client';
import { formatDateTime, formatDay, formatRelative } from '../../app/format';
import { PriorityIcon, StatusBadge, TypeIcon, UserBadge, formatDuration, type IssueConfig } from './shared';

export type IssueRow = Schemas['IssueRow'];

/** A column of the issue navigator: how it shows an issue, and as text (for exports). */
export interface Column {
  key: string;
  label: string;
  /** Narrow columns show an icon and hide their label. */
  narrow?: boolean;
  /** The name to order issues by in KQL, for columns issues can be ordered by. */
  order?: string;
  /** Ordering by the column starts with the highest values (dates, priority, votes). */
  descending?: boolean;
  cell: (row: IssueRow) => ReactNode;
  text: (row: IssueRow) => string;
}

/** The columns of the navigator unless a filter or the user chose others. */
export const defaultColumns = ['issuetype', 'key', 'summary', 'status', 'priority', 'assignee', 'updated'];

const pad = (n: number) => String(n).padStart(2, '0');

/** A time as text for exports: YYYY-MM-DD HH:MM in the user's time zone. */
function exportTime(value: string | null | undefined, timeZone?: string): string {
  if (!value) return '';
  const parts = Object.fromEntries(
    new Intl.DateTimeFormat('en-CA', {
      timeZone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hourCycle: 'h23',
    })
      .formatToParts(new Date(value))
      .map((p) => [p.type, p.value]),
  );
  return `${parts.year}-${parts.month}-${parts.day} ${pad(Number(parts.hour))}:${parts.minute}`;
}

/** Options of the catalogue: units of durations, the decimal mark of numbers in exports, the time zone. */
export interface ColumnOptions {
  hoursPerDay: number;
  daysPerWeek: number;
  decimalComma?: boolean;
  timeZone?: string;
}

/** The columns the navigator offers: the system fields and the custom fields of the configuration. */
export function columnCatalog(t: TFunction, cfg: IssueConfig | undefined, o: ColumnOptions): Column[] {
  const hours = (seconds: number | null | undefined) => {
    if (!seconds) return '';
    const v = (Math.round((seconds / 3600) * 100) / 100).toString();
    return o.decimalComma ? v.replace('.', ',') : v;
  };
  const duration = (seconds: number | null | undefined) =>
    seconds ? formatDuration(seconds, o.hoursPerDay, o.daysPerWeek) : '';
  const time = (value: string | null | undefined) =>
    value ? (
      <Tooltip label={formatDateTime(value)}>
        <Text size="sm" span>
          {formatRelative(value)}
        </Text>
      </Tooltip>
    ) : null;
  const names = (refs: { name: string }[] | undefined) => (refs ?? []).map((r) => r.name).join(', ');
  const plain = (s: string) => (s ? <Text size="sm">{s}</Text> : null);
  const user = (u: IssueRow['assignee'], empty?: string) =>
    u ? (
      <UserBadge user={u} size="xs" />
    ) : empty ? (
      <Text size="sm" c="dimmed">
        {empty}
      </Text>
    ) : null;
  const columns: Column[] = [
    {
      key: 'issuetype',
      label: t('issues.type'),
      narrow: true,
      order: 'issuetype',
      cell: (r) => <TypeIcon type={r.type} />,
      text: (r) => r.type?.name ?? '',
    },
    {
      key: 'key',
      label: t('issues.key'),
      order: 'key',
      cell: (r) => (
        <Anchor component={Link} to={`/browse/${r.key}`} size="sm" fw={550} style={{ whiteSpace: 'nowrap' }}>
          {r.key}
        </Anchor>
      ),
      text: (r) => r.key,
    },
    {
      key: 'summary',
      label: t('issues.summary'),
      order: 'summary',
      cell: (r) => (
        <Anchor component={Link} to={`/browse/${r.key}`} size="sm" c="var(--mantine-color-text)">
          {r.summary}
        </Anchor>
      ),
      text: (r) => r.summary,
    },
    {
      key: 'project',
      label: t('issues.project'),
      order: 'project',
      cell: (r) => plain(r.project.name),
      text: (r) => r.project.name,
    },
    {
      key: 'status',
      label: t('issues.status'),
      order: 'status',
      cell: (r) => <StatusBadge status={r.status} />,
      text: (r) => r.status?.name ?? '',
    },
    {
      key: 'priority',
      label: t('issues.priority'),
      narrow: true,
      order: 'priority',
      descending: true,
      cell: (r) => <PriorityIcon priority={r.priority} />,
      text: (r) => r.priority?.name ?? '',
    },
    {
      key: 'resolution',
      label: t('issues.resolution'),
      order: 'resolution',
      cell: (r) =>
        r.resolution ? (
          plain(r.resolution.name)
        ) : (
          <Text size="sm" c="dimmed">
            {t('issues.unresolved')}
          </Text>
        ),
      text: (r) => r.resolution?.name ?? '',
    },
    {
      key: 'assignee',
      label: t('issues.assignee'),
      order: 'assignee',
      cell: (r) => user(r.assignee, t('issues.unassigned')),
      text: (r) => r.assignee?.displayName ?? '',
    },
    {
      key: 'reporter',
      label: t('issues.reporter'),
      order: 'reporter',
      cell: (r) => user(r.reporter),
      text: (r) => r.reporter?.displayName ?? '',
    },
    {
      key: 'created',
      label: t('issues.created'),
      order: 'created',
      descending: true,
      cell: (r) => time(r.created),
      text: (r) => exportTime(r.created, o.timeZone),
    },
    {
      key: 'updated',
      label: t('issues.updated'),
      order: 'updated',
      descending: true,
      cell: (r) => time(r.updated),
      text: (r) => exportTime(r.updated, o.timeZone),
    },
    {
      key: 'resolved',
      label: t('issues.resolved'),
      order: 'resolved',
      descending: true,
      cell: (r) => time(r.resolved),
      text: (r) => exportTime(r.resolved, o.timeZone),
    },
    {
      key: 'duedate',
      label: t('issues.dueDate'),
      order: 'due',
      cell: (r) => plain(formatDay(r.dueDate)),
      text: (r) => r.dueDate ?? '',
    },
    {
      key: 'labels',
      label: t('issues.labels'),
      cell: (r) =>
        r.labels.length > 0 ? (
          <Group gap={4}>
            {r.labels.map((l) => (
              <Badge key={l} variant="light" color="gray" tt="none" size="sm">
                {l}
              </Badge>
            ))}
          </Group>
        ) : null,
      text: (r) => r.labels.join(', '),
    },
    {
      key: 'components',
      label: t('issues.components'),
      cell: (r) => plain(names(r.components)),
      text: (r) => names(r.components),
    },
    {
      key: 'fixVersions',
      label: t('issues.fixVersions'),
      cell: (r) => plain(names(r.fixVersions)),
      text: (r) => names(r.fixVersions),
    },
    {
      key: 'affectsVersions',
      label: t('issues.affectsVersions'),
      cell: (r) => plain(names(r.affectsVersions)),
      text: (r) => names(r.affectsVersions),
    },
    {
      key: 'sprint',
      label: t('navigator.sprint'),
      cell: (r) => plain(r.sprint?.name ?? ''),
      text: (r) => r.sprint?.name ?? '',
    },
    {
      key: 'parent',
      label: t('issues.parent'),
      order: 'parent',
      cell: (r) =>
        r.parentKey ? (
          <Anchor component={Link} to={`/browse/${r.parentKey}`} size="sm">
            {r.parentKey}
          </Anchor>
        ) : null,
      text: (r) => r.parentKey ?? '',
    },
    {
      key: 'originalEstimate',
      label: t('issues.originalEstimate'),
      order: 'originalEstimate',
      descending: true,
      cell: (r) => plain(duration(r.originalEstimate)),
      text: (r) => hours(r.originalEstimate),
    },
    {
      key: 'remainingEstimate',
      label: t('issues.remainingEstimate'),
      order: 'remainingEstimate',
      descending: true,
      cell: (r) => plain(duration(r.remainingEstimate)),
      text: (r) => hours(r.remainingEstimate),
    },
    {
      key: 'timeSpent',
      label: t('issues.timeSpent'),
      order: 'timeSpent',
      descending: true,
      cell: (r) => plain(duration(r.timeSpent)),
      text: (r) => hours(r.timeSpent),
    },
    {
      key: 'votes',
      label: t('navigator.votes'),
      order: 'votes',
      descending: true,
      cell: (r) => plain(r.votes ? String(r.votes) : ''),
      text: (r) => String(r.votes),
    },
  ];
  for (const f of cfg?.fields ?? []) {
    if (!f.custom) continue;
    const id = f.key.replace(/^cf_/, '');
    columns.push({
      key: f.key,
      label: f.name,
      order: f.orderable ? `cf[${id}]` : undefined,
      descending: f.type === 'number' || f.type === 'date' || f.type === 'datetime',
      cell: (r) => plain(r.display?.[f.key] ?? ''),
      text: (r) => {
        const v = r.display?.[f.key] ?? '';
        return o.decimalComma && f.type === 'number' ? v.replace('.', ',') : v;
      },
    });
  }
  return columns;
}

/** The columns to show: those chosen that exist (the defaults when none do). */
export function resolveColumns(catalog: Column[], keys: readonly string[] | null | undefined): Column[] {
  const byKey = new Map(catalog.map((c) => [c.key, c]));
  const chosen = (keys ?? []).map((k) => byKey.get(k)).filter((c): c is Column => !!c);
  return chosen.length > 0 ? chosen : defaultColumns.map((k) => byKey.get(k)!).filter(Boolean);
}
