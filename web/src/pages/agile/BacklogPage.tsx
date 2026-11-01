// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  closestCorners,
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  useDroppable,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragStartEvent,
} from '@dnd-kit/core';
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import {
  ActionIcon,
  Badge,
  Box,
  Button,
  Card as MCard,
  Drawer,
  Grid,
  Group,
  Menu,
  Paper,
  Progress,
  Select,
  Stack,
  Text,
  TextInput,
  Tooltip,
  UnstyledButton,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconChevronDown, IconChevronRight, IconDots, IconListCheck, IconPlus } from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState, type MouseEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatDate } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError } from '../../components/notify';
import { BulkChangeModal } from '../issues/BulkChange';
import { useCreateIssue } from '../issues/CreateIssue';
import { IssueView } from '../issues/IssueView';
import { PriorityIcon, StatusBadge, TypeIcon, useProject } from '../issues/shared';
import { BoardToolbar, useBoardFilters, useQuickFilters } from './BoardPage';
import { BoardNav } from './BoardNav';
import {
  epicColor,
  formatEstimate,
  useBacklog,
  useMoveIssues,
  useRank,
  type BacklogView,
  type Board,
  type Card,
  type Sprint,
} from './hooks';
import { AssigneeAvatar, EpicBadge } from './IssueCard';
import { CompleteSprintModal, SprintModal } from './SprintDialogs';
import { LinkedPageModal, type LinkedPageKind } from '../issues/LinkedPage';

const BACKLOG = 'backlog';

function containerOf(view: BacklogView, key: string): string | null {
  if (view.backlog.some((c) => c.key === key)) return BACKLOG;
  for (const s of view.sprints) if (s.cards.some((c) => c.key === key)) return `sprint|${s.sprint.id}`;
  return null;
}

function cardsOf(view: BacklogView, container: string): Card[] {
  if (container === BACKLOG) return view.backlog;
  const id = Number(container.split('|')[1]);
  return view.sprints.find((s) => s.sprint.id === id)?.cards ?? [];
}

function Row({
  card,
  board,
  selected,
  onClick,
}: {
  card: Card;
  board: Board;
  selected: boolean;
  onClick: (key: string, e: MouseEvent) => void;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: card.key,
  });
  return (
    <Group
      ref={setNodeRef}
      className="kogge-backlog-row"
      data-selected={selected || undefined}
      gap="sm"
      px="sm"
      py={6}
      wrap="nowrap"
      style={{ transform: CSS.Translate.toString(transform), transition, opacity: isDragging ? 0.35 : 1 }}
      onClick={(e) => onClick(card.key, e)}
      {...attributes}
      {...listeners}
    >
      <TypeIcon type={card.type} size={16} />
      <Text
        size="sm"
        fw={600}
        c="dimmed"
        w={90}
        style={{ flexShrink: 0 }}
        td={card.done ? 'line-through' : undefined}
      >
        {card.key}
      </Text>
      <Text size="sm" truncate style={{ flex: 1 }}>
        {card.summary}
      </Text>
      {card.epic && <EpicBadge epic={card.epic} />}
      <StatusBadge status={card.status} />
      <PriorityIcon priority={card.priority} />
      <Box w={24}>
        <AssigneeAvatar card={card} />
      </Box>
      <Badge variant="default" radius="xl" w={46} style={{ flexShrink: 0 }}>
        {formatEstimate(card.estimate, board.config.estimation) || '–'}
      </Badge>
    </Group>
  );
}

function InlineCreate({ board, sprintId }: { board: Board; sprintId?: number }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const project = useProject(board.project.key);
  const types = (project.data?.issueTypes ?? []).filter((ty) => ty.level === 0);
  const [open, setOpen] = useState(false);
  const [summary, setSummary] = useState('');
  const [typeId, setTypeId] = useState<string | null>(null);
  const effective = typeId ?? (types[0] ? String(types[0].id) : null);
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/issues', {
          body: {
            project: board.project.key,
            typeId: Number(effective),
            summary: summary.trim(),
            fields: sprintId ? { sprint: sprintId } : undefined,
          },
        }),
      ),
    onSuccess: () => {
      setSummary('');
      void client.invalidateQueries({ queryKey: ['board', board.id] });
    },
    onError: notifyError,
  });
  if (!open) {
    return (
      <Button
        variant="subtle"
        size="xs"
        leftSection={<IconPlus size={14} />}
        onClick={() => setOpen(true)}
        mt={4}
      >
        {t('issues.createIssue')}
      </Button>
    );
  }
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (summary.trim() && effective) create.mutate();
      }}
    >
      <Group gap="xs" mt={4} wrap="nowrap">
        <Select
          size="xs"
          w={140}
          data={types.map((ty) => ({ value: String(ty.id), label: ty.name }))}
          value={effective}
          onChange={setTypeId}
          allowDeselect={false}
          aria-label={t('issues.type')}
        />
        <TextInput
          size="xs"
          style={{ flex: 1 }}
          placeholder={t('agile.whatNeedsDoing')}
          value={summary}
          onChange={(e) => setSummary(e.currentTarget.value)}
          onKeyDown={(e) => e.key === 'Escape' && setOpen(false)}
          autoFocus
          aria-label={t('issues.summary')}
        />
        <Button size="xs" type="submit" loading={create.isPending} disabled={!summary.trim()}>
          {t('issues.create')}
        </Button>
      </Group>
    </form>
  );
}

function Section({
  id,
  title,
  meta,
  actions,
  cards,
  board,
  selection,
  onRowClick,
  sprintId,
  filtered = false,
}: {
  id: string;
  title: string;
  meta?: string;
  actions?: ReactNode;
  cards: Card[];
  board: Board;
  selection: Set<string>;
  onRowClick: (key: string, e: MouseEvent) => void;
  sprintId?: number;
  /** The toolbar's filters hide some issues. */
  filtered?: boolean;
}) {
  const { t } = useTranslation();
  const { setNodeRef, isOver } = useDroppable({ id });
  const [collapsed, setCollapsed] = useState(false);
  const total = cards.reduce((sum, c) => sum + (c.estimate ?? 0), 0);
  const byCategory = (cat: string) => cards.filter((c) => c.status.category === cat).length;
  return (
    <Paper withBorder radius="md" mb="md">
      <Group justify="space-between" px="sm" py="xs" wrap="nowrap">
        <UnstyledButton onClick={() => setCollapsed((c) => !c)} style={{ minWidth: 0 }}>
          <Group gap={6} wrap="nowrap">
            {collapsed ? <IconChevronRight size={16} /> : <IconChevronDown size={16} />}
            <Text fw={700} truncate>
              {title}
            </Text>
            {meta && (
              <Text size="xs" c="dimmed" truncate>
                {meta}
              </Text>
            )}
            <Text size="xs" c="dimmed">
              {t('issues.issueCount', { count: cards.length })}
            </Text>
          </Group>
        </UnstyledButton>
        <Group gap={6} wrap="nowrap">
          <Tooltip label={t('agile.byCategory')}>
            <Group gap={4} wrap="nowrap">
              <Badge size="sm" color="gray" variant="light">
                {byCategory('todo')}
              </Badge>
              <Badge size="sm" color="blue" variant="light">
                {byCategory('inprogress')}
              </Badge>
              <Badge size="sm" color="teal" variant="light">
                {byCategory('done')}
              </Badge>
            </Group>
          </Tooltip>
          {board.config.estimation.type !== 'count' && (
            <Badge size="sm" variant="default" radius="xl">
              {formatEstimate(total, board.config.estimation) || '0'}
            </Badge>
          )}
          {actions}
        </Group>
      </Group>
      {!collapsed && (
        <Box
          ref={setNodeRef}
          style={{
            minHeight: 36,
            outline: isOver ? '2px dashed var(--mantine-primary-color-filled)' : undefined,
          }}
        >
          <SortableContext items={cards.map((c) => c.key)} strategy={verticalListSortingStrategy}>
            {cards.map((c) => (
              <Row key={c.key} card={c} board={board} selected={selection.has(c.key)} onClick={onRowClick} />
            ))}
          </SortableContext>
          {cards.length === 0 && (
            <Text size="sm" c="dimmed" ta="center" py="sm">
              {filtered
                ? t('agile.noneMatch')
                : id === BACKLOG
                  ? t('agile.backlogEmpty')
                  : t('agile.sprintEmpty')}
            </Text>
          )}
          <Box px="sm" pb="xs">
            <InlineCreate board={board} sprintId={sprintId} />
          </Box>
        </Box>
      )}
    </Paper>
  );
}

function EpicsPanel({
  view,
  epic,
  onEpic,
}: {
  view: BacklogView;
  epic: string | null;
  onEpic: (e: string | null) => void;
}) {
  const { t } = useTranslation();
  const createIssue = useCreateIssue();
  const project = useProject(view.board.project.key);
  const epicType = project.data?.issueTypes.find((ty) => ty.level === 1);
  return (
    <Stack gap={6}>
      <Group justify="space-between">
        <Text fw={700}>{t('agile.epics')}</Text>
        {epicType && (
          <ActionIcon
            variant="subtle"
            onClick={() => createIssue({ project: view.board.project.key, typeId: epicType.id })}
            aria-label={t('agile.createEpic')}
          >
            <IconPlus size={16} />
          </ActionIcon>
        )}
      </Group>
      <UnstyledButton onClick={() => onEpic(null)}>
        <Text size="sm" fw={epic === null ? 700 : 400}>
          {t('agile.allIssues')}
        </Text>
      </UnstyledButton>
      {view.epics.map((e) => (
        <MCard
          key={e.id}
          withBorder
          padding="xs"
          radius="sm"
          style={{
            cursor: 'pointer',
            borderLeft: `3px solid var(--mantine-color-${epicColor(e.id)}-6)`,
            outline: epic === e.key ? '2px solid var(--mantine-primary-color-filled)' : undefined,
          }}
          onClick={() => onEpic(epic === e.key ? null : e.key)}
        >
          <Text size="sm" fw={600} lineClamp={2}>
            {e.summary}
          </Text>
          <Text size="xs" c="dimmed">
            {e.key} · {e.issuesDone}/{e.issues}
          </Text>
          <Progress
            value={e.issues ? (100 * e.issuesDone) / e.issues : 0}
            size="sm"
            mt={4}
            color={epicColor(e.id)}
            aria-label={t('agile.epicProgress', { done: e.issuesDone, total: e.issues })}
          />
        </MCard>
      ))}
      <UnstyledButton onClick={() => onEpic(epic === 'none' ? null : 'none')}>
        <Text size="sm" fw={epic === 'none' ? 700 : 400}>
          {t('agile.noEpic')}
        </Text>
      </UnstyledButton>
    </Stack>
  );
}

export function BacklogPage() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const { key = '', boardId = '' } = useParams();
  const id = Number(boardId);
  const [quick, setQuick] = useQuickFilters();
  const [epic, setEpic] = useState<string | null>(null);
  const backlog = useBacklog(id, quick, epic);
  const move = useMoveIssues(id);
  const rank = useRank(id);
  const [selection, setSelection] = useState<Set<string>>(new Set());
  const [anchorKey, setAnchorKey] = useState<string | null>(null);
  const [openKey, setOpenKey] = useState<string | null>(null);
  const [activeKey, setActiveKey] = useState<string | null>(null);
  const [starting, setStarting] = useState<Sprint | null>(null);
  const [editing, setEditing] = useState<Sprint | null>(null);
  const [retroFor, setRetroFor] = useState<LinkedPageKind | null>(null);
  const [completing, setCompleting] = useState<Sprint | null>(null);
  const [showEpics, setShowEpics] = useState(true);
  const [bulk, setBulk] = useState(false);
  const filters = useBoardFilters(backlog.data?.board);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const createSprint = useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/boards/{id}/sprints', { params: { path: { id } }, body: {} })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['board', id] }),
    onError: notifyError,
  });
  const deleteSprint = useMutation({
    mutationFn: (sid: number) =>
      unwrap(api.DELETE('/api/v1/sprints/{id}', { params: { path: { id: sid } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['board', id] }),
    onError: notifyError,
  });
  if (backlog.error) return <ErrorAlert error={backlog.error} />;
  const view = backlog.data;
  if (!view) return null;
  const board = view.board;
  const viewKey = ['board', id, 'backlog', quick, epic];

  const onRowClick = (rowKey: string, e: MouseEvent) => {
    const container = containerOf(view, rowKey);
    if (e.metaKey || e.ctrlKey) {
      setSelection((s) => {
        const n = new Set(s);
        if (n.has(rowKey)) n.delete(rowKey);
        else n.add(rowKey);
        return n;
      });
      setAnchorKey(rowKey);
      return;
    }
    if (e.shiftKey && anchorKey && container && containerOf(view, anchorKey) === container) {
      const list = cardsOf(view, container).map((c) => c.key);
      const [a, b] = [list.indexOf(anchorKey), list.indexOf(rowKey)].sort((x, y) => x - y);
      setSelection(new Set(list.slice(a, b + 1)));
      return;
    }
    setSelection(new Set([rowKey]));
    setAnchorKey(rowKey);
    setOpenKey(rowKey);
  };

  const onDragStart = (e: DragStartEvent) => {
    const k = String(e.active.id);
    setActiveKey(k);
    if (!selection.has(k)) setSelection(new Set([k]));
  };

  const onDragEnd = (e: DragEndEvent) => {
    setActiveKey(null);
    if (!e.over) return;
    const activeId = String(e.active.id);
    const overId = String(e.over.id);
    const source = containerOf(view, activeId);
    const target = overId === BACKLOG || overId.startsWith('sprint|') ? overId : containerOf(view, overId);
    if (!source || !target) return;
    const sourceList = cardsOf(view, source);
    const moving = (selection.has(activeId) ? sourceList.filter((c) => selection.has(c.key)) : []).map(
      (c) => c.key,
    );
    // Selected issues from other containers move along.
    for (const s of [view.backlog, ...view.sprints.map((x) => x.cards)]) {
      for (const c of s) if (selection.has(c.key) && !moving.includes(c.key)) moving.push(c.key);
    }
    if (!moving.includes(activeId)) moving.unshift(activeId);
    const targetList = cardsOf(view, target).filter((c) => !moving.includes(c.key));
    let before: string | undefined;
    let after: string | undefined;
    if (overId !== target && !moving.includes(overId)) {
      const fromIndex = sourceList.findIndex((c) => c.key === activeId);
      const overIndex = cardsOf(view, target).findIndex((c) => c.key === overId);
      if (source === target && fromIndex < overIndex) after = overId;
      else before = overId;
    } else if (overId === target && targetList.length > 0) {
      after = targetList[targetList.length - 1].key;
    }
    if (source === target && !before && !after) return;
    // Optimistic update.
    client.setQueryData<BacklogView>(viewKey, (old) => {
      if (!old) return old;
      const all = [...old.backlog, ...old.sprints.flatMap((s) => s.cards)];
      const movingCards = moving.map((k) => all.find((c) => c.key === k)!).filter(Boolean);
      const strip = (cs: Card[]) => cs.filter((c) => !moving.includes(c.key));
      const insert = (cs: Card[], container: string) => {
        if (container !== target) return cs;
        const i = before
          ? cs.findIndex((c) => c.key === before)
          : after
            ? cs.findIndex((c) => c.key === after) + 1
            : cs.length;
        const copy = [...cs];
        copy.splice(i < 0 ? copy.length : i, 0, ...movingCards);
        return copy;
      };
      return {
        ...old,
        backlog: insert(strip(old.backlog), BACKLOG),
        sprints: old.sprints.map((s) => ({ ...s, cards: insert(strip(s.cards), `sprint|${s.sprint.id}`) })),
      };
    });
    const sameContainer = moving.every((k) => containerOf(view, k) === target);
    if (sameContainer) {
      rank.mutate({ issues: moving, before, after });
    } else {
      move.mutate({
        sprint: target === BACKLOG ? 0 : Number(target.split('|')[1]),
        issues: moving,
        before,
        after,
      });
    }
  };

  const futureSprints = view.sprints.filter((s) => s.sprint.state === 'future').map((s) => s.sprint);
  const hasActive = view.sprints.some((s) => s.sprint.state === 'active');
  const activeCard = activeKey
    ? [...view.backlog, ...view.sprints.flatMap((s) => s.cards)].find((c) => c.key === activeKey)
    : null;

  return (
    <Stack gap="md">
      <BoardNav projectKey={key} board={board} active="backlog" />
      {retroFor && (
        <LinkedPageModal projectKey={board.project.key} kind={retroFor} onClose={() => setRetroFor(null)} />
      )}
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <BoardToolbar
          board={board}
          cards={[...view.backlog, ...view.sprints.flatMap((s) => s.cards)]}
          filters={filters}
          quick={quick}
          setQuick={setQuick}
          grouping={false}
        />
        <Group gap="xs" wrap="nowrap">
          {selection.size > 0 && (
            <Button
              size="xs"
              variant="light"
              leftSection={<IconListCheck size={14} />}
              onClick={() => setBulk(true)}
              data-testid="backlog-bulk"
            >
              {t('bulk.openCount', { count: selection.size })}
            </Button>
          )}
          <Button size="xs" variant="subtle" onClick={() => setShowEpics((v) => !v)}>
            {showEpics ? t('agile.hideEpics') : t('agile.showEpics')}
          </Button>
        </Group>
      </Group>
      {bulk && (
        <BulkChangeModal
          target={{ issues: [...selection], count: selection.size }}
          onClose={() => {
            setBulk(false);
            setSelection(new Set());
          }}
        />
      )}
      <Grid gap="md">
        {showEpics && (
          <Grid.Col span={{ base: 12, md: 3 }}>
            <EpicsPanel view={view} epic={epic} onEpic={setEpic} />
          </Grid.Col>
        )}
        <Grid.Col span={{ base: 12, md: showEpics ? 9 : 12 }}>
          <DndContext
            sensors={sensors}
            collisionDetection={closestCorners}
            onDragStart={onDragStart}
            onDragEnd={onDragEnd}
          >
            {view.sprints.map(({ sprint, cards }, index) => {
              const isFirstFuture = sprint.state === 'future' && futureSprints[0]?.id === sprint.id;
              const canStart =
                sprint.state === 'future' &&
                (!hasActive || board.config.parallelSprints) &&
                (isFirstFuture || board.config.parallelSprints);
              return (
                <Section
                  key={sprint.id}
                  id={`sprint|${sprint.id}`}
                  title={sprint.name}
                  meta={[
                    sprint.state === 'active' ? t('agile.active') : '',
                    sprint.start && sprint.end
                      ? `${formatDate(sprint.start)} – ${formatDate(sprint.end)}`
                      : '',
                    sprint.goal,
                  ]
                    .filter(Boolean)
                    .join(' · ')}
                  cards={cards.filter(filters.shown)}
                  filtered={filters.active}
                  board={board}
                  selection={selection}
                  onRowClick={onRowClick}
                  sprintId={sprint.id}
                  actions={
                    board.canManageSprints && (
                      <Group gap={4} wrap="nowrap">
                        {sprint.state === 'active' && (
                          <Button size="xs" variant="light" onClick={() => setCompleting(sprint)}>
                            {t('agile.completeSprint')}
                          </Button>
                        )}
                        {canStart && (
                          <Button
                            size="xs"
                            variant="light"
                            onClick={() => setStarting(sprint)}
                            disabled={cards.length === 0}
                          >
                            {t('agile.startSprint')}
                          </Button>
                        )}
                        <Menu position="bottom-end">
                          <Menu.Target>
                            <ActionIcon variant="subtle" aria-label={t('issues.more')}>
                              <IconDots size={16} />
                            </ActionIcon>
                          </Menu.Target>
                          <Menu.Dropdown>
                            <Menu.Item onClick={() => setEditing(sprint)}>{t('agile.editSprint')}</Menu.Item>
                            {sprint.state !== 'future' && (
                              <Menu.Item
                                onClick={() =>
                                  setRetroFor({
                                    template: 'retrospective',
                                    title: t('linkedPage.retrospectiveTitle', { sprint: sprint.name }),
                                    variables: { sprint: sprint.name },
                                    query: `sprint = ${sprint.id} ORDER BY rank`,
                                  })
                                }
                              >
                                {t('linkedPage.createRetrospective')}
                              </Menu.Item>
                            )}
                            {index > 0 && sprint.state === 'future' && (
                              <Menu.Item
                                onClick={() => {
                                  const ids = futureSprints.map((s) => s.id);
                                  const i = ids.indexOf(sprint.id);
                                  [ids[i - 1], ids[i]] = [ids[i], ids[i - 1]];
                                  void unwrap(
                                    api.PUT('/api/v1/boards/{id}/sprint-order', {
                                      params: { path: { id } },
                                      body: ids,
                                    }),
                                  ).then(
                                    () => client.invalidateQueries({ queryKey: ['board', id] }),
                                    notifyError,
                                  );
                                }}
                              >
                                {t('agile.moveSprintUp')}
                              </Menu.Item>
                            )}
                            <Menu.Item
                              color="red"
                              onClick={() =>
                                modals.openConfirmModal({
                                  title: t('agile.deleteSprint'),
                                  children: (
                                    <Text size="sm">
                                      {t('agile.deleteSprintText', { name: sprint.name })}
                                    </Text>
                                  ),
                                  labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                                  confirmProps: { color: 'red' },
                                  onConfirm: () => deleteSprint.mutate(sprint.id),
                                })
                              }
                            >
                              {t('agile.deleteSprint')}
                            </Menu.Item>
                          </Menu.Dropdown>
                        </Menu>
                      </Group>
                    )
                  }
                />
              );
            })}
            <Section
              id={BACKLOG}
              title={t('agile.backlog')}
              cards={view.backlog.filter(filters.shown)}
              filtered={filters.active}
              board={board}
              selection={selection}
              onRowClick={onRowClick}
              actions={
                board.canManageSprints && (
                  <Button
                    size="xs"
                    variant="light"
                    onClick={() => createSprint.mutate()}
                    loading={createSprint.isPending}
                  >
                    {t('agile.createSprint')}
                  </Button>
                )
              }
            />
            <DragOverlay>
              {activeCard ? (
                <Paper shadow="md" withBorder px="sm" py={6}>
                  <Group gap="sm" wrap="nowrap">
                    <TypeIcon type={activeCard.type} size={16} />
                    <Text size="sm" fw={600}>
                      {activeCard.key}
                    </Text>
                    <Text size="sm" truncate>
                      {activeCard.summary}
                    </Text>
                    {selection.size > 1 && <Badge>{t('agile.nIssues', { count: selection.size })}</Badge>}
                  </Group>
                </Paper>
              ) : null}
            </DragOverlay>
          </DndContext>
          {view.truncated && (
            <Text size="xs" c="orange">
              {t('agile.truncated')}
            </Text>
          )}
        </Grid.Col>
      </Grid>
      <Drawer opened={!!openKey} onClose={() => setOpenKey(null)} position="right" size="xl" title={openKey}>
        {openKey && <IssueView key={openKey} issueKey={openKey} />}
      </Drawer>
      {starting && (
        <SprintModal
          sprint={starting}
          mode="start"
          issueCount={view.sprints.find((s) => s.sprint.id === starting.id)?.cards.length ?? 0}
          onClose={() => setStarting(null)}
        />
      )}
      {editing && <SprintModal sprint={editing} mode="edit" onClose={() => setEditing(null)} />}
      {completing && (
        <CompleteSprintModal
          sprint={completing}
          done={
            view.sprints.find((s) => s.sprint.id === completing.id)?.cards.filter((c) => c.done).length ?? 0
          }
          open={
            view.sprints.find((s) => s.sprint.id === completing.id)?.cards.filter((c) => !c.done).length ?? 0
          }
          futureSprints={futureSprints}
          onClose={() => setCompleting(null)}
        />
      )}
    </Stack>
  );
}
