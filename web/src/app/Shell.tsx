// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Anchor,
  AppShell,
  Box,
  Burger,
  Divider,
  Drawer,
  Group,
  Indicator,
  Menu,
  NavLink as MantineNavLink,
  Popover,
  ScrollArea,
  SegmentedControl,
  Stack,
  Text,
  ThemeIcon,
  Tooltip,
  UnstyledButton,
  useMantineColorScheme,
} from '@mantine/core';
import { useMediaQuery } from '@mantine/hooks';
import { Notifications } from '@mantine/notifications';
import {
  IconApi,
  IconBellCheck,
  IconBell,
  IconGridDots,
  IconLogout,
  IconSettings,
  IconShieldLock,
  IconSpeakerphone,
  IconUserCircle,
} from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, matchPath, Outlet, useLocation, useNavigate } from 'react-router';
import { api, unwrap, type Schemas } from '../api/client';
import { EmptyState } from '../components/EmptyState';
import { Logo } from '../components/Logo';
import { notifyError } from '../components/notify';
import { phoneQuery } from '../components/PageHeader';
import { PersonAvatar } from '../components/PersonAvatar';
import { personPath } from '../components/PersonLink';
import { ShellDrawerContext } from '../components/SidebarLayout';
import { CreateRepositoryProvider } from '../pages/code/CreateRepo';
import { CreateIssueProvider } from '../pages/issues/CreateIssue';
import { CreatePageProvider } from '../pages/wiki/CreatePage';
import { productList, productOf, products, type Product, type ProductId } from '../products';
import { formatRelative, setTimeZone } from './format';
import { useLiveUpdates } from './live';
import {
  HeaderSearchField,
  QuickSearchButton,
  QuickSearchProvider,
  useQuickSearchScope,
  type QuickSearchScope,
} from './QuickSearch';
import { meQueryKey, useSession } from './session';

/**
 * Switching between the light and the dark scheme happens at once: transitions of colours (on hover and focus)
 * would otherwise run for every control, showing text on the other scheme's background for a moment.
 */
function useInstantSchemeChange() {
  useEffect(() => {
    const root = document.documentElement;
    let frame = 0;
    const observer = new MutationObserver(() => {
      root.classList.add('kogge-no-transitions');
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        frame = requestAnimationFrame(() => root.classList.remove('kogge-no-transitions'));
      });
    });
    observer.observe(root, { attributes: true, attributeFilter: ['data-mantine-color-scheme'] });
    return () => {
      observer.disconnect();
      cancelAnimationFrame(frame);
    };
  }, []);
}

/** What every page needs, whichever product it belongs to. */
export function Root() {
  const { me } = useSession();
  setTimeZone(me.user.timeZone);
  useLiveUpdates(true);
  useInstantSchemeChange();
  // Notifications render inside the router so that their messages can contain links. The quick search offers
  // the create dialogs, so it comes inside their providers.
  return (
    <CreateIssueProvider>
      <CreatePageProvider>
        <CreateRepositoryProvider>
          <QuickSearchProvider>
            <Notifications position="top-right" />
            <Outlet />
          </QuickSearchProvider>
        </CreateRepositoryProvider>
      </CreatePageProvider>
    </CreateIssueProvider>
  );
}

/** A place of the app switcher (and of the drawer of narrow screens): a product, the overview, the administration. */
interface Place {
  to: string;
  label: string;
  description?: string;
  icon: ReactNode;
  /** The icon in the drawer's list, of the size of its other icons (16 px). */
  small: ReactNode;
  current: boolean;
}

/** The products, the overview and the administration; all with an icon of 32 px, so that their names line up. */
function usePlaces(current: ProductId | null): { products: Place[]; platform: Place[] } {
  const { t } = useTranslation();
  const { can, me } = useSession();
  return {
    products: productList.map((p) => ({
      to: p.home,
      label: p.name,
      description: t(`products.${p.id}.tagline`),
      icon: <p.Logo size={32} />,
      small: <p.Logo size={16} />,
      current: p.id === current,
    })),
    platform: [
      {
        to: '/',
        label: t('switcher.overview', { name: me.instanceName }),
        icon: <Logo size={32} />,
        small: <Logo size={16} />,
        current: false,
      },
      ...(can('admin')
        ? [
            {
              to: '/admin',
              label: t('nav.administration'),
              icon: (
                <ThemeIcon size={32} radius={7} variant="light" color="gray" aria-hidden>
                  <IconShieldLock size={18} />
                </ThemeIcon>
              ),
              small: <IconShieldLock size={16} />,
              current: false,
            },
          ]
        : []),
    ],
  };
}

function PlaceItem({ place }: { place: Place }) {
  return (
    <Menu.Item
      component={Link}
      to={place.to}
      leftSection={place.icon}
      bg={place.current ? 'var(--mantine-color-default-hover)' : undefined}
      aria-current={place.current ? 'page' : undefined}
    >
      <Text fw={place.description ? 650 : 550} size="sm">
        {place.label}
      </Text>
      {place.description && (
        <Text size="xs" c="dimmed">
          {place.description}
        </Text>
      )}
    </Menu.Item>
  );
}

/** The switcher between the products and the platform, in the corner of every header. */
function AppSwitcher({ current }: { current: ProductId | null }) {
  const { t } = useTranslation();
  const places = usePlaces(current);
  const [opened, setOpened] = useState(false);
  return (
    <Menu position="bottom-start" width={320} shadow="lg" offset={10} opened={opened} onChange={setOpened}>
      {/* The tooltip names the button; it gives way to the open menu. */}
      <Tooltip label={t('switcher.label')} disabled={opened}>
        <Menu.Target>
          <ActionIcon
            variant="subtle"
            color="gray"
            className="kogge-header-icon"
            aria-label={t('switcher.label')}
          >
            <IconGridDots size={20} />
          </ActionIcon>
        </Menu.Target>
      </Tooltip>
      <Menu.Dropdown p="xs" className="kogge-switcher">
        <Text size="xs" fw={700} c="dimmed" tt="uppercase" px="xs" pb={6}>
          {t('switcher.products')}
        </Text>
        {places.products.map((p) => (
          <PlaceItem key={p.to} place={p} />
        ))}
        <Menu.Divider />
        {places.platform.map((p) => (
          <PlaceItem key={p.to} place={p} />
        ))}
      </Menu.Dropdown>
    </Menu>
  );
}

type Notification = Schemas['Notification'];

/** One notification: who did it, what, where and when; unread ones are bold with a dot. */
function NotificationRow({
  n,
  showProduct,
  onOpen,
}: {
  n: Notification;
  showProduct: boolean;
  onOpen: () => void;
}) {
  const { t } = useTranslation();
  const product = productOf(n.url);
  const P = product ? products[product] : null;
  return (
    <UnstyledButton className="kogge-notification" data-unread={!n.readAt || undefined} onClick={onOpen}>
      <Group gap="sm" wrap="nowrap" align="flex-start">
        {n.actorName ? (
          <PersonAvatar person={n.actorName} size={32} />
        ) : P ? (
          <P.Logo size={32} />
        ) : (
          <Logo size={32} />
        )}
        <Box miw={0} style={{ flex: 1 }}>
          <Text size="sm" fw={n.readAt ? 400 : 650} c={n.hidden ? 'dimmed' : undefined} lineClamp={2}>
            {n.hidden
              ? t('notifications.hidden')
              : n.entityType === 'issue-bulk'
                ? t('notifications.bulk', { count: Number(n.data?.count ?? 0), title: n.title })
                : n.title}
          </Text>
          {n.summary && !n.hidden && (
            <Text size="xs" c="dimmed" lineClamp={2}>
              {n.summary}
            </Text>
          )}
          <Group gap={6} mt={2} wrap="nowrap">
            {showProduct && P && <P.Logo size={14} />}
            <Text size="xs" c="dimmed" truncate>
              {n.actorName ? `${n.actorName} · ` : ''}
              {formatRelative(n.createdAt)}
            </Text>
          </Group>
        </Box>
        {!n.readAt && <span className="kogge-unread-dot" role="img" aria-label={t('notifications.unread')} />}
      </Group>
    </UnstyledButton>
  );
}

/**
 * The notifications of the product on screen, or of all products (a switch), with the way to the notification
 * settings; on the platform's pages all of them.
 */
function NotificationsMenu({ product }: { product: ProductId | null }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const [opened, setOpened] = useState(false);
  const [all, setAll] = useState(false);
  const list = useQuery({
    queryKey: ['notifications', 'list'],
    queryFn: () => unwrap(api.GET('/api/v1/notifications', { params: { query: { limit: 50 } } })),
    refetchInterval: 120_000,
  });
  const markRead = useMutation({
    mutationFn: (ids: number[]) => unwrap(api.POST('/api/v1/notifications/read', { body: { ids } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['notifications'] }),
    onError: notifyError,
  });
  const everything = list.data ?? [];
  const own = everything.filter((n) => product === null || productOf(n.url) === product);
  const scoped = product !== null && !all;
  const items = scoped ? own : everything;
  const unreadIDs = items.filter((n) => !n.readAt).map((n) => n.id);
  // The bell counts what the product's list shows.
  const unread = own.filter((n) => !n.readAt).length;
  const elsewhere = everything.length - own.length;
  const name = product ? products[product].name : '';
  return (
    <Popover width={400} position="bottom-end" shadow="md" opened={opened} onChange={setOpened}>
      <Popover.Target>
        <ActionIcon
          variant="subtle"
          color="gray"
          className="kogge-header-icon"
          onClick={() => setOpened((o) => !o)}
          aria-label={unread > 0 ? t('nav.notificationsUnread', { count: unread }) : t('nav.notifications')}
        >
          <Indicator
            label={unread > 49 ? '50+' : unread}
            size={16}
            disabled={unread === 0}
            offset={-2}
            color="amber.6"
          >
            <IconBell size={20} />
          </Indicator>
        </ActionIcon>
      </Popover.Target>
      <Popover.Dropdown p={0} className="kogge-notifications">
        <Group justify="space-between" px="md" pt="sm" pb={product ? 6 : 'xs'} wrap="nowrap">
          <Text fw={600}>{t('notifications.title')}</Text>
          <Group gap={4} wrap="nowrap">
            {unreadIDs.length > 0 && (
              <Anchor component="button" size="xs" onClick={() => markRead.mutate(unreadIDs)}>
                {t('notifications.markAllRead')}
              </Anchor>
            )}
            <Tooltip label={t('notifications.settings')}>
              <ActionIcon
                component={Link}
                to="/profile?tab=notifications"
                variant="subtle"
                color="gray"
                aria-label={t('notifications.settings')}
                onClick={() => setOpened(false)}
              >
                <IconSettings size={16} />
              </ActionIcon>
            </Tooltip>
          </Group>
        </Group>
        {product && (
          <Box px="md" pb="xs">
            <SegmentedControl
              size="xs"
              fullWidth
              value={all ? 'all' : 'product'}
              onChange={(v) => setAll(v === 'all')}
              aria-label={t('notifications.which')}
              data={[
                { value: 'product', label: name },
                { value: 'all', label: t('notifications.all') },
              ]}
            />
          </Box>
        )}
        <ScrollArea.Autosize mah={440}>
          {items.length === 0 ? (
            <EmptyState
              size="sm"
              icon={IconBellCheck}
              title={scoped ? t('notifications.emptyIn', { name }) : t('notifications.emptyTitle')}
              description={t('notifications.emptyText')}
              action={
                scoped && elsewhere > 0 ? (
                  <Anchor component="button" size="sm" onClick={() => setAll(true)}>
                    {t('notifications.elsewhere', { count: elsewhere })}
                  </Anchor>
                ) : undefined
              }
              secondary={
                <Anchor
                  component={Link}
                  to="/profile?tab=notifications"
                  size="sm"
                  onClick={() => setOpened(false)}
                >
                  {t('notifications.settings')}
                </Anchor>
              }
            />
          ) : (
            <Stack gap={0}>
              {items.map((n) => (
                <NotificationRow
                  key={n.id}
                  n={n}
                  showProduct={!scoped}
                  onOpen={() => {
                    if (!n.readAt) markRead.mutate([n.id]);
                    if (!n.hidden) {
                      setOpened(false);
                      navigate(n.url);
                    }
                  }}
                />
              ))}
            </Stack>
          )}
        </ScrollArea.Autosize>
      </Popover.Dropdown>
    </Popover>
  );
}

function UserMenu() {
  const { t, i18n } = useTranslation();
  const { me } = useSession();
  const client = useQueryClient();
  const navigate = useNavigate();
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const update = useMutation({
    mutationFn: (body: { locale?: 'de' | 'en'; theme?: 'light' | 'dark' | 'auto' }) =>
      unwrap(api.PATCH('/api/v1/me', { body })),
    onSuccess: (data) => client.setQueryData(meQueryKey, data),
    onError: notifyError,
  });
  const logout = async () => {
    try {
      const res = await unwrap(api.POST('/api/v1/auth/logout'));
      client.setQueryData(meQueryKey, null);
      if (res?.redirect) window.location.assign(res.redirect);
      else navigate('/');
    } catch (e) {
      notifyError(e);
    }
  };
  return (
    <Menu position="bottom-end" width={260} shadow="md">
      <Menu.Target>
        <UnstyledButton aria-label={t('nav.profile')} className="kogge-header-avatar">
          <PersonAvatar person={me.user} size={32} />
        </UnstyledButton>
      </Menu.Target>
      <Menu.Dropdown>
        {/* Who is signed in, leading to their page as others see it. */}
        <Menu.Item
          component={Link}
          to={personPath(me.user.username)}
          leftSection={<PersonAvatar person={me.user} size={32} />}
        >
          <Text size="sm" fw={600} truncate>
            {me.user.displayName}
          </Text>
          <Text size="xs" c="dimmed" truncate>
            @{me.user.username} · {t('people.yourPage')}
          </Text>
        </Menu.Item>
        <Menu.Divider />
        <Menu.Item component={Link} to="/profile" leftSection={<IconUserCircle size={16} />}>
          {t('nav.profile')}
        </Menu.Item>
        <Menu.Item component={Link} to="/api/docs" leftSection={<IconApi size={16} />}>
          {t('nav.apiDocs')}
        </Menu.Item>
        <Menu.Divider />
        <Menu.Label>{t('nav.language')}</Menu.Label>
        <Group px="xs" pb="xs">
          <SegmentedControl
            size="xs"
            fullWidth
            style={{ flex: 1 }}
            value={i18n.language}
            data={[
              { value: 'de', label: 'Deutsch' },
              { value: 'en', label: 'English' },
            ]}
            onChange={(locale) => update.mutate({ locale: locale as 'de' | 'en' })}
          />
        </Group>
        <Menu.Label>{t('nav.theme')}</Menu.Label>
        <Group px="xs" pb="xs">
          <SegmentedControl
            size="xs"
            fullWidth
            style={{ flex: 1 }}
            value={colorScheme}
            data={[
              { value: 'light', label: t('nav.themeLight') },
              { value: 'dark', label: t('nav.themeDark') },
              { value: 'auto', label: t('nav.themeAuto') },
            ]}
            onChange={(theme) => {
              const scheme = theme as 'light' | 'dark' | 'auto';
              setColorScheme(scheme);
              update.mutate({ theme: scheme });
            }}
          />
        </Group>
        <Menu.Divider />
        <Menu.Item leftSection={<IconLogout size={16} />} onClick={logout}>
          {t('nav.signOut')}
        </Menu.Item>
      </Menu.Dropdown>
    </Menu>
  );
}

export interface NavItem {
  to: string;
  label: string;
  end?: boolean;
  hidden?: boolean;
  /** Further pages that belong to the item (a repository's pages to "Repositories"): it is current there too. */
  also?: (path: string) => boolean;
}

/** Whether a navigation item is the current one. */
function isCurrent(item: NavItem, path: string): boolean {
  return !!matchPath({ path: item.to, end: item.end ?? false }, path) || (item.also?.(path) ?? false);
}

export interface FrameProps {
  /** The product, or null for the platform. */
  product: Product | null;
  nav: NavItem[];
  /** The product's search: the quick search's text within it and the address of its own search page. */
  search: { placeholder: string; go: (q: string) => string };
  /** The product's "Create" (compact: the "+" of phones). */
  create?: (compact: boolean) => ReactNode;
}

/**
 * The frame of a product's pages: a header in its colours (navigation, "Create", the search field, notifications
 * and the person's menu), then the page. On narrow screens the navigation, the page's own navigation (a project's,
 * a space's: SidebarLayout) and the products are in a drawer that the burger opens.
 */
export function Frame({ product, nav, search, create }: FrameProps) {
  const { t } = useTranslation();
  const { me } = useSession();
  const location = useLocation();
  const phone = useMediaQuery(phoneQuery, false, { getInitialValueInEffect: false });
  // The drawer belongs to the address it was opened on: going anywhere closes it.
  const [drawerAt, setDrawerAt] = useState<string | null>(null);
  const opened = drawerAt === location.key;
  const close = useCallback(() => setDrawerAt(null), []);
  const [slot, setSlot] = useState<HTMLElement | null>(null);
  const drawer = useMemo(() => ({ slot, open: () => setDrawerAt(location.key) }), [slot, location.key]);
  const items = nav.filter((n) => !n.hidden);
  const ProductLogo = product?.Logo ?? Logo;
  const name = product?.name ?? me.instanceName;
  const scope = useMemo<QuickSearchScope | null>(
    () => (product ? { product: product.id, placeholder: search.placeholder, go: search.go } : null),
    [product, search.placeholder, search.go],
  );
  useQuickSearchScope(scope);
  const searchLabel = product ? t('nav.searchIn', { name: product.name }) : t('nav.search');
  const places = usePlaces(product?.id ?? null);
  return (
    <ShellDrawerContext value={drawer}>
      <AppShell header={{ height: 52 }} padding="lg" data-product={product?.id ?? 'kogge'}>
        <AppShell.Header className="kogge-header">
          <Group h="100%" px="md" justify="space-between" wrap="nowrap" gap="md">
            <Group gap="xs" wrap="nowrap" miw={0} h="100%">
              <Burger
                opened={opened}
                onClick={() => (opened ? close() : drawer.open())}
                hiddenFrom="md"
                size="sm"
                aria-label={t('shell.menu')}
              />
              <Box visibleFrom="sm">
                <AppSwitcher current={product?.id ?? null} />
              </Box>
              <Link to={product?.home ?? '/'} className="kogge-brand" data-product={product?.id ?? 'kogge'}>
                <ProductLogo size={28} />
                <span>{name}</span>
              </Link>
              <Group
                gap={4}
                visibleFrom="md"
                component="nav"
                ml="md"
                wrap="nowrap"
                h="100%"
                aria-label={name}
              >
                {items.map((m) => (
                  <Link
                    key={m.to}
                    to={m.to}
                    className="kogge-nav-link"
                    aria-current={isCurrent(m, location.pathname) ? 'page' : undefined}
                  >
                    {m.label}
                  </Link>
                ))}
              </Group>
            </Group>
            <Group gap="xs" wrap="nowrap">
              <Box visibleFrom="sm">
                <HeaderSearchField label={searchLabel} />
              </Box>
              {create && create(phone)}
              <Box hiddenFrom="sm">
                <QuickSearchButton label={searchLabel} />
              </Box>
              <NotificationsMenu product={product?.id ?? null} />
              <UserMenu />
            </Group>
          </Group>
        </AppShell.Header>
        <Drawer
          opened={opened}
          onClose={close}
          size={320}
          padding="md"
          className="kogge-drawer"
          title={
            <Group gap="xs" wrap="nowrap">
              <ProductLogo size={26} />
              <Text fw={650}>{name}</Text>
            </Group>
          }
        >
          <Stack
            gap="md"
            onClick={(e) => {
              // Choosing a place closes the drawer, also when it is the page on screen.
              if ((e.target as HTMLElement).closest('a[href]')) close();
            }}
          >
            <Stack gap={2} component="nav" aria-label={t('shell.navigation', { name })}>
              {items.map((m) => (
                <MantineNavLink
                  key={m.to}
                  component={Link}
                  to={m.to}
                  label={m.label}
                  active={isCurrent(m, location.pathname)}
                  aria-current={isCurrent(m, location.pathname) ? 'page' : undefined}
                />
              ))}
            </Stack>
            {/* The page's own navigation (SidebarLayout) comes here. */}
            <div ref={setSlot} className="kogge-drawer-slot" />
            <Stack gap={2} component="nav" aria-label={t('switcher.label')}>
              <Divider label={t('switcher.label')} labelPosition="left" mb={4} />
              {/* The platform's own pages list the overview and the administration in their navigation already. */}
              {[...places.products, ...(product ? places.platform : [])].map((p) => (
                <MantineNavLink
                  key={p.to}
                  component={Link}
                  to={p.to}
                  label={p.label}
                  leftSection={p.small}
                  active={p.current}
                  aria-current={p.current ? 'page' : undefined}
                />
              ))}
            </Stack>
          </Stack>
        </Drawer>
        <AppShell.Main>
          {me.announcement && (
            <Alert color="amber" icon={<IconSpeakerphone size={16} />} mb="md">
              {me.announcement}
            </Alert>
          )}
          <Outlet />
        </AppShell.Main>
      </AppShell>
    </ShellDrawerContext>
  );
}
