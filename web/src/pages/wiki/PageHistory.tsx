// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Badge,
  Box,
  Button,
  Checkbox,
  Group,
  Loader,
  Paper,
  Stack,
  Table,
  Text,
  Title,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { diffWordsWithSpace } from 'diff';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatDateTime } from '../../app/format';
import { DocView, type DocNode } from '../../components/editor/DocView';
import type { Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import { latexOf } from '../../components/math/latex';
import { notifyError } from '../../components/notify';
import { UserBadge } from '../issues/shared';
import { attachmentUrl, pagePath, usePage, useShowMore } from './hooks';
import { nextOffset, uniqueItems } from './paging';

/** Versions per load of the history: pages that scripts change have thousands of them. */
const historyPageSize = 50;

/**
 * The text of a block for comparing versions, with what stands in it as a whole (a formula as $…$, a mention, an
 * issue, a status, a date, an emoji): a change of only that would otherwise not show.
 */
function inlineText(node: DocNode): string {
  const a = node.attrs ?? {};
  switch (node.type) {
    case 'text':
      return node.text ?? '';
    case 'inlineMath':
      return `$${latexOf(a)}$`;
    case 'math':
      return `$$${latexOf(a)}$$`;
    case 'mention':
      return `@${a.label ?? ''}`;
    case 'issue':
      return String(a.key ?? '');
    case 'status':
      return `[${a.text ?? ''}]`;
    case 'date':
      return String(a.value ?? '');
    case 'emoji':
      return `:${a.name ?? ''}:`;
    default:
      return (node.content ?? []).map(inlineText).join('');
  }
}

/** The text of a document, one line per block, for comparing versions. */
export function versionText(doc: Doc | undefined): string {
  const lines: string[] = [];
  const walk = (nodes: DocNode[] | undefined, prefix: string) => {
    for (const n of nodes ?? []) {
      switch (n.type) {
        case 'bulletList':
        case 'orderedList':
        case 'taskList':
          walk(n.content, prefix + '  ');
          break;
        case 'listItem':
        case 'taskItem':
          walk(n.content, prefix);
          break;
        case 'table':
          for (const row of n.content ?? [])
            lines.push(prefix + (row.content ?? []).map(inlineText).join(' | '));
          break;
        case 'heading':
          lines.push(prefix + '#'.repeat(Number(n.attrs?.level ?? 2)) + ' ' + inlineText(n));
          break;
        case 'macro':
          lines.push(`${prefix}[${String(n.attrs?.name)}]`);
          walk(n.content, prefix + '  ');
          break;
        case 'panel':
        case 'blockquote':
        case 'expand':
          walk(n.content, prefix + '> ');
          break;
        case 'columns':
          walk(n.content, prefix);
          break;
        case 'column':
          walk(n.content, prefix + '│ ');
          break;
        case 'math':
          // As Markdown writes it: the LaTeX between lines of $$.
          for (const line of ['$$', ...latexOf(n.attrs).split('\n'), '$$']) lines.push(prefix + line);
          break;
        default:
          lines.push(prefix + inlineText(n));
      }
    }
  };
  walk(doc?.content, '');
  return lines.join('\n');
}

function Diff({ id, from, to }: { id: number; from: number; to: number }) {
  const { t } = useTranslation();
  const load = (v: number) => () =>
    unwrap(api.GET('/api/v1/wiki/pages/{id}/versions/{version}', { params: { path: { id, version: v } } }));
  const a = useQuery({ queryKey: ['page', id, 'version', from], queryFn: load(from) });
  const b = useQuery({ queryKey: ['page', id, 'version', to], queryFn: load(to) });
  if (!a.data || !b.data) return null;
  const titleChanged = a.data.title !== b.data.title;
  const parts = diffWordsWithSpace(versionText(a.data.body as Doc), versionText(b.data.body as Doc));
  return (
    <Paper withBorder p="md">
      <Title order={5} mb="xs">
        {t('wiki.compareVersions', { from, to })}
      </Title>
      {titleChanged && (
        <Text mb="sm">
          <del className="kogge-del">{a.data.title}</del> <ins className="kogge-ins">{b.data.title}</ins>
        </Text>
      )}
      <Box style={{ whiteSpace: 'pre-wrap', fontSize: 14, lineHeight: 1.6 }}>
        {parts.map((p, i) =>
          p.added ? (
            <ins key={i} className="kogge-ins">
              {p.value}
            </ins>
          ) : p.removed ? (
            <del key={i} className="kogge-del">
              {p.value}
            </del>
          ) : (
            <span key={i}>{p.value}</span>
          ),
        )}
      </Box>
    </Paper>
  );
}

export function PageHistory() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const { key = '', pageId = '' } = useParams();
  const id = Number(pageId);
  const page = usePage(id);
  // The newest versions first; older ones load on demand and stay, so that any two can be compared.
  const versions = useInfiniteQuery({
    queryKey: ['page', id, 'versions'],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/api/v1/wiki/pages/{id}/versions', {
          params: { path: { id }, query: { limit: historyPageSize, offset: pageParam } },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (_last, pages) => nextOffset(pages),
  });
  const list = useMemo(() => uniqueItems(versions.data?.pages ?? [], (v) => v.version), [versions.data]);
  const total = versions.data?.pages.at(-1)?.total ?? 0;
  const { listRef, showMore } = useShowMore<HTMLTableSectionElement>(
    list.length,
    async () => {
      const next = await versions.fetchNextPage();
      return !next.isError && uniqueItems(next.data?.pages ?? [], (v) => v.version).length > list.length;
    },
    versions.isFetchingNextPage,
  );
  const [selected, setSelected] = useState<number[]>([]);
  const [viewing, setViewing] = useState<number | null>(null);
  const viewed = useQuery({
    queryKey: ['page', id, 'version', viewing],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/wiki/pages/{id}/versions/{version}', {
          params: { path: { id, version: viewing! } },
        }),
      ),
    enabled: viewing !== null,
  });
  const restore = useMutation({
    mutationFn: (v: number) =>
      unwrap(
        api.POST('/api/v1/wiki/pages/{id}/versions/{version}/restore', {
          params: { path: { id, version: v } },
        }),
      ),
    onSuccess: (p) => {
      void client.invalidateQueries({ queryKey: ['page'] });
      navigate(pagePath(p.space.key, p.id, p.title));
    },
    onError: notifyError,
  });
  if (page.error) return <ErrorAlert error={page.error} />;
  if (versions.error) return <ErrorAlert error={versions.error} />;
  if (!page.data) return null;
  const p = page.data;
  const toggle = (v: number) =>
    setSelected((s) =>
      (s.includes(v) ? s.filter((x) => x !== v) : [...s, v].slice(-2)).sort((x, y) => x - y),
    );
  return (
    <Stack gap="md">
      <Group justify="space-between">
        <Stack gap={0}>
          <Anchor component={Link} to={pagePath(key, id, p.title)} size="sm">
            ← {p.title}
          </Anchor>
          <Title order={2}>{t('wiki.history')}</Title>
        </Stack>
        <Button disabled={selected.length !== 2} variant="light" onClick={() => setViewing(null)}>
          {t('wiki.compareSelected')}
        </Button>
      </Group>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th w={40} />
            <Table.Th>{t('wiki.version')}</Table.Th>
            <Table.Th>{t('wiki.author')}</Table.Th>
            <Table.Th>{t('wiki.date')}</Table.Th>
            <Table.Th>{t('wiki.message')}</Table.Th>
            <Table.Th w={180} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody ref={listRef}>
          {list.map((v) => (
            <Table.Tr key={v.version}>
              <Table.Td>
                <Checkbox
                  checked={selected.includes(v.version)}
                  onChange={() => toggle(v.version)}
                  aria-label={t('wiki.versionN', { version: v.version })}
                />
              </Table.Td>
              <Table.Td>
                <Group gap={6}>
                  <Anchor component="button" type="button" size="sm" onClick={() => setViewing(v.version)}>
                    {t('wiki.versionN', { version: v.version })}
                  </Anchor>
                  {v.version === p.version && <Badge size="xs">{t('wiki.current')}</Badge>}
                  {v.minor && (
                    <Badge size="xs" variant="outline" color="gray">
                      {t('wiki.minor')}
                    </Badge>
                  )}
                </Group>
              </Table.Td>
              <Table.Td>
                {v.author ? <UserBadge user={v.author} size="xs" /> : '—'}
                {v.contributors.length > 0 && (
                  <Text size="xs" c="dimmed" data-contributors>
                    {t('wiki.contributors', { names: v.contributors.map((c) => c.displayName).join(', ') })}
                  </Text>
                )}
              </Table.Td>
              <Table.Td>{formatDateTime(v.created)}</Table.Td>
              <Table.Td>
                <Text size="sm" lineClamp={1}>
                  {v.message}
                </Text>
              </Table.Td>
              <Table.Td>
                <Group gap={4} wrap="nowrap">
                  {v.version > 1 && (
                    <Button
                      size="compact-xs"
                      variant="subtle"
                      onClick={() => setSelected([v.version - 1, v.version])}
                    >
                      {t('wiki.changes')}
                    </Button>
                  )}
                  {v.version !== p.version && p.permissions.edit && (
                    <Button
                      size="compact-xs"
                      variant="subtle"
                      onClick={() =>
                        modals.openConfirmModal({
                          title: t('wiki.restoreVersion'),
                          children: (
                            <Text size="sm">{t('wiki.restoreVersionText', { version: v.version })}</Text>
                          ),
                          labels: { confirm: t('wiki.restore'), cancel: t('common.cancel') },
                          onConfirm: () => restore.mutate(v.version),
                        })
                      }
                    >
                      {t('wiki.restore')}
                    </Button>
                  )}
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      {versions.hasNextPage && (
        <Group justify="center">
          <Button
            variant="default"
            onClick={() => void showMore()}
            aria-busy={versions.isFetchingNextPage || undefined}
            leftSection={versions.isFetchingNextPage ? <Loader size={12} /> : undefined}
          >
            {t('wiki.olderVersions', { shown: list.length, total })}
          </Button>
        </Group>
      )}
      {selected.length === 2 && viewing === null && <Diff id={id} from={selected[0]} to={selected[1]} />}
      {viewing !== null && viewed.data && (
        <Paper withBorder p="md">
          <Group justify="space-between" mb="sm">
            <Title order={4}>
              {viewed.data.title} · {t('wiki.versionN', { version: viewing })}
            </Title>
            <Button size="xs" variant="subtle" onClick={() => setViewing(null)}>
              {t('common.close')}
            </Button>
          </Group>
          <DocView doc={viewed.data.body as Doc} context={{ attachmentUrl }} />
        </Paper>
      )}
    </Stack>
  );
}
