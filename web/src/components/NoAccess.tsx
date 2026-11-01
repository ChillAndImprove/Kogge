// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { IconLock } from '@tabler/icons-react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { EmptyState } from './EmptyState';

export interface NoAccessProps {
  /** "Pipeline settings are managed by the administrators of payments"; "No access" by default. */
  title?: ReactNode;
  /** Who can do it, or how to get the permission. */
  description?: ReactNode;
  /** A way on: "Open the permissions of Webshop", back to the repository … */
  action?: ReactNode;
  secondary?: ReactNode;
  size?: 'sm' | 'md';
}

/**
 * For pages and sections that know beforehand that the viewer may not see or change something: a calm statement
 * with a lock and who to ask, in place of the section (never an error, never actions that would fail).
 */
export function NoAccess({ title, description, action, secondary, size = 'md' }: NoAccessProps) {
  const { t } = useTranslation();
  return (
    <EmptyState
      icon={IconLock}
      color="gray"
      size={size}
      title={title ?? t('noAccess.title')}
      description={description ?? t('noAccess.text')}
      action={action}
      secondary={secondary}
    />
  );
}
