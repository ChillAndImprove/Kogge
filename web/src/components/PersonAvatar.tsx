// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Avatar, type AvatarProps } from '@mantine/core';
import type { CSSProperties } from 'react';
import { initials, personColor } from './avatarColor';

/** Who an avatar shows: a user (as the API refers to one) or just a name (authors of commits, senders). */
export interface PersonLike {
  displayName: string;
  username?: string;
  /** Deactivated people are drawn in grey. */
  active?: boolean;
}

/** The sizes of people's avatars: lists (20, 24), comments and the header (32), cards (40), a person's page (64). */
export type PersonAvatarSize = 20 | 24 | 32 | 40 | 64;

export interface PersonAvatarProps extends Omit<
  AvatarProps,
  'size' | 'radius' | 'color' | 'name' | 'children'
> {
  person: PersonLike | string | null | undefined;
  size?: PersonAvatarSize | number;
  /** The avatar stands alone (no name next to it): it is then named for screen readers. */
  labelled?: boolean;
}

/** A person's round avatar: white initials on their colour; the same person looks the same everywhere. */
export function PersonAvatar({ person, size = 24, labelled = false, style, ...rest }: PersonAvatarProps) {
  const p = typeof person === 'string' ? { displayName: person } : person;
  const name = p?.displayName ?? '';
  const inactive = p?.active === false;
  // Small avatars get letters a little larger than Mantine's (size / 2.5), so that they stay readable.
  const letters = size <= 20 ? '9px' : size <= 24 ? '10px' : undefined;
  return (
    <Avatar
      size={size}
      radius="100%"
      variant="filled"
      color={!p || inactive ? 'gray.6' : personColor(name)}
      style={[letters ? ({ '--avatar-placeholder-fz': letters } as CSSProperties) : undefined, style]}
      {...(labelled ? { role: 'img', 'aria-label': name || '?' } : { 'aria-hidden': true })}
      {...rest}
    >
      {initials(name)}
    </Avatar>
  );
}
