// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Anchor,
  Avatar,
  Badge,
  Box,
  Button,
  Card,
  Checkbox,
  Grid,
  Group,
  Menu,
  Modal,
  Select,
  Stack,
  Tabs,
  Text,
  TextInput,
  Textarea,
  ThemeIcon,
  Title,
  Tooltip,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import {
  IconCheck,
  IconCircleCheck,
  IconCircleX,
  IconClockPause,
  IconClockPlay,
  IconDots,
  IconGitCommit,
  IconGitMerge,
  IconGitPullRequest,
  IconGitPullRequestClosed,
  IconMessage,
  IconPencil,
  IconShieldCheck,
  IconSquare,
  IconSquareCheck,
  IconX,
} from '@tabler/icons-react';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { TFunction } from 'i18next';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatDateTime, formatRelative } from '../../app/format';
import { useSession } from '../../app/session';
import { useRecordVisit } from '../../app/visits';
import { ErrorAlert } from '../../components/ErrorAlert';
import { MarkdownEditor } from '../../components/MarkdownEditor';
import { isEmptyDoc } from '../../components/editor/doc';
import { DocView } from '../../components/editor/DocView';
import { notifyError } from '../../components/notify';
import { UsersPicker } from '../../components/Pickers';
import { checkText, strategyText } from './checks';
import { CodeOwnersCard } from './CodeOwners';
import { AutoMergeBadge, CommentForm, CommitList } from './components';
import { DiffView, type LineTarget } from './DiffView';
import {
  repoParams,
  repoPath,
  useRepo,
  type Activity,
  type PRComment,
  type PullRequest,
  type Repository,
} from './hooks';

const stateColor = { open: 'teal', merged: 'grape', declined: 'red' } as const;

/** Commits of a pull request loaded at a time. */
const commitPageSize = 100;

function StateBadge({ pr }: { pr: PullRequest }) {
  const { t } = useTranslation();
  const icon =
    pr.state === 'merged' ? (
      <IconGitMerge size={14} />
    ) : pr.state === 'declined' ? (
      <IconGitPullRequestClosed size={14} />
    ) : (
      <IconGitPullRequest size={14} />
    );
  return (
    <Badge size="lg" color={stateColor[pr.state]} leftSection={icon}>
      {t(`code.pr.${pr.state}`)}
    </Badge>
  );
}

/** A thread of comments (a comment and its replies) with reply, edit, delete and task actions. */
function Thread({
  repo,
  pr,
  comments,
  onChanged,
}: {
  repo: Repository;
  pr: PullRequest;
  comments: PRComment[];
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const [replying, setReplying] = useState(false);
  const [editing, setEditing] = useState<number | null>(null);
  const root = comments[0];
  const reply = (markdown: string) =>
    unwrap(
      api.POST('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/comments', {
        params: { path: { ...repoParams(repo), number: pr.number } },
        body: { markdown, parentId: root.id },
      }),
    ).then(() => {
      setReplying(false);
      onChanged();
    }, notifyError);
  const resolve = (c: PRComment) =>
    (c.resolved
      ? unwrap(
          api.DELETE('/api/v1/code/pull-request-comments/{id}/resolved', { params: { path: { id: c.id } } }),
        )
      : unwrap(
          api.PUT('/api/v1/code/pull-request-comments/{id}/resolved', { params: { path: { id: c.id } } }),
        )
    ).then(onChanged, notifyError);
  const remove = (c: PRComment) =>
    modals.openConfirmModal({
      title: t('common.delete'),
      labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
      confirmProps: { color: 'red' },
      onConfirm: () =>
        void unwrap(
          api.DELETE('/api/v1/code/pull-request-comments/{id}', { params: { path: { id: c.id } } }),
        ).then(onChanged, notifyError),
    });
  return (
    <Card withBorder padding="sm" radius="md">
      <Stack gap="sm">
        {comments.map((c) => (
          <Box key={c.id} pl={c.parentId ? 'lg' : 0}>
            <Group justify="space-between" wrap="nowrap" mb={4}>
              <Group gap="xs" wrap="nowrap">
                <Avatar size={22} radius="xl" name={c.author?.displayName} color="initials" />
                <Text size="sm" fw={500}>
                  {c.author?.displayName ?? '—'}
                </Text>
                <Tooltip label={formatDateTime(c.created)}>
                  <Text size="xs" c="dimmed">
                    {formatRelative(c.created)}
                    {c.edited ? ` · ${t('code.pr.edited')}` : ''}
                  </Text>
                </Tooltip>
                {c.task && (
                  <Badge
                    size="xs"
                    variant={c.resolved ? 'outline' : 'filled'}
                    color={c.resolved ? 'gray' : 'orange'}
                  >
                    {t('code.pr.task')}
                  </Badge>
                )}
                {c.outdated && (
                  <Badge size="xs" variant="outline" color="gray">
                    {t('code.pr.outdated')}
                  </Badge>
                )}
              </Group>
              <Group gap={2} wrap="nowrap">
                {c.canResolve && (
                  <Tooltip label={c.resolved ? t('code.pr.reopenTask') : t('code.pr.resolve')}>
                    <ActionIcon
                      variant="subtle"
                      size="sm"
                      onClick={() => void resolve(c)}
                      aria-label={c.resolved ? t('code.pr.reopenTask') : t('code.pr.resolve')}
                    >
                      {c.resolved ? <IconSquareCheck size={16} /> : <IconSquare size={16} />}
                    </ActionIcon>
                  </Tooltip>
                )}
                {c.canEdit && (
                  <Menu position="bottom-end" withinPortal>
                    <Menu.Target>
                      <ActionIcon variant="subtle" size="sm" aria-label={t('common.more')}>
                        <IconDots size={14} />
                      </ActionIcon>
                    </Menu.Target>
                    <Menu.Dropdown>
                      <Menu.Item leftSection={<IconPencil size={14} />} onClick={() => setEditing(c.id)}>
                        {t('common.edit')}
                      </Menu.Item>
                      <Menu.Item color="red" leftSection={<IconX size={14} />} onClick={() => remove(c)}>
                        {t('common.delete')}
                      </Menu.Item>
                    </Menu.Dropdown>
                  </Menu>
                )}
              </Group>
            </Group>
            {c.path && !c.parentId && (
              <Text size="xs" c="dimmed" mb={4}>
                {c.line
                  ? t('code.pr.onLine', { path: c.path, line: c.line })
                  : t('code.pr.onFile', { path: c.path })}
                {c.outdated && c.lineText ? ` — ${c.lineText.trim()}` : ''}
              </Text>
            )}
            {editing === c.id ? (
              <EditComment
                comment={c}
                onDone={() => {
                  setEditing(null);
                  onChanged();
                }}
              />
            ) : (
              <Box style={{ textDecoration: c.task && c.resolved ? 'line-through' : undefined }}>
                <DocView doc={c.suggestion ? withoutSuggestion(c.body) : c.body} />
                {c.suggestion && <SuggestionBox repo={repo} pr={pr} comment={c} onApplied={onChanged} />}
              </Box>
            )}
          </Box>
        ))}
        {replying ? (
          <CommentForm
            taskOption={false}
            onCancel={() => setReplying(false)}
            onSubmit={(md) => reply(md)}
            submitLabel={t('code.pr.reply')}
          />
        ) : (
          <Group>
            <Button
              size="compact-xs"
              variant="subtle"
              leftSection={<IconMessage size={12} />}
              onClick={() => setReplying(true)}
            >
              {t('code.pr.reply')}
            </Button>
          </Group>
        )}
      </Stack>
    </Card>
  );
}

type Doc = PRComment['body'];

/** A comment's text without its suggestion, which is shown as a change. */
function withoutSuggestion(doc: Doc): Doc {
  let found = false;
  return {
    ...doc,
    content: (doc.content ?? []).filter((n) => {
      if (found || n.type !== 'codeBlock' || n.attrs?.language !== 'suggestion') return true;
      found = true;
      return false;
    }),
  };
}

/** The change a line comment suggests: the line before and after, and applying it (or who did). */
function SuggestionBox({
  repo,
  pr,
  comment,
  onApplied,
}: {
  repo: Repository;
  pr: PullRequest;
  comment: PRComment;
  onApplied: () => void;
}) {
  const { t } = useTranslation();
  const apply = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/code/pull-request-comments/{id}/apply', { params: { path: { id: comment.id } } }),
      ),
    onSuccess: onApplied,
    onError: notifyError,
  });
  const s = comment.suggestion!;
  return (
    <Card withBorder padding={0} radius="sm" mt="xs">
      <Group justify="space-between" px="sm" py={4} className="kogge-suggestion-head">
        <Text size="xs" fw={600}>
          {t('code.pr.suggestion')}
        </Text>
        {s.applied ? (
          <Text size="xs" c="dimmed">
            {t('code.pr.appliedBy', { name: s.applied.by?.displayName ?? '—' })}{' '}
            <Anchor
              component={Link}
              to={repoPath(repo, 'commits', s.applied.commit)}
              size="xs"
              ff="monospace"
            >
              {s.applied.commit.slice(0, 10)}
            </Anchor>
          </Text>
        ) : comment.outdated ? (
          <Badge size="xs" variant="outline" color="gray">
            {t('code.pr.outdated')}
          </Badge>
        ) : (
          comment.canApply &&
          pr.state === 'open' && (
            <Button size="compact-xs" onClick={() => apply.mutate()} loading={apply.isPending}>
              {t('code.pr.applySuggestion')}
            </Button>
          )
        )}
      </Group>
      <table className="kogge-code" aria-label={t('code.pr.suggestion')}>
        <tbody>
          <tr className="del">
            <td className="marker">−</td>
            <td className="src">{comment.lineText}</td>
          </tr>
          {s.lines.map((l, i) => (
            <tr key={i} className="add">
              <td className="marker">+</td>
              <td className="src">{l}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  );
}

function EditComment({ comment, onDone }: { comment: PRComment; onDone: () => void }) {
  const { t } = useTranslation();
  const [text, setText] = useState(comment.markdown ?? '');
  const [task, setTask] = useState(comment.task);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/code/pull-request-comments/{id}', {
          params: { path: { id: comment.id } },
          body: { markdown: text, task },
        }),
      ),
    onSuccess: onDone,
  });
  return (
    <Stack gap="xs">
      <MarkdownEditor
        value={text}
        onChange={setText}
        ariaLabel={t('common.edit')}
        minRows={2}
        autoFocus
        onSubmit={() => save.mutate()}
      />
      <ErrorAlert error={save.error} />
      <Group justify="space-between">
        <Checkbox
          size="xs"
          label={t('code.pr.asTask')}
          checked={task}
          onChange={(e) => setTask(e.currentTarget.checked)}
        />
        <Group gap="xs">
          <Button size="xs" variant="default" onClick={onDone}>
            {t('common.cancel')}
          </Button>
          <Button size="xs" onClick={() => save.mutate()} loading={save.isPending}>
            {t('common.save')}
          </Button>
        </Group>
      </Group>
    </Stack>
  );
}

/** Changes the description of a pull request (as Markdown); a change by someone else meanwhile is reported. */
function DescriptionForm({
  pr,
  path,
  onDone,
  onSaved,
}: {
  pr: PullRequest;
  path: { key: string; slug: string; number: number };
  onDone: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState(pr.descriptionMarkdown ?? '');
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}', {
          params: { path },
          body: { descriptionMarkdown: text, version: pr.version },
        }),
      ),
    onSuccess: () => {
      onSaved();
      onDone();
    },
  });
  return (
    <Stack gap="xs">
      <MarkdownEditor
        label={t('code.pr.description')}
        value={text}
        onChange={setText}
        minRows={4}
        autoFocus
        onSubmit={() => save.mutate()}
      />
      <ErrorAlert error={save.error} />
      <Group justify="flex-end" gap="xs">
        <Button size="xs" variant="default" onClick={onDone}>
          {t('common.cancel')}
        </Button>
        <Button size="xs" onClick={() => save.mutate()} loading={save.isPending}>
          {t('common.save')}
        </Button>
      </Group>
    </Stack>
  );
}

function eventText(t: TFunction, a: Activity): string {
  const actor = a.actor?.displayName ?? t('code.pr.someone');
  const data = a.data as Record<string, unknown>;
  switch (a.kind) {
    case 'rescoped': {
      const added = (data.added as unknown[] | undefined)?.length ?? 0;
      return added === 0 && ((data.removed as unknown[] | undefined)?.length ?? 0) > 0
        ? t('code.pr.events.rescopedRemoved', { actor })
        : t('code.pr.events.rescoped', { actor, count: added });
    }
    case 'reviewed':
      return t('code.pr.events.reviewed', { actor, state: t(`code.pr.reviewState.${String(data.state)}`) });
    case 'merged': {
      if (data.remote) return t('code.pr.events.mergedRemote');
      if (!data.strategy) return t('code.pr.events.merged', { actor });
      const strategy = t(`code.pr.strategies.${String(data.strategy)}`);
      return t(data.auto ? 'code.pr.events.mergedAuto' : 'code.pr.events.mergedWith', { actor, strategy });
    }
    case 'auto-merge-enabled':
      return t('code.pr.events.auto-merge-enabled', {
        actor,
        strategy: t(`code.pr.strategies.${String(data.strategy)}`),
      });
    case 'auto-merge-cancelled':
      return t(`code.pr.autoMergeEnded.${String(data.reason)}`, {
        actor,
        defaultValue: t('code.pr.autoMergeEnded.failed'),
      });
    case 'auto-merge-blocked':
      return t('code.pr.events.auto-merge-blocked', {
        reason: t(`code.pr.autoMerge.reasons.${String(data.reason)}`),
      });
    case 'declined':
      return data.branchDeleted
        ? t('code.pr.events.declinedBranch', { branch: String(data.branchDeleted) })
        : t('code.pr.events.declined', { actor });
    default:
      return t(`code.pr.events.${a.kind}`, { actor, defaultValue: a.kind });
  }
}

function eventIcon(a: Activity): ReactNode {
  switch (a.kind) {
    case 'merged':
      return (
        <ThemeIcon size="sm" radius="xl" color="grape">
          <IconGitMerge size={12} />
        </ThemeIcon>
      );
    case 'declined':
      return (
        <ThemeIcon size="sm" radius="xl" color="red">
          <IconGitPullRequestClosed size={12} />
        </ThemeIcon>
      );
    case 'reviewed':
      return (a.data as { state?: string }).state === 'approved' ? (
        <ThemeIcon size="sm" radius="xl" color="teal">
          <IconCheck size={12} />
        </ThemeIcon>
      ) : (
        <ThemeIcon size="sm" radius="xl" color="orange" variant="light">
          <IconCircleX size={12} />
        </ThemeIcon>
      );
    case 'rescoped':
      return (
        <ThemeIcon size="sm" radius="xl" variant="light">
          <IconGitCommit size={12} />
        </ThemeIcon>
      );
    case 'auto-merge-enabled':
      return (
        <ThemeIcon size="sm" radius="xl" color="grape" variant="light">
          <IconClockPlay size={12} />
        </ThemeIcon>
      );
    case 'auto-merge-cancelled':
    case 'auto-merge-blocked':
      return (
        <ThemeIcon size="sm" radius="xl" color="orange" variant="light">
          <IconClockPause size={12} />
        </ThemeIcon>
      );
    default:
      return (
        <ThemeIcon size="sm" radius="xl" variant="light">
          <IconGitPullRequest size={12} />
        </ThemeIcon>
      );
  }
}

type Strategy = Repository['settings']['mergeStrategies'][number];

/** The message a merge with a strategy writes unless given one ("" for strategies that write none). */
function defaultMessage(pr: PullRequest, strategy: Strategy): string {
  if (strategy === 'squash') return pr.mergeMessages?.squash ?? '';
  if (strategy === 'merge' || strategy === 'rebase-merge') return pr.mergeMessages?.merge ?? '';
  return '';
}

/**
 * Merges a pull request: the strategy (each explained in a sentence), its message, and why merging is not possible
 * yet — then it can be merged when ready.
 */
function MergeModal({ repo, pr, onClose }: { repo: Repository; pr: PullRequest; onClose: () => void }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const strategies = repo.settings.mergeStrategies;
  const [strategy, setStrategy] = useState<Strategy>(
    pr.autoMerge && strategies.includes(pr.autoMerge.strategy) ? pr.autoMerge.strategy : strategies[0],
  );
  const [message, setMessage] = useState(() => defaultMessage(pr, strategy));
  const [edited, setEdited] = useState(false);
  const [deleteSource, setDeleteSource] = useState(repo.settings.deleteSourceBranch);
  const takesMessage = strategy === 'merge' || strategy === 'squash' || strategy === 'rebase-merge';
  const failing = (pr.checks ?? []).filter((c) => !c.ok);
  const own = pr.strategies?.find((s) => s.strategy === strategy);
  const blocked = failing.length > 0 || (own !== undefined && !own.ok);
  const done = () => {
    void client.invalidateQueries({ queryKey: ['pull-request'] });
    void client.invalidateQueries({ queryKey: ['repository', repo.id] });
    onClose();
  };
  // An unchanged default is left to the server, which writes it as the branches are at the merge.
  const body = {
    strategy,
    message: takesMessage && edited ? message : undefined,
    deleteSource,
    version: pr.version,
  };
  const merge = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/merge', {
          params: { path: { ...repoParams(repo), number: pr.number } },
          body,
        }),
      ),
    onSuccess: done,
  });
  const auto = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/auto-merge', {
          params: { path: { ...repoParams(repo), number: pr.number } },
          body,
        }),
      ),
    onSuccess: done,
  });
  return (
    <Modal opened onClose={onClose} title={t('code.pr.mergeTitle', { number: pr.number })} size="lg">
      <Stack>
        <Select
          label={t('code.pr.strategy')}
          description={t(`code.pr.strategyHelp.${strategy}`)}
          value={strategy}
          allowDeselect={false}
          onChange={(v) => {
            if (!v) return;
            const next = v as Strategy;
            setStrategy(next);
            if (!edited) setMessage(defaultMessage(pr, next));
          }}
          data={strategies.map((s) => ({ value: s, label: t(`code.pr.strategies.${s}`) }))}
        />
        {takesMessage ? (
          <Textarea
            label={t('code.pr.message')}
            description={t('code.pr.messageHelp')}
            value={message}
            onChange={(e) => {
              setMessage(e.currentTarget.value);
              setEdited(true);
            }}
            autosize
            minRows={3}
            maxRows={12}
            styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
          />
        ) : (
          strategy === 'rebase' && (
            <Text size="sm" c="dimmed">
              {t('code.pr.keepsMessages')}
            </Text>
          )
        )}
        <Checkbox
          label={t('code.pr.deleteSource')}
          checked={deleteSource}
          onChange={(e) => setDeleteSource(e.currentTarget.checked)}
        />
        {blocked && (
          <Alert color="orange" variant="light" title={t('code.pr.notYet')}>
            <Stack gap={4}>
              {failing.map((c) => (
                <Text key={c.key} size="sm">
                  {checkText(t, c)}
                </Text>
              ))}
              {own && !own.ok && <Text size="sm">{strategyText(t, own)}</Text>}
            </Stack>
          </Alert>
        )}
        <ErrorAlert error={merge.error ?? auto.error} />
        <Group justify="flex-end">
          {blocked && (
            <Tooltip label={t('code.pr.mergeWhenReadyHelp')} multiline w={300}>
              <Button
                variant="light"
                color="grape"
                onClick={() => auto.mutate()}
                loading={auto.isPending}
                leftSection={<IconClockPlay size={16} />}
              >
                {pr.autoMerge ? t('code.pr.autoMerge.update') : t('code.pr.mergeWhenReady')}
              </Button>
            </Tooltip>
          )}
          <Button
            color="grape"
            onClick={() => merge.mutate()}
            loading={merge.isPending}
            disabled={blocked}
            leftSection={<IconGitMerge size={16} />}
          >
            {t('code.pr.merge')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** The auto-merge of a pull request: who turned it on, what it waits for, and turning it off. */
function AutoMergeCard({ repo, pr }: { repo: Repository; pr: PullRequest }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const off = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/auto-merge', {
          params: { path: { ...repoParams(repo), number: pr.number } },
        }),
      ),
    onSuccess: () => void client.invalidateQueries({ queryKey: ['pull-request'] }),
    onError: notifyError,
  });
  const am = pr.autoMerge;
  if (!am) return null;
  return (
    <Card withBorder>
      <Group justify="space-between" wrap="nowrap" mb={4}>
        <Group gap={6} wrap="nowrap">
          <IconClockPlay size={16} color="var(--mantine-color-grape-6)" />
          <Text fw={600} size="sm">
            {t('code.pr.autoMerge.title')}
          </Text>
        </Group>
        {pr.permissions.disableAutoMerge && (
          <Button
            size="compact-xs"
            variant="default"
            onClick={() => off.mutate()}
            loading={off.isPending}
            aria-label={t('code.pr.autoMerge.cancelLabel')}
          >
            {t('code.pr.autoMerge.cancel')}
          </Button>
        )}
      </Group>
      <Text size="xs">
        {t('code.pr.autoMerge.by', {
          name: am.enabledBy?.displayName ?? t('code.pr.someone'),
          strategy: t(`code.pr.strategies.${am.strategy}`),
        })}
      </Text>
      <Text size="xs" mt={4} c={am.blocked ? 'orange' : 'dimmed'}>
        {am.blocked
          ? t('code.pr.autoMerge.blocked', { reason: t(`code.pr.autoMerge.reasons.${am.blocked}`) })
          : t('code.pr.autoMerge.waiting')}
      </Text>
    </Card>
  );
}

/** A pull request: overview with timeline, changes with comments on lines, commits. */
export function PullRequestPage() {
  const { t } = useTranslation();
  const repo = useRepo();
  const { me } = useSession();
  const client = useQueryClient();
  const { number: numberParam = '0' } = useParams();
  const number = Number(numberParam);
  const [tab, setTab] = useState<string | null>('overview');
  const [merging, setMerging] = useState(false);
  const [editingTitle, setEditingTitle] = useState(false);
  const [editingDescription, setEditingDescription] = useState(false);
  const [title, setTitle] = useState('');
  const [ignoreWhitespace, setIgnoreWhitespace] = useState(false);
  const path = { ...repoParams(repo), number };
  const pr = useQuery({
    queryKey: ['pull-request', repo.id, number],
    queryFn: () =>
      unwrap(api.GET('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}', { params: { path } })),
  });
  useRecordVisit('pull-request', pr.data?.id);
  const timeline = useQuery({
    queryKey: ['pull-request', repo.id, number, 'timeline'],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/timeline', { params: { path } }),
      ),
  });
  const diff = useQuery({
    queryKey: ['pull-request', repo.id, number, 'diff', ignoreWhitespace],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/diff', {
          params: { path, query: { ignoreWhitespace } },
        }),
      ),
    enabled: tab === 'diff',
    placeholderData: (p) => p,
  });
  // The commits a page at a time; the tab counts those loaded and says when more follow.
  const commits = useInfiniteQuery({
    queryKey: ['pull-request', repo.id, number, 'commits'],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/commits', {
          params: { path, query: { limit: commitPageSize, offset: pageParam } },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (last, pages) =>
      last.more ? pages.reduce((n, p) => n + p.commits.length, 0) : undefined,
  });
  // A push to the source while the next page loads shifts the pages; a commit is shown once.
  const seenCommits = new Set<string>();
  const commitList = (commits.data?.pages ?? [])
    .flatMap((p) => p.commits)
    .filter((c) => !seenCommits.has(c.sha) && !!seenCommits.add(c.sha));
  const commitCount = commits.data ? `${commitList.length}${commits.hasNextPage ? '+' : ''}` : '…';
  const refresh = () => {
    void client.invalidateQueries({ queryKey: ['pull-request', repo.id, number] });
  };
  const update = useMutation({
    mutationFn: (body: { title?: string; reviewers?: number[]; draft?: boolean }) =>
      unwrap(api.PATCH('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}', { params: { path }, body })),
    onSuccess: refresh,
    onError: notifyError,
  });
  const review = useMutation({
    mutationFn: (state: 'approved' | 'needs-work' | 'unapproved') =>
      unwrap(
        api.PUT('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/review', {
          params: { path },
          body: { state },
        }),
      ),
    onSuccess: refresh,
    onError: notifyError,
  });
  const close = useMutation({
    mutationFn: (reopen: boolean) =>
      reopen
        ? unwrap(
            api.POST('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/reopen', { params: { path } }),
          )
        : unwrap(
            api.POST('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/decline', { params: { path } }),
          ),
    onSuccess: refresh,
    onError: notifyError,
  });
  const comment = (target: LineTarget | { path: string } | null, markdown: string, task: boolean) =>
    unwrap(
      api.POST('/api/v1/code/repos/{key}/{slug}/pull-requests/{number}/comments', {
        params: { path },
        body: { markdown, task, ...(target ?? {}) },
      }),
    ).then(refresh, (e) => {
      notifyError(e);
      throw e;
    });
  if (pr.error) return <ErrorAlert error={pr.error} />;
  if (!pr.data) return null;
  const p = pr.data;
  const allComments = (timeline.data ?? []).flatMap((a) => (a.comment ? [a.comment] : []));
  const repliesOf = (c: PRComment) => allComments.filter((r) => r.parentId === c.id);
  const mine = p.reviewers.find((r) => r.user.id === me.user.id);
  const checksOk = (p.checks ?? []).every((c) => c.ok);
  const renderThread = (list: PRComment[]) => (
    <Thread repo={repo} pr={p} comments={list} onChanged={refresh} />
  );
  return (
    <Stack>
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <Stack gap={4} style={{ minWidth: 0 }}>
          {editingTitle ? (
            <Group gap="xs">
              <TextInput
                value={title}
                onChange={(e) => setTitle(e.currentTarget.value)}
                w={480}
                autoFocus
                aria-label={t('code.pr.title')}
              />
              <Button
                size="xs"
                onClick={() => {
                  update.mutate({ title });
                  setEditingTitle(false);
                }}
              >
                {t('common.save')}
              </Button>
              <Button size="xs" variant="default" onClick={() => setEditingTitle(false)}>
                {t('common.cancel')}
              </Button>
            </Group>
          ) : (
            <Group gap="xs" wrap="nowrap">
              <Title order={3}>
                {p.title}{' '}
                <Text span c="dimmed" inherit>
                  #{p.number}
                </Text>
              </Title>
              {p.permissions.edit && (
                <ActionIcon
                  variant="subtle"
                  onClick={() => {
                    setTitle(p.title);
                    setEditingTitle(true);
                  }}
                  aria-label={t('common.edit')}
                >
                  <IconPencil size={16} />
                </ActionIcon>
              )}
            </Group>
          )}
          <Group gap="xs">
            <StateBadge pr={p} />
            {p.draft && <Badge variant="outline">{t('code.pr.draft')}</Badge>}
            {p.autoMerge && <AutoMergeBadge blocked={!!p.autoMerge.blocked} />}
            <Text size="sm" c="dimmed">
              {p.author?.displayName} · {formatRelative(p.created)} ·{' '}
              <Text span ff="monospace" size="sm">
                {t('code.pr.intoFrom', { source: p.source, target: p.target })}
              </Text>
            </Text>
          </Group>
        </Stack>
        <Group gap="xs" wrap="nowrap">
          {p.permissions.review && (
            <>
              <Button
                variant={mine?.state === 'approved' ? 'filled' : 'default'}
                color="teal"
                leftSection={<IconCircleCheck size={16} />}
                onClick={() => review.mutate(mine?.state === 'approved' ? 'unapproved' : 'approved')}
              >
                {mine?.state === 'approved' ? t('code.pr.withdraw') : t('code.pr.approve')}
              </Button>
              <Button
                variant={mine?.state === 'needs-work' ? 'filled' : 'default'}
                color="orange"
                onClick={() => review.mutate(mine?.state === 'needs-work' ? 'unapproved' : 'needs-work')}
              >
                {t('code.pr.needsWork')}
              </Button>
            </>
          )}
          {p.permissions.merge && (
            // The dialog says what keeps the pull request from merging, and can merge it when ready.
            <Button
              color="grape"
              variant={checksOk ? 'filled' : 'light'}
              leftSection={<IconGitMerge size={16} />}
              onClick={() => setMerging(true)}
            >
              {t('code.pr.merge')}
            </Button>
          )}
          {(p.permissions.decline || p.state === 'declined') && (
            <Menu position="bottom-end">
              <Menu.Target>
                <ActionIcon variant="default" size="lg" aria-label={t('common.more')}>
                  <IconDots size={16} />
                </ActionIcon>
              </Menu.Target>
              <Menu.Dropdown>
                {p.state === 'open' && p.permissions.edit && (
                  <Menu.Item onClick={() => update.mutate({ draft: !p.draft })}>
                    {p.draft ? '✓ ' : ''}
                    {t('code.pr.draft')}
                  </Menu.Item>
                )}
                {p.state === 'open' ? (
                  <Menu.Item
                    color="red"
                    onClick={() =>
                      modals.openConfirmModal({
                        title: t('code.pr.decline'),
                        children: <Text size="sm">{t('code.pr.declineConfirm', { number: p.number })}</Text>,
                        labels: { confirm: t('code.pr.decline'), cancel: t('common.cancel') },
                        confirmProps: { color: 'red' },
                        onConfirm: () => close.mutate(false),
                      })
                    }
                  >
                    {t('code.pr.decline')}
                  </Menu.Item>
                ) : (
                  p.state === 'declined' && (
                    <Menu.Item onClick={() => close.mutate(true)}>{t('code.pr.reopen')}</Menu.Item>
                  )
                )}
              </Menu.Dropdown>
            </Menu>
          )}
        </Group>
      </Group>
      <Grid>
        <Grid.Col span={{ base: 12, md: 9 }}>
          <Tabs value={tab} onChange={setTab} keepMounted={false}>
            <Tabs.List mb="md">
              <Tabs.Tab value="overview">{t('code.pr.overview')}</Tabs.Tab>
              <Tabs.Tab value="diff">{t('code.pr.diff')}</Tabs.Tab>
              <Tabs.Tab value="commits">
                {t('code.pr.commits')} ({commitCount})
              </Tabs.Tab>
            </Tabs.List>
            <Tabs.Panel value="overview">
              <Stack>
                <Card withBorder>
                  {editingDescription ? (
                    <DescriptionForm
                      pr={p}
                      path={path}
                      onDone={() => setEditingDescription(false)}
                      onSaved={refresh}
                    />
                  ) : (
                    <Group justify="space-between" align="flex-start" wrap="nowrap" gap="xs">
                      <Box style={{ flex: 1, minWidth: 0 }}>
                        {isEmptyDoc(p.description) ? (
                          <Text size="sm" c="dimmed" fs="italic">
                            {t('code.pr.noDescription')}
                          </Text>
                        ) : (
                          <DocView doc={p.description} />
                        )}
                      </Box>
                      {p.permissions.edit && (
                        <Tooltip label={t('code.pr.editDescription')}>
                          <ActionIcon
                            variant="subtle"
                            color="gray"
                            onClick={() => setEditingDescription(true)}
                            aria-label={t('code.pr.editDescription')}
                          >
                            <IconPencil size={16} />
                          </ActionIcon>
                        </Tooltip>
                      )}
                    </Group>
                  )}
                </Card>
                {(timeline.data ?? []).map((a) => {
                  if (a.comment) {
                    if (a.comment.parentId) return null;
                    return <Box key={a.id}>{renderThread([a.comment, ...repliesOf(a.comment)])}</Box>;
                  }
                  return (
                    <Group key={a.id} gap="xs" wrap="nowrap">
                      {eventIcon(a)}
                      <Text size="sm">{eventText(t, a)}</Text>
                      <Text size="xs" c="dimmed">
                        {formatRelative(a.at)}
                      </Text>
                    </Group>
                  );
                })}
                <Card withBorder>
                  <CommentForm onSubmit={(md, task) => comment(null, md, task)} />
                </Card>
              </Stack>
            </Tabs.Panel>
            <Tabs.Panel value="diff">
              <Stack>
                <Checkbox
                  size="xs"
                  label={t('code.ignoreWhitespace')}
                  checked={ignoreWhitespace}
                  onChange={(e) => setIgnoreWhitespace(e.currentTarget.checked)}
                />
                <ErrorAlert error={diff.error} />
                {diff.data && (
                  <DiffView
                    repo={repo}
                    diff={diff.data}
                    viewRef={p.state === 'open' ? p.source : p.sourceSha}
                    contentRef={p.sourceSha}
                    viewedKey={`pr-${repo.id}-${p.number}`}
                    comments={allComments}
                    renderThread={renderThread}
                    onComment={(target, md, task) => comment(target, md, task)}
                  />
                )}
              </Stack>
            </Tabs.Panel>
            <Tabs.Panel value="commits">
              <ErrorAlert error={commits.error} />
              {commits.data && <CommitList repo={repo} commits={commitList} />}
              {commits.hasNextPage && (
                <Group justify="center" mt="md">
                  <Button
                    variant="default"
                    onClick={() => void commits.fetchNextPage()}
                    loading={commits.isFetchingNextPage}
                  >
                    {t('code.loadMore')}
                  </Button>
                </Group>
              )}
            </Tabs.Panel>
          </Tabs>
        </Grid.Col>
        <Grid.Col span={{ base: 12, md: 3 }}>
          <Stack>
            <Card withBorder>
              <Text fw={600} size="sm" mb="xs">
                {t('code.pr.reviewers')}
              </Text>
              <Stack gap={6}>
                {p.reviewers.map((r) => (
                  <Group key={r.user.id} justify="space-between" wrap="nowrap">
                    <Group gap={6} wrap="nowrap" style={{ minWidth: 0 }}>
                      <Avatar size={22} radius="xl" name={r.user.displayName} color="initials" />
                      <Text size="sm" truncate>
                        {r.user.displayName}
                      </Text>
                      {r.codeOwner && (
                        <Tooltip label={t('code.owners.owner')}>
                          <IconShieldCheck
                            size={14}
                            color="var(--mantine-color-dimmed)"
                            style={{ flexShrink: 0 }}
                            role="img"
                            aria-label={t('code.owners.owner')}
                          />
                        </Tooltip>
                      )}
                    </Group>
                    <Badge
                      size="xs"
                      variant="light"
                      color={r.state === 'approved' ? 'teal' : r.state === 'needs-work' ? 'orange' : 'gray'}
                    >
                      {t(`code.pr.reviewState.${r.state}`)}
                    </Badge>
                  </Group>
                ))}
                {p.permissions.edit && (
                  <UsersPicker
                    size="xs"
                    value={p.reviewers.map((r) => r.user.id)}
                    known={p.reviewers.map((r) => ({ id: r.user.id, label: r.user.displayName }))}
                    onChange={(ids) => update.mutate({ reviewers: ids })}
                    placeholder={p.reviewers.length === 0 ? t('code.pr.addReviewers') : undefined}
                    aria-label={t('code.pr.reviewers')}
                  />
                )}
              </Stack>
            </Card>
            <AutoMergeCard repo={repo} pr={p} />
            <CodeOwnersCard repo={repo} pr={p} />
            {p.checks && p.checks.length > 0 && (
              <Card withBorder>
                <Text fw={600} size="sm" mb="xs">
                  {t('code.pr.checks')}
                </Text>
                <Stack gap={6}>
                  {p.checks.map((c) => (
                    <Group key={c.key} gap={6} wrap="nowrap" align="flex-start">
                      {c.ok ? (
                        <IconCircleCheck
                          size={16}
                          color="var(--mantine-color-teal-6)"
                          style={{ flexShrink: 0 }}
                          aria-label={t('code.pr.checkPassed')}
                        />
                      ) : (
                        <IconCircleX
                          size={16}
                          color="var(--mantine-color-red-6)"
                          style={{ flexShrink: 0 }}
                          aria-label={t('code.pr.checkFailed')}
                        />
                      )}
                      <Text size="xs">{checkText(t, c)}</Text>
                    </Group>
                  ))}
                </Stack>
              </Card>
            )}
            {(p.openTasks > 0 || p.resolvedTasks > 0) && (
              <Text size="xs" c="dimmed">
                {t('code.pr.tasks', { open: p.openTasks, resolved: p.resolvedTasks })}
              </Text>
            )}
            {p.state === 'merged' && p.mergeSha && (
              <Text size="xs">
                <Anchor component={Link} to={repoPath(repo, 'commits', p.mergeSha)} ff="monospace">
                  {p.mergeSha.slice(0, 10)}
                </Anchor>
              </Text>
            )}
          </Stack>
        </Grid.Col>
      </Grid>
      {merging && <MergeModal repo={repo} pr={p} onClose={() => setMerging(false)} />}
    </Stack>
  );
}
