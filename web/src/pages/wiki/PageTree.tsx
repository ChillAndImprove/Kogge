// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Box,
  Button,
  Group,
  Loader,
  Text,
  Tooltip,
  UnstyledButton,
  VisuallyHidden,
} from '@mantine/core';
import { IconChevronDown, IconChevronRight, IconLock, IconPlus, IconPoint } from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { createContext, useContext, useEffect, useId, useRef, useState, type DragEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../../api/client';
import { notifyError, notifySuccess } from '../../components/notify';
import { pagePath, useShowMore, useTree, type PageRef, type TreeLevel } from './hooks';
import { dropPosition, moveInput, pathBelow, type DropPosition, type MoveInput } from './tree';

/** The type of drag data that identifies a page of the tree. */
const dragType = 'application/x-kogge-page';
/** How long a dragged page hovers over a collapsed page before it opens. */
const expandDelay = 600;

interface Dragged {
  id: number;
  title: string;
}

interface TreeContextValue {
  spaceKey: string;
  canEdit: boolean;
  current?: number;
  expandedIds: Set<number>;
  /** For the top level (0) and each page on the way to the page on screen, the page below it on the way. */
  pathBelow: Map<number, PageRef>;
  dragged: Dragged | null;
  setDragged: (d: Dragged | null) => void;
  move: (page: Dragged, input: MoveInput) => void;
  onCreateChild?: (parentId: number) => void;
}

const TreeContext = createContext<TreeContextValue | null>(null);

function useTreeContext() {
  const ctx = useContext(TreeContext);
  if (!ctx) throw new Error('TreeNode outside of PageTree');
  return ctx;
}

function TreeNode({
  node,
  parentId,
  depth,
  insideDragged,
}: {
  node: PageRef;
  parentId: number;
  depth: number;
  /** An ancestor of this page is being dragged: the page cannot take it. */
  insideDragged: boolean;
}) {
  const { t } = useTranslation();
  const ctx = useTreeContext();
  const [open, setOpen] = useState(ctx.expandedIds.has(node.id));
  const [drop, setDrop] = useState<DropPosition | null>(null);
  const expandTimer = useRef<number | undefined>(undefined);
  const children = useTree(ctx.spaceKey, node.id, open && node.hasChildren);
  const active = node.id === ctx.current;
  const movable = ctx.canEdit && node.kind === 'page' && node.status === 'current';
  const isDragged = ctx.dragged?.id === node.id;
  const blocked = insideDragged || isDragged;
  const shownChildren = open ? children.items.map((c) => c.id) : [];

  useEffect(() => {
    const timer = expandTimer;
    return () => window.clearTimeout(timer.current);
  }, []);

  const stopExpanding = () => {
    window.clearTimeout(expandTimer.current);
    expandTimer.current = undefined;
  };

  const onDragStart = (e: DragEvent) => {
    if (!movable) return;
    e.stopPropagation();
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData(dragType, String(node.id));
    ctx.setDragged({ id: node.id, title: node.title });
  };

  const onDragOver = (e: DragEvent<HTMLDivElement>) => {
    if (!ctx.dragged || blocked || !movable || !e.dataTransfer.types.includes(dragType)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    const rect = e.currentTarget.getBoundingClientRect();
    const position = dropPosition(e.clientY - rect.top, rect.height);
    setDrop(position);
    if (position === 'inside' && node.hasChildren && !open) {
      expandTimer.current ??= window.setTimeout(() => setOpen(true), expandDelay);
    } else {
      stopExpanding();
    }
  };

  const onDragLeave = (e: DragEvent<HTMLDivElement>) => {
    if (e.currentTarget.contains(e.relatedTarget as Node | null)) return;
    setDrop(null);
    stopExpanding();
  };

  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    stopExpanding();
    const position = drop;
    setDrop(null);
    if (!ctx.dragged || !position || blocked) return;
    ctx.move(ctx.dragged, moveInput(position, { id: node.id, parentId, shownChildren }));
    if (position === 'inside') setOpen(true);
  };

  return (
    <Box component="li">
      <Group
        gap={2}
        wrap="nowrap"
        className="kogge-tree-row"
        data-active={active || undefined}
        data-drop={(ctx.dragged && drop) || undefined}
        data-dragging={isDragged || undefined}
        style={{ paddingLeft: depth * 14, ['--kogge-tree-indent' as string]: `${depth * 14 + 22}px` }}
        draggable={movable}
        onDragStart={onDragStart}
        onDragEnd={() => ctx.setDragged(null)}
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
      >
        {node.hasChildren ? (
          <ActionIcon
            size="xs"
            variant="subtle"
            color="gray"
            onClick={() => setOpen((o) => !o)}
            aria-expanded={open}
            aria-label={t(open ? 'wiki.collapsePage' : 'wiki.expandPage', { title: node.title })}
          >
            {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
          </ActionIcon>
        ) : (
          <Box w={22} style={{ display: 'flex', justifyContent: 'center', opacity: 0.4 }} aria-hidden>
            <IconPoint size={10} />
          </Box>
        )}
        <UnstyledButton
          component={Link}
          to={pagePath(ctx.spaceKey, node.id, node.title)}
          style={{ flex: 1, minWidth: 0 }}
          py={4}
          aria-current={active ? 'page' : undefined}
          draggable={movable}
        >
          <Group gap={4} wrap="nowrap">
            <Text size="sm" truncate fw={active ? 650 : 400}>
              {node.title}
            </Text>
            {node.restricted && (
              <IconLock
                size={12}
                style={{ flexShrink: 0, opacity: 0.6 }}
                role="img"
                aria-label={t('wiki.restricted')}
              />
            )}
          </Group>
        </UnstyledButton>
        {ctx.onCreateChild && movable && (
          <Tooltip label={t('wiki.createChildPage', { title: node.title })} openDelay={400}>
            <ActionIcon
              size="sm"
              variant="subtle"
              color="gray"
              className="kogge-tree-add"
              aria-label={t('wiki.createChildPage', { title: node.title })}
              onClick={() => ctx.onCreateChild?.(node.id)}
            >
              <IconPlus size={14} />
            </ActionIcon>
          </Tooltip>
        )}
      </Group>
      {open && children.isLoading && <Loader size="xs" ml={depth * 14 + 24} />}
      {open && (
        <TreeChildren
          parentId={node.id}
          parentTitle={node.title}
          depth={depth + 1}
          level={children}
          insideDragged={blocked}
        />
      )}
    </Box>
  );
}

/**
 * The child pages of a page, or the top level, as far as they are loaded, with "Show more" for the next ones. The
 * page on the way to the page on screen follows them as long as it is not loaded yet, so that the tree shows where
 * the reader is even among thousands of siblings.
 */
function TreeChildren({
  parentId,
  parentTitle,
  depth,
  level,
  insideDragged,
}: {
  parentId: number;
  /** The title of the parent page; none at the top level. */
  parentTitle?: string;
  depth: number;
  level: TreeLevel;
  insideDragged: boolean;
}) {
  const { t } = useTranslation();
  const ctx = useTreeContext();
  const { items } = level;
  const { listRef, showMore } = useShowMore<HTMLUListElement>(
    items.length,
    level.loadMore,
    level.loadingMore,
  );
  const onPath = ctx.pathBelow.get(parentId);
  const pinned = onPath && level.hasMore && !items.some((c) => c.id === onPath.id) ? onPath : undefined;
  if (items.length === 0 && !pinned) return null;
  const shown = { shown: items.length, total: level.total };
  return (
    <Box component="ul" className="kogge-tree-list" ref={listRef}>
      {items.map((c) => (
        <TreeNode key={c.id} node={c} parentId={parentId} depth={depth} insideDragged={insideDragged} />
      ))}
      {level.hasMore && (
        <Box component="li" pl={depth * 14 + 22}>
          <Button
            variant="subtle"
            size="compact-xs"
            onClick={() => void showMore()}
            aria-busy={level.loadingMore || undefined}
            aria-label={parentTitle ? t('wiki.treeShowMoreIn', { ...shown, title: parentTitle }) : undefined}
            leftSection={level.loadingMore ? <Loader size={10} /> : undefined}
          >
            {t('wiki.treeShowMore', shown)}
          </Button>
        </Box>
      )}
      {pinned && (
        <TreeNode
          key={pinned.id}
          node={pinned}
          parentId={parentId}
          depth={depth}
          insideDragged={insideDragged}
        />
      )}
    </Box>
  );
}

/**
 * The page tree of a space; children load when a page opens, a hundred at a time. People who may edit the space add
 * pages below a page with its "+" and rearrange pages by dragging them; "Move" in a page's menu does the same with
 * the keyboard.
 */
export function PageTree({
  spaceKey,
  current,
  path,
  canEdit = false,
  onCreateChild,
}: {
  spaceKey: string;
  current?: number;
  /** The pages above the page on screen, the top first, and the page itself: the tree opens them. */
  path: PageRef[];
  canEdit?: boolean;
  onCreateChild?: (parentId: number) => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const hintId = useId();
  const roots = useTree(spaceKey, 0);
  const [dragged, setDragged] = useState<Dragged | null>(null);
  const move = useMutation({
    mutationFn: ({ page, input }: { page: Dragged; input: MoveInput }) =>
      unwrap(api.POST('/api/v1/wiki/pages/{id}/move', { params: { path: { id: page.id } }, body: input })),
    onSuccess: (p) => {
      void client.invalidateQueries({ queryKey: ['page'] });
      notifySuccess(t('wiki.pageMoved', { title: p.title }));
    },
    onError: notifyError,
  });
  const value: TreeContextValue = {
    spaceKey,
    canEdit,
    current,
    expandedIds: new Set(path.map((p) => p.id)),
    pathBelow: pathBelow(path, spaceKey),
    dragged,
    setDragged,
    move: (page, input) => move.mutate({ page, input }),
    onCreateChild,
  };
  return (
    <TreeContext.Provider value={value}>
      <Box component="nav" aria-label={t('wiki.pageTree')} aria-describedby={canEdit ? hintId : undefined}>
        {canEdit && <VisuallyHidden id={hintId}>{t('wiki.pageTreeHint')}</VisuallyHidden>}
        {/* Another page on screen opens the tree anew along its way. */}
        <TreeChildren
          key={path.map((p) => p.id).join('.')}
          parentId={0}
          depth={0}
          level={roots}
          insideDragged={false}
        />
      </Box>
    </TreeContext.Provider>
  );
}
