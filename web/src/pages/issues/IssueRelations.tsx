// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Box,
  Button,
  Combobox,
  Group,
  Modal,
  Progress,
  Select,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
  Title,
  Tooltip,
  useCombobox,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { modals } from '@mantine/modals';
import {
  IconFile,
  IconFileTypePdf,
  IconFileZip,
  IconPhoto,
  IconPlus,
  IconTrash,
  IconX,
} from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatBytes, formatRelative } from '../../app/format';
import { DocView } from '../../components/editor/DocView';
import { Editor, isEmptyDoc, type Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError } from '../../components/notify';
import { findIssuesQuery, looksLikeKey } from './findIssues';
import { useUpdateIssue } from './issueHooks';
import { PriorityIcon, StatusBadge, TypeIcon, useIssueConfig, useProject, type Issue } from './shared';

/** A section of the issue's main column. */
export function Section({
  title,
  actions,
  children,
}: {
  title: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <Box component="section">
      <Group justify="space-between" mb="xs" wrap="nowrap" gap="xs">
        <Title order={5}>{title}</Title>
        {actions}
      </Group>
      {children}
    </Box>
  );
}

/** A rich text field — the description or a long text field — read as a document and edited in place. */
export function DocField({
  issue,
  field,
  label,
  value,
  editable,
  placeholder,
}: {
  issue: Issue;
  field: string;
  /** The field's name, for the accessible name of the editable text. */
  label: string;
  value: Doc | null | undefined;
  editable: boolean;
  placeholder: string;
}) {
  const { t } = useTranslation();
  const update = useUpdateIssue(issue);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<Doc | null>(null);
  const save = () =>
    update.mutate(
      { [field]: draft && !isEmptyDoc(draft) ? draft : null },
      { onSuccess: () => setEditing(false) },
    );
  if (editing) {
    return (
      <Stack gap="xs">
        <Editor value={draft} onChange={setDraft} autofocus minHeight={160} onSubmit={save} />
        <Group gap="xs">
          <Button size="xs" loading={update.isPending} onClick={save}>
            {t('common.save')}
          </Button>
          <Button size="xs" variant="subtle" onClick={() => setEditing(false)}>
            {t('common.cancel')}
          </Button>
        </Group>
      </Stack>
    );
  }
  const empty = !value || isEmptyDoc(value);
  const open = () => {
    setDraft(value ?? null);
    setEditing(true);
  };
  if (!editable)
    return empty ? (
      <Text c="dimmed" size="sm">
        —
      </Text>
    ) : (
      <DocView doc={value} />
    );
  return (
    <div
      className="kogge-editable-block"
      role="button"
      tabIndex={0}
      aria-label={t('issues.editField', { field: label })}
      onClick={(e) => {
        // Links in the text open, they do not start editing.
        if (!(e.target as HTMLElement).closest('a, button, input')) open();
      }}
      onKeyDown={(e) => {
        if (e.key === 'Enter' && e.target === e.currentTarget) {
          e.preventDefault();
          open();
        }
      }}
    >
      {empty ? (
        <Text c="dimmed" size="sm">
          {placeholder}
        </Text>
      ) : (
        <DocView doc={value} />
      )}
    </div>
  );
}

function fileIcon(contentType: string) {
  if (contentType.startsWith('image/')) return IconPhoto;
  if (contentType === 'application/pdf') return IconFileTypePdf;
  if (/zip|compressed|tar|gzip/.test(contentType)) return IconFileZip;
  return IconFile;
}

/** Files of the issue: pictures with a preview, other files with their type. */
export function Attachments({ issue, onAttach }: { issue: Issue; onAttach?: () => void }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/issue-attachments/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['issue', issue.key] }),
    onError: notifyError,
  });
  if (issue.attachments.length === 0) return null;
  return (
    <Section
      title={`${t('issues.attachments')} (${issue.attachments.length})`}
      actions={
        onAttach && (
          <Button size="compact-xs" variant="subtle" leftSection={<IconPlus size={12} />} onClick={onAttach}>
            {t('issues.attach')}
          </Button>
        )
      }
    >
      <SimpleGrid cols={{ base: 2, xs: 3, md: 4 }} spacing="xs">
        {issue.attachments.map((a) => {
          const Icon = fileIcon(a.contentType);
          const preview = a.contentType.startsWith('image/') && a.contentType !== 'image/svg+xml';
          return (
            <Box key={a.id} className="kogge-attachment">
              <a href={a.url} target="_blank" rel="noopener noreferrer" className="kogge-attachment-preview">
                {preview ? <img src={a.url} alt="" loading="lazy" /> : <Icon size={32} stroke={1.4} />}
              </a>
              <Box px={8} py={6}>
                <Anchor
                  href={a.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  size="xs"
                  fw={550}
                  lineClamp={1}
                >
                  {a.filename}
                </Anchor>
                <Text size="xs" c="dimmed" lineClamp={1}>
                  {formatBytes(a.size)} · {formatRelative(a.createdAt)}
                </Text>
              </Box>
              {a.canDelete && (
                <ActionIcon
                  className="kogge-attachment-delete"
                  size="sm"
                  variant="default"
                  aria-label={t('issues.deleteAttachment', { name: a.filename })}
                  onClick={() =>
                    modals.openConfirmModal({
                      title: t('issues.deleteAttachment', { name: a.filename }),
                      children: <Text size="sm">{t('issues.deleteAttachmentConfirm')}</Text>,
                      labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                      confirmProps: { color: 'red' },
                      onConfirm: () => del.mutate(a.id),
                    })
                  }
                >
                  <IconTrash size={14} />
                </ActionIcon>
              )}
            </Box>
          );
        })}
      </SimpleGrid>
    </Section>
  );
}

/** The action of an issue in a list, shown while the row is hovered or focused. */
function RowAction({
  label,
  onClick,
  children,
}: {
  label: string;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <ActionIcon
      size="sm"
      variant="subtle"
      color="gray"
      className="kogge-issue-row-action"
      onClick={onClick}
      aria-label={label}
    >
      {children}
    </ActionIcon>
  );
}

/** An issue in a list of related issues. */
function IssueRow({
  issue,
  label,
  action,
}: {
  issue: Schemas['IssueRef'];
  label?: string;
  action?: ReactNode;
}) {
  return (
    <Group gap="xs" wrap="nowrap" className="kogge-issue-row">
      <TypeIcon type={issue.type} size={16} />
      <Anchor
        component={Link}
        to={`/browse/${issue.key}`}
        size="sm"
        fw={600}
        style={{ whiteSpace: 'nowrap' }}
      >
        {issue.key}
      </Anchor>
      <Text
        size="sm"
        truncate
        style={{ flex: 1 }}
        td={issue.status.category === 'done' ? 'line-through' : undefined}
        c={issue.status.category === 'done' ? 'dimmed' : undefined}
      >
        {issue.summary}
      </Text>
      {label && (
        <Text size="xs" c="dimmed" visibleFrom="xs">
          {label}
        </Text>
      )}
      <PriorityIcon priority={issue.priority} />
      {issue.assignee && (
        <Tooltip label={issue.assignee.displayName} withinPortal>
          <Text size="xs" c="dimmed" visibleFrom="sm" maw={110} truncate>
            {issue.assignee.displayName}
          </Text>
        </Tooltip>
      )}
      <StatusBadge status={issue.status} />
      {action}
    </Group>
  );
}

/** The sub-tasks of an issue with their progress, and a field to add one. */
export function Subtasks({ issue, focus }: { issue: Issue; focus?: number }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const config = useIssueConfig();
  const project = useProject(issue.project.key);
  const subtaskType =
    (project.data?.issueTypes ?? []).find((ty) => ty.level === -1) ??
    config.data?.issueTypes.find((ty) => ty.level === -1);
  const del = useMutation({
    mutationFn: (key: string) => unwrap(api.DELETE('/api/v1/issues/{key}', { params: { path: { key } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['issue', issue.key] }),
    onError: notifyError,
  });
  const canAdd = issue.type.level === 0 && !!subtaskType && issue.permissions.edit;
  if (issue.subtasks.length === 0 && !canAdd) return null;
  if (issue.subtasks.length === 0 && !focus) return null;
  const done = issue.subtasks.filter((s) => s.status.category === 'done').length;
  const total = issue.subtasks.length;
  const confirmDelete = (key: string) =>
    modals.openConfirmModal({
      title: t('issues.deleteSubtask', { key }),
      children: <Text size="sm">{t('issues.deleteSubtaskConfirm', { key })}</Text>,
      labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
      confirmProps: { color: 'red' },
      onConfirm: () => del.mutate(key),
    });
  return (
    <Section
      title={t('issues.subtasks')}
      actions={
        total > 0 && (
          <Group gap="xs" wrap="nowrap">
            <Progress value={(done / total) * 100} w={90} size="sm" color="teal" aria-hidden />
            <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
              {t('issues.subtasksDone', { done, total })}
            </Text>
          </Group>
        )
      }
    >
      <Stack gap={2}>
        {issue.subtasks.map((s) => (
          <IssueRow
            key={s.id}
            issue={s}
            action={
              issue.permissions.delete && (
                <RowAction
                  label={t('issues.deleteSubtask', { key: s.key })}
                  onClick={() => confirmDelete(s.key)}
                >
                  <IconTrash size={14} />
                </RowAction>
              )
            }
          />
        ))}
        {canAdd && <AddSubtask issue={issue} typeId={subtaskType.id} focus={focus} />}
      </Stack>
    </Section>
  );
}

const NEW_SUBTASK = '\u0000new';

/**
 * The field under the sub-tasks: Enter creates a sub-task with the title typed; a key or words find issues of the
 * project, and the one chosen becomes a sub-task.
 */
function AddSubtask({ issue, typeId, focus }: { issue: Issue; typeId: number; focus?: number }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [text, setText] = useState('');
  const [debounced] = useDebouncedValue(text.trim(), 250);
  const combobox = useCombobox({ onDropdownClose: () => combobox.resetSelectedOption() });
  const project = issue.project.key;
  const kql = findIssuesQuery(debounced, { project, exclude: issue.key, inProject: true });
  const results = useQuery({
    queryKey: ['issue', 'search', 'subtask', kql],
    queryFn: () => unwrap(api.GET('/api/v1/search', { params: { query: { q: kql, limit: 10 } } })),
    enabled: debounced !== '',
  });
  const summary = text.trim();
  const keyish = looksLikeKey(summary, project);
  const current = summary !== '' && debounced === summary && !results.isFetching;
  const taken = new Set([issue.key, ...issue.subtasks.map((s) => s.key)]);
  const found = current
    ? (results.data?.issues ?? []).filter(
        (i) => i.project.key === project && i.type.level !== 1 && !taken.has(i.key),
      )
    : [];
  const finish = (adopted?: string) => {
    setText('');
    combobox.closeDropdown();
    void client.invalidateQueries({ queryKey: ['issue', issue.key] });
    if (adopted) void client.invalidateQueries({ queryKey: ['issue', adopted] });
  };
  const create = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/api/v1/issues', { body: { project, typeId, summary, parent: issue.key } })),
    onSuccess: () => finish(),
    onError: notifyError,
  });
  const adopt = useMutation({
    mutationFn: (key: string) =>
      unwrap(
        api.POST('/api/v1/issues/{key}/subtasks', {
          params: { path: { key: issue.key } },
          body: { issue: key },
        }),
      ),
    onSuccess: (_, key) => finish(key),
    onError: notifyError,
  });
  const pending = create.isPending || adopt.isPending;
  const choose = (value: string) => {
    if (pending) return;
    if (value === NEW_SUBTASK) create.mutate();
    else adopt.mutate(value);
  };
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        // Enter without a chosen option: a title becomes a new sub-task, a key the issue it names.
        if (!keyish && summary) choose(NEW_SUBTASK);
        else if (/\d$/.test(summary) && found.length === 1) choose(found[0].key);
      }}
    >
      <Combobox store={combobox} onOptionSubmit={choose}>
        <Combobox.Target>
          <TextInput
            key={focus}
            mt={4}
            size="xs"
            placeholder={t('issues.subtaskPlaceholder')}
            value={text}
            onChange={(e) => {
              setText(e.currentTarget.value);
              combobox.openDropdown();
              combobox.resetSelectedOption();
            }}
            onFocus={() => combobox.openDropdown()}
            onBlur={() => combobox.closeDropdown()}
            autoFocus={!!focus}
            leftSection={<IconPlus size={14} />}
            rightSection={pending ? <Text size="xs">…</Text> : undefined}
            aria-label={t('issues.addSubtask')}
          />
        </Combobox.Target>
        <Combobox.Dropdown hidden={!summary}>
          <Combobox.Options>
            {!keyish && (
              <Combobox.Option value={NEW_SUBTASK}>
                <Group gap={6} wrap="nowrap">
                  <IconPlus size={14} />
                  <Text size="sm" truncate>
                    {t('issues.newSubtask', { summary })}
                  </Text>
                </Group>
              </Combobox.Option>
            )}
            {found.length > 0 && (
              <Combobox.Group label={t('issues.existingIssues')}>
                {found.map((i) => (
                  <Combobox.Option key={i.key} value={i.key}>
                    <Group gap={6} wrap="nowrap">
                      <TypeIcon type={i.type} size={14} />
                      <Text size="sm" fw={600} style={{ whiteSpace: 'nowrap' }}>
                        {i.key}
                      </Text>
                      <Text size="sm" truncate>
                        {i.summary}
                      </Text>
                    </Group>
                  </Combobox.Option>
                ))}
              </Combobox.Group>
            )}
            {keyish && current && found.length === 0 && (
              <Combobox.Empty>{t('issues.noIssueFound')}</Combobox.Empty>
            )}
          </Combobox.Options>
        </Combobox.Dropdown>
      </Combobox>
    </form>
  );
}

/** Linked issues grouped by the kind of link. */
export function Links({ issue, onAdd }: { issue: Issue; onAdd?: () => void }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/api/v1/issue-links/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['issue', issue.key] }),
    onError: notifyError,
  });
  if (issue.links.length === 0) return null;
  const groups = new Map<string, typeof issue.links>();
  for (const l of issue.links) groups.set(l.label, [...(groups.get(l.label) ?? []), l]);
  return (
    <Section
      title={t('issues.links')}
      actions={
        onAdd && (
          <Button size="compact-xs" variant="subtle" leftSection={<IconPlus size={12} />} onClick={onAdd}>
            {t('issues.addLink')}
          </Button>
        )
      }
    >
      <Stack gap="sm">
        {[...groups.entries()].map(([label, links]) => (
          <Box key={label}>
            <Text size="xs" c="dimmed" fw={600} tt="uppercase" mb={2}>
              {label}
            </Text>
            <Stack gap={2}>
              {links.map((l) => (
                <IssueRow
                  key={l.id}
                  issue={l.issue}
                  action={
                    issue.permissions.link && (
                      <RowAction
                        label={t('issues.removeLink', { key: l.issue.key })}
                        onClick={() => remove.mutate(l.id)}
                      >
                        <IconX size={14} />
                      </RowAction>
                    )
                  }
                />
              ))}
            </Stack>
          </Box>
        ))}
      </Stack>
    </Section>
  );
}

/** Links the issue to another one, found by key or summary. */
export function LinkModal({
  issue,
  opened,
  onClose,
}: {
  issue: Issue;
  opened: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const config = useIssueConfig();
  const [type, setType] = useState<string | null>(null);
  const [target, setTarget] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search.trim(), 250);
  const kql = findIssuesQuery(debounced, { project: issue.project.key, exclude: issue.key });
  const results = useQuery({
    queryKey: ['issue', 'search', 'link', kql],
    queryFn: () => unwrap(api.GET('/api/v1/search', { params: { query: { q: kql, limit: 20 } } })),
    enabled: opened,
  });
  const options = (config.data?.linkTypes ?? []).flatMap((l) => [
    { value: `${l.id}:outward`, label: l.outward },
    ...(l.inward !== l.outward ? [{ value: `${l.id}:inward`, label: l.inward }] : []),
  ]);
  const chosen = type ?? options[0]?.value ?? null;
  const close = () => {
    setTarget(null);
    setSearch('');
    onClose();
  };
  const link = useMutation({
    mutationFn: () => {
      const [id, direction] = (chosen ?? '').split(':');
      return unwrap(
        api.POST('/api/v1/issues/{key}/links', {
          params: { path: { key: issue.key } },
          body: { typeId: Number(id), direction: direction as 'outward' | 'inward', issue: target! },
        }),
      );
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['issue', issue.key] });
      close();
    },
  });
  return (
    <Modal opened={opened} onClose={close} title={t('issues.addLink')}>
      <Stack>
        <Group gap="xs" wrap="nowrap">
          <Text size="sm" fw={600} style={{ whiteSpace: 'nowrap' }}>
            {issue.key}
          </Text>
          <Select
            style={{ flex: 1 }}
            data={options}
            value={chosen}
            onChange={setType}
            aria-label={t('issues.linkType')}
            allowDeselect={false}
          />
        </Group>
        <Select
          label={t('issues.issue')}
          placeholder={t('issues.findIssue')}
          searchable
          clearable
          data={(results.data?.issues ?? [])
            .filter((i) => i.key !== issue.key)
            .map((i) => ({ value: i.key, label: `${i.key} ${i.summary}` }))}
          value={target}
          onChange={setTarget}
          searchValue={search}
          onSearchChange={setSearch}
          filter={({ options: o }) => o}
          nothingFoundMessage="—"
          data-autofocus
        />
        <ErrorAlert error={link.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={close}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => link.mutate()} loading={link.isPending} disabled={!target || !chosen}>
            {t('issues.addLink')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** Wiki pages that mention the issue. */
export function Mentions({ issueKey }: { issueKey: string }) {
  const { t } = useTranslation();
  const pages = useQuery({
    queryKey: ['page', 'mentions', issueKey],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/mentions', { params: { query: { issue: issueKey } } })),
  });
  if (!pages.data?.length) return null;
  return (
    <Section title={t('issues.mentionedIn')}>
      <Stack gap={4}>
        {pages.data.map((p) => (
          <Group key={p.id} gap="xs" wrap="nowrap">
            <Anchor component={Link} to={p.url} size="sm">
              {p.title}
            </Anchor>
            <Text size="xs" c="dimmed">
              {p.space.name} · {formatRelative(p.updated)}
            </Text>
          </Group>
        ))}
      </Stack>
    </Section>
  );
}
