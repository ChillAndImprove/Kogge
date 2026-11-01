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
  Paper,
  Stack,
  Text,
  Tooltip,
} from '@mantine/core';
import {
  IconBell,
  IconDeviceFloppy,
  IconDots,
  IconDownload,
  IconFilter,
  IconListCheck,
  IconPencil,
  IconSettings,
  IconStar,
  IconStarFilled,
  IconTrash,
} from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { Pagination } from '../../components/Pagination';
import { ApiError, api, unwrap } from '../../api/client';
import { today } from '../../app/format';
import { meQueryKey, useSession, type Me } from '../../app/session';
import { ErrorAlert, errorMessage } from '../../components/ErrorAlert';
import { notifyError, notifySuccess } from '../../components/notify';
import { BulkChangeModal } from './BulkChange';
import type { BulkTarget } from './bulkInput';
import { ColumnChooser } from './ColumnChooser';
import { columnCatalog, defaultColumns, resolveColumns, type IssueRow } from './columns';
import { download, fileName, toCSV, type Separator } from './csv';
import { FilterForm, type Filter } from './FilterForm';
import { useDeleteFilter, useFilterFavourite, useFilters, useMySubscriptions } from './FiltersPage';
import { IssueTable } from './IssueTable';
import { KqlInput } from './KqlInput';
import { orderBy } from './kqlOrder';
import { UserBadge, useIssueConfig } from './shared';
import { SubscriptionForm } from './SubscriptionForm';

const pageSize = 50;

/** The most filters of each kind (favourites, own) the Filters menu shows. */
const menuSize = 20;

/** The most issues an export contains. */
const exportMax = 10_000;

/** The most issues a bulk change takes (the server's limit). */
const bulkMax = 1000;

/** The saved filter the navigator shows, with what its owner and viewers can do with it. */
function FilterBar({
  filter,
  query,
  columns,
  onSaved,
}: {
  filter: Filter;
  query: string;
  /** Columns chosen while the filter is open, or null when they are the filter's. */
  columns: string[] | null;
  onSaved: (f: Filter) => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const favourite = useFilterFavourite();
  const subscriptions = useMySubscriptions();
  const [form, setForm] = useState<'edit' | 'copy' | 'subscribe' | null>(null);
  const remove = useDeleteFilter(() => onSaved({ ...filter, id: 0 }));
  const changed = query.trim() !== filter.query.trim() || columns !== null;
  const update = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/filters/{id}', {
          params: { path: { id: filter.id } },
          body: {
            name: filter.name,
            description: filter.description,
            query,
            columns: columns ?? filter.columns,
            shares: filter.shares,
          },
        }),
      ),
    onSuccess: (f) => {
      void client.invalidateQueries({ queryKey: ['filters'] });
      notifySuccess(t('filters.saved'));
      onSaved(f);
    },
    onError: notifyError,
  });
  return (
    <Paper withBorder p="xs" px="sm" radius="md" data-testid="current-filter">
      <Group justify="space-between" wrap="nowrap" gap="sm">
        <Group gap="xs" wrap="nowrap" style={{ minWidth: 0 }}>
          <Tooltip label={filter.favourite ? t('filters.unfavourite') : t('filters.favourite')}>
            <ActionIcon
              variant="subtle"
              color="yellow"
              onClick={() => favourite.mutate(filter)}
              aria-label={filter.favourite ? t('filters.unfavourite') : t('filters.favourite')}
              aria-pressed={filter.favourite}
            >
              {filter.favourite ? <IconStarFilled size={16} /> : <IconStar size={16} />}
            </ActionIcon>
          </Tooltip>
          <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
            {t('filters.current')}
          </Text>
          <Text fw={600} truncate>
            {filter.name}
          </Text>
          {!filter.canEdit && <UserBadge user={filter.owner} size="xs" />}
          {changed && (
            <Badge variant="light" color="orange" tt="none" title={t('filters.changed')}>
              {t('filters.modified')}
            </Badge>
          )}
        </Group>
        <Group gap="xs" wrap="nowrap">
          {changed && filter.canEdit && (
            <Button size="xs" onClick={() => update.mutate()} loading={update.isPending}>
              {t('filters.saveChanges')}
            </Button>
          )}
          {changed && (
            <Button size="xs" variant="default" onClick={() => setForm('copy')}>
              {t('filters.saveAs')}
            </Button>
          )}
          <Tooltip
            label={
              subscriptions.data?.mail === false ? t('filters.subscription.noMail') : t('filters.subscribe')
            }
          >
            <Button
              size="xs"
              variant="default"
              leftSection={<IconBell size={14} />}
              onClick={() => setForm('subscribe')}
              disabled={subscriptions.data?.mail === false}
            >
              {t('filters.subscribe')}
            </Button>
          </Tooltip>
          <Menu position="bottom-end">
            <Menu.Target>
              <ActionIcon
                variant="default"
                size="md"
                aria-label={t('filters.actions', { name: filter.name })}
              >
                <IconDots size={16} />
              </ActionIcon>
            </Menu.Target>
            <Menu.Dropdown>
              {filter.canEdit && (
                <Menu.Item leftSection={<IconPencil size={14} />} onClick={() => setForm('edit')}>
                  {t('filters.details')}
                </Menu.Item>
              )}
              <Menu.Item leftSection={<IconSettings size={14} />} component={Link} to="/issues/filters">
                {t('filters.title')}
              </Menu.Item>
              {filter.canEdit && (
                <>
                  <Menu.Divider />
                  <Menu.Item color="red" leftSection={<IconTrash size={14} />} onClick={() => remove(filter)}>
                    {t('filters.delete')}
                  </Menu.Item>
                </>
              )}
            </Menu.Dropdown>
          </Menu>
        </Group>
      </Group>
      {changed && !filter.canEdit && (
        <Text size="xs" c="dimmed" mt={4}>
          {t('filters.notOwner')}
        </Text>
      )}
      {form === 'edit' && (
        <FilterForm
          filter={filter}
          onClose={(f) => {
            setForm(null);
            if (f) onSaved(f);
          }}
        />
      )}
      {form === 'copy' && (
        <FilterForm
          filter={null}
          query={query}
          columns={columns ?? filter.columns}
          onClose={(f) => {
            setForm(null);
            if (f) onSaved(f);
          }}
        />
      )}
      {form === 'subscribe' && <SubscriptionForm filter={filter} onClose={() => setForm(null)} />}
    </Paper>
  );
}

/**
 * The issue navigator: a KQL query with results. The query lives in the URL (?q=), and so does the saved filter it
 * came from (?filter=), which can be saved again, subscribed to and managed from here.
 */
export function SearchView({ defaultQuery = '' }: { defaultQuery?: string }) {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const filterId = Number(params.get('filter') ?? '') || 0;
  const current = useQuery({
    queryKey: ['filters', filterId],
    queryFn: () => unwrap(api.GET('/api/v1/filters/{id}', { params: { path: { id: filterId } } })),
    enabled: filterId > 0,
    retry: false,
  });
  const query = params.get('q') ?? (filterId ? current.data?.query : undefined) ?? defaultQuery;
  const page = Number(params.get('page') ?? '1');
  const [draft, setDraft] = useState(query);
  const [shown, setShown] = useState(query);
  // The issues chosen for a bulk change: by key, or all the query finds.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [allResults, setAllResults] = useState(false);
  const [bulk, setBulk] = useState<BulkTarget | null>(null);
  // The query box follows the URL (another filter, back and forward) unless the user is typing; another query
  // chooses other issues.
  if (shown !== query) {
    setShown(query);
    setDraft(query);
    setSelected(new Set());
    setAllResults(false);
  }
  const [saving, setSaving] = useState(false);
  const waiting = filterId > 0 && !params.has('q') && current.isPending;
  const results = useQuery({
    queryKey: ['issue', 'search', query, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/search', {
          params: { query: { q: query, limit: pageSize, offset: (page - 1) * pageSize } },
        }),
      ),
    placeholderData: (prev) => prev,
    retry: false,
    enabled: !waiting,
  });
  // The menu offers the user's favourite filters and their own; the Filters page lists and finds all the others.
  const favourites = useFilters({ favourite: true, limit: menuSize });
  const own = useFilters({ owner: 'me', limit: menuSize });
  const menuFavourites = favourites.data?.items ?? [];
  const menuOwn = (own.data?.items ?? []).filter((f) => !f.favourite);
  const favourite = useFilterFavourite();
  const { me } = useSession();
  const client = useQueryClient();
  const config = useIssueConfig();
  const units = config.data?.timeTracking ?? { hoursPerDay: 8, daysPerWeek: 5 };
  const catalog = useMemo(
    () =>
      columnCatalog(t, config.data, {
        ...(config.data?.timeTracking ?? { hoursPerDay: 8, daysPerWeek: 5 }),
        timeZone: me.timeZone,
      }),
    [t, config.data, me.timeZone],
  );
  // Columns: chosen while a filter is open (in the URL), else the filter's, else the user's, else the defaults.
  const chosen = params.get('columns')?.split(',').filter(Boolean) ?? null;
  const filterColumns = current.data?.columns.length ? current.data.columns : null;
  const mine = me.preferences.navigatorColumns?.length ? me.preferences.navigatorColumns : null;
  const columns = resolveColumns(catalog, chosen ?? filterColumns ?? mine);
  const saveColumns = useMutation({
    mutationFn: (keys: string[]) =>
      unwrap(api.PATCH('/api/v1/me', { body: { preferences: { navigatorColumns: keys } } })),
    onMutate: (keys) => {
      client.setQueryData<Me | null>(meQueryKey, (old) =>
        old ? { ...old, preferences: { ...old.preferences, navigatorColumns: keys } } : old,
      );
    },
    onSuccess: (data) => client.setQueryData(meQueryKey, data),
    onError: notifyError,
  });
  const setColumns = (keys: string[]) => {
    if (filterId > 0 && current.data) {
      const next = new URLSearchParams(params);
      next.set('columns', keys.join(','));
      setParams(next, { replace: true });
    } else {
      saveColumns.mutate(keys);
    }
  };
  const [exporting, setExporting] = useState(false);
  const exportCSV = async (sep: Separator) => {
    setExporting(true);
    try {
      const rows: IssueRow[] = [];
      let total = 0;
      while (rows.length < exportMax) {
        const page = await unwrap(
          api.GET('/api/v1/search', { params: { query: { q: query, limit: 1000, offset: rows.length } } }),
        );
        total = page.total;
        rows.push(...page.issues);
        if (page.issues.length < 1000 || rows.length >= total) break;
      }
      const cols = resolveColumns(
        columnCatalog(t, config.data, { ...units, timeZone: me.timeZone, decimalComma: sep === ';' }),
        columns.map((c) => c.key),
      );
      const name = fileName(`${current.data?.name ?? t('navigator.file')}-${today()}`, 'csv');
      download(
        name,
        toCSV(
          cols.map((c) => c.label),
          rows.map((r) => cols.map((c) => c.text(r))),
          sep,
        ),
      );
      notifySuccess(
        total > rows.length
          ? t('navigator.exportLimited', { count: rows.length })
          : t('navigator.exported', { count: rows.length }),
      );
    } catch (e) {
      notifyError(e);
    } finally {
      setExporting(false);
    }
  };
  const run = (q: string, p = 1) => {
    const next = new URLSearchParams(params);
    next.set('q', q);
    if (p > 1) next.set('page', String(p));
    else next.delete('page');
    setParams(next);
  };
  const open = (f: { id: number; query: string }) => {
    const next = new URLSearchParams();
    if (f.id) next.set('filter', String(f.id));
    next.set('q', f.query);
    setParams(next);
  };
  const error = results.error;
  const position =
    error instanceof ApiError ? error.errors.find((e) => e.location?.startsWith('q:'))?.location : undefined;
  const total = results.data?.total ?? 0;
  return (
    <Stack gap="md">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          run(draft);
        }}
      >
        <Group align="flex-start" wrap="nowrap">
          <KqlInput
            value={draft}
            onChange={setDraft}
            onSubmit={() => run(draft)}
            placeholder={t('issues.queryHelp')}
            label={t('issues.query')}
            error={position ? errorMessage(error) : undefined}
          />
          <Button type="submit">{t('issues.run')}</Button>
          <Menu position="bottom-end" width={300}>
            <Menu.Target>
              <Button variant="default" leftSection={<IconFilter size={16} />}>
                {t('issues.filters')}
              </Button>
            </Menu.Target>
            <Menu.Dropdown>
              {menuFavourites.length + menuOwn.length === 0 && <Menu.Label>{t('common.none')}</Menu.Label>}
              {[
                { label: t('filters.favourites'), list: menuFavourites },
                { label: t('filters.mine'), list: menuOwn },
              ]
                .filter((section) => section.list.length > 0)
                .map((section) => [
                  <Menu.Label key={section.label}>{section.label}</Menu.Label>,
                  ...section.list.map((f) => (
                    <Menu.Item
                      key={f.id}
                      onClick={() => open(f)}
                      rightSection={
                        <ActionIcon
                          size="xs"
                          variant="transparent"
                          onClick={(e) => {
                            e.stopPropagation();
                            favourite.mutate(f);
                          }}
                          aria-label={f.favourite ? t('filters.unfavourite') : t('filters.favourite')}
                          aria-pressed={f.favourite}
                        >
                          {f.favourite ? (
                            <IconStarFilled size={14} color="var(--mantine-color-amber-6)" />
                          ) : (
                            <IconStar size={14} />
                          )}
                        </ActionIcon>
                      }
                    >
                      {f.name}
                    </Menu.Item>
                  )),
                ])}
              <Menu.Divider />
              <Menu.Item leftSection={<IconSettings size={14} />} component={Link} to="/issues/filters">
                {t('filters.title')}
              </Menu.Item>
            </Menu.Dropdown>
          </Menu>
          <Tooltip label={t('issues.saveFilter')}>
            <ActionIcon
              variant="default"
              size="lg"
              onClick={() => setSaving(true)}
              aria-label={t('issues.saveFilter')}
            >
              <IconDeviceFloppy size={18} />
            </ActionIcon>
          </Tooltip>
        </Group>
      </form>
      {filterId > 0 && current.error && <ErrorAlert error={current.error} />}
      {current.data && (
        <FilterBar
          filter={current.data}
          query={query}
          columns={chosen}
          onSaved={(f) => (f.id ? open(f) : open({ id: 0, query }))}
        />
      )}
      {error && !position && <ErrorAlert error={error} />}
      {results.data && (
        <>
          <Group justify="space-between" gap="xs">
            <Text size="sm" c="dimmed">
              {t('issues.results', { count: total })}
            </Text>
            <Group gap="xs">
              <ColumnChooser
                catalog={catalog}
                value={columns}
                onChange={setColumns}
                onReset={() => setColumns(filterId > 0 ? (filterColumns ?? defaultColumns) : [])}
              />
              <Menu position="bottom-end" width={300}>
                <Menu.Target>
                  <Button
                    variant="default"
                    size="xs"
                    leftSection={<IconDownload size={14} />}
                    loading={exporting}
                    disabled={total === 0}
                  >
                    {t('navigator.export')}
                  </Button>
                </Menu.Target>
                <Menu.Dropdown>
                  <Menu.Label>{t('navigator.exportHelp', { max: exportMax.toLocaleString() })}</Menu.Label>
                  <Menu.Item onClick={() => void exportCSV(',')}>{t('navigator.csvComma')}</Menu.Item>
                  <Menu.Item onClick={() => void exportCSV(';')}>{t('navigator.csvSemicolon')}</Menu.Item>
                </Menu.Dropdown>
              </Menu>
            </Group>
          </Group>
          {(selected.size > 0 || allResults) && (
            <Paper withBorder p="xs" px="sm" radius="md" className="kogge-bulk-bar" data-testid="bulk-bar">
              <Group justify="space-between" gap="xs">
                <Group gap="sm">
                  <Text size="sm" fw={600}>
                    {allResults
                      ? t('bulk.allChosen', { count: Math.min(total, bulkMax) })
                      : t('bulk.chosen', { count: selected.size })}
                  </Text>
                  {!allResults &&
                    total > results.data.issues.length &&
                    results.data.issues.every((r) => selected.has(r.key)) && (
                      <Anchor component="button" type="button" size="sm" onClick={() => setAllResults(true)}>
                        {total > bulkMax
                          ? t('bulk.chooseFirst', { count: bulkMax, total })
                          : t('bulk.chooseAll', { count: total })}
                      </Anchor>
                    )}
                  <Anchor
                    component="button"
                    type="button"
                    size="sm"
                    c="dimmed"
                    onClick={() => {
                      setSelected(new Set());
                      setAllResults(false);
                    }}
                  >
                    {t('bulk.clear')}
                  </Anchor>
                </Group>
                <Button
                  size="xs"
                  leftSection={<IconListCheck size={14} />}
                  onClick={() =>
                    setBulk(
                      allResults
                        ? { query, count: Math.min(total, bulkMax) }
                        : { issues: [...selected], count: selected.size },
                    )
                  }
                >
                  {t('bulk.open')}
                </Button>
              </Group>
            </Paper>
          )}
          {total === 0 ? (
            <Alert color="gray">{t('issues.noResults')}</Alert>
          ) : (
            <IssueTable
              rows={results.data.issues}
              columns={columns}
              query={query}
              onSort={(field, desc) => run(orderBy(query, field, desc))}
              selection={{
                selected: allResults ? new Set(results.data.issues.map((r) => r.key)) : selected,
                onChange: (keys, on) => {
                  // Choosing single issues again ends "all results".
                  const next = new Set(allResults ? results.data.issues.map((r) => r.key) : selected);
                  for (const k of keys) {
                    if (on) next.add(k);
                    else next.delete(k);
                  }
                  setAllResults(false);
                  setSelected(next);
                },
              }}
            />
          )}
          {total > pageSize && (
            <Pagination total={Math.ceil(total / pageSize)} value={page} onChange={(p) => run(query, p)} />
          )}
        </>
      )}
      {bulk && <BulkChangeModal target={bulk} onClose={() => setBulk(null)} />}
      {saving && (
        <FilterForm
          filter={null}
          query={query}
          columns={columns.map((c) => c.key)}
          onClose={(f) => {
            setSaving(false);
            if (f) open(f);
          }}
        />
      )}
    </Stack>
  );
}
