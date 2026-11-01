// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Anchor,
  Badge,
  Box,
  Breadcrumbs,
  Button,
  Card,
  Divider,
  FileButton,
  Group,
  Menu,
  Modal,
  Paper,
  SegmentedControl,
  Stack,
  TagsInput,
  Text,
  Title,
  Tooltip,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import {
  IconArchive,
  IconArrowsHorizontal,
  IconArrowsMinimize,
  IconCheck,
  IconCopy,
  IconDots,
  IconDownload,
  IconEye,
  IconEyeOff,
  IconFileTypePdf,
  IconFileZip,
  IconFolderSymlink,
  IconHistory,
  IconLock,
  IconMessage,
  IconPaperclip,
  IconPencil,
  IconPlus,
  IconThumbUp,
  IconTicket,
  IconTrash,
  IconX,
} from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import { Link, Navigate, useLocation, useNavigate, useParams } from 'react-router';
import { api, unwrap, upload, type Schemas } from '../../api/client';
import { formatDateTime, formatRelative } from '../../app/format';
import { useSession } from '../../app/session';
import { attachable } from '../../app/uploads';
import { useRecordVisit } from '../../app/visits';
import { DocView } from '../../components/editor/DocView';
import { Editor, isEmptyDoc, type Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import { GroupPicker, PagePicker, SpacePicker, UserPicker } from '../../components/Pickers';
import { notifyError, notifySuccess } from '../../components/notify';
import { useCreateIssue } from '../issues/CreateIssue';
import { UserBadge } from '../issues/shared';
import { ExportDialog } from './ExportDialog';
import { pagePath, usePage, type Page, useWidePages } from './hooks';
import { wikiContext } from './Macros';
import { PresenceAvatars, usePresence } from './PageEditor';

type Comment = Schemas['WikiComment'];
type Holder = Schemas['Holder'];

function formatSize(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 102.4) / 10} KB`;
  return `${Math.round(n / 104857.6) / 10} MB`;
}

function Labels({ page }: { page: Page }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState<string[]>(page.labels);
  const save = useMutation({
    mutationFn: (labels: string[]) =>
      unwrap(api.PUT('/api/v1/wiki/pages/{id}/labels', { params: { path: { id: page.id } }, body: labels })),
    onSuccess: () => {
      setEditing(false);
      void client.invalidateQueries({ queryKey: ['page', page.id] });
    },
    onError: notifyError,
  });
  if (editing) {
    return (
      <Group gap="xs" align="flex-end">
        <TagsInput
          size="xs"
          value={value}
          onChange={(v) => setValue(v.map((l) => l.toLowerCase().replace(/\s+/g, '-')))}
          w={360}
          aria-label={t('wiki.labels')}
          autoFocus
        />
        <Button size="xs" onClick={() => save.mutate(value)} loading={save.isPending}>
          {t('common.save')}
        </Button>
        <Button size="xs" variant="subtle" onClick={() => setEditing(false)}>
          {t('common.cancel')}
        </Button>
      </Group>
    );
  }
  return (
    <Group gap={6}>
      {page.labels.map((l) => (
        <Badge
          key={l}
          component={Link}
          to={`/wiki/search?label=${encodeURIComponent(l)}`}
          variant="light"
          tt="none"
          style={{ cursor: 'pointer' }}
        >
          {l}
        </Badge>
      ))}
      {page.permissions.edit && (
        <Button
          size="compact-xs"
          variant="subtle"
          leftSection={<IconPlus size={12} />}
          onClick={() => {
            setValue(page.labels);
            setEditing(true);
          }}
        >
          {page.labels.length ? t('common.edit') : t('wiki.addLabels')}
        </Button>
      )}
    </Group>
  );
}

function Attachments({ page }: { page: Page }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const client = useQueryClient();
  const list = useQuery({
    queryKey: ['page', page.id, 'attachments'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/wiki/pages/{id}/attachments', { params: { path: { id: page.id } } })),
  });
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/wiki/attachments/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['page', page.id, 'attachments'] }),
    onError: notifyError,
  });
  const send = async (all: File[]) => {
    const files = attachable(all, me.maxAttachmentMb, notifyError);
    if (files.length === 0) return;
    const form = new FormData();
    for (const f of files) form.append('file', f);
    try {
      await upload(`/api/v1/wiki/pages/${page.id}/attachments`, form);
      void client.invalidateQueries({ queryKey: ['page', page.id, 'attachments'] });
    } catch (e) {
      notifyError(e);
    }
  };
  const items = list.data ?? [];
  if (items.length === 0 && !page.permissions.attach) return null;
  return (
    <Box>
      <Group justify="space-between" mb="xs">
        <Title order={4}>
          {t('wiki.attachments')}{' '}
          {items.length > 0 && (
            <Text span c="dimmed">
              ({items.length})
            </Text>
          )}
        </Title>
        {page.permissions.attach && (
          <FileButton onChange={(f) => f.length > 0 && void send(f)} multiple>
            {(props) => (
              <Button size="xs" variant="subtle" leftSection={<IconPaperclip size={14} />} {...props}>
                {t('wiki.attachFiles')}
              </Button>
            )}
          </FileButton>
        )}
      </Group>
      {items.length > 0 && (
        <Stack gap={4}>
          {items.map((a) => (
            <Group key={a.id} gap="xs" wrap="nowrap">
              <IconPaperclip size={14} />
              <Anchor href={a.url} target="_blank" size="sm" truncate>
                {a.filename}
              </Anchor>
              <Text size="xs" c="dimmed" style={{ flexShrink: 0 }}>
                {formatSize(a.size)} · v{a.version} · {a.author?.displayName ?? ''} ·{' '}
                {formatRelative(a.created)}
              </Text>
              <ActionIcon
                size="xs"
                variant="subtle"
                component="a"
                href={`${a.url}?download=1`}
                aria-label={t('wiki.download')}
              >
                <IconDownload size={12} />
              </ActionIcon>
              {a.canDelete && (
                <ActionIcon
                  size="xs"
                  variant="subtle"
                  color="red"
                  aria-label={t('common.delete')}
                  onClick={() =>
                    modals.openConfirmModal({
                      title: t('common.delete'),
                      children: <Text size="sm">{t('wiki.deleteAttachment', { name: a.filename })}</Text>,
                      labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                      confirmProps: { color: 'red' },
                      onConfirm: () => del.mutate(a.id),
                    })
                  }
                >
                  <IconX size={12} />
                </ActionIcon>
              )}
            </Group>
          ))}
        </Stack>
      )}
    </Box>
  );
}

function CommentItem({
  c,
  replies,
  page,
  onReply,
  refresh,
}: {
  c: Comment;
  replies: Comment[];
  page: Page;
  onReply: (id: number) => void;
  refresh: () => void;
}) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<Doc | null>(c.body as Doc);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/wiki/comments/{id}', {
          params: { path: { id: c.id } },
          body: { body: draft as Schemas['Doc'] },
        }),
      ),
    onSuccess: () => {
      setEditing(false);
      refresh();
    },
    onError: notifyError,
  });
  const like = useMutation({
    mutationFn: () =>
      c.liked
        ? unwrap(api.DELETE('/api/v1/wiki/comments/{id}/like', { params: { path: { id: c.id } } }))
        : unwrap(api.PUT('/api/v1/wiki/comments/{id}/like', { params: { path: { id: c.id } } })),
    onSuccess: refresh,
    onError: notifyError,
  });
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE('/api/v1/wiki/comments/{id}', { params: { path: { id: c.id } } })),
    onSuccess: refresh,
    onError: notifyError,
  });
  return (
    <Box id={`comment-${c.id}`}>
      <Group gap="xs" mb={4}>
        <UserBadge user={c.author} size="xs" />
        <Text size="xs" c="dimmed">
          {formatRelative(c.created)}
          {c.edited ? ` · ${t('issues.edited')}` : ''}
        </Text>
      </Group>
      {editing ? (
        <Stack gap="xs" pl={30}>
          <Editor value={draft} onChange={setDraft} autofocus minHeight={60} onSubmit={() => save.mutate()} />
          <Group gap="xs">
            <Button
              size="xs"
              onClick={() => save.mutate()}
              loading={save.isPending}
              disabled={isEmptyDoc(draft)}
            >
              {t('common.save')}
            </Button>
            <Button size="xs" variant="subtle" onClick={() => setEditing(false)}>
              {t('common.cancel')}
            </Button>
          </Group>
        </Stack>
      ) : (
        <Box pl={30}>
          <DocView doc={c.body as Doc} />
        </Box>
      )}
      <Group gap={4} pl={26}>
        <Button
          size="compact-xs"
          variant="subtle"
          leftSection={<IconThumbUp size={12} />}
          color={c.liked ? undefined : 'gray'}
          onClick={() => like.mutate()}
        >
          {c.likes || ''}
        </Button>
        {page.permissions.comment && !c.parentId && (
          <Button size="compact-xs" variant="subtle" color="gray" onClick={() => onReply(c.id)}>
            {t('wiki.reply')}
          </Button>
        )}
        {c.canEdit && (
          <Button size="compact-xs" variant="subtle" color="gray" onClick={() => setEditing(true)}>
            {t('common.edit')}
          </Button>
        )}
        {c.canDelete && (
          <Button
            size="compact-xs"
            variant="subtle"
            color="red"
            onClick={() =>
              modals.openConfirmModal({
                title: t('common.delete'),
                children: <Text size="sm">{t('issues.deleteComment')}</Text>,
                labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                confirmProps: { color: 'red' },
                onConfirm: () => del.mutate(),
              })
            }
          >
            {t('common.delete')}
          </Button>
        )}
      </Group>
      {replies.length > 0 && (
        <Stack
          gap="sm"
          pl={30}
          mt="xs"
          style={{ borderLeft: '2px solid var(--mantine-color-default-border)' }}
        >
          {replies.map((r) => (
            <Box key={r.id} pl="sm">
              <CommentItem c={r} replies={[]} page={page} onReply={onReply} refresh={refresh} />
            </Box>
          ))}
        </Stack>
      )}
    </Box>
  );
}

function Comments({ page, comments, refresh }: { page: Page; comments: Comment[]; refresh: () => void }) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<Doc | null>(null);
  const [replyTo, setReplyTo] = useState<number | null>(null);
  const [reply, setReply] = useState<Doc | null>(null);
  const add = useMutation({
    mutationFn: (v: { body: Doc | null; parentId?: number }) =>
      unwrap(
        api.POST('/api/v1/wiki/pages/{id}/comments', {
          params: { path: { id: page.id } },
          body: { body: v.body as Schemas['Doc'], parentId: v.parentId },
        }),
      ),
    onSuccess: (_d, v) => {
      if (v.parentId) {
        setReplyTo(null);
        setReply(null);
      } else setDraft(null);
      refresh();
    },
    onError: notifyError,
  });
  const top = comments.filter((c) => !c.parentId && !c.anchor);
  return (
    <Box>
      <Title order={4} mb="sm">
        {t('wiki.comments')}{' '}
        {top.length > 0 && (
          <Text span c="dimmed">
            ({top.length})
          </Text>
        )}
      </Title>
      <Stack gap="md">
        {top.map((c) => (
          <Box key={c.id}>
            <CommentItem
              c={c}
              replies={comments.filter((r) => r.parentId === c.id)}
              page={page}
              onReply={setReplyTo}
              refresh={refresh}
            />
            {replyTo === c.id && (
              <Stack gap="xs" pl={60} mt="xs">
                <Editor
                  value={reply}
                  onChange={setReply}
                  autofocus
                  minHeight={60}
                  placeholder={t('wiki.replyPlaceholder')}
                  onSubmit={() => add.mutate({ body: reply, parentId: c.id })}
                />
                <Group gap="xs">
                  <Button
                    size="xs"
                    onClick={() => add.mutate({ body: reply, parentId: c.id })}
                    disabled={isEmptyDoc(reply)}
                    loading={add.isPending}
                  >
                    {t('wiki.reply')}
                  </Button>
                  <Button size="xs" variant="subtle" onClick={() => setReplyTo(null)}>
                    {t('common.cancel')}
                  </Button>
                </Group>
              </Stack>
            )}
          </Box>
        ))}
        {page.permissions.comment && (
          <Stack gap="xs">
            <Editor
              value={draft}
              onChange={setDraft}
              minHeight={70}
              placeholder={t('issues.addComment')}
              onSubmit={() => add.mutate({ body: draft })}
            />
            <Group>
              <Button
                size="xs"
                onClick={() => add.mutate({ body: draft })}
                disabled={isEmptyDoc(draft)}
                loading={add.isPending}
              >
                {t('issues.comment')}
              </Button>
            </Group>
          </Stack>
        )}
      </Stack>
    </Box>
  );
}

function InlineComments({
  page,
  comments,
  active,
  onActivate,
  refresh,
}: {
  page: Page;
  comments: Comment[];
  active: string | null;
  onActivate: (anchor: string | null) => void;
  refresh: () => void;
}) {
  const { t } = useTranslation();
  const [showResolved, setShowResolved] = useState(false);
  const inline = comments.filter((c) => c.anchor && !c.parentId && (showResolved || !c.resolved));
  const resolve = useMutation({
    mutationFn: (c: Comment) =>
      c.resolved
        ? unwrap(api.DELETE('/api/v1/wiki/comments/{id}/resolved', { params: { path: { id: c.id } } }))
        : unwrap(api.PUT('/api/v1/wiki/comments/{id}/resolved', { params: { path: { id: c.id } } })),
    onSuccess: refresh,
    onError: notifyError,
  });
  const all = comments.filter((c) => c.anchor && !c.parentId);
  if (all.length === 0) return null;
  return (
    <Card withBorder padding="sm">
      <Group justify="space-between" mb="xs">
        <Group gap={6}>
          <IconMessage size={16} />
          <Text fw={600} size="sm">
            {t('wiki.inlineComments')} ({all.filter((c) => !c.resolved).length})
          </Text>
        </Group>
        <SegmentedControl
          size="xs"
          value={showResolved ? 'all' : 'open'}
          onChange={(v) => setShowResolved(v === 'all')}
          data={[
            { value: 'open', label: t('wiki.open') },
            { value: 'all', label: t('wiki.all') },
          ]}
        />
      </Group>
      <Stack gap="sm">
        {inline.map((c) => (
          <Paper
            key={c.id}
            withBorder
            p="xs"
            onMouseEnter={() => onActivate(c.anchor ?? null)}
            onMouseLeave={() => onActivate(null)}
            style={{
              borderColor: active === c.anchor ? 'var(--mantine-color-yellow-5)' : undefined,
              opacity: c.resolved ? 0.6 : 1,
            }}
          >
            <Text size="xs" c="dimmed" fs="italic" lineClamp={2} mb={4}>
              „{c.anchorText}“{c.detached ? ` · ${t('wiki.detached')}` : ''}
            </Text>
            <CommentItem
              c={c}
              replies={comments.filter((r) => r.parentId === c.id)}
              page={page}
              onReply={() => {}}
              refresh={refresh}
            />
            {page.permissions.comment && (
              <Button
                size="compact-xs"
                variant="light"
                mt={4}
                leftSection={c.resolved ? undefined : <IconCheck size={12} />}
                onClick={() => resolve.mutate(c)}
              >
                {c.resolved ? t('wiki.reopen') : t('wiki.resolve')}
              </Button>
            )}
          </Paper>
        ))}
      </Stack>
    </Card>
  );
}

/** Counts how often the selected text occurs in the page before the selection (for inline comments). */
function occurrenceOf(container: HTMLElement, range: Range, text: string): number {
  const before = document.createRange();
  before.selectNodeContents(container);
  before.setEnd(range.startContainer, range.startOffset);
  const prefix = before.toString();
  let n = 0;
  for (let i = prefix.indexOf(text); i !== -1; i = prefix.indexOf(text, i + 1)) n++;
  return n;
}

interface Selection {
  text: string;
  occurrence: number;
  x: number;
  y: number;
}

function SelectionActions({
  page,
  selection,
  onDone,
}: {
  page: Page;
  selection: Selection;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const createIssue = useCreateIssue();
  const [commenting, setCommenting] = useState(false);
  const [body, setBody] = useState<Doc | null>(null);
  const add = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/wiki/pages/{id}/inline-comments', {
          params: { path: { id: page.id } },
          body: { body: body as Schemas['Doc'], text: selection.text, occurrence: selection.occurrence },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['page', page.id] });
      onDone();
    },
    onError: notifyError,
  });
  // The selected text becomes a node of the new issue (editors only; others just create the issue). The menu closes
  // before the issue exists, so this is a plain call rather than a mutation of this component.
  const { text, occurrence } = selection;
  const link = (key: string) =>
    unwrap(
      api.POST('/api/v1/wiki/pages/{id}/link-issue', {
        params: { path: { id: page.id } },
        body: { text, occurrence, key },
      }),
    ).then(() => client.invalidateQueries({ queryKey: ['page', page.id] }), notifyError);
  const url = `${window.location.origin}${pagePath(page.space.key, page.id, page.title)}`;
  return (
    <>
      <Paper
        shadow="md"
        withBorder
        p={4}
        style={{ position: 'absolute', left: selection.x, top: selection.y, zIndex: 50 }}
        onMouseDown={(e: ReactMouseEvent) => e.preventDefault()}
      >
        <Group gap={4}>
          {page.permissions.comment && (
            <Button
              size="compact-xs"
              variant="subtle"
              leftSection={<IconMessage size={14} />}
              onClick={() => setCommenting(true)}
            >
              {t('wiki.comment')}
            </Button>
          )}
          <Button
            size="compact-xs"
            variant="subtle"
            leftSection={<IconTicket size={14} />}
            onClick={() => {
              createIssue({
                summary: selection.text.slice(0, 250),
                description: {
                  type: 'doc',
                  content: [
                    {
                      type: 'blockquote',
                      content: [
                        {
                          type: 'paragraph',
                          content: [{ type: 'text', text: selection.text.slice(0, 2000) }],
                        },
                      ],
                    },
                    {
                      type: 'paragraph',
                      content: [
                        { type: 'text', text: `${t('wiki.fromPage')}: ` },
                        { type: 'text', text: page.title, marks: [{ type: 'link', attrs: { href: url } }] },
                      ],
                    },
                  ],
                },
                onCreated: page.permissions.edit ? (key) => void link(key) : undefined,
              });
              onDone();
            }}
          >
            {t('wiki.createIssue')}
          </Button>
        </Group>
      </Paper>
      <Modal opened={commenting} onClose={onDone} title={t('wiki.inlineComment')}>
        <Stack>
          <Text size="sm" fs="italic" c="dimmed" lineClamp={3}>
            „{selection.text}“
          </Text>
          <Editor value={body} onChange={setBody} autofocus minHeight={80} onSubmit={() => add.mutate()} />
          <Group justify="flex-end">
            <Button onClick={() => add.mutate()} disabled={isEmptyDoc(body)} loading={add.isPending}>
              {t('wiki.comment')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}

function HolderList({
  value,
  onChange,
  label,
}: {
  value: Holder[];
  onChange: (v: Holder[]) => void;
  label: string;
}) {
  const { t } = useTranslation();
  const [type, setType] = useState<'user' | 'group'>('user');
  const [id, setId] = useState<number | null>(null);
  return (
    <Stack gap="xs">
      <Text size="sm" fw={600}>
        {label}
      </Text>
      <Group gap={6}>
        {value.length === 0 && (
          <Text size="sm" c="dimmed">
            {t('wiki.noRestriction')}
          </Text>
        )}
        {value.map((h, i) => (
          <Badge
            key={`${h.type}-${h.id}`}
            variant="light"
            tt="none"
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
            {h.type === 'group' ? `${t('sharing.group')}: ` : ''}
            {h.name || `#${h.id}`}
          </Badge>
        ))}
      </Group>
      <Group gap="xs" align="flex-end">
        <SegmentedControl
          size="xs"
          value={type}
          onChange={(v) => {
            setType(v as 'user' | 'group');
            setId(null);
          }}
          data={[
            { value: 'user', label: t('sharing.user') },
            { value: 'group', label: t('sharing.group') },
          ]}
        />
        {type === 'user' ? (
          <UserPicker size="xs" w={220} value={id} onChange={setId} aria-label={t('sharing.user')} />
        ) : (
          <GroupPicker size="xs" w={220} value={id} onChange={setId} aria-label={t('sharing.group')} />
        )}
        <Button
          size="xs"
          variant="light"
          disabled={!id}
          onClick={() => {
            if (id && !value.some((h) => h.type === type && h.id === id)) onChange([...value, { type, id }]);
            setId(null);
          }}
        >
          {t('sharing.add')}
        </Button>
      </Group>
    </Stack>
  );
}

function RestrictionsDialog({ page, onClose }: { page: Page; onClose: () => void }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const current = useQuery({
    queryKey: ['page', page.id, 'restrictions'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/wiki/pages/{id}/restrictions', { params: { path: { id: page.id } } })),
  });
  const [view, setView] = useState<Holder[] | null>(null);
  const [edit, setEdit] = useState<Holder[] | null>(null);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/wiki/pages/{id}/restrictions', {
          params: { path: { id: page.id } },
          body: { view: view ?? current.data?.view ?? [], edit: edit ?? current.data?.edit ?? [] },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['page'] });
      notifySuccess(t('common.saved'));
      onClose();
    },
  });
  if (!current.data) return null;
  return (
    <Modal opened onClose={onClose} title={t('wiki.restrictions')} size="lg">
      <Stack>
        {current.data.inherited.length > 0 && (
          <Alert color="blue" variant="light">
            {t('wiki.inheritedRestrictions')}{' '}
            {current.data.inherited.map((r) => (
              <Anchor
                key={r.id}
                component={Link}
                to={pagePath(r.spaceKey, r.id, r.title)}
                onClick={onClose}
                mr="xs"
              >
                {r.title}
              </Anchor>
            ))}
          </Alert>
        )}
        <HolderList label={t('wiki.viewRestriction')} value={view ?? current.data.view} onChange={setView} />
        <Divider />
        <HolderList label={t('wiki.editRestriction')} value={edit ?? current.data.edit} onChange={setEdit} />
        <Text size="xs" c="dimmed">
          {t('wiki.restrictionsHelp')}
        </Text>
        <ErrorAlert error={save.error} />
        <Group justify="flex-end">
          <Button onClick={() => save.mutate()} loading={save.isPending}>
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function MoveCopyDialog({ page, mode, onClose }: { page: Page; mode: 'move' | 'copy'; onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [space, setSpace] = useState<string | null>(page.space.key);
  const [parent, setParent] = useState<number | null>(page.parentId ?? null);
  const run = useMutation({
    mutationFn: () =>
      mode === 'move'
        ? unwrap(
            api.POST('/api/v1/wiki/pages/{id}/move', {
              params: { path: { id: page.id } },
              body: { space: space ?? undefined, parentId: parent ?? 0 },
            }),
          )
        : unwrap(
            api.POST('/api/v1/wiki/pages/{id}/copy', {
              params: { path: { id: page.id } },
              body: { space: space ?? undefined, parentId: parent ?? 0 },
            }),
          ),
    onSuccess: (p) => {
      void client.invalidateQueries({ queryKey: ['page'] });
      onClose();
      navigate(pagePath(p.space.key, p.id, p.title));
    },
  });
  return (
    <Modal opened onClose={onClose} title={mode === 'move' ? t('wiki.movePage') : t('wiki.copyPage')}>
      <Stack>
        <SpacePicker
          label={t('wiki.space')}
          value={space}
          onChange={setSpace}
          allowDeselect={false}
          permission="page.edit"
        />
        <PagePicker
          label={t('wiki.parentPage')}
          value={parent}
          onChange={setParent}
          description={t('wiki.parentHelp')}
        />
        <ErrorAlert error={run.error} />
        <Group justify="flex-end">
          <Button onClick={() => run.mutate()} loading={run.isPending}>
            {mode === 'move' ? t('wiki.move') : t('wiki.copy')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function Byline({ page }: { page: Page }) {
  const { t } = useTranslation();
  return (
    <Text size="sm" c="dimmed">
      {page.creator && t('wiki.createdBy', { name: page.creator.displayName })}
      {page.updatedBy && page.version > 1 && (
        <>
          {' '}
          · {t('wiki.updatedBy', { name: page.updatedBy.displayName, when: formatRelative(page.updated) })}
        </>
      )}
      {page.kind === 'blog' && page.published && <> · {formatDateTime(page.published)}</>}
      {page.version > 0 && (
        <>
          {' · '}
          <Anchor
            component={Link}
            to={`/wiki/spaces/${page.space.key}/pages/${page.id}/history`}
            size="sm"
            c="dimmed"
          >
            {t('wiki.versionN', { version: page.version })}
          </Anchor>
        </>
      )}
    </Text>
  );
}

export function PageView() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const client = useQueryClient();
  const { key = '', pageId = '' } = useParams();
  const id = Number(pageId);
  const page = usePage(id);
  useRecordVisit('page', page.data?.status === 'current' ? page.data.id : undefined);
  const comments = useQuery({
    queryKey: ['page', id, 'comments'],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/pages/{id}/comments', { params: { path: { id } } })),
    enabled: page.isSuccess,
  });
  const presence = usePresence(id, false);
  const [activeAnchor, setActiveAnchor] = useState<string | null>(null);
  const [selection, setSelection] = useState<Selection | null>(null);
  const [dialog, setDialog] = useState<'restrictions' | 'move' | 'copy' | 'export' | null>(null);
  const [wide, toggleWide] = useWidePages();
  const body = useRef<HTMLDivElement>(null);
  const refreshComments = () => void client.invalidateQueries({ queryKey: ['page', id] });

  const watch = useMutation({
    mutationFn: (on: boolean) =>
      on
        ? unwrap(api.PUT('/api/v1/wiki/pages/{id}/watch', { params: { path: { id } } }))
        : unwrap(api.DELETE('/api/v1/wiki/pages/{id}/watch', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['page', id] }),
    onError: notifyError,
  });
  const like = useMutation({
    mutationFn: (on: boolean) =>
      on
        ? unwrap(api.PUT('/api/v1/wiki/pages/{id}/like', { params: { path: { id } } }))
        : unwrap(api.DELETE('/api/v1/wiki/pages/{id}/like', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['page', id] }),
    onError: notifyError,
  });
  const trash = useMutation({
    mutationFn: () => unwrap(api.DELETE('/api/v1/wiki/pages/{id}', { params: { path: { id } } })),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['page'] });
      navigate(`/wiki/spaces/${key}`);
    },
    onError: notifyError,
  });
  const archive = useMutation({
    mutationFn: (on: boolean) =>
      on
        ? unwrap(api.PUT('/api/v1/wiki/pages/{id}/archived', { params: { path: { id } } }))
        : unwrap(api.DELETE('/api/v1/wiki/pages/{id}/archived', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['page'] }),
    onError: notifyError,
  });
  const restore = useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/wiki/pages/{id}/restore', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['page'] }),
    onError: notifyError,
  });

  // Comments that refer to anchors: resolved ones are not highlighted.
  const anchorState = useMemo(() => {
    const m = new Map<string, Comment>();
    for (const c of comments.data ?? []) if (c.anchor && !c.parentId) m.set(c.anchor, c);
    return m;
  }, [comments.data]);

  useEffect(() => {
    if (!location.hash) return;
    const el = document.getElementById(decodeURIComponent(location.hash.slice(1)));
    el?.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }, [location.hash, page.data, comments.data]);

  if (page.error) return <ErrorAlert error={page.error} />;
  const p = page.data;
  if (!p) return null;
  if (p.status === 'draft') return <Navigate to={`/wiki/spaces/${p.space.key}/pages/${p.id}/edit`} replace />;

  const context = wikiContext(p, {
    anchor: (anchorId, child: ReactNode) => {
      const c = anchorState.get(anchorId);
      if (!c) return child;
      return (
        <span
          className="kogge-anchor"
          data-resolved={c.resolved ? '' : undefined}
          data-active={activeAnchor === anchorId ? '' : undefined}
          onMouseEnter={() => setActiveAnchor(anchorId)}
          onMouseLeave={() => setActiveAnchor(null)}
        >
          {child}
        </span>
      );
    },
  });

  const onMouseUp = (e: ReactMouseEvent) => {
    // Events from dialogs (portals) bubble through React here too; only selections in the page count.
    if (!body.current?.contains(e.target as globalThis.Node)) return;
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || !body.current) {
      setSelection(null);
      return;
    }
    const range = sel.getRangeAt(0);
    if (!body.current.contains(range.commonAncestorContainer)) return;
    const text = sel.toString().trim();
    if (!text || text.length > 2000) return;
    const rect = range.getBoundingClientRect();
    const host = body.current.getBoundingClientRect();
    setSelection({
      text,
      occurrence: occurrenceOf(body.current, range, text),
      x: rect.left - host.left,
      y: rect.bottom - host.top + 6,
    });
  };

  const others = presence.data ?? [];
  return (
    <Stack gap="md" className="logbuch-page" data-wide={wide || undefined}>
      {p.status === 'trashed' && (
        <Alert color="red" variant="light" title={t('wiki.inTrash')}>
          <Button size="xs" onClick={() => restore.mutate()} loading={restore.isPending}>
            {t('wiki.restore')}
          </Button>
        </Alert>
      )}
      {p.status === 'archived' && (
        <Alert color="gray" variant="light" icon={<IconArchive size={16} />}>
          {t('wiki.archivedPage')}
        </Alert>
      )}
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <Breadcrumbs separator="/">
          <Anchor component={Link} to="/wiki" size="sm">
            {t('wiki.spaces')}
          </Anchor>
          <Anchor component={Link} to={`/wiki/spaces/${p.space.key}`} size="sm">
            {p.space.name}
          </Anchor>
          {p.ancestors.map((a) => (
            <Anchor key={a.id} component={Link} to={pagePath(p.space.key, a.id, a.title)} size="sm">
              {a.title}
            </Anchor>
          ))}
        </Breadcrumbs>
        <Group gap="xs" wrap="nowrap">
          <PresenceAvatars entries={others} />
          {p.restricted && (
            <Tooltip label={t('wiki.restricted')}>
              <IconLock size={18} color="var(--mantine-color-orange-6)" />
            </Tooltip>
          )}
          {p.permissions.edit && (
            <Button
              size="xs"
              leftSection={<IconPencil size={14} />}
              component={Link}
              to={`/wiki/spaces/${p.space.key}/pages/${p.id}/edit`}
            >
              {p.draft ? t('wiki.continueEditing') : t('common.edit')}
            </Button>
          )}
          <Tooltip label={wide ? t('wiki.fixedWidth') : t('wiki.fullWidth')}>
            <ActionIcon
              variant="default"
              size="lg"
              onClick={toggleWide}
              aria-label={wide ? t('wiki.fixedWidth') : t('wiki.fullWidth')}
              aria-pressed={wide}
              visibleFrom="md"
            >
              {wide ? <IconArrowsMinimize size={16} /> : <IconArrowsHorizontal size={16} />}
            </ActionIcon>
          </Tooltip>
          <Tooltip label={p.watching ? t('wiki.watching') : t('wiki.watch')}>
            <ActionIcon
              variant={p.watching ? 'light' : 'default'}
              size="lg"
              onClick={() => watch.mutate(!p.watching)}
              aria-label={t('wiki.watch')}
            >
              {p.watching ? <IconEye size={16} /> : <IconEyeOff size={16} />}
            </ActionIcon>
          </Tooltip>
          <Menu position="bottom-end">
            <Menu.Target>
              <ActionIcon variant="default" size="lg" aria-label={t('issues.more')}>
                <IconDots size={16} />
              </ActionIcon>
            </Menu.Target>
            <Menu.Dropdown>
              <Menu.Item
                leftSection={<IconHistory size={14} />}
                component={Link}
                to={`/wiki/spaces/${p.space.key}/pages/${p.id}/history`}
              >
                {t('wiki.history')}
              </Menu.Item>
              {p.permissions.restrict && (
                <Menu.Item leftSection={<IconLock size={14} />} onClick={() => setDialog('restrictions')}>
                  {t('wiki.restrictions')}
                </Menu.Item>
              )}
              {p.permissions.edit && p.kind === 'page' && (
                <Menu.Item leftSection={<IconFolderSymlink size={14} />} onClick={() => setDialog('move')}>
                  {t('wiki.movePage')}
                </Menu.Item>
              )}
              <Menu.Item leftSection={<IconCopy size={14} />} onClick={() => setDialog('copy')}>
                {t('wiki.copyPage')}
              </Menu.Item>
              {p.permissions.export && (
                <>
                  <Menu.Item
                    leftSection={<IconDownload size={14} />}
                    component="a"
                    href={`/api/v1/wiki/pages/${p.id}/export?format=html`}
                  >
                    {t('wiki.exportHtml')}
                  </Menu.Item>
                  <Menu.Item
                    leftSection={<IconDownload size={14} />}
                    component="a"
                    href={`/api/v1/wiki/pages/${p.id}/export?format=md`}
                  >
                    {t('wiki.exportMarkdown')}
                  </Menu.Item>
                  <Menu.Item
                    leftSection={<IconFileTypePdf size={14} />}
                    component="a"
                    href={`/api/v1/wiki/pages/${p.id}/export?format=pdf`}
                  >
                    {t('wiki.exportPdf')}
                  </Menu.Item>
                  {p.kind === 'page' && p.status === 'current' && (
                    <Menu.Item leftSection={<IconFileZip size={14} />} onClick={() => setDialog('export')}>
                      {t('wiki.exportTree')}
                    </Menu.Item>
                  )}
                </>
              )}
              {p.permissions.archive && p.status !== 'trashed' && !p.isHome && (
                <Menu.Item
                  leftSection={<IconArchive size={14} />}
                  onClick={() => archive.mutate(p.status !== 'archived')}
                >
                  {p.status === 'archived' ? t('wiki.unarchive') : t('wiki.archive')}
                </Menu.Item>
              )}
              {p.permissions.delete && p.status !== 'trashed' && !p.isHome && (
                <Menu.Item
                  color="red"
                  leftSection={<IconTrash size={14} />}
                  onClick={() =>
                    modals.openConfirmModal({
                      title: t('wiki.deletePage'),
                      children: <Text size="sm">{t('wiki.deletePageText', { title: p.title })}</Text>,
                      labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                      confirmProps: { color: 'red' },
                      onConfirm: () => trash.mutate(),
                    })
                  }
                >
                  {t('wiki.deletePage')}
                </Menu.Item>
              )}
            </Menu.Dropdown>
          </Menu>
        </Group>
      </Group>
      <Stack gap={4}>
        <Title order={1} fz={26}>
          {p.title}
        </Title>
        <Byline page={p} />
      </Stack>
      <Box ref={body} pos="relative" onMouseUp={onMouseUp}>
        <DocView doc={p.body as Doc} context={context} />
        {selection && <SelectionActions page={p} selection={selection} onDone={() => setSelection(null)} />}
      </Box>
      <InlineComments
        page={p}
        comments={comments.data ?? []}
        active={activeAnchor}
        onActivate={setActiveAnchor}
        refresh={refreshComments}
      />
      <Group justify="space-between">
        <Labels page={p} />
        <Button
          size="xs"
          variant={p.liked ? 'light' : 'subtle'}
          leftSection={<IconThumbUp size={14} />}
          onClick={() => like.mutate(!p.liked)}
        >
          {p.likes > 0 ? t('wiki.likes', { count: p.likes }) : t('wiki.like')}
        </Button>
      </Group>
      <Divider />
      <Attachments page={p} />
      <Comments page={p} comments={comments.data ?? []} refresh={refreshComments} />
      {dialog === 'restrictions' && <RestrictionsDialog page={p} onClose={() => setDialog(null)} />}
      {(dialog === 'move' || dialog === 'copy') && (
        <MoveCopyDialog page={p} mode={dialog} onClose={() => setDialog(null)} />
      )}
      {dialog === 'export' && (
        <ExportDialog space={p.space} page={{ id: p.id, title: p.title }} onClose={() => setDialog(null)} />
      )}
    </Stack>
  );
}
