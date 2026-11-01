// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Group, Stack, Text, ThemeIcon, type MantineColor } from '@mantine/core';
import type { TablerIcon } from '@tabler/icons-react';
import type { ReactNode } from 'react';

export interface EmptyStateProps {
  /** A symbol of what is missing (a Tabler icon). */
  icon: TablerIcon;
  /** What is missing, or that all is done: "No pull requests to review". */
  title: ReactNode;
  /** One sentence: why it is empty, or what appears here once there is something. */
  description?: ReactNode;
  /** The next step: usually one button (a filled one on pages, a light or subtle one in panels). */
  action?: ReactNode;
  /** A further way on, quieter: a link such as "Notification settings". */
  secondary?: ReactNode;
  /** md for a page or a large section, sm for panels, cards and popovers. */
  size?: 'sm' | 'md';
  /** The colour of the icon's tint: the product's by default; gray for calm states such as "no access". */
  color?: MantineColor;
  /** Placed at the start (in narrow panels) instead of in the middle. */
  align?: 'center' | 'start';
}

/**
 * What an empty list, page or panel shows instead of a bare dimmed sentence: an icon in a tinted circle, a title,
 * one sentence and the next step, so that people see what the place is for and what to do.
 */
export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
  secondary,
  size = 'md',
  color,
  align = 'center',
}: EmptyStateProps) {
  const small = size === 'sm';
  const centered = align === 'center';
  return (
    <Stack
      className="kogge-empty"
      align={centered ? 'center' : 'flex-start'}
      ta={centered ? 'center' : 'start'}
      gap={small ? 6 : 'xs'}
      py={small ? 'md' : 48}
      px="md"
    >
      <ThemeIcon
        size={small ? 40 : 56}
        radius="100%"
        variant="light"
        color={color}
        mb={small ? 2 : 6}
        aria-hidden
      >
        <Icon size={small ? 20 : 28} stroke={1.6} />
      </ThemeIcon>
      <Text fw={600} size={small ? 'sm' : 'lg'} maw={480}>
        {title}
      </Text>
      {description && (
        <Text c="dimmed" size={small ? 'xs' : 'sm'} maw={small ? 320 : 460}>
          {description}
        </Text>
      )}
      {(action || secondary) && (
        <Group gap="sm" mt={small ? 4 : 'xs'} justify={centered ? 'center' : 'flex-start'}>
          {action}
          {secondary}
        </Group>
      )}
    </Stack>
  );
}
