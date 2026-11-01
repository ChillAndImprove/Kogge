// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Paper, Text, UnstyledButton } from '@mantine/core';
import { forwardRef, useImperativeHandle, useState } from 'react';

export interface MentionItem {
  id: number;
  label: string;
  username: string;
}

export interface MentionListHandle {
  onKeyDown: (event: KeyboardEvent) => boolean;
}

interface Props {
  items: MentionItem[];
  command: (item: { user: number; label: string }) => void;
}

/** The suggestion popup for @mentions. */
export const MentionList = forwardRef<MentionListHandle, Props>(function MentionList(
  { items, command },
  ref,
) {
  const [selected, setSelected] = useState(0);
  const select = (i: number) => {
    const item = items[i];
    if (item) command({ user: item.id, label: item.label });
  };
  useImperativeHandle(ref, () => ({
    onKeyDown: (event) => {
      if (event.key === 'ArrowUp') {
        setSelected((s) => (s + items.length - 1) % Math.max(items.length, 1));
        return true;
      }
      if (event.key === 'ArrowDown') {
        setSelected((s) => (s + 1) % Math.max(items.length, 1));
        return true;
      }
      if (event.key === 'Enter') {
        select(selected);
        return true;
      }
      return false;
    },
  }));
  if (items.length === 0) return null;
  return (
    <Paper shadow="md" withBorder p={4} miw={220} role="listbox">
      {items.map((item, i) => (
        <UnstyledButton
          key={item.id}
          display="block"
          w="100%"
          px="sm"
          py={4}
          role="option"
          aria-selected={i === selected}
          style={{
            borderRadius: 6,
            background: i === selected ? 'var(--mantine-primary-color-light)' : undefined,
          }}
          onMouseDown={(e) => {
            e.preventDefault();
            select(i);
          }}
        >
          <Text size="sm">{item.label}</Text>
          <Text size="xs" c="dimmed">
            {item.username}
          </Text>
        </UnstyledButton>
      ))}
    </Paper>
  );
});
