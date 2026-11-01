// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { NavLink as MantineNavLink, Stack, Text, ThemeIcon } from '@mantine/core';
import {
  IconAdjustments,
  IconBell,
  IconCategory,
  IconDatabase,
  IconFlag,
  IconHistory,
  IconKey,
  IconLayoutDashboard,
  IconLink,
  IconLogin2,
  IconLock,
  IconProgressCheck,
  IconRoute,
  IconServer,
  IconSettings,
  IconShieldCheck,
  IconShieldLock,
  IconUserStar,
  IconUsers,
  IconUsersGroup,
  IconCircleCheck,
  IconWebhook,
} from '@tabler/icons-react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { NavLink, Outlet } from 'react-router';
import { useSession } from '../../app/session';
import { SidebarLayout } from '../../components/SidebarLayout';
import { products } from '../../products';
import { NotFoundPage } from '../NotFoundPage';

const items = [
  { to: '/admin', key: 'overviewNav', icon: IconLayoutDashboard, end: true },
  { to: '/admin/users', key: 'users', icon: IconUsers },
  { to: '/admin/groups', key: 'groups', icon: IconUsersGroup },
  { to: '/admin/permissions', key: 'permissions', icon: IconKey },
  { to: '/admin/settings', key: 'settings', icon: IconSettings },
  { to: '/admin/sso', key: 'ssoNav', icon: IconLogin2 },
  { to: '/admin/audit', key: 'audit', icon: IconHistory },
  { to: '/admin/system', key: 'system', icon: IconServer },
  { to: '/admin/storage', key: 'storage', icon: IconDatabase },
  { to: '/admin/webhooks', key: 'webhooks', icon: IconWebhook },
] as const;

const issueItems = [
  { to: '/issues/admin/types', key: 'issueTypes', icon: IconCategory },
  { to: '/issues/admin/statuses', key: 'statuses', icon: IconProgressCheck },
  { to: '/issues/admin/priorities', key: 'priorities', icon: IconFlag },
  { to: '/issues/admin/resolutions', key: 'resolutions', icon: IconCircleCheck },
  { to: '/issues/admin/link-types', key: 'linkTypes', icon: IconLink },
  { to: '/issues/admin/roles', key: 'roles', icon: IconUserStar },
  { to: '/issues/admin/fields', key: 'fields', icon: IconAdjustments },
  { to: '/issues/admin/workflows', key: 'workflows', icon: IconRoute },
  { to: '/issues/admin/permission-schemes', key: 'permissionSchemes', icon: IconShieldCheck },
  { to: '/issues/admin/security-schemes', key: 'securitySchemes', icon: IconLock },
  { to: '/issues/admin/notification-schemes', key: 'notificationSchemes', icon: IconBell },
] as const;

/** A label above a group of the sidebar's items. */
function GroupLabel({ children }: { children: ReactNode }) {
  return (
    <Text size="xs" fw={600} c="dimmed" px="sm" mt="md" mb={4}>
      {children}
    </Text>
  );
}

function Item({ to, label, icon, end }: { to: string; label: string; icon: ReactNode; end?: boolean }) {
  return <MantineNavLink component={NavLink} to={to} end={end} label={label} leftSection={icon} />;
}

/** The platform's administration: people, permissions and settings, with links to the products' own. */
export function AdminLayout() {
  const { t } = useTranslation();
  const { can, me } = useSession();
  if (!can('admin')) return <NotFoundPage />;
  const Kontor = products.kontor.Logo;
  const Helling = products.helling.Logo;
  return (
    <SidebarLayout
      header={{
        avatar: (
          <ThemeIcon size={32} radius={6} variant="light" color="gray" aria-hidden>
            <IconShieldLock size={18} />
          </ThemeIcon>
        ),
        title: t('admin.title'),
        meta: me.instanceName,
        to: '/admin',
      }}
      sidebar={
        <Stack gap={2}>
          {items.map((item) => (
            <Item
              key={item.key}
              to={item.to}
              end={'end' in item ? item.end : undefined}
              label={t(`admin.${item.key}`)}
              icon={<item.icon size={16} />}
            />
          ))}
          <GroupLabel>{t('admin.products')}</GroupLabel>
          <Item to="/issues/admin" label={t('admin.kontorConfiguration')} icon={<Kontor size={16} />} />
          <Item to="/builds/agents" label={t('admin.hellingAgents')} icon={<Helling size={16} />} />
        </Stack>
      }
    >
      <Outlet />
    </SidebarLayout>
  );
}

/** Kontor's configuration: issue types, workflows, fields and schemes. */
export function IssueAdminLayout() {
  const { t } = useTranslation();
  const { can } = useSession();
  if (!can('admin')) return <NotFoundPage />;
  const Kontor = products.kontor.Logo;
  return (
    <SidebarLayout
      header={{
        avatar: <Kontor size={32} />,
        title: t('kontor.configuration'),
        meta: products.kontor.name,
        to: '/issues/admin',
      }}
      sidebar={
        <Stack gap={2}>
          {issueItems.map((item) => (
            <Item
              key={item.key}
              to={item.to}
              label={t(`issueAdmin.${item.key}`)}
              icon={<item.icon size={16} />}
            />
          ))}
        </Stack>
      }
    >
      <Outlet />
    </SidebarLayout>
  );
}
