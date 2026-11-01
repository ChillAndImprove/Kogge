// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Avatar, type AvatarProps } from '@mantine/core';
import type { CSSProperties } from 'react';
import { avatarColor, keyLetters } from './avatarColor';

export interface ContainerAvatarProps extends Omit<AvatarProps, 'size' | 'radius' | 'color' | 'children'> {
  /** The key of the project, space or code project: its letters and its colour. */
  containerKey: string;
  /** 20 and 24 in lists, 32 in headers and cards, 40 to 48 at the top of a container's pages. */
  size?: number;
  /** An image instead of the letters. */
  src?: string | null;
}

/**
 * The avatar of a container (an issue project, a space, a code project): a rounded square, unlike people's round
 * ones, with two letters of its key in white on the key's colour.
 */
export function ContainerAvatar({ containerKey, size = 32, src, style, ...rest }: ContainerAvatarProps) {
  // Corners grow with the size (4 px at 20, 6 at 32, 8 at 40); letters are 40 % of it, bold.
  const radius = Math.min(10, Math.max(4, Math.round(size * 0.19)));
  return (
    <Avatar
      size={size}
      radius={radius}
      variant="filled"
      color={avatarColor(containerKey)}
      src={src ?? undefined}
      aria-hidden
      style={[{ '--avatar-placeholder-fz': `${Math.round(size * 0.4)}px` } as CSSProperties, style]}
      {...rest}
    >
      {keyLetters(containerKey)}
    </Avatar>
  );
}
