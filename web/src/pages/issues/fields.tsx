// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/**
 * The fields of issues — system and custom — for the issue view and the create form: what the field layout of a
 * project and issue type shows, how values read, and their inputs. Values have the form the API
 * takes them in: ids of options, users, groups and versions, keys of issues, days as YYYY-MM-DD.
 */

import {
  Anchor,
  Badge,
  Group,
  MultiSelect,
  NumberInput,
  Select,
  Stack,
  TagsInput,
  Text,
  TextInput,
  type ComboboxItem,
} from '@mantine/core';
import { DateInput, DateTimePicker } from '@mantine/dates';
import { useDebouncedValue } from '@mantine/hooks';
import { IconLock } from '@tabler/icons-react';
import dayjs from 'dayjs';
import { useQuery } from '@tanstack/react-query';
import { useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatDateTime, formatDay, formatNumber, today } from '../../app/format';
import { GroupPicker, UserPicker, UsersPicker } from '../../components/Pickers';
import { DocView } from '../../components/editor/DocView';
import { Editor, isEmptyDoc, type Doc } from '../../components/editor/Editor';
import type { DoneEditing } from '../../components/InlineEdit';
import {
  PriorityIcon,
  TypeIcon,
  UserBadge,
  formatDuration,
  useIssueConfig,
  useProject,
  type Issue,
  type UserRef,
} from './shared';

export type FieldInfo = Schemas['FieldInfo'];
export type LayoutItem = Schemas['LayoutItem'];
type Option = Schemas['Option'];

/** Keys of the system fields. */
export const F = {
  summary: 'summary',
  description: 'description',
  priority: 'priority',
  assignee: 'assignee',
  reporter: 'reporter',
  labels: 'labels',
  components: 'components',
  fixVersions: 'fixVersions',
  affectsVersions: 'affectsVersions',
  dueDate: 'duedate',
  parent: 'parent',
  originalEstimate: 'originalEstimate',
  remainingEstimate: 'remainingEstimate',
  security: 'security',
  environment: 'environment',
  sprint: 'sprint',
} as const;

export function useFieldLayout(projectKey: string | undefined, typeId: number | undefined) {
  return useQuery({
    queryKey: ['field-layout', projectKey, typeId],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/projects/{key}/field-layouts/{typeId}', {
          params: { path: { key: projectKey!, typeId: typeId! } },
        }),
      ),
    enabled: !!projectKey && typeId != null,
    staleTime: 60_000,
  });
}

/** The security levels the user may choose in a project. */
export function useSecurityLevels(projectKey: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['security-levels', projectKey],
    queryFn: () =>
      unwrap(api.GET('/api/v1/projects/{key}/security-levels', { params: { path: { key: projectKey! } } })),
    enabled: !!projectKey && enabled,
    staleTime: 60_000,
  });
}

/** A field of a layout with its definition. */
export interface LayoutField {
  def: FieldInfo;
  item: LayoutItem;
}

/** The fields of a layout in its order, with their definitions. */
export function layoutFields(
  layout: LayoutItem[] | undefined,
  fields: FieldInfo[] | undefined,
): LayoutField[] {
  const byKey = new Map((fields ?? []).map((f) => [f.key, f]));
  return (layout ?? []).flatMap((item) => {
    const def = byKey.get(item.field);
    return def ? [{ def, item }] : [];
  });
}

/** What showing and editing values needs besides the value: names and choices of the project. */
export interface FieldContext {
  projectKey: string;
  versions: Schemas['Version'][];
  components: { id: number; name: string; archived?: boolean }[];
  users: Map<number, UserRef>;
  groups: Map<number, string>;
  units: { hoursPerDay: number; daysPerWeek: number };
  /** The issue being shown; absent in the create form. */
  issue?: Issue;
  /** The level of the issue type: 1 epics, 0 standard issues, -1 sub-tasks. */
  typeLevel: number;
}

export function useFieldContext(
  projectKey: string | undefined,
  typeLevel: number,
  issue?: Issue,
): FieldContext {
  const config = useIssueConfig();
  const project = useProject(projectKey);
  const users = new Map<number, UserRef>();
  for (const u of [...(issue?.fieldUsers ?? []), issue?.assignee, issue?.reporter]) if (u) users.set(u.id, u);
  return {
    projectKey: projectKey ?? '',
    versions: project.data?.versions ?? [],
    components: project.data?.components ?? [],
    users,
    groups: new Map((issue?.fieldGroups ?? []).map((g) => [g.id, g.name])),
    units: config.data?.timeTracking ?? { hoursPerDay: 8, daysPerWeek: 5 },
    issue,
    typeLevel,
  };
}

/** The names of system fields in the user's language (the configuration has them in English). */
export function useFieldLabel() {
  const { t } = useTranslation();
  const keys: Record<string, string> = { duedate: 'issues.dueDate', sprint: 'agile.sprint' };
  return (def: FieldInfo) =>
    def.custom ? def.name : t(keys[def.key] ?? `issues.${def.key}`, { defaultValue: def.name });
}

/** The current value of a field of an issue, in the API's form. */
export function issueValue(issue: Issue, key: string): unknown {
  switch (key) {
    case F.summary:
      return issue.summary;
    case F.description:
      return issue.description ?? null;
    case F.priority:
      return issue.priority?.id ?? null;
    case F.assignee:
      return issue.assignee?.id ?? null;
    case F.reporter:
      return issue.reporter?.id ?? null;
    case F.labels:
      return issue.labels;
    case F.components:
      return issue.components.map((c) => c.id);
    case F.fixVersions:
      return issue.fixVersions.map((v) => v.id);
    case F.affectsVersions:
      return issue.affectsVersions.map((v) => v.id);
    case F.dueDate:
      return issue.dueDate ?? null;
    case F.parent:
      return issue.parent?.key ?? null;
    case F.originalEstimate:
      return issue.originalEstimate ?? null;
    case F.remainingEstimate:
      return issue.remainingEstimate ?? null;
    case F.security:
      return issue.securityLevel?.id ?? null;
    case F.environment:
      return issue.environment || null;
    case F.sprint:
      return issue.sprint?.id ?? null;
    default:
      return issue.fields?.[key] ?? null;
  }
}

export function isEmptyValue(v: unknown): boolean {
  if (v == null || v === '') return true;
  if (Array.isArray(v)) return v.length === 0;
  if (typeof v === 'object' && 'type' in (v as object)) return isEmptyDoc(v as Doc);
  return false;
}

function sameValue(a: unknown, b: unknown): boolean {
  return JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
}

/**
 * Whether the user may change the field of an issue, as the server decides (applyFields): most fields need
 * issue.edit, the assignee issue.assign, the reporter issue.modify-reporter, the due date and the sprint
 * issue.schedule, the security level issue.set-security.
 */
export function canEditField(issue: Issue, key: string): boolean {
  const p = issue.permissions;
  switch (key) {
    case F.assignee:
      return p.assign;
    case F.reporter:
      return p.modifyReporter;
    case F.dueDate:
      return p.schedule;
    case F.sprint:
      return p.schedule && issue.type.level !== -1;
    case F.security:
      return p.security && issue.type.level !== -1;
    case F.parent:
      return p.edit && issue.type.level !== 1;
    default:
      return p.edit;
  }
}

/** Types whose input saves a choice at once; the others save when focus leaves (and text with Enter). */
const choiceTypes = new Set([
  'priority',
  'user',
  'group',
  'select',
  'radio',
  'version',
  'security',
  'sprint',
  'issue',
]);
const textTypes = new Set(['text', 'url', 'number', 'duration']);

/** Whether a field is edited in the details panel (the others have places of their own). */
export function isDetailField(def: FieldInfo): boolean {
  const own: string[] = [F.summary, F.description, F.originalEstimate, F.remainingEstimate];
  if (own.includes(def.key) || def.type === 'textarea') return false;
  return def.custom === true || !['issuetype', 'status', 'resolution', 'datetime'].includes(def.type);
}

function optionLabel(options: Option[] | undefined, id: unknown): string {
  return options?.find((o) => o.id === Number(id))?.value ?? String(id);
}

function Badges({ items }: { items: { key: string | number; label: ReactNode; to?: string }[] }) {
  return (
    <Group gap={4}>
      {items.map((it) =>
        it.to ? (
          <Badge
            key={it.key}
            component={Link}
            to={it.to}
            variant="light"
            color="gray"
            radius="sm"
            tt="none"
            style={{ cursor: 'pointer' }}
            onClick={(e: React.MouseEvent) => e.stopPropagation()}
          >
            {it.label}
          </Badge>
        ) : (
          <Badge key={it.key} variant="light" color="gray" radius="sm" tt="none">
            {it.label}
          </Badge>
        ),
      )}
    </Group>
  );
}

const search = (kql: string) => `/issues/search?q=${encodeURIComponent(kql)}`;
const quote = (s: string) => `"${s.replace(/["\\]/g, (c) => `\\${c}`)}"`;

/** Shows a value; empty values read "None" (or what empty says). */
export function FieldDisplay({
  def,
  value,
  ctx,
  empty,
}: {
  def: FieldInfo;
  value: unknown;
  ctx: FieldContext;
  empty?: string;
}) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  if (isEmptyValue(value)) {
    return (
      <Text size="sm" c="dimmed" component="span">
        {empty ?? t('issues.none')}
      </Text>
    );
  }
  const issue = ctx.issue;
  const ids = (v: unknown) => (Array.isArray(v) ? v.map(Number) : [Number(v)]);
  const versionName = (id: number) =>
    ctx.versions.find((v) => v.id === id)?.name ??
    [...(issue?.fixVersions ?? []), ...(issue?.affectsVersions ?? [])].find((v) => v.id === id)?.name ??
    `#${id}`;
  switch (def.key) {
    case F.priority:
      return <PriorityIcon priority={config.data?.priorities.find((p) => p.id === Number(value))} withName />;
    case F.parent: {
      const parent = issue?.parent;
      if (!parent) return <Text size="sm">{String(value)}</Text>;
      return (
        <Group gap={6} wrap="nowrap" style={{ minWidth: 0 }}>
          <TypeIcon type={parent.type} size={16} />
          <Anchor
            component={Link}
            to={`/browse/${parent.key}`}
            size="sm"
            fw={600}
            style={{ flex: 'none', whiteSpace: 'nowrap' }}
            onClick={(e) => e.stopPropagation()}
          >
            {parent.key}
          </Anchor>
          <Text size="sm" truncate>
            {parent.summary}
          </Text>
        </Group>
      );
    }
    case F.security:
      return (
        <Badge color="red" variant="light" radius="sm" tt="none" leftSection={<IconLock size={12} />}>
          {issue?.securityLevel?.name ?? String(value)}
        </Badge>
      );
    case F.sprint:
      return (
        <Group gap={6}>
          <Text size="sm">{issue?.sprint?.name ?? String(value)}</Text>
          {issue?.sprint?.state === 'active' && (
            <Badge size="xs" variant="light" color="blue" tt="none">
              {t('agile.active')}
            </Badge>
          )}
        </Group>
      );
    case F.components:
      return (
        <Badges
          items={ids(value).map((id) => {
            const name =
              ctx.components.find((c) => c.id === id)?.name ??
              issue?.components.find((c) => c.id === id)?.name;
            return {
              key: id,
              label: name ?? `#${id}`,
              to: name ? search(`project = ${ctx.projectKey} AND component = ${quote(name)}`) : undefined,
            };
          })}
        />
      );
    case F.labels:
      return (
        <Badges
          items={(value as string[]).map((l) => ({ key: l, label: l, to: search(`labels = ${quote(l)}`) }))}
        />
      );
  }
  switch (def.type) {
    case 'user': {
      const u = ctx.users.get(Number(value));
      return u ? <UserBadge user={u} size="sm" /> : <Text size="sm">#{String(value)}</Text>;
    }
    case 'multiuser':
      return (
        <Stack gap={4}>
          {ids(value).map((id) => {
            const u = ctx.users.get(id);
            return u ? (
              <UserBadge key={id} user={u} size="sm" />
            ) : (
              <Text key={id} size="sm">
                #{id}
              </Text>
            );
          })}
        </Stack>
      );
    case 'group':
      return <Text size="sm">{ctx.groups.get(Number(value)) ?? `#${String(value)}`}</Text>;
    case 'version':
    case 'multiversion':
      return (
        <Badges
          items={ids(value).map((id) => ({
            key: id,
            label: versionName(id),
            to: `/projects/${ctx.projectKey}/releases/${id}`,
          }))}
        />
      );
    case 'labels':
      return <Badges items={(value as string[]).map((l) => ({ key: l, label: l }))} />;
    case 'select':
    case 'radio':
      return <Text size="sm">{optionLabel(def.options, value)}</Text>;
    case 'multiselect':
    case 'checkboxes':
      return <Badges items={ids(value).map((id) => ({ key: id, label: optionLabel(def.options, id) }))} />;
    case 'cascading': {
      const v = value as { parent: number; child?: number };
      return (
        <Text size="sm">
          {optionLabel(def.options, v.parent)}
          {v.child ? ` › ${optionLabel(def.options, v.child)}` : ''}
        </Text>
      );
    }
    case 'date': {
      const overdue = def.key === F.dueDate && issue?.status.category !== 'done' && String(value) < today();
      return (
        <Text size="sm" c={overdue ? 'red' : undefined} fw={overdue ? 600 : undefined}>
          {formatDay(String(value))}
          {overdue ? ` · ${t('issues.overdue')}` : ''}
        </Text>
      );
    }
    case 'datetime':
      return <Text size="sm">{formatDateTime(String(value))}</Text>;
    case 'number':
      return <Text size="sm">{formatNumber(Number(value))}</Text>;
    case 'duration':
      return (
        <Text size="sm">{formatDuration(Number(value), ctx.units.hoursPerDay, ctx.units.daysPerWeek)}</Text>
      );
    case 'url':
      return (
        <Anchor
          href={String(value)}
          target="_blank"
          rel="noopener noreferrer nofollow"
          size="sm"
          style={{ overflowWrap: 'anywhere' }}
          onClick={(e) => e.stopPropagation()}
        >
          {String(value)}
        </Anchor>
      );
    case 'textarea':
      return <DocView doc={value as Doc} />;
    default:
      return (
        <Text size="sm" style={{ overflowWrap: 'anywhere', whiteSpace: 'pre-wrap' }}>
          {String(value)}
        </Text>
      );
  }
}

/** Searches the issues that can be the parent of an issue of the given level. */
function IssuePicker({
  projectKey,
  level,
  value,
  onChange,
  current,
  ...props
}: {
  projectKey: string;
  level: number;
  value: string | null;
  onChange: (key: string | null) => void;
  current?: Schemas['IssueRef'];
  label?: string;
  required?: boolean;
  error?: ReactNode;
  autoFocus?: boolean;
  defaultDropdownOpened?: boolean;
  description?: string;
  'aria-label'?: string;
}) {
  const config = useIssueConfig();
  const [searchValue, setSearch] = useState('');
  const [debounced] = useDebouncedValue(searchValue.trim(), 250);
  // Sub-tasks belong to standard issues, standard issues to epics.
  const parentLevel = level === -1 ? 0 : 1;
  const types = (config.data?.issueTypes ?? []).filter((ty) => ty.level === parentLevel);
  const asKey = /^[A-Za-z][A-Za-z0-9_]*-\d+$/.test(debounced);
  const kql =
    `project = ${quote(projectKey)} AND issuetype in (${types.map((ty) => quote(ty.name)).join(', ')})` +
    (asKey
      ? ` AND key = ${debounced.toUpperCase()}`
      : debounced
        ? ` AND summary ~ ${quote(`${debounced}*`)}`
        : '') +
    ' ORDER BY updated DESC';
  const results = useQuery({
    queryKey: ['issue', 'search', 'parent', kql],
    queryFn: () => unwrap(api.GET('/api/v1/search', { params: { query: { q: kql, limit: 20 } } })),
    enabled: types.length > 0 && !!projectKey,
  });
  const data: ComboboxItem[] = (results.data?.issues ?? []).map((i) => ({
    value: i.key,
    label: `${i.key} ${i.summary}`,
  }));
  if (value && !data.some((d) => d.value === value)) {
    data.unshift({
      value,
      label: current && current.key === value ? `${current.key} ${current.summary}` : value,
    });
  }
  return (
    <Select
      searchable
      clearable
      data={data}
      value={value}
      onChange={onChange}
      searchValue={searchValue}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}

/** Chooses one of the users who may be assigned issues of a project. */
function AssigneePicker({
  projectKey,
  value,
  onChange,
  known,
  ...props
}: {
  projectKey: string;
  value: number | null;
  onChange: (id: number | null) => void;
  known?: UserRef;
  label?: string;
  placeholder?: string;
  autoFocus?: boolean;
  defaultDropdownOpened?: boolean;
  'aria-label'?: string;
}) {
  const [searchValue, setSearch] = useState('');
  const [debounced] = useDebouncedValue(searchValue, 200);
  const users = useQuery({
    queryKey: ['assignable', projectKey, debounced],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/projects/{key}/assignable', {
          params: { path: { key: projectKey }, query: { query: debounced } },
        }),
      ),
    enabled: !!projectKey,
  });
  const data = (users.data ?? []).map((u) => ({ value: String(u.id), label: u.displayName }));
  if (known && !data.some((d) => d.value === String(known.id))) {
    data.unshift({ value: String(known.id), label: known.displayName });
  }
  return (
    <Select
      searchable
      clearable
      data={data}
      value={value == null ? null : String(value)}
      onChange={(v) => onChange(v ? Number(v) : null)}
      searchValue={searchValue}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}

/** Chooses one of the open sprints of the project. */
function SprintPicker({
  projectKey,
  value,
  onChange,
  current,
  ...props
}: {
  projectKey: string;
  value: number | null;
  onChange: (id: number | null) => void;
  current?: Schemas['SprintRef'];
  label?: string;
  placeholder?: string;
  autoFocus?: boolean;
  defaultDropdownOpened?: boolean;
  'aria-label'?: string;
}) {
  const sprints = useQuery({
    queryKey: ['board', 'sprints', projectKey],
    queryFn: () =>
      unwrap(api.GET('/api/v1/sprints', { params: { query: { project: projectKey, open: true } } })),
    enabled: !!projectKey,
  });
  const data = (sprints.data ?? []).map((s) => ({ value: String(s.id), label: s.name }));
  if (current && !data.some((d) => d.value === String(current.id))) {
    data.unshift({ value: String(current.id), label: current.name });
  }
  return (
    <Select
      clearable
      data={data}
      value={value == null ? null : String(value)}
      onChange={(v) => onChange(v ? Number(v) : null)}
      {...props}
    />
  );
}

/** The input of a field. inline opens it ready for typing or choosing. */
export function FieldInput({
  def,
  value,
  onChange,
  ctx,
  label,
  required,
  error,
  inline = false,
}: {
  def: FieldInfo;
  value: unknown;
  onChange: (v: unknown) => void;
  ctx: FieldContext;
  label?: string;
  required?: boolean;
  error?: ReactNode;
  inline?: boolean;
}) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  const security = useSecurityLevels(ctx.projectKey, def.key === F.security);
  const common = {
    label,
    required,
    error,
    size: inline ? ('xs' as const) : ('sm' as const),
    'aria-label': label ? undefined : def.name,
    autoFocus: inline,
    description: !inline && def.description ? def.description : undefined,
  };
  const choice = { ...common, defaultDropdownOpened: inline, comboboxProps: { withinPortal: true } };
  const ids = Array.isArray(value) ? (value as number[]).map(String) : [];
  const single = value == null ? null : String(value);
  const toId = (v: string | null) => (v ? Number(v) : null);
  const options = (keep: unknown[]) =>
    (def.options ?? [])
      .filter((o) => !o.parentId && (!o.disabled || keep.includes(o.id)))
      .map((o) => ({ value: String(o.id), label: o.value }));

  switch (def.key) {
    case F.priority: {
      const priorities = config.data?.priorities ?? [];
      return (
        <Select
          {...choice}
          allowDeselect={false}
          data={priorities.map((p) => ({ value: String(p.id), label: p.name }))}
          value={single}
          onChange={(v) => onChange(toId(v))}
          leftSection={<PriorityIcon priority={priorities.find((p) => p.id === Number(value))} />}
          renderOption={({ option }) => (
            <PriorityIcon priority={priorities.find((p) => String(p.id) === option.value)} withName />
          )}
        />
      );
    }
    case F.assignee:
      return (
        <AssigneePicker
          {...choice}
          projectKey={ctx.projectKey}
          value={value == null ? null : Number(value)}
          onChange={onChange}
          known={ctx.users.get(Number(value))}
          placeholder={t('issues.unassigned')}
        />
      );
    case F.components: {
      const list = ctx.components.filter((c) => !c.archived || ids.includes(String(c.id)));
      return (
        <MultiSelect
          {...choice}
          searchable
          data={list.map((c) => ({ value: String(c.id), label: c.name }))}
          value={ids}
          onChange={(v) => onChange(v.map(Number))}
          nothingFoundMessage={t('issues.noComponents')}
        />
      );
    }
    case F.security: {
      const levels = security.data?.levels ?? [];
      const data = levels.map((l) => ({ value: String(l.id), label: l.name }));
      if (single && !data.some((d) => d.value === single)) {
        data.unshift({ value: single, label: ctx.issue?.securityLevel?.name ?? single });
      }
      return (
        <Select
          {...choice}
          clearable
          data={data}
          value={single}
          onChange={(v) => onChange(toId(v))}
          placeholder={t('issues.noSecurityLevel')}
          leftSection={<IconLock size={14} />}
        />
      );
    }
    case F.sprint:
      return (
        <SprintPicker
          {...choice}
          projectKey={ctx.projectKey}
          value={value == null ? null : Number(value)}
          onChange={onChange}
          current={ctx.issue?.sprint ?? undefined}
          placeholder={t('agile.backlog')}
        />
      );
    case F.parent:
      return (
        <IssuePicker
          {...choice}
          projectKey={ctx.projectKey}
          level={ctx.typeLevel}
          value={value == null ? null : String(value)}
          onChange={onChange}
          current={ctx.issue?.parent ?? undefined}
        />
      );
  }
  switch (def.type) {
    case 'user': {
      const known = ctx.users.get(Number(value));
      return (
        <UserPicker
          {...choice}
          clearable={def.key !== F.reporter}
          value={value == null ? null : Number(value)}
          onChange={onChange}
          known={known ? { id: known.id, label: known.displayName } : undefined}
        />
      );
    }
    case 'multiuser':
      return (
        <UsersPicker
          {...choice}
          value={ids.map(Number)}
          onChange={onChange}
          known={ids.flatMap((id) => {
            const u = ctx.users.get(Number(id));
            return u ? [{ id: u.id, label: u.displayName }] : [];
          })}
        />
      );
    case 'group':
      return (
        <GroupPicker
          {...choice}
          value={value == null ? null : Number(value)}
          onChange={onChange}
          known={single ? { id: Number(single), label: ctx.groups.get(Number(single)) ?? single } : undefined}
        />
      );
    case 'labels':
      return (
        <TagsInput
          {...common}
          value={Array.isArray(value) ? (value as string[]) : []}
          onChange={onChange}
          splitChars={[',', ' ']}
          placeholder={t('issues.addLabels')}
          clearable
        />
      );
    case 'version':
    case 'multiversion': {
      const keep = def.type === 'version' ? (single ? [single] : []) : ids;
      const unreleased = ctx.versions.filter((v) => !v.archived && !v.released);
      const released = ctx.versions.filter((v) => !v.archived && v.released);
      const archived = ctx.versions.filter((v) => v.archived && keep.includes(String(v.id)));
      const item = (v: Schemas['Version']) => ({ value: String(v.id), label: v.name });
      const data = [
        { group: t('issues.unreleased'), items: unreleased.map(item) },
        { group: t('issues.released'), items: [...released, ...archived].map(item) },
      ].filter((g) => g.items.length > 0);
      return def.type === 'version' ? (
        <Select
          {...choice}
          clearable
          searchable
          data={data}
          value={single}
          onChange={(v) => onChange(toId(v))}
          nothingFoundMessage={t('issues.noVersions')}
        />
      ) : (
        <MultiSelect
          {...choice}
          searchable
          data={data}
          value={ids}
          onChange={(v) => onChange(v.map(Number))}
          nothingFoundMessage={t('issues.noVersions')}
        />
      );
    }
    case 'select':
    case 'radio':
      return (
        <Select
          {...choice}
          clearable
          searchable={(def.options ?? []).length > 8}
          data={options(value == null ? [] : [Number(value)])}
          value={single}
          onChange={(v) => onChange(toId(v))}
        />
      );
    case 'multiselect':
    case 'checkboxes':
      return (
        <MultiSelect
          {...choice}
          searchable={(def.options ?? []).length > 8}
          data={options(ids.map(Number))}
          value={ids}
          onChange={(v) => onChange(v.length ? v.map(Number) : null)}
        />
      );
    case 'cascading':
      return <CascadingInput def={def} value={value} onChange={onChange} common={choice} />;
    case 'date':
      return (
        <DateInput
          {...common}
          clearable
          valueFormat="L"
          value={value ? String(value) : null}
          onChange={(v) => onChange(v ? String(v).slice(0, 10) : null)}
          popoverProps={{ withinPortal: true }}
          defaultDate={value ? String(value) : undefined}
        />
      );
    case 'datetime':
      return (
        <DateTimePicker
          {...common}
          clearable
          valueFormat="L LT"
          value={value ? dayjs(String(value)).format('YYYY-MM-DD HH:mm:ss') : null}
          onChange={(v) => onChange(v ? dayjs(v).toISOString() : null)}
          popoverProps={{ withinPortal: true }}
        />
      );
    case 'number':
      return (
        <NumberInput
          {...common}
          value={typeof value === 'number' ? value : ''}
          onChange={(v) => onChange(v === '' ? null : Number(v))}
          decimalScale={6}
        />
      );
    case 'duration':
      return (
        <TextInput
          {...common}
          placeholder={t('issues.durationPlaceholder')}
          defaultValue={
            typeof value === 'number'
              ? formatDuration(value, ctx.units.hoursPerDay, ctx.units.daysPerWeek)
              : ''
          }
          onChange={(e) => onChange(e.currentTarget.value.trim() || null)}
        />
      );
    case 'textarea':
      return (
        <Stack gap={4}>
          {label && (
            <Text size="sm" fw={550}>
              {label}
            </Text>
          )}
          <Editor
            value={(value as Doc) ?? null}
            onChange={(d) => onChange(isEmptyDoc(d) ? null : d)}
            minHeight={100}
          />
        </Stack>
      );
    default:
      return (
        <TextInput
          {...common}
          type={def.type === 'url' ? 'url' : 'text'}
          placeholder={def.type === 'url' ? 'https://' : undefined}
          value={typeof value === 'string' ? value : ''}
          onChange={(e) => onChange(e.currentTarget.value || null)}
          maxLength={4000}
        />
      );
  }
}

function CascadingInput({
  def,
  value,
  onChange,
  common,
}: {
  def: FieldInfo;
  value: unknown;
  onChange: (v: unknown) => void;
  common: Record<string, unknown>;
}) {
  const v = (value as { parent?: number; child?: number } | null) ?? {};
  const all = def.options ?? [];
  const parents = all.filter((o) => !o.parentId && (!o.disabled || o.id === v.parent));
  const children = all.filter(
    (o) => v.parent && o.parentId === v.parent && (!o.disabled || o.id === v.child),
  );
  return (
    <Group gap="xs" grow align="flex-start">
      <Select
        {...common}
        clearable
        data={parents.map((o) => ({ value: String(o.id), label: o.value }))}
        value={v.parent ? String(v.parent) : null}
        onChange={(p) => onChange(p ? { parent: Number(p) } : null)}
      />
      {children.length > 0 && (
        <Select
          {...common}
          label={common.label ? ' ' : undefined}
          aria-label={`${def.name} ›`}
          autoFocus={false}
          defaultDropdownOpened={false}
          clearable
          data={children.map((o) => ({ value: String(o.id), label: o.value }))}
          value={v.child ? String(v.child) : null}
          onChange={(c) => onChange(c ? { parent: v.parent, child: Number(c) } : { parent: v.parent })}
        />
      )}
    </Group>
  );
}

/**
 * The inline editor of a field in the details panel: a choice is saved at once, text and lists when focus leaves
 * the editor or Enter is pressed; Escape cancels.
 */
export function InlineFieldEditor({
  def,
  value,
  ctx,
  save,
  done,
}: {
  def: FieldInfo;
  value: unknown;
  ctx: FieldContext;
  save: (v: unknown) => void;
  done: DoneEditing;
}) {
  const [draft, setDraft] = useState<unknown>(value);
  // Inputs such as dates report their value as they lose focus, just before the editor learns of it: the editor
  // reads the latest value from the ref, not from the state of the last render.
  const latest = useRef<unknown>(value);
  const finished = useRef(false);
  const commit = (v: unknown, refocus: boolean) => {
    if (finished.current) return;
    finished.current = true;
    if (!sameValue(v, value)) save(v);
    done(refocus);
  };
  const immediate = choiceTypes.has(def.type);
  return (
    <div
      onBlur={(e) => {
        const next = e.relatedTarget as HTMLElement | null;
        if (next && (e.currentTarget.contains(next) || next.closest('[data-portal]'))) return;
        commit(latest.current, false);
      }}
      onKeyDown={(e) => {
        // As DetailItem, which closes the editor: an Escape that closed a dropdown is not one for the editor.
        if (e.key === 'Escape' && !e.defaultPrevented) finished.current = true;
        if (e.key === 'Enter' && textTypes.has(def.type)) {
          e.preventDefault();
          commit(latest.current, true);
        }
      }}
    >
      <FieldInput
        def={def}
        value={draft}
        ctx={ctx}
        inline
        onChange={(v) => {
          latest.current = v;
          setDraft(v);
          if (immediate) commit(v, true);
        }}
      />
    </div>
  );
}
