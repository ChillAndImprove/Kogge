// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Badge,
  Box,
  Breadcrumbs,
  Button,
  CopyButton,
  FileButton,
  Group,
  Menu,
  Stack,
  Text,
  Textarea,
  Title,
  Tooltip,
  UnstyledButton,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import {
  IconCheck,
  IconClockPlus,
  IconDots,
  IconEye,
  IconEyeOff,
  IconLink,
  IconPaperclip,
  IconTransfer,
  IconSubtask,
  IconThumbUp,
  IconTrash,
  IconUpload,
} from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { api, unwrap, upload } from '../../api/client';
import { useSession } from '../../app/session';
import { attachable } from '../../app/uploads';
import { useRecordVisit } from '../../app/visits';
import type { Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError, notifySuccess } from '../../components/notify';
import { Development } from './Development';
import { F, layoutFields, useFieldLabel, useFieldLayout } from './fields';
import { IssueActivity, LogWorkModal } from './IssueActivity';
import { IssueDetails } from './IssueDetails';
import { useIssue, useUpdateIssue } from './issueHooks';
import { Attachments, DocField, LinkModal, Links, Mentions, Section, Subtasks } from './IssueRelations';
import { StatusMenu } from './IssueStatus';
import { MoveIssueModal } from './MoveIssue';
import { TypeIcon, useIssueConfig, type Issue } from './shared';

/** The summary as the heading, edited in place. */
function Summary({ issue }: { issue: Issue }) {
  const { t } = useTranslation();
  const update = useUpdateIssue(issue);
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(issue.summary);
  if (editing) {
    const save = () => {
      setEditing(false);
      const v = value.replace(/\s+/g, ' ').trim();
      if (v && v !== issue.summary) update.mutate({ summary: v });
    };
    return (
      <Textarea
        size="lg"
        autosize
        minRows={1}
        maxLength={255}
        value={value}
        aria-label={t('issues.summary')}
        autoFocus
        onFocus={(e) => e.currentTarget.select()}
        onChange={(e) => setValue(e.currentTarget.value)}
        onBlur={save}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            save();
          }
          if (e.key === 'Escape') {
            e.stopPropagation();
            setValue(issue.summary);
            setEditing(false);
          }
        }}
        classNames={{ input: 'kogge-summary-input' }}
      />
    );
  }
  if (!issue.permissions.edit) {
    return (
      <Title order={2} className="kogge-summary">
        {issue.summary}
      </Title>
    );
  }
  // The heading stays a heading; a button in it starts editing.
  return (
    <Title order={2} className="kogge-summary">
      <UnstyledButton
        className="kogge-summary-editable"
        title={t('issues.editField', { field: t('issues.summary') })}
        onClick={() => {
          setValue(issue.summary);
          setEditing(true);
        }}
      >
        {issue.summary}
      </UnstyledButton>
    </Title>
  );
}

/**
 * The full issue view: a page of its own, or the drawer of boards and backlogs. formerKey is the key the issue was
 * opened by, when it had another one before it moved.
 */
export function IssueView({ issueKey, formerKey }: { issueKey: string; formerKey?: string }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const navigate = useNavigate();
  const client = useQueryClient();
  const config = useIssueConfig();
  const label = useFieldLabel();
  const issueQuery = useIssue(issueKey);
  const issue = issueQuery.data;
  useRecordVisit('issue', issue?.id);
  const layout = useFieldLayout(issue?.project.key, issue?.type.id);
  const [logging, setLogging] = useState(false);
  const [linking, setLinking] = useState(false);
  const [moving, setMoving] = useState(false);
  const [subtaskFocus, setSubtaskFocus] = useState(0);
  const [dragging, setDragging] = useState(false);
  const [uploading, setUploading] = useState(false);
  const fileInput = useRef<() => void>(null);
  const refresh = () => client.invalidateQueries({ queryKey: ['issue', issueKey] });
  const watch = useMutation({
    mutationFn: (on: boolean) =>
      on
        ? unwrap(api.PUT('/api/v1/issues/{key}/watchers', { params: { path: { key: issueKey } } }))
        : unwrap(api.DELETE('/api/v1/issues/{key}/watchers', { params: { path: { key: issueKey } } })),
    onSuccess: refresh,
    onError: notifyError,
  });
  const vote = useMutation({
    mutationFn: (on: boolean) =>
      on
        ? unwrap(api.PUT('/api/v1/issues/{key}/vote', { params: { path: { key: issueKey } } }))
        : unwrap(api.DELETE('/api/v1/issues/{key}/vote', { params: { path: { key: issueKey } } })),
    onSuccess: refresh,
    onError: notifyError,
  });
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE('/api/v1/issues/{key}', { params: { path: { key: issueKey } } })),
    onSuccess: () => {
      notifySuccess(t('issues.deleted', { key: issueKey }));
      navigate(`/projects/${issue?.project.key ?? ''}`);
    },
    onError: notifyError,
  });
  const attach = async (all: File[]) => {
    const files = attachable(all, me.maxAttachmentMb, notifyError);
    if (files.length === 0) return;
    const form = new FormData();
    for (const f of files) form.append('file', f);
    setUploading(true);
    try {
      await upload(`/api/v1/issues/${encodeURIComponent(issueKey)}/attachments`, form);
      void refresh();
    } catch (e) {
      notifyError(e);
    } finally {
      setUploading(false);
    }
  };

  if (issueQuery.error) return <ErrorAlert error={issueQuery.error} />;
  if (!issue) return null;
  const p = issue.permissions;
  // Long text fields of the layout read as documents in the main column.
  const docFields = layoutFields(layout.data, config.data?.fields).filter(
    ({ def, item }) => def.custom && def.type === 'textarea' && item.view,
  );
  const descriptionShown = !layout.data || layout.data.some((it) => it.field === F.description && it.view);
  const descriptionEditable = p.edit && (layout.data?.find((it) => it.field === F.description)?.edit ?? true);
  const canSubtask = issue.type.level === 0 && p.edit;
  return (
    <Box
      className="kogge-issue"
      data-dragging={dragging || undefined}
      onDragEnter={(e) => {
        if (p.attach && e.dataTransfer.types.includes('Files')) setDragging(true);
      }}
      onDragOver={(e) => {
        if (p.attach && e.dataTransfer.types.includes('Files')) e.preventDefault();
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false);
      }}
      onDrop={(e) => {
        if (!p.attach || !e.dataTransfer.files.length) return;
        e.preventDefault();
        setDragging(false);
        void attach([...e.dataTransfer.files]);
      }}
    >
      {dragging && (
        <div className="kogge-issue-drop" aria-hidden>
          <IconUpload size={28} />
          <Text fw={600}>{t('issues.dropToAttach')}</Text>
        </div>
      )}
      <Stack gap="md">
        <Group justify="space-between" align="flex-start" wrap="nowrap" gap="md">
          <Stack gap={6} style={{ flex: 1, minWidth: 0 }}>
            <Breadcrumbs separator="/" separatorMargin={6}>
              <Anchor component={Link} to="/issues/projects" size="sm" c="dimmed">
                {t('issues.projects')}
              </Anchor>
              <Anchor component={Link} to={`/projects/${issue.project.key}`} size="sm" c="dimmed">
                {issue.project.name}
              </Anchor>
              <Group gap={6} wrap="nowrap">
                <TypeIcon type={issue.type} size={16} />
                <CopyButton value={`${window.location.origin}/browse/${issue.key}`} timeout={1500}>
                  {({ copied, copy }) => (
                    <Tooltip label={copied ? t('issues.linkCopied') : t('issues.copyLink')} withinPortal>
                      <Anchor
                        component={Link}
                        to={`/browse/${issue.key}`}
                        size="sm"
                        fw={600}
                        onClick={(e) => {
                          if (e.metaKey || e.ctrlKey) return;
                          e.preventDefault();
                          copy();
                        }}
                      >
                        {issue.key}
                        {copied && <IconCheck size={12} style={{ marginLeft: 4 }} />}
                      </Anchor>
                    </Tooltip>
                  )}
                </CopyButton>
              </Group>
            </Breadcrumbs>
            <Summary key={issue.version} issue={issue} />
            {formerKey && formerKey !== issue.key && (
              <Text size="xs" c="dimmed" data-testid="former-key">
                {t('move.formerKey', { key: formerKey, current: issue.key })}
              </Text>
            )}
          </Stack>
          <Group gap={6} wrap="nowrap">
            <Tooltip label={issue.watching ? t('issues.stopWatching') : t('issues.watch')} withinPortal>
              <Button
                variant={issue.watching ? 'light' : 'default'}
                size="sm"
                px="xs"
                leftSection={issue.watching ? <IconEye size={16} /> : <IconEyeOff size={16} />}
                onClick={() => watch.mutate(!issue.watching)}
                loading={watch.isPending}
                aria-pressed={issue.watching}
                aria-label={issue.watching ? t('issues.stopWatching') : t('issues.watch')}
              >
                {p.watchers ? issue.watchers : null}
              </Button>
            </Tooltip>
            <Tooltip
              label={
                issue.reporter?.id === me.user.id
                  ? t('issues.voteOwn')
                  : t('issues.votes', { count: issue.votes })
              }
              withinPortal
            >
              <Button
                variant={issue.voted ? 'light' : 'default'}
                size="sm"
                px="xs"
                leftSection={<IconThumbUp size={16} />}
                onClick={() => vote.mutate(!issue.voted)}
                loading={vote.isPending}
                disabled={issue.reporter?.id === me.user.id}
                aria-pressed={issue.voted}
                aria-label={t('issues.vote')}
              >
                {issue.votes}
              </Button>
            </Tooltip>
            <Menu position="bottom-end" shadow="md">
              <Menu.Target>
                <ActionIcon variant="default" size={32} aria-label={t('issues.more')}>
                  <IconDots size={18} />
                </ActionIcon>
              </Menu.Target>
              <Menu.Dropdown>
                <CopyButton value={`${window.location.origin}/browse/${issue.key}`}>
                  {({ copy }) => (
                    <Menu.Item leftSection={<IconLink size={14} />} onClick={copy}>
                      {t('issues.copyLink')}
                    </Menu.Item>
                  )}
                </CopyButton>
                {p.logWork && (
                  <Menu.Item leftSection={<IconClockPlus size={14} />} onClick={() => setLogging(true)}>
                    {t('issues.logWork')}
                  </Menu.Item>
                )}
                {p.move && issue.type.level !== -1 && (
                  <Menu.Item leftSection={<IconTransfer size={14} />} onClick={() => setMoving(true)}>
                    {t('move.action')}
                  </Menu.Item>
                )}
                {p.delete && (
                  <>
                    <Menu.Divider />
                    <Menu.Item
                      color="red"
                      leftSection={<IconTrash size={14} />}
                      onClick={() =>
                        modals.openConfirmModal({
                          title: t('issues.deleteIssue'),
                          children: (
                            <Text size="sm">{t('issues.deleteIssueConfirm', { key: issue.key })}</Text>
                          ),
                          labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                          confirmProps: { color: 'red' },
                          onConfirm: () => del.mutate(),
                        })
                      }
                    >
                      {t('issues.deleteIssue')}
                    </Menu.Item>
                  </>
                )}
              </Menu.Dropdown>
            </Menu>
          </Group>
        </Group>

        <Group gap="xs">
          <StatusMenu issue={issue} />
          {issue.resolution && (
            <Badge variant="light" color="teal" radius="sm" tt="none" leftSection={<IconCheck size={12} />}>
              {issue.resolution.name}
            </Badge>
          )}
          <Group gap={4} ml="xs" className="kogge-issue-actions">
            {p.attach && (
              <FileButton onChange={(f) => void attach(f)} multiple resetRef={fileInput}>
                {(props) => (
                  <Button
                    size="xs"
                    variant="default"
                    leftSection={<IconPaperclip size={14} />}
                    loading={uploading}
                    {...props}
                  >
                    {t('issues.attach')}
                  </Button>
                )}
              </FileButton>
            )}
            {canSubtask && (
              <Button
                size="xs"
                variant="default"
                leftSection={<IconSubtask size={14} />}
                onClick={() => setSubtaskFocus((n) => n + 1)}
              >
                {t('issues.subtask')}
              </Button>
            )}
            {p.link && (
              <Button
                size="xs"
                variant="default"
                leftSection={<IconLink size={14} />}
                onClick={() => setLinking(true)}
              >
                {t('issues.linkIssue')}
              </Button>
            )}
          </Group>
        </Group>

        <div className="kogge-issue-layout">
          <Stack gap="xl" className="kogge-issue-main">
            {descriptionShown && (
              <Section title={t('issues.description')}>
                <DocField
                  key={`d-${issue.version}`}
                  issue={issue}
                  field={F.description}
                  label={t('issues.description')}
                  value={issue.description as Doc | undefined}
                  editable={descriptionEditable}
                  placeholder={t('issues.addDescription')}
                />
              </Section>
            )}
            {docFields.map(({ def, item }) => {
              const value = issue.fields?.[def.key] as Doc | undefined;
              const editable = p.edit && item.edit;
              if (!value && !editable) return null;
              return (
                <Section key={def.key} title={label(def)}>
                  <DocField
                    key={`${def.key}-${issue.version}`}
                    issue={issue}
                    field={def.key}
                    label={label(def)}
                    value={value}
                    editable={editable}
                    placeholder={t('issues.addText')}
                  />
                </Section>
              );
            })}
            <Attachments issue={issue} />
            <Subtasks issue={issue} focus={subtaskFocus} />
            <Links issue={issue} onAdd={p.link ? () => setLinking(true) : undefined} />
            <Development issue={issue} />
            <Mentions issueKey={issue.key} />
            <IssueActivity issue={issue} />
          </Stack>
          <div className="kogge-issue-side">
            <IssueDetails issue={issue} onLogWork={() => setLogging(true)} />
          </div>
        </div>
      </Stack>
      <LogWorkModal issue={issue} opened={logging} onClose={() => setLogging(false)} />
      <LinkModal issue={issue} opened={linking} onClose={() => setLinking(false)} />
      {moving && (
        <MoveIssueModal
          issueKey={issue.key}
          projectKey={issue.project.key}
          onClose={() => setMoving(false)}
        />
      )}
    </Box>
  );
}
