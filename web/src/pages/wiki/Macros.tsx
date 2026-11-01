// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Badge,
  Box,
  Button,
  Group,
  Loader,
  Paper,
  Stack,
  Table,
  Text,
} from '@mantine/core';
import { IconChevronDown, IconChevronRight, IconPaperclip, IconPoint } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatRelative } from '../../app/format';
import {
  DocNodes,
  DocView,
  headingId,
  IssueChip,
  nodeText,
  type DocContext,
  type DocNode,
} from '../../components/editor/DocView';
import type { Doc } from '../../components/editor/Editor';
import { MermaidDiagram } from '../../components/MermaidDiagram';
import { runPath } from '../builds/hooks';
import { StateIcon } from '../builds/status';
import { CodeLines } from '../code/components';
import '../code/code.css';
import { PriorityIcon, StatusBadge, TypeIcon, UserBadge } from '../issues/shared';
import { attachmentUrl, pagePath, useShowMore, useTree, type PageRef } from './hooks';

function MacroBox({ children }: { children: ReactNode }) {
  return (
    <Box my="sm" data-macro-render>
      {children}
    </Box>
  );
}

function Toc({ doc, maxLevel }: { doc: Doc; maxLevel: number }) {
  const headings = (doc.content ?? []).filter(
    (n) => n.type === 'heading' && Number(n.attrs?.level ?? 2) <= maxLevel,
  );
  if (headings.length === 0) return null;
  const min = Math.min(...headings.map((h) => Number(h.attrs?.level ?? 2)));
  return (
    <MacroBox>
      <Stack gap={2}>
        {headings.map((h, i) => (
          <Anchor key={i} href={`#${headingId(h)}`} size="sm" pl={(Number(h.attrs?.level ?? 2) - min) * 16}>
            {nodeText(h)}
          </Anchor>
        ))}
      </Stack>
    </MacroBox>
  );
}

/**
 * The child pages of a page (children macro: depth levels of them; page-tree macro: a tree whose pages open on
 * demand), a hundred at a time with "Show more" for the next ones.
 */
function ChildList({
  spaceKey,
  pageId,
  depth,
  collapsible = false,
}: {
  spaceKey: string;
  pageId: number;
  depth: number;
  collapsible?: boolean;
}) {
  const { t } = useTranslation();
  const tree = useTree(spaceKey, pageId);
  const { listRef, showMore } = useShowMore<HTMLUListElement>(
    tree.items.length,
    tree.loadMore,
    tree.loadingMore,
  );
  if (tree.items.length === 0) return null;
  return (
    <Box
      component="ul"
      my={4}
      ref={listRef}
      style={collapsible ? { listStyle: 'none', paddingLeft: 0 } : undefined}
    >
      {tree.items.map((c) =>
        collapsible ? (
          <ChildTreeItem key={c.id} spaceKey={spaceKey} page={c} />
        ) : (
          <li key={c.id}>
            <Anchor component={Link} to={pagePath(spaceKey, c.id, c.title)} size="sm">
              {c.title}
            </Anchor>
            {depth > 1 && c.hasChildren && <ChildList spaceKey={spaceKey} pageId={c.id} depth={depth - 1} />}
          </li>
        ),
      )}
      {tree.hasMore && (
        <li>
          <Button
            variant="subtle"
            size="compact-xs"
            onClick={() => void showMore()}
            aria-busy={tree.loadingMore || undefined}
            leftSection={tree.loadingMore ? <Loader size={10} /> : undefined}
          >
            {t('wiki.treeShowMore', { shown: tree.items.length, total: tree.total })}
          </Button>
        </li>
      )}
    </Box>
  );
}

/** A page of the page-tree macro: pages with children open and close, and load their children when they open. */
function ChildTreeItem({ spaceKey, page }: { spaceKey: string; page: PageRef }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <li>
      <Group gap={2} wrap="nowrap">
        {page.hasChildren ? (
          <ActionIcon
            size="xs"
            variant="subtle"
            color="gray"
            onClick={() => setOpen((o) => !o)}
            aria-expanded={open}
            aria-label={t(open ? 'wiki.collapsePage' : 'wiki.expandPage', { title: page.title })}
          >
            {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
          </ActionIcon>
        ) : (
          <Box w={22} style={{ display: 'flex', justifyContent: 'center', opacity: 0.4 }} aria-hidden>
            <IconPoint size={10} />
          </Box>
        )}
        <Anchor component={Link} to={pagePath(spaceKey, page.id, page.title)} size="sm">
          {page.title}
        </Anchor>
      </Group>
      {open && (
        <Box pl={22}>
          <ChildList spaceKey={spaceKey} pageId={page.id} depth={1} collapsible />
        </Box>
      )}
    </li>
  );
}

function useMacroPage(id: number | undefined) {
  return useQuery({
    queryKey: ['page', id],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/pages/{id}', { params: { path: { id: id! } } })),
    enabled: !!id,
    retry: false,
  });
}

function Include({ pageId, context, depth }: { pageId: number; context: DocContext; depth: number }) {
  const { t } = useTranslation();
  const page = useMacroPage(pageId);
  if (depth > 3)
    return (
      <Text size="sm" c="dimmed">
        {t('macros.tooDeep')}
      </Text>
    );
  if (page.isLoading) return <Loader size="xs" />;
  if (!page.data)
    return (
      <Text size="sm" c="dimmed">
        {t('macros.notVisible')}
      </Text>
    );
  return (
    <MacroBox>
      <DocView doc={page.data.body as Doc} context={context} />
    </MacroBox>
  );
}

function findExcerpt(doc: Doc): DocNode[] | null {
  let found: DocNode[] | null = null;
  const walk = (nodes: DocNode[] | undefined) => {
    for (const n of nodes ?? []) {
      if (found) return;
      if (n.type === 'macro' && n.attrs?.name === 'excerpt') found = n.content ?? [];
      else walk(n.content);
    }
  };
  walk(doc.content);
  return found;
}

function ExcerptInclude({ pageId, context }: { pageId: number; context: DocContext }) {
  const { t } = useTranslation();
  const page = useMacroPage(pageId);
  if (page.isLoading) return <Loader size="xs" />;
  if (!page.data)
    return (
      <Text size="sm" c="dimmed">
        {t('macros.notVisible')}
      </Text>
    );
  const excerpt = findExcerpt(page.data.body as Doc);
  return (
    <MacroBox>
      {excerpt ? (
        <DocNodes nodes={excerpt} context={context} />
      ) : (
        <Text size="sm" c="dimmed">
          {t('macros.noExcerpt')}
        </Text>
      )}
      <Anchor component={Link} to={pagePath(page.data.space.key, page.data.id, page.data.title)} size="xs">
        {page.data.title}
      </Anchor>
    </MacroBox>
  );
}

function PageList({ query }: { query: { space?: string; label?: string; max: number; recent?: boolean } }) {
  const { t } = useTranslation();
  // The author's maximum, within what the lists hand out (100 recent pages, 500 found ones): beyond it, the
  // request would be refused and the macro would say there are no pages.
  const limit = (most: number) =>
    Math.max(1, Math.min(Number.isFinite(query.max) ? Math.floor(query.max) : 10, most));
  const res = useQuery({
    queryKey: ['page', 'macro-list', query],
    queryFn: async () =>
      query.recent
        ? unwrap(
            api.GET('/api/v1/wiki/recent', {
              params: { query: { mode: 'updated', space: query.space, limit: limit(100) } },
            }),
          )
        : (
            await unwrap(
              api.GET('/api/v1/wiki/search', {
                params: { query: { label: query.label, space: query.space, limit: limit(500) } },
              }),
            )
          ).pages,
  });
  if (res.isLoading) return <Loader size="xs" />;
  if (!res.data?.length)
    return (
      <Text size="sm" c="dimmed">
        {t('macros.noPages')}
      </Text>
    );
  return (
    <MacroBox>
      <Stack gap={4}>
        {res.data.map((p) => (
          <Group key={p.id} gap="xs" wrap="nowrap" align="baseline">
            <Anchor component={Link} to={p.url} size="sm" truncate>
              {p.title}
            </Anchor>
            <Text size="xs" c="dimmed" style={{ flexShrink: 0 }}>
              {p.space.name} · {formatRelative(p.updated)}
              {p.updatedBy ? ` · ${p.updatedBy.displayName}` : ''}
            </Text>
          </Group>
        ))}
      </Stack>
    </MacroBox>
  );
}

function AttachmentList({ pageId }: { pageId: number }) {
  const { t } = useTranslation();
  const res = useQuery({
    queryKey: ['page', pageId, 'attachments'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/wiki/pages/{id}/attachments', { params: { path: { id: pageId } } })),
  });
  if (!res.data?.length)
    return (
      <Text size="sm" c="dimmed">
        {t('wiki.noAttachments')}
      </Text>
    );
  return (
    <MacroBox>
      <Stack gap={2}>
        {res.data.map((a) => (
          <Group key={a.id} gap={6}>
            <IconPaperclip size={14} />
            <Anchor href={a.url} target="_blank" size="sm">
              {a.filename}
            </Anchor>
          </Group>
        ))}
      </Stack>
    </MacroBox>
  );
}

function IssueCard({ issueKey }: { issueKey: string }) {
  const issue = useQuery({
    queryKey: ['issue', issueKey, 'chip'],
    queryFn: () => unwrap(api.GET('/api/v1/issues/{key}', { params: { path: { key: issueKey } } })),
    retry: false,
  });
  if (!issue.data) return <IssueChip issueKey={issueKey} />;
  const i = issue.data;
  return (
    <Paper withBorder p="xs" my="xs" maw={560} data-macro-render>
      <Group gap="xs" wrap="nowrap">
        <TypeIcon type={i.type} size={16} />
        <Anchor component={Link} to={`/browse/${i.key}`} fw={600} size="sm">
          {i.key}
        </Anchor>
        <Text size="sm" truncate style={{ flex: 1 }}>
          {i.summary}
        </Text>
        <PriorityIcon priority={i.priority} />
        <StatusBadge status={i.status} />
      </Group>
      {i.assignee && (
        <Box mt={4}>
          <UserBadge user={i.assignee} size="xs" />
        </Box>
      )}
    </Paper>
  );
}

function IssueList({ query, max, count }: { query: string; max: number; count?: boolean }) {
  const { t } = useTranslation();
  const res = useQuery({
    queryKey: ['issue', 'search', query, 'macro', max],
    queryFn: () =>
      unwrap(api.GET('/api/v1/search', { params: { query: { q: query, limit: count ? 1 : max } } })),
    retry: false,
  });
  if (res.error)
    return (
      <Text size="sm" c="red">
        {t('macros.queryError')}
      </Text>
    );
  if (!res.data) return <Loader size="xs" />;
  if (count) {
    return (
      <Anchor component={Link} to={`/issues/search?q=${encodeURIComponent(query)}`}>
        <Badge variant="light">{t('issues.issueCount', { count: res.data.total })}</Badge>
      </Anchor>
    );
  }
  return (
    <MacroBox>
      <Table withTableBorder>
        <Table.Tbody>
          {res.data.issues.map((i) => (
            <Table.Tr key={i.id}>
              <Table.Td w={24}>
                <TypeIcon type={i.type} size={16} />
              </Table.Td>
              <Table.Td w={100}>
                <Anchor component={Link} to={`/browse/${i.key}`} size="sm">
                  {i.key}
                </Anchor>
              </Table.Td>
              <Table.Td>{i.summary}</Table.Td>
              <Table.Td w={140}>
                <StatusBadge status={i.status} />
              </Table.Td>
              <Table.Td w={160}>{i.assignee ? <UserBadge user={i.assignee} size="xs" /> : '—'}</Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <Anchor component={Link} to={`/issues/search?q=${encodeURIComponent(query)}`} size="xs">
        {t('issues.issueCount', { count: res.data.total })}
      </Anchor>
    </MacroBox>
  );
}

function splitRepository(value: string | undefined) {
  const [key = '', slug = ''] = (value ?? '').split('/');
  return { key, slug };
}

function NotVisible() {
  const { t } = useTranslation();
  return (
    <Text size="sm" c="dimmed">
      {t('macros.repositoryNotVisible')}
    </Text>
  );
}

/** The latest run of a pipeline on a branch. */
function BuildStatus({
  repository,
  pipeline,
  branch,
}: {
  repository?: string;
  pipeline?: string;
  branch?: string;
}) {
  const { t } = useTranslation();
  const { key, slug } = splitRepository(repository);
  const runs = useQuery({
    queryKey: ['run', 'macro', key, slug, pipeline, branch],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/runs', {
          params: { path: { key, slug }, query: { pipeline, branch, limit: 1, offset: 0 } },
        }),
      ),
    enabled: !!key && !!slug,
    retry: false,
  });
  if (runs.error) return <NotVisible />;
  const r = runs.data?.items[0];
  return (
    <MacroBox>
      <Paper withBorder p="xs" radius="md" display="inline-block">
        {!runs.data ? (
          <Loader size="xs" />
        ) : !r ? (
          <Text size="sm" c="dimmed">
            {t('macros.noRuns')}
          </Text>
        ) : (
          <Group gap="xs" wrap="nowrap">
            <StateIcon state={r.state} size={18} />
            <Anchor component={Link} to={runPath({ projectKey: key, slug }, r.number)} size="sm" fw={500}>
              {r.name} #{r.number}
            </Anchor>
            <Text size="xs" c="dimmed">
              {key}/{slug}
              {r.branch ? ` · ${r.branch}` : ''} · {formatRelative(r.created)}
            </Text>
          </Group>
        )}
      </Paper>
    </MacroBox>
  );
}

/** Lines of a file at a branch, tag or commit; lines are "10-25" or "7". */
function CodeSnippet({
  repository,
  path,
  gitRef,
  lines,
}: {
  repository?: string;
  path?: string;
  gitRef?: string;
  lines?: string;
}) {
  const { key, slug } = splitRepository(repository);
  const file = useQuery({
    queryKey: ['repository', 'macro-file', key, slug, gitRef, path],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/file', {
          params: { path: { key, slug }, query: { ref: gitRef, path } },
        }),
      ),
    enabled: !!key && !!slug && !!path,
    retry: false,
  });
  if (file.error) return <NotVisible />;
  if (!file.data) return null;
  const m = /^(\d+)(?:\s*-\s*(\d+))?$/.exec((lines ?? '').trim());
  const from = m ? Math.max(1, Number(m[1])) : 1;
  const to = m ? Math.min(file.data.lines.length, Number(m[2] ?? m[1])) : file.data.lines.length;
  const src = `/code/${key.toLowerCase()}/${slug}/src/${path}${gitRef ? `?at=${encodeURIComponent(gitRef)}` : ''}#L${from}`;
  return (
    <MacroBox>
      <Paper withBorder radius="md" style={{ overflow: 'hidden' }}>
        <Group
          justify="space-between"
          px="sm"
          py={4}
          style={{ borderBottom: '1px solid var(--mantine-color-default-border)' }}
        >
          <Anchor component={Link} to={src} size="xs" ff="monospace">
            {key}/{slug}: {path}
            {m ? `:${from}${to !== from ? `-${to}` : ''}` : ''}
          </Anchor>
          <Text size="xs" c="dimmed" ff="monospace">
            {file.data.ref}
          </Text>
        </Group>
        <CodeLines lines={file.data.lines.slice(from - 1, to)} first={from} />
      </Paper>
    </MacroBox>
  );
}

/** The README of a repository. */
function Readme({ repository, gitRef }: { repository?: string; gitRef?: string }) {
  const { key, slug } = splitRepository(repository);
  const tree = useQuery({
    queryKey: ['repository', 'macro-tree', key, slug, gitRef],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/tree', {
          params: { path: { key, slug }, query: { ref: gitRef } },
        }),
      ),
    enabled: !!key && !!slug,
    retry: false,
  });
  const readme = tree.data?.readme;
  const file = useQuery({
    queryKey: ['repository', 'macro-file', key, slug, gitRef, readme],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/file', {
          params: { path: { key, slug }, query: { ref: gitRef, path: readme } },
        }),
      ),
    enabled: !!readme,
    retry: false,
  });
  if (tree.error || file.error) return <NotVisible />;
  if (!file.data) return null;
  return (
    <MacroBox>
      <Paper withBorder radius="md" p="md">
        {file.data.markdown ? (
          <DocView doc={file.data.markdown as Doc} />
        ) : (
          <CodeLines lines={file.data.lines} />
        )}
      </Paper>
    </MacroBox>
  );
}

/** Renders the wiki macros of a page for a reader (with the reader's permissions). */
export function wikiContext(
  page: { id: number; space: { key: string }; body: unknown },
  extra?: Partial<DocContext>,
  depth = 0,
): DocContext {
  const ctx: DocContext = {
    attachmentUrl,
    ...extra,
    macro: (node, inline) => {
      const p = (node.attrs?.params ?? {}) as Record<string, unknown>;
      const num = (k: string, d: number) => (p[k] === undefined ? d : Number(p[k]));
      const str = (k: string) => (p[k] === undefined ? undefined : String(p[k]));
      switch (node.attrs?.name) {
        case 'toc':
          return <Toc doc={page.body as Doc} maxLevel={num('maxLevel', 3)} />;
        case 'children':
          return <ChildList spaceKey={page.space.key} pageId={page.id} depth={num('depth', 1)} />;
        case 'page-tree':
          // A tree that opens on demand: opening every level at once loaded each page of it with children.
          return <ChildList spaceKey={page.space.key} pageId={num('root', page.id)} depth={1} collapsible />;
        case 'include':
          return (
            <Include
              pageId={num('page', 0)}
              context={wikiContext(page, extra, depth + 1)}
              depth={depth + 1}
            />
          );
        case 'excerpt':
          return <DocNodes nodes={node.content} context={ctx} />;
        case 'excerpt-include':
          return <ExcerptInclude pageId={num('page', 0)} context={ctx} />;
        case 'recently-updated':
          return <PageList query={{ space: str('space'), max: num('max', 10), recent: true }} />;
        case 'content-by-label':
          return <PageList query={{ label: str('label'), space: str('space'), max: num('max', 20) }} />;
        case 'attachments':
          return <AttachmentList pageId={page.id} />;
        case 'issue':
          return str('display') === 'inline' || inline ? (
            <IssueChip issueKey={str('key') ?? ''} />
          ) : (
            <IssueCard issueKey={str('key') ?? ''} />
          );
        case 'issue-list':
          return <IssueList query={str('query') ?? ''} max={num('max', 20)} />;
        case 'issue-count':
          return <IssueList query={str('query') ?? ''} max={1} count />;
        case 'anchor':
          return <span id={str('name')} />;
        case 'build-status':
          return (
            <BuildStatus repository={str('repository')} pipeline={str('pipeline')} branch={str('branch')} />
          );
        case 'code-snippet':
          return (
            <CodeSnippet
              repository={str('repository')}
              path={str('path')}
              gitRef={str('ref')}
              lines={str('lines')}
            />
          );
        case 'readme':
          return <Readme repository={str('repository')} gitRef={str('ref')} />;
        case 'mermaid':
          return <MermaidDiagram code={str('code') ?? ''} />;
        default:
          return (
            <Text size="sm" c="dimmed" className="kogge-unsupported">
              [{String(node.attrs?.name ?? '?')}]
            </Text>
          );
      }
    },
  };
  return ctx;
}
