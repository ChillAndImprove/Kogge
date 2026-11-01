// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Anchor,
  Badge,
  Button,
  Group,
  Menu,
  SegmentedControl,
  Stack,
  Switch,
  Table,
  Tabs,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { modals } from '@mantine/modals';
import {
  IconAlertTriangle,
  IconBell,
  IconDots,
  IconPencil,
  IconPlus,
  IconSearch,
  IconSend,
  IconStar,
  IconStarFilled,
  IconTrash,
  IconUsersGroup,
} from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import type { paths } from '../../api/schema';
import { formatDateTime, formatRelative } from '../../app/format';
import { useSession } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { Pagination } from '../../components/Pagination';
import { useShareLabel } from '../../components/ShareEditor';
import { notifyError, notifySuccess } from '../../components/notify';
import { FilterForm, type Filter } from './FilterForm';
import { describeSchedule, type Subscription } from './schedule';
import { SubscriptionForm } from './SubscriptionForm';
import { UserBadge } from './shared';

/** The address of a filter in the issue navigator. */
export function filterLink(f: { id: number; query: string }): string {
  return `/issues/search?filter=${f.id}&q=${encodeURIComponent(f.query)}`;
}

export type FilterSelection = NonNullable<paths['/api/v1/filters']['get']['parameters']['query']>;

/** A page of the saved filters the user sees, chosen, counted and paged by the server. */
export function useFilters(selection: FilterSelection) {
  return useQuery({
    queryKey: ['filters', 'list', selection],
    queryFn: () => unwrap(api.GET('/api/v1/filters', { params: { query: selection } })),
    placeholderData: (prev) => prev,
  });
}

/** The caller's subscriptions, and whether this installation sends e-mail. */
export function useMySubscriptions() {
  return useQuery({
    queryKey: ['filter-subscriptions', 'mine'],
    queryFn: () => unwrap(api.GET('/api/v1/filter-subscriptions')),
  });
}

export function useFilterFavourite() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (f: { id: number; favourite: boolean }) =>
      f.favourite
        ? unwrap(api.DELETE('/api/v1/filters/{id}/favourite', { params: { path: { id: f.id } } }))
        : unwrap(api.PUT('/api/v1/filters/{id}/favourite', { params: { path: { id: f.id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['filters'] }),
    onError: notifyError,
  });
}

/** Asks before deleting a filter (and its subscriptions). */
export function useDeleteFilter(onDeleted?: () => void) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/api/v1/filters/{id}', { params: { path: { id } } })),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['filters'] });
      void client.invalidateQueries({ queryKey: ['filter-subscriptions'] });
      notifySuccess(t('filters.deleted'));
      onDeleted?.();
    },
    onError: notifyError,
  });
  return (f: Filter) =>
    modals.openConfirmModal({
      title: t('filters.delete'),
      children: <Text size="sm">{t('filters.deleteConfirm', { name: f.name })}</Text>,
      labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
      confirmProps: { color: 'red' },
      onConfirm: () => del.mutate(f.id),
    });
}

/** Filters: those of the caller and those shared with them, and their subscriptions by e-mail. */
export function FiltersPage() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') === 'subscriptions' ? 'subscriptions' : 'filters';
  const [form, setForm] = useState<Filter | 'new' | null>(null);
  return (
    <>
      <PageHeader
        title={t('filters.title')}
        subtitle={t('filters.subtitle')}
        actions={
          <Button leftSection={<IconPlus size={16} />} onClick={() => setForm('new')}>
            {t('filters.create')}
          </Button>
        }
      />
      <Tabs
        value={tab}
        onChange={(v) => setParams(v === 'subscriptions' ? { tab: v } : {}, { replace: true })}
        keepMounted={false}
      >
        <Tabs.List mb="md">
          <Tabs.Tab value="filters">{t('filters.tabFilters')}</Tabs.Tab>
          <Tabs.Tab value="subscriptions">{t('filters.tabSubscriptions')}</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="filters">
          <FilterList onEdit={setForm} />
        </Tabs.Panel>
        <Tabs.Panel value="subscriptions">
          <SubscriptionList />
        </Tabs.Panel>
      </Tabs>
      {form && <FilterForm filter={form === 'new' ? null : form} onClose={() => setForm(null)} />}
    </>
  );
}

type Show = 'all' | 'mine' | 'favourites' | 'shared';

const pageSize = 50;

function FilterList({ onEdit }: { onEdit: (f: Filter) => void }) {
  const { t } = useTranslation();
  const subscriptions = useMySubscriptions();
  const favourite = useFilterFavourite();
  const remove = useDeleteFilter();
  const shareLabel = useShareLabel();
  const [show, setShow] = useState<Show>('all');
  const [find, setFind] = useState('');
  const [debounced] = useDebouncedValue(find.trim(), 250);
  const [page, setPage] = useState(1);
  // The server chooses, counts and pages the filters, so that the list stays complete with thousands of them.
  const filters = useFilters({
    query: debounced || undefined,
    owner: show === 'mine' ? 'me' : show === 'shared' ? 'others' : undefined,
    favourite: show === 'favourites' || undefined,
    limit: pageSize,
    offset: (page - 1) * pageSize,
  });
  const [subscribe, setSubscribe] = useState<Filter | null>(null);
  const mail = subscriptions.data?.mail ?? false;
  const list = filters.data?.items ?? [];
  const total = filters.data?.total ?? 0;
  const pages = Math.ceil(total / pageSize);
  // When the last filter of the last page goes (deleted, or no longer a favourite), the page before it is shown.
  if (pages > 0 && page > pages) setPage(pages);
  return (
    <Stack gap="md">
      <Group justify="space-between" wrap="wrap">
        <SegmentedControl
          value={show}
          onChange={(v) => {
            setShow(v as Show);
            setPage(1);
          }}
          aria-label={t('filters.show')}
          data={(['all', 'mine', 'favourites', 'shared'] as const).map((v) => ({
            value: v,
            label: t(`filters.${v}`),
          }))}
        />
        <TextInput
          value={find}
          onChange={(e) => {
            setFind(e.currentTarget.value);
            setPage(1);
          }}
          placeholder={t('filters.find')}
          aria-label={t('filters.find')}
          leftSection={<IconSearch size={16} />}
          w={260}
        />
      </Group>
      <ErrorAlert error={filters.error} />
      {filters.isSuccess && total === 0 && (
        <Text c="dimmed">{show === 'all' && !debounced ? t('filters.none') : t('filters.noneMatch')}</Text>
      )}
      {total > 0 && (
        <Text size="sm" c="dimmed" aria-live="polite">
          {t('filters.count', { count: total })}
        </Text>
      )}
      {list.length > 0 && (
        <Table data-testid="filter-list">
          <Table.Thead>
            <Table.Tr>
              <Table.Th w={40} />
              <Table.Th>{t('filters.name')}</Table.Th>
              <Table.Th w={200}>{t('filters.owner')}</Table.Th>
              <Table.Th>{t('sharing.title')}</Table.Th>
              <Table.Th w={50} />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {list.map((f) => (
              <Table.Tr key={f.id}>
                <Table.Td>
                  <Tooltip label={f.favourite ? t('filters.unfavourite') : t('filters.favourite')}>
                    <ActionIcon
                      variant="subtle"
                      color="yellow"
                      onClick={() => favourite.mutate(f)}
                      aria-label={f.favourite ? t('filters.unfavourite') : t('filters.favourite')}
                      aria-pressed={f.favourite}
                    >
                      {f.favourite ? <IconStarFilled size={16} /> : <IconStar size={16} />}
                    </ActionIcon>
                  </Tooltip>
                </Table.Td>
                <Table.Td>
                  <Anchor component={Link} to={filterLink(f)} fw={600}>
                    {f.name}
                  </Anchor>
                  {f.description && (
                    <Text size="xs" c="dimmed" lineClamp={1}>
                      {f.description}
                    </Text>
                  )}
                </Table.Td>
                <Table.Td>
                  <UserBadge user={f.owner} size="xs" />
                </Table.Td>
                <Table.Td>
                  {!f.canEdit ? (
                    <Text size="sm" c="dimmed">
                      {t('filters.sharedWithYou')}
                    </Text>
                  ) : f.shares.length === 0 ? (
                    <Text size="sm" c="dimmed">
                      {t('sharing.private')}
                    </Text>
                  ) : (
                    <Group gap={4}>
                      {f.shares.map((s, i) => (
                        <Badge
                          key={i}
                          variant="light"
                          tt="none"
                          color={s.type === 'anyone' ? 'red' : undefined}
                        >
                          {shareLabel(s)}
                        </Badge>
                      ))}
                    </Group>
                  )}
                </Table.Td>
                <Table.Td>
                  <Menu position="bottom-end" withinPortal>
                    <Menu.Target>
                      <ActionIcon
                        variant="subtle"
                        color="gray"
                        aria-label={t('filters.actions', { name: f.name })}
                      >
                        <IconDots size={16} />
                      </ActionIcon>
                    </Menu.Target>
                    <Menu.Dropdown>
                      <Menu.Item
                        leftSection={<IconBell size={14} />}
                        disabled={!mail}
                        onClick={() => setSubscribe(f)}
                      >
                        {t('filters.subscribe')}
                      </Menu.Item>
                      {f.canEdit && (
                        <>
                          <Menu.Item leftSection={<IconPencil size={14} />} onClick={() => onEdit(f)}>
                            {t('filters.edit')}
                          </Menu.Item>
                          <Menu.Divider />
                          <Menu.Item
                            color="red"
                            leftSection={<IconTrash size={14} />}
                            onClick={() => remove(f)}
                          >
                            {t('filters.delete')}
                          </Menu.Item>
                        </>
                      )}
                    </Menu.Dropdown>
                  </Menu>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      {pages > 1 && (
        <Group justify="center">
          <Pagination total={pages} value={Math.min(page, pages)} onChange={setPage} />
        </Group>
      )}
      {subscriptions.data && !mail && (
        <Alert color="gray" icon={<IconBell size={16} />}>
          {t('filters.subscription.noMail')}
        </Alert>
      )}
      {subscribe && <SubscriptionForm filter={subscribe} onClose={() => setSubscribe(null)} />}
    </Stack>
  );
}

/** Actions on subscriptions: send now, delete (asking first) and stop or resume the e-mails of a group. */
export function useSubscriptionActions() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const refresh = () => client.invalidateQueries({ queryKey: ['filter-subscriptions'] });
  const send = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.POST('/api/v1/filter-subscriptions/{id}/send', { params: { path: { id } } })),
    onSuccess: () => {
      notifySuccess(t('filters.subs.sent'));
      void refresh();
    },
    onError: notifyError,
  });
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/filter-subscriptions/{id}', { params: { path: { id } } })),
    onSuccess: refresh,
    onError: notifyError,
  });
  const mute = useMutation({
    mutationFn: ({ id, muted }: { id: number; muted: boolean }) =>
      muted
        ? unwrap(api.PUT('/api/v1/filter-subscriptions/{id}/mute', { params: { path: { id } } }))
        : unwrap(api.DELETE('/api/v1/filter-subscriptions/{id}/mute', { params: { path: { id } } })),
    onSuccess: refresh,
    onError: notifyError,
  });
  const remove = (s: Subscription) =>
    modals.openConfirmModal({
      title: t('filters.subs.deleteTitle'),
      children: <Text size="sm">{t('filters.subs.deleteConfirm', { name: s.filter.name })}</Text>,
      labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
      confirmProps: { color: 'red' },
      onConfirm: () => del.mutate(s.id),
    });
  return { send, remove, mute };
}

/** A table of subscriptions; withFilter adds the column of their filters. */
export function SubscriptionTable({
  items,
  withFilter = true,
}: {
  items: Subscription[];
  withFilter?: boolean;
}) {
  const { t } = useTranslation();
  const { me } = useSession();
  const { send, remove, mute } = useSubscriptionActions();
  const [edit, setEdit] = useState<Subscription | null>(null);
  return (
    <>
      <Table data-testid="subscription-list">
        <Table.Thead>
          <Table.Tr>
            {withFilter && <Table.Th>{t('filters.subs.filter')}</Table.Th>}
            <Table.Th>{t('filters.subs.recipient')}</Table.Th>
            <Table.Th>{t('filters.subs.schedule')}</Table.Th>
            <Table.Th w={170}>{t('filters.subs.next')}</Table.Th>
            <Table.Th w={150}>{t('filters.subs.last')}</Table.Th>
            <Table.Th w={110} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {items.map((s) => (
            <Table.Tr key={s.id}>
              {withFilter && (
                <Table.Td>
                  <Anchor component={Link} to={`/issues/search?filter=${s.filter.id}`} fw={600}>
                    {s.filter.name}
                  </Anchor>
                </Table.Td>
              )}
              <Table.Td>
                {s.group ? (
                  <Stack gap={4}>
                    <Group gap={6} wrap="nowrap">
                      <IconUsersGroup size={16} aria-hidden />
                      <Text size="sm">{s.group.name}</Text>
                    </Group>
                    {s.canMute && (
                      <Switch
                        size="xs"
                        label={t('filters.subs.receive')}
                        checked={!s.muted}
                        onChange={(e) => mute.mutate({ id: s.id, muted: !e.currentTarget.checked })}
                      />
                    )}
                  </Stack>
                ) : s.user && s.user.id === me.user.id ? (
                  <Text size="sm">{t('filters.subs.you')}</Text>
                ) : (
                  <UserBadge user={s.user} size="xs" />
                )}
                {s.createdBy.id !== me.user.id && (
                  <Text size="xs" c="dimmed">
                    {t('filters.subs.setUpBy', { name: s.createdBy.displayName })}
                  </Text>
                )}
              </Table.Td>
              <Table.Td>
                <Text size="sm">{describeSchedule(t, s)}</Text>
                <Text size="xs" c="dimmed">
                  {s.timeZone}
                  {s.sendEmpty ? ` · ${t('filters.subscription.sendEmpty')}` : ''}
                </Text>
              </Table.Td>
              <Table.Td>
                <Text size="sm">{formatDateTime(s.next)}</Text>
              </Table.Td>
              <Table.Td>
                <Group gap={6} wrap="nowrap">
                  <Text size="sm">{s.last ? formatRelative(s.last) : t('filters.subs.never')}</Text>
                  {s.lastError && (
                    <Tooltip label={t('filters.subs.failed', { error: s.lastError })} multiline w={320}>
                      <IconAlertTriangle
                        size={16}
                        color="var(--mantine-color-red-7)"
                        role="img"
                        aria-label={t('filters.subs.failed', { error: s.lastError })}
                      />
                    </Tooltip>
                  )}
                </Group>
              </Table.Td>
              <Table.Td>
                {s.canManage && (
                  <Group gap={2} wrap="nowrap" justify="flex-end">
                    <Tooltip label={t('filters.subs.sendNow')}>
                      <ActionIcon
                        variant="subtle"
                        onClick={() => send.mutate(s.id)}
                        loading={send.isPending && send.variables === s.id}
                        aria-label={t('filters.subs.sendNow')}
                      >
                        <IconSend size={16} />
                      </ActionIcon>
                    </Tooltip>
                    <ActionIcon variant="subtle" onClick={() => setEdit(s)} aria-label={t('common.edit')}>
                      <IconPencil size={16} />
                    </ActionIcon>
                    <ActionIcon
                      variant="subtle"
                      color="red"
                      onClick={() => remove(s)}
                      aria-label={t('common.delete')}
                    >
                      <IconTrash size={16} />
                    </ActionIcon>
                  </Group>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      {edit && <SubscriptionForm filter={edit.filter} subscription={edit} onClose={() => setEdit(null)} />}
    </>
  );
}

function SubscriptionList() {
  const { t } = useTranslation();
  const subscriptions = useMySubscriptions();
  const data = subscriptions.data;
  return (
    <Stack gap="md">
      <ErrorAlert error={subscriptions.error} />
      {data && !data.mail && (
        <Alert color="gray" icon={<IconBell size={16} />}>
          {t('filters.subscription.noMail')}
        </Alert>
      )}
      {data && data.subscriptions.length === 0 && <Text c="dimmed">{t('filters.subs.none')}</Text>}
      {data && data.subscriptions.length > 0 && <SubscriptionTable items={data.subscriptions} />}
    </Stack>
  );
}
