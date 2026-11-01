// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Button,
  Group,
  Modal,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
  Tooltip,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { modals } from '@mantine/modals';
import { IconPencil, IconPlus, IconSearch, IconStar, IconStarFilled, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import type { paths } from '../../api/schema';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { Pagination } from '../../components/Pagination';
import { ShareEditor, type Share } from '../../components/ShareEditor';
import { notifyError } from '../../components/notify';
import { UserBadge } from '../issues/shared';
import { DashboardView, type Dashboard } from './DashboardView';

type DashboardSelection = NonNullable<paths['/api/v1/dashboards']['get']['parameters']['query']>;

/** A page of the dashboards the user sees, chosen, counted and paged by the server. */
export function useDashboards(selection: DashboardSelection) {
  return useQuery({
    queryKey: ['dashboard', 'list', selection],
    queryFn: () => unwrap(api.GET('/api/v1/dashboards', { params: { query: selection } })),
    placeholderData: (prev) => prev,
  });
}

/**
 * The user's favourite dashboards, which the start page shows as tabs: all of them, as a page holds up to 500, far
 * more than tabs anyone can use.
 */
export function useFavouriteDashboards() {
  return useDashboards({ favourite: true, limit: 500 });
}

const pageSize = 50;

/** Creates a dashboard or changes its name, description and sharing. */
export function DashboardForm({
  dashboard,
  onClose,
}: {
  dashboard: Dashboard | null;
  onClose: (id?: number) => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [name, setName] = useState(dashboard?.name ?? '');
  const [description, setDescription] = useState(dashboard?.description ?? '');
  const [shares, setShares] = useState<Share[]>(dashboard?.shares ?? []);
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        description,
        shares,
        columns: dashboard?.columns ?? 2,
        widgets: dashboard?.widgets ?? [
          { id: 'w1', type: 'assigned-to-me' as const, column: 0, position: 0, config: { limit: 10 } },
          { id: 'w2', type: 'activity' as const, column: 1, position: 0, config: { limit: 15 } },
        ],
      };
      return dashboard
        ? unwrap(
            api.PUT('/api/v1/dashboards/{id}', {
              params: { path: { id: dashboard.id }, header: { 'If-Match': `"${dashboard.version}"` } },
              body,
            }),
          )
        : unwrap(api.POST('/api/v1/dashboards', { body }));
    },
    onSuccess: (d) => {
      void client.invalidateQueries({ queryKey: ['dashboard'] });
      onClose(d.id);
    },
  });
  return (
    <Modal
      opened
      onClose={() => onClose()}
      title={dashboard ? t('dashboards.edit') : t('dashboards.create')}
      size="lg"
    >
      <Stack>
        <TextInput
          label={t('common.name')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
          data-autofocus
        />
        <Textarea
          label={t('common.description')}
          value={description}
          onChange={(e) => setDescription(e.currentTarget.value)}
          autosize
        />
        <Stack gap={4}>
          <Text size="sm" fw={500}>
            {t('sharing.title')}
          </Text>
          <ShareEditor value={shares} onChange={setShares} />
        </Stack>
        <ErrorAlert error={save.error} />
        <Group justify="flex-end">
          <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name.trim()}>
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

export function useFavourite() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (d: { id: number; favourite: boolean }) =>
      d.favourite
        ? unwrap(api.DELETE('/api/v1/dashboards/{id}/favourite', { params: { path: { id: d.id } } }))
        : unwrap(api.PUT('/api/v1/dashboards/{id}/favourite', { params: { path: { id: d.id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['dashboard'] }),
    onError: notifyError,
  });
}

export function DashboardsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [find, setFind] = useState('');
  const [debounced] = useDebouncedValue(find.trim(), 250);
  const [page, setPage] = useState(1);
  // The server chooses, counts and pages the dashboards, so that the list stays complete with thousands of them.
  const list = useDashboards({
    query: debounced || undefined,
    limit: pageSize,
    offset: (page - 1) * pageSize,
  });
  const total = list.data?.total ?? 0;
  const pages = Math.ceil(total / pageSize);
  // When the last dashboard of the last page is deleted, the page before it is shown.
  if (pages > 0 && page > pages) setPage(pages);
  const fav = useFavourite();
  const [form, setForm] = useState<Dashboard | 'new' | null>(null);
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/api/v1/dashboards/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['dashboard'] }),
    onError: notifyError,
  });
  return (
    <>
      <PageHeader
        title={t('dashboards.heading')}
        subtitle={t('dashboards.subtitle')}
        actions={
          <Button leftSection={<IconPlus size={16} />} onClick={() => setForm('new')}>
            {t('dashboards.create')}
          </Button>
        }
      />
      <Group justify="space-between" mb="sm">
        <Text size="sm" c="dimmed" aria-live="polite">
          {list.isSuccess &&
            (total > 0
              ? t('dashboards.count', { count: total })
              : debounced
                ? t('dashboards.noMatch')
                : t('dashboards.noDashboards'))}
        </Text>
        <TextInput
          value={find}
          onChange={(e) => {
            setFind(e.currentTarget.value);
            setPage(1);
          }}
          placeholder={t('dashboards.find')}
          aria-label={t('dashboards.find')}
          leftSection={<IconSearch size={16} />}
          w={260}
        />
      </Group>
      <ErrorAlert error={list.error} />
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th w={40} />
            <Table.Th>{t('common.name')}</Table.Th>
            <Table.Th>{t('agile.owner')}</Table.Th>
            <Table.Th w={80} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(list.data?.items ?? []).map((d) => (
            <Table.Tr key={d.id}>
              <Table.Td>
                <Tooltip label={d.favourite ? t('dashboards.unfavourite') : t('dashboards.favourite')}>
                  <ActionIcon
                    variant="subtle"
                    color="yellow"
                    onClick={() => fav.mutate(d)}
                    aria-label={t('dashboards.favourite')}
                  >
                    {d.favourite ? <IconStarFilled size={16} /> : <IconStar size={16} />}
                  </ActionIcon>
                </Tooltip>
              </Table.Td>
              <Table.Td>
                <Anchor component={Link} to={`/dashboards/${d.id}`} fw={600}>
                  {d.name}
                </Anchor>
                {d.description && (
                  <Text size="xs" c="dimmed" lineClamp={1}>
                    {d.description}
                  </Text>
                )}
              </Table.Td>
              <Table.Td>
                <UserBadge user={d.owner} size="xs" />
              </Table.Td>
              <Table.Td>
                {d.canEdit && (
                  <Group gap={2} wrap="nowrap">
                    <ActionIcon variant="subtle" onClick={() => setForm(d)} aria-label={t('common.edit')}>
                      <IconPencil size={16} />
                    </ActionIcon>
                    <ActionIcon
                      variant="subtle"
                      color="red"
                      aria-label={t('common.delete')}
                      onClick={() =>
                        modals.openConfirmModal({
                          title: t('common.delete'),
                          children: <Text size="sm">{t('dashboards.deleteConfirm', { name: d.name })}</Text>,
                          labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                          confirmProps: { color: 'red' },
                          onConfirm: () => del.mutate(d.id),
                        })
                      }
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
      {pages > 1 && (
        <Group justify="center" mt="md">
          <Pagination total={pages} value={Math.min(page, pages)} onChange={setPage} />
        </Group>
      )}
      {form && (
        <DashboardForm
          dashboard={form === 'new' ? null : form}
          onClose={(id) => {
            setForm(null);
            if (id && form === 'new') navigate(`/dashboards/${id}?edit=1`);
          }}
        />
      )}
    </>
  );
}

export function DashboardPage() {
  const { t } = useTranslation();
  const { id = '' } = useParams();
  const [params, setParams] = useSearchParams();
  const dashboard = useQuery({
    queryKey: ['dashboard', Number(id)],
    queryFn: () => unwrap(api.GET('/api/v1/dashboards/{id}', { params: { path: { id: Number(id) } } })),
  });
  const [form, setForm] = useState(false);
  if (dashboard.error) return <ErrorAlert error={dashboard.error} />;
  const d = dashboard.data;
  if (!d) return null;
  const editing = params.get('edit') === '1' && d.canEdit;
  return (
    <>
      <PageHeader
        title={d.name}
        subtitle={d.description || undefined}
        actions={
          d.canEdit && (
            <>
              <Button variant="default" onClick={() => setForm(true)}>
                {t('dashboards.settings')}
              </Button>
              <Button
                variant={editing ? 'filled' : 'light'}
                onClick={() => setParams(editing ? {} : { edit: '1' }, { replace: true })}
              >
                {editing ? t('dashboards.done') : t('dashboards.arrange')}
              </Button>
            </>
          )
        }
      />
      <DashboardView key={d.version} dashboard={d} editing={editing} />
      {form && <DashboardForm dashboard={d} onClose={() => setForm(false)} />}
    </>
  );
}
