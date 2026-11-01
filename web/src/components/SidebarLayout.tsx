// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Group, Stack, Text, UnstyledButton, VisuallyHidden } from '@mantine/core';
import { useLocalStorage, useMediaQuery } from '@mantine/hooks';
import { IconLayoutSidebarLeftCollapse, IconLayoutSidebarLeftExpand } from '@tabler/icons-react';
import { createContext, use, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { IconButton } from './IconButton';

/** Below md (992 px): tablets and phones, where a page's navigation moves into the header's menu drawer. */
export const narrowQuery = '(max-width: 61.99em)';

/** The header's menu drawer of narrow screens (provided by the shell, app/Shell.tsx). */
export interface ShellDrawer {
  /** The place in the open drawer that takes a page's navigation; null while the drawer is closed. */
  slot: HTMLElement | null;
  open: () => void;
}

export const ShellDrawerContext = createContext<ShellDrawer>({ slot: null, open: () => {} });

/** The container a sidebar belongs to: its avatar, name and kind ("SHOP · Software project"). */
export interface SidebarHeaderProps {
  avatar: ReactNode;
  title: ReactNode;
  meta?: ReactNode;
  /** The container's start page. */
  to?: string;
}

/** The head of a sidebar: the container's avatar, name and kind. */
export function SidebarHeader({ avatar, title, meta, to }: SidebarHeaderProps) {
  const name = (
    <Stack gap={0} miw={0}>
      <Text fw={600} size="md" lh={1.3} truncate>
        {title}
      </Text>
      {meta && (
        <Text size="xs" c="dimmed" truncate>
          {meta}
        </Text>
      )}
    </Stack>
  );
  return (
    <Group gap="sm" wrap="nowrap" miw={0} className="kogge-sidebar-header">
      {avatar}
      {to ? (
        <Link to={to} className="kogge-sidebar-title">
          {name}
        </Link>
      ) : (
        name
      )}
    </Group>
  );
}

export interface SidebarLayoutProps {
  /** The container: at the top of the sidebar, and the line above the page on narrow screens. */
  header?: SidebarHeaderProps;
  /** The container's navigation (NavLinks) and its other parts (a page tree, a "Create" button). */
  sidebar: ReactNode;
  children: ReactNode;
}

/**
 * A container's pages (a project's, a space's, the administration's): a sidebar of 248 px on a surface one step
 * off the page, from the header to the bottom of the window, which can be collapsed. On narrow screens the content
 * comes first: the navigation moves into the header's menu drawer, and a line naming the container opens it.
 */
export function SidebarLayout({ header, sidebar, children }: SidebarLayoutProps) {
  const { t } = useTranslation();
  const narrow = useMediaQuery(narrowQuery, false, { getInitialValueInEffect: false });
  const drawer = use(ShellDrawerContext);
  const [collapsed, setCollapsed] = useLocalStorage({ key: 'kogge.sidebar.collapsed', defaultValue: false });
  if (narrow) {
    return (
      <>
        {header && (
          <UnstyledButton className="kogge-context-bar" onClick={drawer.open} aria-haspopup="dialog">
            <Group gap="xs" wrap="nowrap" miw={0}>
              {header.avatar}
              <Text fw={600} size="sm" truncate>
                {header.title}
              </Text>
            </Group>
            <VisuallyHidden>{t('shell.openNavigation')}</VisuallyHidden>
            <IconLayoutSidebarLeftExpand size={18} aria-hidden />
          </UnstyledButton>
        )}
        {drawer.slot &&
          createPortal(
            <Stack gap="sm">
              {header && <SidebarHeader {...header} />}
              {sidebar}
            </Stack>,
            drawer.slot,
          )}
        {children}
      </>
    );
  }
  return (
    <div className="kogge-sidebar-layout" data-collapsed={collapsed || undefined}>
      <aside className="kogge-sidebar">
        {collapsed ? (
          <Stack gap="sm" align="center">
            <IconButton
              label={t('shell.expandSidebar')}
              icon={IconLayoutSidebarLeftExpand}
              tooltipPosition="right"
              onClick={() => setCollapsed(false)}
            />
            {header?.avatar}
          </Stack>
        ) : (
          <Stack gap="xs">
            {header && (
              <Group gap={4} wrap="nowrap" justify="space-between" mb={4}>
                <SidebarHeader {...header} />
                <IconButton
                  label={t('shell.collapseSidebar')}
                  icon={IconLayoutSidebarLeftCollapse}
                  className="kogge-sidebar-collapse"
                  onClick={() => setCollapsed(true)}
                />
              </Group>
            )}
            {sidebar}
          </Stack>
        )}
      </aside>
      <div className="kogge-sidebar-main">{children}</div>
    </div>
  );
}
