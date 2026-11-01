// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Tooltip, type ActionIconProps, type FloatingPosition } from '@mantine/core';
import type { TablerIcon } from '@tabler/icons-react';
import type { MouseEventHandler } from 'react';

export interface IconButtonProps extends Omit<ActionIconProps, 'children'> {
  /** What the button does: its tooltip and its name for screen readers. */
  label: string;
  icon: TablerIcon;
  onClick?: MouseEventHandler<HTMLButtonElement>;
  tooltipPosition?: FloatingPosition;
}

/** An icon-only button of 32 px that always says what it does: a tooltip and an accessible name of one text. */
export function IconButton({
  label,
  icon: Icon,
  variant = 'subtle',
  color = 'gray',
  tooltipPosition,
  ...rest
}: IconButtonProps) {
  return (
    <Tooltip label={label} position={tooltipPosition}>
      <ActionIcon variant={variant} color={color} aria-label={label} {...rest}>
        <Icon size={18} />
      </ActionIcon>
    </Tooltip>
  );
}
