// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Alert,
  Avatar,
  Badge,
  Box,
  Button,
  Checkbox,
  Group,
  Menu,
  Modal,
  Popover,
  SegmentedControl,
  Stack,
  Text,
  TextInput,
  Title,
  Tooltip,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { notifications } from '@mantine/notifications';
import {
  IconAlertTriangle,
  IconChevronDown,
  IconCircleCheck,
  IconCloudOff,
  IconEye,
  IconLoader2,
  IconPencil,
} from '@tabler/icons-react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import type { Editor as TiptapEditor } from '@tiptap/react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useBlocker, useNavigate, useParams } from 'react-router';
import { ApiError, api, unwrap, type Schemas } from '../../api/client';
import { formatRelative } from '../../app/format';
import { useSession } from '../../app/session';
import { collaboration } from '../../components/editor/collab';
import { flushMarkdown } from '../../components/editor/MarkdownMode';
import { DocView } from '../../components/editor/DocView';
import { Editor, isEmptyDoc, type Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError } from '../../components/notify';
import { DraftLink, newClientId, type DraftPerson, type LinkStatus } from './draftLink';
import { attachmentUrl, pagePath, pageUpload, usePage, useWidePages } from './hooks';
import { wikiContext } from './Macros';

/** Reports presence every 20 seconds and returns the others who have the page open. */
export function usePresence(pageId: number, editing: boolean) {
  return useQuery({
    queryKey: ['page', pageId, 'presence', editing],
    queryFn: () =>
      unwrap(
        api.POST('/api/v1/wiki/pages/{id}/presence', { params: { path: { id: pageId } }, body: { editing } }),
      ),
    refetchInterval: 20_000,
    refetchIntervalInBackground: false,
  });
}

function initials(name: string) {
  return name
    .split(/\s+/)
    .map((p) => p[0])
    .join('')
    .slice(0, 2)
    .toUpperCase();
}

export function PresenceAvatars({ entries }: { entries: Schemas['PresenceEntry'][] }) {
  const { t } = useTranslation();
  if (entries.length === 0) return null;
  return (
    <Tooltip.Group>
      <Avatar.Group spacing="sm">
        {entries.slice(0, 5).map((e) => (
          <Tooltip
            key={e.user.id}
            label={`${e.user.displayName}${e.editing ? ` (${t('wiki.editing')})` : ''}`}
          >
            <Avatar size="sm" radius="xl" color={e.editing ? 'orange' : 'initials'} name={e.user.displayName}>
              {initials(e.user.displayName)}
            </Avatar>
          </Tooltip>
        ))}
      </Avatar.Group>
    </Tooltip.Group>
  );
}

/** Who edits the draft right now, in the colours of their carets. */
function People({ people }: { people: DraftPerson[] }) {
  const { t } = useTranslation();
  // One avatar per person, also with several windows.
  const unique = [...new Map(people.map((p) => [p.userId, p])).values()];
  if (unique.length === 0) return null;
  return (
    <Group gap={6} wrap="nowrap" data-people>
      <Tooltip.Group>
        <Avatar.Group spacing="sm">
          {unique.slice(0, 6).map((p) => (
            <Tooltip key={p.userId} label={t('wiki.live.personEditing', { name: p.name })}>
              <Avatar
                size="sm"
                radius="xl"
                color={p.color}
                variant="filled"
                name={p.name}
                aria-label={t('wiki.live.personEditing', { name: p.name })}
              >
                {initials(p.name)}
              </Avatar>
            </Tooltip>
          ))}
        </Avatar.Group>
      </Tooltip.Group>
      <Text size="xs" c="dimmed">
        {t('wiki.live.editingWith', { count: unique.length })}
      </Text>
    </Group>
  );
}

function SyncStatus({ status }: { status: LinkStatus }) {
  const { t } = useTranslation();
  const [icon, color] =
    status === 'saved'
      ? [<IconCircleCheck key="i" size={14} />, 'teal']
      : status === 'offline'
        ? [<IconCloudOff key="i" size={14} />, 'orange']
        : [<IconLoader2 key="i" size={14} className="kogge-spin" />, 'dimmed'];
  return (
    <Group gap={4} wrap="nowrap" c={color} role="status" aria-live="polite" data-sync={status}>
      {icon}
      <Text size="xs" inherit>
        {t(`wiki.live.${status}`)}
      </Text>
    </Group>
  );
}

/** The editor of a page: its shared draft, which everyone editing the page changes together, live. */
export function PageEditor() {
  const { key = '', pageId = '' } = useParams();
  const id = Number(pageId);
  const page = usePage(id);
  // A new generation loads the draft again (after refused changes, or when this editor fell too far behind).
  const [generation, setGeneration] = useState(0);
  const session = useQuery({
    queryKey: ['page', id, 'edit', generation],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/pages/{id}/edit', { params: { path: { id } } })),
    refetchOnWindowFocus: false,
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  });
  if (page.error) return <ErrorAlert error={page.error} />;
  if (session.error) return <ErrorAlert error={session.error} />;
  if (!session.data || !page.data) return null;
  return (
    <DraftEditor
      key={generation}
      spaceKey={key}
      page={page.data}
      session={session.data}
      onResync={() => setGeneration((g) => g + 1)}
    />
  );
}

function DraftEditor({
  spaceKey,
  page,
  session,
  onResync,
}: {
  spaceKey: string;
  page: Schemas['Page'];
  session: Schemas['EditSession'];
  onResync: () => void;
}) {
  const { t } = useTranslation();
  const { me } = useSession();
  const [wide] = useWidePages();
  const navigate = useNavigate();
  const client = useQueryClient();
  const id = page.id;
  usePresence(id, true);
  const clientId = useMemo(() => newClientId(me.user.id), [me.user.id]);
  const extensions = useMemo(() => collaboration(session.version, clientId), [session.version, clientId]);
  const [editor, setEditor] = useState<TiptapEditor | null>(null);
  const link = useRef<DraftLink | null>(null);
  const [title, setTitle] = useState(session.title);
  const [body, setBody] = useState<Doc>(session.doc as Doc);
  const [baseVersion, setBaseVersion] = useState(session.baseVersion);
  const [status, setStatus] = useState<LinkStatus>('connecting');
  const [people, setPeople] = useState<DraftPerson[]>([]);
  const [denied, setDenied] = useState(false);
  // The draft holds content this browser's Kogge does not know: Kogge was updated while the page was open.
  const [outdated, setOutdated] = useState(false);
  const [mode, setMode] = useState<'edit' | 'preview'>('edit');
  const [message, setMessage] = useState('');
  const [minor, setMinor] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [publishError, setPublishError] = useState<unknown>(null);
  const [earlier, setEarlier] = useState(session.earlierDraft ?? null);
  const [showEarlier, setShowEarlier] = useState(false);
  const titleFocused = useRef(false);
  const remoteTitle = useRef<string | null>(null);
  const titleTimer = useRef<number | undefined>(undefined);
  const pendingTitle = useRef<string | null>(null); // typed, waiting for the end of typing to be sent
  const leaving = useRef(false); // published or discarded: nothing is left to save
  const isNew = page.status === 'draft';

  useEffect(() => {
    if (!editor) return;
    const l = new DraftLink(
      editor,
      id,
      clientId,
      { baseVersion: session.baseVersion, title: session.title },
      {
        status: setStatus,
        people: setPeople,
        title: (value) => {
          // Who is typing the title keeps it until they leave the field.
          if (titleFocused.current) remoteTitle.current = value;
          else setTitle(value);
        },
        published: (version, _title, user) => {
          setBaseVersion(version);
          void client.invalidateQueries({ queryKey: ['page', id] });
          if (user && user.id !== me.user.id) {
            notifications.show({
              color: 'blue',
              message: t('wiki.live.publishedBy', { name: user.name, version }),
            });
          }
        },
        discarded: (user) => {
          if (user && user.id !== me.user.id) {
            notifications.show({ color: 'orange', message: t('wiki.live.discardedBy', { name: user.name }) });
          }
        },
        resync: (code, detail) => {
          notifications.show({
            color: 'orange',
            title: t('wiki.live.reloaded'),
            message:
              detail ?? t(code === 'draft.reset' ? 'wiki.live.reloadedBehind' : 'wiki.live.reloadedRefused'),
          });
          onResync();
        },
        denied: () => {
          setDenied(true);
          editor.setEditable(false);
        },
      },
    );
    link.current = l;
    l.start();
    return () => {
      // A title typed just before leaving is sent before the connection closes.
      if (pendingTitle.current !== null) l.sendTitle(pendingTitle.current);
      window.clearTimeout(titleTimer.current);
      l.stop();
      link.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editor]);

  // Changes not saved yet are lost when the page is left: the browser asks first.
  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (link.current && !link.current.synced()) e.preventDefault();
    };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, []);

  // Leaving the editor within the application (Close, a link, back) waits until the changes are saved; what cannot
  // be saved in time is given up only when the user says so.
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      currentLocation.pathname !== nextLocation.pathname &&
      !leaving.current &&
      !!link.current &&
      !link.current.synced(),
  );
  useEffect(() => {
    if (blocker.state !== 'blocked') return;
    let active = true;
    void (link.current?.whenSynced(5000) ?? Promise.resolve(true)).then((saved) => {
      if (!active) return;
      if (saved) {
        blocker.proceed();
        return;
      }
      modals.openConfirmModal({
        title: t('wiki.live.unsavedTitle'),
        children: <Text size="sm">{t('wiki.live.unsavedText')}</Text>,
        labels: { confirm: t('wiki.live.leaveAnyway'), cancel: t('wiki.live.keepEditing') },
        confirmProps: { color: 'red' },
        onConfirm: () => blocker.proceed(),
        onCancel: () => blocker.reset(),
      });
    });
    return () => {
      active = false;
    };
  }, [blocker, t]);

  const changeTitle = (value: string) => {
    setTitle(value);
    pendingTitle.current = value.trim();
    window.clearTimeout(titleTimer.current);
    titleTimer.current = window.setTimeout(() => {
      pendingTitle.current = null;
      link.current?.sendTitle(value.trim());
    }, 400);
  };

  const close = () => navigate(isNew ? `/wiki/spaces/${spaceKey}` : pagePath(spaceKey, id, page.title));

  const publish = async () => {
    const l = link.current;
    if (!l) return;
    setPublishing(true);
    setPublishError(null);
    try {
      window.clearTimeout(titleTimer.current);
      // Markdown still being typed goes into the draft first.
      await flushMarkdown(editor);
      if (!(await l.whenSynced())) throw new Error(t('wiki.live.notSynced'));
      let base = baseVersion;
      for (let attempt = 0; ; attempt++) {
        try {
          const p = await unwrap(
            api.POST('/api/v1/wiki/pages/{id}/publish', {
              params: { path: { id } },
              body: { title: title.trim(), draftVersion: l.version(), baseVersion: base, message, minor },
            }),
          );
          client.setQueryData(['page', id], p);
          void client.invalidateQueries({ queryKey: ['page'] });
          leaving.current = true;
          pendingTitle.current = null;
          navigate(pagePath(p.space.key, p.id, p.title));
          return;
        } catch (e) {
          // Someone changed or published the draft meanwhile: catch up and publish what everyone wrote.
          if (attempt >= 3 || !(e instanceof ApiError)) throw e;
          if (e.code === 'draft.behind') {
            await l.whenSynced();
            await new Promise((r) => window.setTimeout(r, 300));
          } else if (e.code === 'page.conflict') {
            const fresh = await unwrap(api.GET('/api/v1/wiki/pages/{id}/edit', { params: { path: { id } } }));
            base = fresh.baseVersion;
            setBaseVersion(base);
          } else {
            throw e;
          }
        }
      }
    } catch (e) {
      setPublishError(e);
    } finally {
      setPublishing(false);
    }
  };

  const discard = async () => {
    try {
      await unwrap(api.DELETE('/api/v1/wiki/pages/{id}/draft', { params: { path: { id } } }));
      void client.invalidateQueries({ queryKey: ['page'] });
      if (isNew) {
        leaving.current = true;
        pendingTitle.current = null;
        navigate(`/wiki/spaces/${spaceKey}`);
      }
    } catch (e) {
      notifyError(e);
    }
  };

  const dropEarlier = async () => {
    try {
      await unwrap(api.DELETE('/api/v1/wiki/pages/{id}/edit/earlier-draft', { params: { path: { id } } }));
      setEarlier(null);
      setShowEarlier(false);
    } catch (e) {
      notifyError(e);
    }
  };

  // Macros render with the page as it is being written (a table of contents follows its headings).
  const context = wikiContext({ id, space: page.space, body });
  return (
    <Stack gap="md" className="logbuch-page" data-wide={wide || undefined}>
      <Group justify="space-between" wrap="nowrap" align="center">
        <Group gap="sm" wrap="wrap">
          <Badge variant="light" color={isNew ? 'gray' : 'blue'}>
            {isNew ? t('wiki.newDraft') : t('wiki.editingVersion', { version: baseVersion })}
          </Badge>
          <SyncStatus status={status} />
          <People people={people} />
        </Group>
        <Group gap="xs" wrap="nowrap">
          <SegmentedControl
            size="xs"
            value={mode}
            onChange={(v) => {
              if (v === 'preview' && editor) setBody(editor.getJSON());
              setMode(v as 'edit' | 'preview');
            }}
            aria-label={t('wiki.mode')}
            data={[
              {
                value: 'edit',
                label: (
                  <Group gap={4} wrap="nowrap">
                    <IconPencil size={14} aria-hidden />
                    {t('wiki.modeEdit')}
                  </Group>
                ),
              },
              {
                value: 'preview',
                label: (
                  <Group gap={4} wrap="nowrap">
                    <IconEye size={14} aria-hidden />
                    {t('wiki.modePreview')}
                  </Group>
                ),
              },
            ]}
          />
          <Button variant="default" onClick={close}>
            {t('common.close')}
          </Button>
          <Popover position="bottom-end" withArrow trapFocus>
            <Popover.Target>
              <Button
                rightSection={<IconChevronDown size={14} />}
                disabled={!title.trim() || denied || outdated}
                loading={publishing}
              >
                {isNew ? t('wiki.publish') : t('wiki.update')}
              </Button>
            </Popover.Target>
            <Popover.Dropdown>
              <Stack gap="xs" w={300}>
                {people.length > 0 && (
                  <Text size="xs" c="dimmed">
                    {t('wiki.live.publishForAll')}
                  </Text>
                )}
                <TextInput
                  size="xs"
                  label={t('wiki.versionMessage')}
                  value={message}
                  onChange={(e) => setMessage(e.currentTarget.value)}
                />
                {!isNew && (
                  <Checkbox
                    size="xs"
                    label={t('wiki.minorEdit')}
                    checked={minor}
                    onChange={(e) => setMinor(e.currentTarget.checked)}
                  />
                )}
                <Button size="xs" onClick={() => void publish()} loading={publishing}>
                  {isNew ? t('wiki.publish') : t('wiki.update')}
                </Button>
              </Stack>
            </Popover.Dropdown>
          </Popover>
          <Menu position="bottom-end">
            <Menu.Target>
              <Button variant="subtle" px="xs" aria-label={t('issues.more')}>
                …
              </Button>
            </Menu.Target>
            <Menu.Dropdown>
              {earlier && (
                <Menu.Item onClick={() => setShowEarlier(true)}>{t('wiki.earlier.title')}</Menu.Item>
              )}
              <Menu.Item
                color="red"
                disabled={denied || outdated}
                onClick={() =>
                  modals.openConfirmModal({
                    title: isNew ? t('wiki.discardDraft') : t('wiki.live.discard'),
                    children: (
                      <Text size="sm">
                        {isNew
                          ? t('wiki.discardNewText')
                          : t('wiki.live.discardText', { version: baseVersion })}
                      </Text>
                    ),
                    labels: { confirm: t('wiki.discard'), cancel: t('common.cancel') },
                    confirmProps: { color: 'red' },
                    onConfirm: () => void discard(),
                  })
                }
              >
                {isNew ? t('wiki.discardDraft') : t('wiki.live.discard')}
              </Menu.Item>
            </Menu.Dropdown>
          </Menu>
        </Group>
      </Group>
      {denied && (
        <Alert color="red" variant="light" icon={<IconAlertTriangle size={16} />}>
          {t('wiki.live.denied')}
        </Alert>
      )}
      {outdated && (
        <Alert color="orange" variant="light" icon={<IconAlertTriangle size={16} />} data-outdated>
          <Group justify="space-between" gap="xs">
            <Text size="sm">{t('wiki.live.outdated')}</Text>
            <Button size="xs" variant="light" onClick={() => window.location.reload()}>
              {t('wiki.live.reload')}
            </Button>
          </Group>
        </Alert>
      )}
      {earlier && (
        <Alert color="gray" variant="light" data-earlier-draft>
          <Group justify="space-between" gap="xs">
            <Text size="sm">
              {t('wiki.earlier.text', { time: earlier.updated ? formatRelative(earlier.updated) : '' })}
            </Text>
            <Group gap="xs">
              <Button size="xs" variant="light" onClick={() => setShowEarlier(true)}>
                {t('wiki.earlier.show')}
              </Button>
              <Button size="xs" variant="subtle" color="gray" onClick={() => void dropEarlier()}>
                {t('wiki.earlier.delete')}
              </Button>
            </Group>
          </Group>
        </Alert>
      )}
      <ErrorAlert error={publishError} />
      {mode === 'preview' && (
        <Stack gap="md" data-preview>
          <Text size="xs" c="dimmed">
            {t('wiki.previewNote')}
          </Text>
          <Title order={1}>{title.trim() || t('wiki.pageTitle')}</Title>
          {isEmptyDoc(body) ? (
            <Text c="dimmed">{t('wiki.previewEmpty')}</Text>
          ) : (
            <DocView doc={body} context={context} />
          )}
        </Stack>
      )}
      {/* While the preview shows, the editor stays (with its undo history and selection). */}
      <Stack gap="md" display={mode === 'preview' ? 'none' : undefined}>
        <TextInput
          variant="unstyled"
          size="xl"
          placeholder={t('wiki.pageTitle')}
          value={title}
          readOnly={denied || outdated}
          maxLength={255}
          onChange={(e) => changeTitle(e.currentTarget.value)}
          onFocus={() => {
            titleFocused.current = true;
          }}
          onBlur={() => {
            titleFocused.current = false;
            if (remoteTitle.current !== null) {
              setTitle(remoteTitle.current);
              remoteTitle.current = null;
            }
          }}
          styles={{ input: { fontSize: 26, fontWeight: 700 } }}
          aria-label={t('wiki.pageTitle')}
          autoFocus={!title}
        />
        {/* An editor that cannot read the draft shows none of it: it would show an empty page. */}
        <Box hidden={outdated}>
          <Editor
            value={session.doc as Doc}
            onChange={setBody}
            minHeight={420}
            macros
            onUploadFile={pageUpload(id, me.maxAttachmentMb)}
            placeholder={t('wiki.startWriting')}
            renderMacro={context.macro}
            collaboration={extensions}
            onReady={setEditor}
            onOutdated={() => setOutdated(true)}
          />
        </Box>
      </Stack>
      {showEarlier && earlier && (
        <Modal opened onClose={() => setShowEarlier(false)} title={t('wiki.earlier.title')} size="xl">
          <Stack>
            <Text size="sm" c="dimmed">
              {t('wiki.earlier.explain')}
            </Text>
            <Box className="kogge-conflict-pane">
              <Text fw={700} mb="xs">
                {earlier.title}
              </Text>
              <DocView doc={earlier.body as Doc} context={{ attachmentUrl }} />
            </Box>
            <Group justify="flex-end">
              <Button variant="default" color="red" onClick={() => void dropEarlier()}>
                {t('wiki.earlier.delete')}
              </Button>
              <Button
                disabled={denied || outdated || !editor}
                onClick={() => {
                  if (!editor) return;
                  setShowEarlier(false);
                  // An earlier draft with content this Kogge does not know (it was updated meanwhile) would become
                  // an empty page.
                  try {
                    editor.schema.nodeFromJSON(earlier.body);
                  } catch {
                    setOutdated(true);
                    return;
                  }
                  // One change for everyone: the earlier draft becomes the content, loaded as the draft is (the
                  // editor checks shared drafts, but takes content that its rules only order differently).
                  editor.commands.setContent(earlier.body as Doc, { errorOnInvalidContent: false });
                }}
              >
                {t('wiki.earlier.use')}
              </Button>
            </Group>
          </Stack>
        </Modal>
      )}
    </Stack>
  );
}
