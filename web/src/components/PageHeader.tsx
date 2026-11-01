// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Button,
  Group,
  Menu,
  Stack,
  Text,
  Title,
  Tooltip,
  type MantineColor,
  type MantineSpacing,
} from '@mantine/core';
import { useMediaQuery } from '@mantine/hooks';
import { IconDots } from '@tabler/icons-react';
import { Fragment, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';

/** Below sm (768 px): phones, where a header's actions go under its title. */
export const phoneQuery = '(max-width: 47.99em)';

/** An action of a page header that is a button where there is room and an item of its "…" menu on phones. */
export interface PageAction {
  label: string;
  icon?: ReactNode;
  onClick?: () => void;
  /** An address in Kogge: the action is a link. */
  to?: string;
  color?: MantineColor;
  disabled?: boolean;
}

export interface PageHeaderProps {
  title: ReactNode;
  subtitle?: ReactNode;
  /** The main action (usually one filled button): always visible, on phones in a row under the title. */
  actions?: ReactNode;
  /** Further actions: default buttons before the main one, folded into the "…" menu on phones. */
  secondary?: PageAction[];
  /** Rare actions, always in the "…" menu: Menu.Item and Menu.Divider elements. */
  menu?: ReactNode;
  /** Above the title: where the page is, as links (the last one may be the page's parent). */
  breadcrumbs?: { label: string; to?: string }[] | ReactNode;
  /** After the title: its state, such as a badge. */
  badge?: ReactNode;
  mb?: MantineSpacing;
}

function SecondaryButton({ action }: { action: PageAction }) {
  const props = {
    variant: 'default' as const,
    leftSection: action.icon,
    color: action.color,
    disabled: action.disabled,
    onClick: action.onClick,
  };
  return action.to ? (
    <Button component={Link} to={action.to} {...props}>
      {action.label}
    </Button>
  ) : (
    <Button {...props}>{action.label}</Button>
  );
}

function SecondaryItem({ action }: { action: PageAction }) {
  const props = {
    leftSection: action.icon,
    color: action.color,
    disabled: action.disabled,
    onClick: action.onClick,
  };
  return action.to ? (
    <Menu.Item component={Link} to={action.to} {...props}>
      {action.label}
    </Menu.Item>
  ) : (
    <Menu.Item {...props}>{action.label}</Menu.Item>
  );
}

/** Where a page is: links of 13 px, dimmed, divided by slashes. */
function Breadcrumbs({ items }: { items: { label: string; to?: string }[] }) {
  return (
    <Text size="sm" c="dimmed" className="kogge-breadcrumbs" component="nav">
      {items.map((item, i) => (
        <Fragment key={`${item.label}-${i}`}>
          {i > 0 && <span aria-hidden> / </span>}
          {item.to ? (
            <Anchor component={Link} to={item.to} c="dimmed" size="sm">
              {item.label}
            </Anchor>
          ) : (
            <span>{item.label}</span>
          )}
        </Fragment>
      ))}
    </Text>
  );
}

/**
 * The head of every page: a breadcrumb, the title (24 px) with its state badge, a meta line (13 px, dimmed), and
 * the actions at the right: the main one, then the secondary ones, then "…". The title wraps instead of being
 * squeezed; on phones the actions move to a row under the title, the main one stays visible and the secondary ones
 * fold into "…".
 */
export function PageHeader({
  title,
  subtitle,
  actions,
  secondary = [],
  menu,
  breadcrumbs,
  badge,
  mb = 20,
}: PageHeaderProps) {
  const { t } = useTranslation();
  const [moreOpened, setMoreOpened] = useState(false);
  const phone = useMediaQuery(phoneQuery, false, { getInitialValueInEffect: false });
  const folded = phone ? secondary : [];
  const shown = phone ? [] : secondary;
  const more = folded.length > 0 || !!menu;
  return (
    <Group className="kogge-page-header" justify="space-between" align="flex-start" gap="sm" mb={mb}>
      <Stack gap={4} className="kogge-page-header-text">
        {Array.isArray(breadcrumbs) ? <Breadcrumbs items={breadcrumbs} /> : breadcrumbs}
        <Group gap="xs" align="center" wrap="wrap">
          <Title order={2} className="kogge-page-title">
            {title}
          </Title>
          {badge}
        </Group>
        {subtitle && (
          <Text c="dimmed" size="sm">
            {subtitle}
          </Text>
        )}
      </Stack>
      {(actions || shown.length > 0 || more) && (
        <Group gap="xs" className="kogge-page-header-actions">
          {actions}
          {shown.map((a) => (
            <SecondaryButton key={a.label} action={a} />
          ))}
          {more && (
            <Menu position="bottom-end" opened={moreOpened} onChange={setMoreOpened}>
              <Tooltip label={t('common.moreActions')} disabled={moreOpened}>
                <Menu.Target>
                  <ActionIcon variant="default" size={32} aria-label={t('common.moreActions')}>
                    <IconDots size={18} />
                  </ActionIcon>
                </Menu.Target>
              </Tooltip>
              <Menu.Dropdown>
                {folded.map((a) => (
                  <SecondaryItem key={a.label} action={a} />
                ))}
                {folded.length > 0 && menu && <Menu.Divider />}
                {menu}
              </Menu.Dropdown>
            </Menu>
          )}
        </Group>
      )}
    </Group>
  );
}
