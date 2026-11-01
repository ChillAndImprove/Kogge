// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Badge, Box, Group, Paper, Text, Tooltip } from '@mantine/core';
import { IconSubtask } from '@tabler/icons-react';
import type { CSSProperties, MouseEvent, ReactNode } from 'react';
import { formatDate } from '../../app/format';
import { PersonAvatar } from '../../components/PersonAvatar';
import { PriorityIcon, TypeIcon } from '../issues/shared';
import { epicColor, formatEstimate, type Board, type Card } from './hooks';

/** The left border colour of a card: by issue type, priority or the first matching colour query. */
function cardColor(card: Card, board: Board): string | undefined {
  switch (board.config.card.colors) {
    case 'issuetype':
      return card.type.color;
    case 'priority':
      return card.priority?.color;
    case 'queries':
      return card.color;
  }
  return undefined;
}

export function EpicBadge({ epic }: { epic: NonNullable<Card['epic']> }) {
  return (
    <Tooltip label={epic.key} openDelay={400}>
      <Badge size="xs" variant="light" color={epicColor(epic.id)} tt="none" maw={160}>
        {epic.summary}
      </Badge>
    </Tooltip>
  );
}

export function AssigneeAvatar({ card, size = 22 }: { card: Card; size?: number }) {
  if (!card.assignee) return null;
  return (
    <Tooltip label={card.assignee.displayName} openDelay={300}>
      <PersonAvatar person={card.assignee} size={size} labelled />
    </Tooltip>
  );
}

/** A card on a board. */
export function IssueCard({
  card,
  board,
  onOpen,
  selected,
  dragging,
  style,
  handle,
  menu,
}: {
  card: Card;
  board: Board;
  onOpen?: (key: string, e: MouseEvent) => void;
  selected?: boolean;
  dragging?: boolean;
  style?: CSSProperties;
  handle?: ReactNode;
  /** Actions shown at the top right on hover. */
  menu?: ReactNode;
}) {
  const color = cardColor(card, board);
  const estimate = formatEstimate(card.estimate, board.config.estimation);
  return (
    <Paper
      withBorder
      shadow={dragging ? 'md' : 'xs'}
      p="xs"
      radius="sm"
      className="kogge-card"
      data-selected={selected || undefined}
      style={{
        borderLeft: color ? `3px solid var(--mantine-color-${color}-6)` : undefined,
        opacity: dragging ? 0.85 : 1,
        cursor: 'grab',
        ...style,
      }}
      onClick={(e) => onOpen?.(card.key, e)}
    >
      {handle}
      {menu && <div className="kogge-card-actions">{menu}</div>}
      <Text
        size="sm"
        lineClamp={3}
        mb={6}
        pr={menu ? 22 : undefined}
        td={card.done ? 'line-through' : undefined}
        c={card.done ? 'dimmed' : undefined}
      >
        {card.summary}
      </Text>
      {card.epic && (
        <Box mb={6}>
          <EpicBadge epic={card.epic} />
        </Box>
      )}
      {card.extra.length > 0 && (
        <Box mb={6}>
          {card.extra
            .filter((x) => x.value)
            .map((x) => (
              <Text key={x.field} size="xs" c="dimmed" truncate>
                {x.name}: {x.field === 'created' || x.field === 'updated' ? formatDate(x.value) : x.value}
              </Text>
            ))}
        </Box>
      )}
      <Group justify="space-between" gap={6} wrap="nowrap">
        <Group gap={6} wrap="nowrap" style={{ minWidth: 0 }}>
          <TypeIcon type={card.type} size={16} />
          <PriorityIcon priority={card.priority} />
          <Text size="xs" fw={600} c="dimmed" truncate>
            {card.key}
          </Text>
          {card.subtasks > 0 && (
            <Tooltip label={`${card.subtasksDone}/${card.subtasks}`}>
              <Group gap={2} wrap="nowrap">
                <IconSubtask size={12} />
                <Text size="xs" c="dimmed">
                  {card.subtasksDone}/{card.subtasks}
                </Text>
              </Group>
            </Tooltip>
          )}
        </Group>
        <Group gap={6} wrap="nowrap">
          {estimate && (
            <Badge size="sm" variant="default" radius="xl">
              {estimate}
            </Badge>
          )}
          <AssigneeAvatar card={card} />
        </Group>
      </Group>
    </Paper>
  );
}
