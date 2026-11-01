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
  Alert,
  Anchor,
  Avatar,
  Badge,
  Box,
  Button,
  Chip,
  CloseButton,
  Drawer,
  Group,
  Menu,
  Progress,
  ScrollArea,
  Select,
  Stack,
  Text,
  TextInput,
  Title,
  Tooltip,
  UnstyledButton,
} from '@mantine/core';
import {
  IconArrowBarToDown,
  IconArrowBarToUp,
  IconChevronDown,
  IconChevronRight,
  IconDots,
  IconExternalLink,
  IconLink,
  IconSearch,
  IconUserCheck,
  IconUserOff,
} from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams, useSearchParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatDate } from '../../app/format';
import { useSession } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PersonAvatar } from '../../components/PersonAvatar';
import { notifyError, notifySuccess } from '../../components/notify';
import { IssueView } from '../issues/IssueView';
import { UserBadge } from '../issues/shared';
import { BoardNav } from './BoardNav';
import {
  formatEstimate,
  useBoardView,
  useRank,
  useSprints,
  type Board,
  type BoardConfig,
  type BoardView,
  type Card,
} from './hooks';
import { EpicBadge, IssueCard } from './IssueCard';
import { CompleteSprintModal } from './SprintDialogs';
import { runTransition, TransitionDialog, transitionsTo, type PendingTransition } from './TransitionDialog';

interface Lane {
  key: string;
  title: ReactNode;
  cards: Card[];
}

type LaneType = BoardConfig['swimlanes']['type'];

function useLanes(board: Board, cards: Card[], type: LaneType): Lane[] {
  const { t } = useTranslation();
  return useMemo(() => {
    const group = (
      keyOf: (c: Card) => string,
      title: (c: Card | undefined, key: string) => ReactNode,
      lastKey: string,
    ) => {
      const order: string[] = [];
      const byKey = new Map<string, Card[]>();
      for (const c of cards) {
        const k = keyOf(c);
        if (!byKey.has(k)) {
          byKey.set(k, []);
          order.push(k);
        }
        byKey.get(k)!.push(c);
      }
      order.sort((a, b) => Number(a === lastKey) - Number(b === lastKey));
      return order.map((k) => ({ key: k, title: title(byKey.get(k)![0], k), cards: byKey.get(k)! }));
    };
    switch (type) {
      case 'assignee':
        return group(
          (c) => (c.assignee ? `u${c.assignee.id}` : 'none'),
          (c, k) => (k === 'none' ? t('issues.unassigned') : <UserBadge user={c?.assignee} size="sm" />),
          'none',
        );
      case 'epic':
        return group(
          (c) => (c.epic ? `e${c.epic.id}` : 'none'),
          (c, k) => (k === 'none' ? t('agile.noEpic') : c?.epic ? <EpicBadge epic={c.epic} /> : k),
          'none',
        );
      case 'parent':
        return group(
          (c) => c.parentKey ?? 'none',
          (_c, k) => (k === 'none' ? t('agile.otherIssues') : k),
          'none',
        );
      case 'queries': {
        const queries = board.config.swimlanes.queries ?? [];
        const lanes: Lane[] = queries.map((q, i) => ({
          key: `q${i}`,
          title: q.name,
          cards: cards.filter((c) => c.lane === i),
        }));
        lanes.push({ key: 'rest', title: t('agile.everythingElse'), cards: cards.filter((c) => c.lane < 0) });
        return lanes;
      }
    }
    return [{ key: 'all', title: null, cards }];
  }, [board, cards, t, type]);
}

/** Things to do with a card without opening it. */
function CardMenu({ card, board, column }: { card: Card; board: Board; column: Card[] }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const client = useQueryClient();
  const rank = useRank(board.id);
  const refresh = () => client.invalidateQueries({ queryKey: ['board', board.id] });
  const assign = useMutation({
    mutationFn: (id: number | null) =>
      unwrap(
        api.PATCH('/api/v1/issues/{key}', {
          params: { path: { key: card.key }, header: {} },
          body: { fields: { assignee: id } as unknown as Record<string, never> },
        }),
      ),
    onSuccess: refresh,
    onError: notifyError,
  });
  const toBacklog = useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/backlog/issues', { body: { issues: [card.key] } })),
    onSuccess: () => {
      notifySuccess(t('agile.movedToBacklog', { key: card.key }));
      void refresh();
    },
    onError: notifyError,
  });
  const others = column.filter((c) => c.key !== card.key);
  return (
    <Menu position="bottom-end" withinPortal shadow="md">
      <Menu.Target>
        <ActionIcon
          size="sm"
          variant="subtle"
          color="gray"
          className="kogge-card-menu"
          aria-label={t('agile.cardActions', { key: card.key })}
          onClick={(e) => e.stopPropagation()}
          onPointerDown={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
        >
          <IconDots size={16} />
        </ActionIcon>
      </Menu.Target>
      <Menu.Dropdown onClick={(e) => e.stopPropagation()}>
        <Menu.Item leftSection={<IconExternalLink size={14} />} component={Link} to={`/browse/${card.key}`}>
          {t('agile.openIssue')}
        </Menu.Item>
        <Menu.Item
          leftSection={<IconLink size={14} />}
          onClick={() => void navigator.clipboard?.writeText(`${window.location.origin}/browse/${card.key}`)}
        >
          {t('issues.copyLink')}
        </Menu.Item>
        <Menu.Divider />
        {card.assignee?.id === me.user.id ? (
          <Menu.Item leftSection={<IconUserOff size={14} />} onClick={() => assign.mutate(null)}>
            {t('agile.unassign')}
          </Menu.Item>
        ) : (
          <Menu.Item leftSection={<IconUserCheck size={14} />} onClick={() => assign.mutate(me.user.id)}>
            {t('issues.assignToMe')}
          </Menu.Item>
        )}
        {others.length > 0 && (
          <>
            <Menu.Item
              leftSection={<IconArrowBarToUp size={14} />}
              onClick={() => rank.mutate({ issues: [card.key], before: others[0].key })}
            >
              {t('agile.moveToTop')}
            </Menu.Item>
            <Menu.Item
              leftSection={<IconArrowBarToDown size={14} />}
              onClick={() => rank.mutate({ issues: [card.key], after: others[others.length - 1].key })}
            >
              {t('agile.moveToBottom')}
            </Menu.Item>
          </>
        )}
        {board.type === 'scrum' && card.sprintId && board.canManageSprints && card.type.level !== -1 && (
          <>
            <Menu.Divider />
            <Menu.Item onClick={() => toBacklog.mutate()}>{t('agile.removeFromSprint')}</Menu.Item>
          </>
        )}
      </Menu.Dropdown>
    </Menu>
  );
}

function SortableCard({
  card,
  board,
  column,
  onOpen,
}: {
  card: Card;
  board: Board;
  column: Card[];
  onOpen: (key: string) => void;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: card.key,
  });
  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition, opacity: isDragging ? 0.35 : 1 }}
      {...attributes}
      {...listeners}
      // The card is named by its issue, not by all its text and buttons.
      aria-label={`${card.key} ${card.summary}`}
    >
      <IssueCard
        card={card}
        board={board}
        onOpen={(key) => onOpen(key)}
        menu={<CardMenu card={card} board={board} column={column} />}
      />
    </div>
  );
}

function Cell({
  id,
  cards,
  board,
  onOpen,
  wip,
}: {
  id: string;
  cards: Card[];
  board: Board;
  onOpen: (key: string) => void;
  wip?: string;
}) {
  const { setNodeRef, isOver } = useDroppable({ id });
  return (
    <Stack
      ref={setNodeRef}
      gap={6}
      p={6}
      className="kogge-board-column"
      data-over={isOver || undefined}
      data-wip={wip}
      style={{ flex: '1 0 240px', minHeight: 80 }}
    >
      <SortableContext items={cards.map((c) => c.key)} strategy={verticalListSortingStrategy}>
        {cards.map((c) => (
          <SortableCard key={c.key} card={c} board={board} column={cards} onOpen={onOpen} />
        ))}
      </SortableContext>
    </Stack>
  );
}

function wipState(count: number, min?: number, max?: number) {
  if (max != null && count > max) return 'over';
  if (min != null && count < min) return 'under';
  return undefined;
}

function Columns({
  view,
  quick,
  onOpen,
  shown,
  laneType,
}: {
  view: BoardView;
  quick: number[];
  onOpen: (key: string) => void;
  /** Which cards the toolbar's filters let through. */
  shown: (c: Card) => boolean;
  laneType: LaneType;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const board = view.board;
  const rank = useRank(board.id);
  const all = view.cards.filter((c) => c.column >= 0);
  const cards = useMemo(() => all.filter(shown), [all, shown]);
  const lanes = useLanes(board, cards, laneType);
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [active, setActive] = useState<Card | null>(null);
  const [pending, setPending] = useState<PendingTransition | null>(null);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const viewKey = ['board', board.id, 'view', quick, 0];
  // WIP limits count every card of a column, not only those the filters show.
  const counts = board.config.columns.map((_, i) => all.filter((c) => c.column === i).length);
  const shownCounts = board.config.columns.map((_, i) => cards.filter((c) => c.column === i).length);
  const estimation = board.config.estimation;
  const sums = board.config.columns.map((_, i) =>
    cards.filter((c) => c.column === i).reduce((n, c) => n + (c.estimate ?? 0), 0),
  );
  const refresh = () => client.invalidateQueries({ queryKey: ['board', board.id] });

  const cellOf = (key: string): { lane: string; column: number } | null => {
    if (key.startsWith('cell|')) {
      const [, lane, col] = key.split('|');
      return { lane, column: Number(col) };
    }
    const c = cards.find((x) => x.key === key);
    if (!c) return null;
    const lane = lanes.find((l) => l.cards.includes(c));
    return { lane: lane?.key ?? 'all', column: c.column };
  };

  const onDragStart = (e: DragStartEvent) => setActive(cards.find((c) => c.key === e.active.id) ?? null);
  const onDragEnd = async (e: DragEndEvent) => {
    setActive(null);
    const card = cards.find((c) => c.key === e.active.id);
    if (!card || !e.over) return;
    const target = cellOf(String(e.over.id));
    if (!target) return;
    const overCard = String(e.over.id).startsWith('cell|') ? null : cards.find((c) => c.key === e.over!.id);
    if (target.column !== card.column) {
      const statuses = board.config.columns[target.column].statuses;
      try {
        const candidates = await transitionsTo(card.key, statuses);
        if (candidates.length === 0) {
          notifyError(
            new Error(
              t('agile.noTransition', { key: card.key, column: board.config.columns[target.column].name }),
            ),
          );
          return;
        }
        const afterTransition = () => {
          if (overCard && overCard.key !== card.key)
            rank.mutate({ issues: [card.key], before: overCard.key });
          void refresh();
        };
        if (candidates.length === 1 && candidates[0].screen.length === 0 && !candidates[0].commentRequired) {
          // Optimistic: show the card in the target column right away.
          client.setQueryData<BoardView>(viewKey, (old) =>
            old
              ? {
                  ...old,
                  cards: old.cards.map((c) => (c.key === card.key ? { ...c, column: target.column } : c)),
                }
              : old,
          );
          await runTransition(card.key, candidates[0]);
          afterTransition();
        } else {
          setPending({ issueKey: card.key, candidates, onDone: afterTransition });
        }
      } catch (err) {
        notifyError(err);
        void refresh();
      }
      return;
    }
    if (overCard && overCard.key !== card.key) {
      const list =
        lanes.find((l) => l.key === target.lane)?.cards.filter((c) => c.column === target.column) ?? [];
      const from = list.findIndex((c) => c.key === card.key);
      const to = list.findIndex((c) => c.key === overCard.key);
      const anchor = from < to ? { after: overCard.key } : { before: overCard.key };
      client.setQueryData<BoardView>(viewKey, (old) => {
        if (!old) return old;
        const rest = old.cards.filter((c) => c.key !== card.key);
        const i = rest.findIndex((c) => c.key === overCard.key);
        rest.splice(from < to ? i + 1 : i, 0, card);
        return { ...old, cards: rest };
      });
      rank.mutate({ issues: [card.key], ...anchor });
    }
  };

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCorners}
      onDragStart={onDragStart}
      onDragEnd={(e) => void onDragEnd(e)}
    >
      <ScrollArea type="auto" offsetScrollbars>
        <Box miw={board.config.columns.length * 250}>
          <Group gap="sm" wrap="nowrap" align="stretch" mb={6}>
            {board.config.columns.map((col, i) => (
              <Group key={i} justify="space-between" px={6} style={{ flex: '1 0 240px' }} wrap="nowrap">
                <Group gap={6} wrap="nowrap" style={{ minWidth: 0 }}>
                  <Text size="sm" fw={600} truncate>
                    {col.name}
                  </Text>
                  <Text size="xs" fw={700} c="dimmed">
                    {shownCounts[i] === counts[i] ? counts[i] : `${shownCounts[i]}/${counts[i]}`}
                  </Text>
                  {estimation.type !== 'count' && sums[i] > 0 && (
                    <Tooltip label={t('agile.columnEstimate')} withinPortal>
                      <Badge size="xs" variant="default" radius="xl">
                        {formatEstimate(sums[i], estimation)}
                      </Badge>
                    </Tooltip>
                  )}
                </Group>
                {(col.min != null || col.max != null) && (
                  <Badge
                    size="xs"
                    variant="light"
                    color={wipState(counts[i], col.min, col.max) === 'over' ? 'red' : 'gray'}
                  >
                    {col.min != null ? `${t('agile.min')} ${col.min}` : ''}{' '}
                    {col.max != null ? `${t('agile.max')} ${col.max}` : ''}
                  </Badge>
                )}
              </Group>
            ))}
          </Group>
          {lanes.map((lane) => (
            <Box key={lane.key} mb="sm">
              {lane.title !== null && (
                <UnstyledButton
                  onClick={() => setCollapsed((c) => ({ ...c, [lane.key]: !c[lane.key] }))}
                  mb={4}
                  py={2}
                >
                  <Group gap={6}>
                    {collapsed[lane.key] ? <IconChevronRight size={14} /> : <IconChevronDown size={14} />}
                    {typeof lane.title === 'string' ? (
                      <Text size="sm" fw={600}>
                        {lane.title}
                      </Text>
                    ) : (
                      lane.title
                    )}
                    <Text size="xs" c="dimmed">
                      {t('issues.issueCount', { count: lane.cards.length })}
                    </Text>
                  </Group>
                </UnstyledButton>
              )}
              {!collapsed[lane.key] && (
                <Group gap="sm" wrap="nowrap" align="stretch">
                  {board.config.columns.map((col, i) => (
                    <Cell
                      key={i}
                      id={`cell|${lane.key}|${i}`}
                      cards={lane.cards.filter((c) => c.column === i)}
                      board={board}
                      onOpen={onOpen}
                      wip={lanes.length === 1 ? wipState(counts[i], col.min, col.max) : undefined}
                    />
                  ))}
                </Group>
              )}
            </Box>
          ))}
        </Box>
      </ScrollArea>
      <DragOverlay>{active ? <IssueCard card={active} board={board} dragging /> : null}</DragOverlay>
      <TransitionDialog pending={pending} onClose={() => setPending(null)} />
    </DndContext>
  );
}

/** Quick filters of a board, kept in the URL (?qf=1,2). */
export function useQuickFilters(): [number[], (ids: number[]) => void] {
  const [params, setParams] = useSearchParams();
  const quick = (params.get('qf') ?? '')
    .split(',')
    .filter(Boolean)
    .map(Number)
    .filter((n) => !Number.isNaN(n));
  const set = (ids: number[]) =>
    setParams(
      (p) => {
        if (ids.length) p.set('qf', ids.join(','));
        else p.delete('qf');
        return p;
      },
      { replace: true },
    );
  return [quick, set];
}

export function QuickFilterChips({
  board,
  value,
  onChange,
}: {
  board: Board;
  value: number[];
  onChange: (ids: number[]) => void;
}) {
  if (board.config.quickFilters.length === 0) return null;
  return (
    <Chip.Group multiple value={value.map(String)} onChange={(v) => onChange(v.map(Number))}>
      <Group gap={6}>
        {board.config.quickFilters.map((f) => (
          <Chip key={f.id} value={String(f.id)} size="xs" variant="outline">
            {f.name}
          </Chip>
        ))}
      </Group>
    </Chip.Group>
  );
}

/** The toolbar's filters and grouping, kept in the URL (?q=…&who=12,none&lanes=epic). */
export function useBoardFilters(board: Board | undefined) {
  const [params, setParams] = useSearchParams();
  const set = (name: string, value: string) =>
    setParams(
      (p) => {
        if (value) p.set(name, value);
        else p.delete(name);
        return p;
      },
      { replace: true },
    );
  const text = params.get('q') ?? '';
  const whoParam = params.get('who') ?? '';
  const who = useMemo(() => whoParam.split(',').filter(Boolean), [whoParam]);
  const lanes = (params.get('lanes') as LaneType | null) ?? board?.config.swimlanes.type ?? 'none';
  const needle = text.trim().toLowerCase();
  const shown = useMemo(
    () => (c: Card) =>
      (!needle || c.key.toLowerCase().includes(needle) || c.summary.toLowerCase().includes(needle)) &&
      (who.length === 0 || who.includes(c.assignee ? String(c.assignee.id) : 'none')),
    [needle, who],
  );
  return {
    text,
    setText: (v: string) => set('q', v),
    who,
    setWho: (ids: string[]) => set('who', ids.join(',')),
    lanes,
    setLanes: (v: string | null) => set('lanes', v && v !== board?.config.swimlanes.type ? v : ''),
    shown,
    active: !!needle || who.length > 0,
    // One change of the URL: several in a row would each start from the same address.
    clear: () =>
      setParams(
        (p) => {
          p.delete('q');
          p.delete('who');
          return p;
        },
        { replace: true },
      ),
  };
}

/** Search, people and grouping above the columns (and above the backlog, without grouping). */
export function BoardToolbar({
  board,
  cards,
  filters,
  quick,
  setQuick,
  grouping = true,
}: {
  board: Board;
  cards: Card[];
  filters: ReturnType<typeof useBoardFilters>;
  quick: number[];
  setQuick: (ids: number[]) => void;
  grouping?: boolean;
}) {
  const { t } = useTranslation();
  // The people on the board, those with most cards first.
  const people = useMemo(() => {
    const byId = new Map<number, { user: NonNullable<Card['assignee']>; n: number }>();
    for (const c of cards) {
      if (!c.assignee) continue;
      const e = byId.get(c.assignee.id) ?? { user: c.assignee, n: 0 };
      e.n++;
      byId.set(c.assignee.id, e);
    }
    return [...byId.values()].sort((a, b) => b.n - a.n).map((e) => e.user);
  }, [cards]);
  const unassigned = cards.some((c) => !c.assignee);
  const toggle = (id: string) =>
    filters.setWho(filters.who.includes(id) ? filters.who.filter((w) => w !== id) : [...filters.who, id]);
  const laneTypes: LaneType[] = ['none', 'assignee', 'epic', 'parent'];
  if (board.config.swimlanes.queries?.length) laneTypes.push('queries');
  return (
    <Group gap="sm" wrap="wrap">
      <TextInput
        size="xs"
        w={220}
        leftSection={<IconSearch size={14} />}
        placeholder={t('agile.filterCards')}
        aria-label={t('agile.filterCards')}
        value={filters.text}
        onChange={(e) => filters.setText(e.currentTarget.value)}
        rightSection={
          filters.text ? (
            <CloseButton size="xs" onClick={() => filters.setText('')} aria-label={t('common.clear')} />
          ) : undefined
        }
      />
      {(people.length > 0 || unassigned) && (
        <Group gap={4} wrap="nowrap" role="group" aria-label={t('agile.filterPeople')}>
          {people.slice(0, 8).map((u) => {
            const on = filters.who.includes(String(u.id));
            return (
              <Tooltip key={u.id} label={u.displayName} withinPortal>
                <UnstyledButton
                  className="kogge-person-filter"
                  data-active={on || undefined}
                  aria-pressed={on}
                  aria-label={u.displayName}
                  onClick={() => toggle(String(u.id))}
                >
                  <PersonAvatar person={u} size={28} />
                </UnstyledButton>
              </Tooltip>
            );
          })}
          {unassigned && (
            <Tooltip label={t('issues.unassigned')} withinPortal>
              <UnstyledButton
                className="kogge-person-filter"
                data-active={filters.who.includes('none') || undefined}
                aria-pressed={filters.who.includes('none')}
                aria-label={t('issues.unassigned')}
                onClick={() => toggle('none')}
              >
                <Avatar size={28} radius="xl" aria-hidden>
                  <IconUserOff size={14} />
                </Avatar>
              </UnstyledButton>
            </Tooltip>
          )}
        </Group>
      )}
      {grouping && (
        <Select
          size="xs"
          w={190}
          aria-label={t('agile.groupBy')}
          leftSection={
            <Text size="xs" c="dimmed" pl={4}>
              {t('agile.groupByShort')}
            </Text>
          }
          leftSectionWidth={64}
          data={laneTypes.map((v) => ({ value: v, label: t(`agile.lanes.${v}`) }))}
          value={filters.lanes}
          onChange={filters.setLanes}
          allowDeselect={false}
        />
      )}
      <QuickFilterChips board={board} value={quick} onChange={setQuick} />
      {filters.active && (
        <Button size="compact-xs" variant="subtle" onClick={filters.clear}>
          {t('agile.clearFilters')}
        </Button>
      )}
    </Group>
  );
}

/** How far the sprint is: done work against all of it, by the board's estimation. */
function SprintProgress({ board, cards }: { board: Board; cards: Card[] }) {
  const { t } = useTranslation();
  const estimation = board.config.estimation;
  const count = estimation.type === 'count';
  const value = (c: Card) => (count ? 1 : (c.estimate ?? 0));
  const relevant = cards.filter((c) => c.type.level !== -1);
  const total = relevant.reduce((n, c) => n + value(c), 0);
  const done = relevant.filter((c) => c.done).reduce((n, c) => n + value(c), 0);
  if (total === 0) return null;
  const label = count
    ? t('agile.issuesDone', { done, total })
    : t('agile.estimateDone', {
        done: formatEstimate(done, estimation) || '0',
        total: formatEstimate(total, estimation),
      });
  return (
    <Stack gap={4} w={260}>
      <Progress value={(done / total) * 100} size="sm" color="teal" aria-label={label} />
      <Text size="xs" c="dimmed">
        {label}
      </Text>
    </Stack>
  );
}

function daysLeft(end?: string) {
  if (!end) return null;
  return Math.ceil((new Date(end).getTime() - Date.now()) / 86_400_000);
}

export function BoardPage() {
  const { t } = useTranslation();
  const { key = '', boardId = '' } = useParams();
  const id = Number(boardId);
  const [quick, setQuick] = useQuickFilters();
  const view = useBoardView(id, quick);
  const [openKey, setOpenKey] = useState<string | null>(null);
  const [completing, setCompleting] = useState(false);
  const future = useSprints(id, 'future', completing);
  const filters = useBoardFilters(view.data?.board);
  if (view.error) return <ErrorAlert error={view.error} />;
  const data = view.data;
  if (!data) return null;
  const board = data.board;
  const sprint = data.sprints[0];
  const left = daysLeft(sprint?.end);
  const unmapped = data.cards.filter((c) => c.column < 0).length;
  return (
    <Stack gap="md">
      <BoardNav projectKey={key} board={board} active="board" />
      <Group justify="space-between" align="flex-start">
        <Stack gap={2}>
          <Title order={3}>{board.type === 'scrum' && sprint ? sprint.name : board.name}</Title>
          {sprint && (
            <Text size="sm" c="dimmed">
              {sprint.goal ? `${sprint.goal} · ` : ''}
              {sprint.start ? formatDate(sprint.start) : ''} – {sprint.end ? formatDate(sprint.end) : ''}
              {left !== null && ` · ${left >= 0 ? t('agile.daysLeft', { count: left }) : t('agile.overdue')}`}
            </Text>
          )}
        </Stack>
        <Group gap="lg" align="flex-start">
          {sprint && <SprintProgress board={board} cards={data.cards.filter((c) => c.column >= 0)} />}
          {sprint && board.canManageSprints && (
            <Button variant="light" onClick={() => setCompleting(true)}>
              {t('agile.completeSprint')}
            </Button>
          )}
        </Group>
      </Group>
      <BoardToolbar
        board={board}
        cards={data.cards.filter((c) => c.column >= 0)}
        filters={filters}
        quick={quick}
        setQuick={setQuick}
      />
      {board.type === 'scrum' && data.sprints.length === 0 ? (
        <Alert color="blue" variant="light" title={t('agile.noActiveSprint')}>
          <Text size="sm">
            {t('agile.noActiveSprintText')}{' '}
            <Anchor component={Link} to={`/projects/${key}/boards/${board.id}/backlog`}>
              {t('agile.backlog')}
            </Anchor>
          </Text>
        </Alert>
      ) : (
        <Columns
          view={data}
          quick={quick}
          onOpen={setOpenKey}
          shown={filters.shown}
          laneType={filters.lanes}
        />
      )}
      {unmapped > 0 && (
        <Text size="xs" c="dimmed">
          {t('agile.unmapped', { count: unmapped })}
        </Text>
      )}
      {data.truncated && (
        <Text size="xs" c="orange">
          {t('agile.truncated')}
        </Text>
      )}
      <Drawer opened={!!openKey} onClose={() => setOpenKey(null)} position="right" size="xl" title={openKey}>
        {openKey && <IssueView key={openKey} issueKey={openKey} />}
      </Drawer>
      {completing && sprint && (
        <CompleteSprintModal
          sprint={sprint}
          done={data.cards.filter((c) => c.done && c.type.level !== -1).length}
          open={data.cards.filter((c) => !c.done && c.type.level !== -1).length}
          futureSprints={future.data ?? []}
          onClose={() => setCompleting(false)}
        />
      )}
    </Stack>
  );
}
