// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Avatar,
  Badge,
  Button,
  Card,
  Checkbox,
  CloseButton,
  Group,
  Modal,
  SegmentedControl,
  SimpleGrid,
  Stack,
  Table,
  TagsInput,
  Text,
  TextInput,
  Textarea,
  UnstyledButton,
  VisuallyHidden,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import {
  IconArrowDown,
  IconArrowUp,
  IconLayoutGrid,
  IconList,
  IconSearch,
  IconSelector,
  IconStar,
  IconStarFilled,
  IconUser,
} from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { avatarColor } from '../../components/avatarColor';
import { api, unwrap } from '../../api/client';
import { formatDateTime, formatRelative } from '../../app/format';
import { useSession } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { Pagination } from '../../components/Pagination';
import { notifyError } from '../../components/notify';
import { pagePath, type Space } from './hooks';

export function SpaceAvatar({
  space,
  size = 40,
}: {
  space: Pick<Space, 'key' | 'name' | 'type'>;
  size?: number;
}) {
  return (
    <Avatar size={size} radius="md" color={avatarColor(space.key)} variant="filled">
      {space.type === 'personal' ? <IconUser size={size * 0.5} /> : space.name.slice(0, 2).toUpperCase()}
    </Avatar>
  );
}

function CreateSpace({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [name, setName] = useState('');
  const [key, setKey] = useState('');
  const [touched, setTouched] = useState(false);
  const [description, setDescription] = useState('');
  const [categories, setCategories] = useState<string[]>([]);
  const [open, setOpen] = useState(true);
  const suggested = name
    .toUpperCase()
    .replace(/[^A-Z0-9 ]/g, '')
    .split(/\s+/)
    .filter(Boolean)
    .map((w, _i, all) => (all.length > 1 ? w[0] : w.slice(0, 8)))
    .join('')
    .slice(0, 32);
  const effective = touched ? key : suggested;
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/wiki/spaces', { body: { key: effective, name, description, categories, open } }),
      ),
    onSuccess: (s) => {
      void client.invalidateQueries({ queryKey: ['space'] });
      onClose();
      navigate(pagePath(s.key, s.homePageId ?? 0));
    },
  });
  return (
    <Modal opened onClose={onClose} title={t('wiki.createSpace')} size="lg">
      <Stack>
        <TextInput
          label={t('common.name')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
          data-autofocus
        />
        <TextInput
          label={t('wiki.spaceKey')}
          description={t('wiki.spaceKeyHelp')}
          value={effective}
          onChange={(e) => {
            setTouched(true);
            setKey(e.currentTarget.value.toUpperCase().replace(/[^A-Z0-9]/g, ''));
          }}
          required
        />
        <Textarea
          label={t('common.description')}
          value={description}
          onChange={(e) => setDescription(e.currentTarget.value)}
          autosize
        />
        <TagsInput label={t('wiki.categories')} value={categories} onChange={setCategories} />
        <Checkbox
          label={t('wiki.openSpace')}
          description={t('wiki.openSpaceHelp')}
          checked={open}
          onChange={(e) => setOpen(e.currentTarget.checked)}
        />
        <ErrorAlert error={create.error} />
        <Group justify="flex-end">
          <Button
            onClick={() => create.mutate()}
            loading={create.isPending}
            disabled={!name.trim() || !effective}
          >
            {t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

type SpaceView = 'list' | 'cards';
type SpaceSort = 'name' | 'key' | 'type' | 'updated';

/** Spaces per page: the list is compact, cards fill three columns. */
const pageSizes: Record<SpaceView, number> = { list: 50, cards: 24 };

const viewKey = 'kogge.wiki.spaceView';

/** How the directory shows spaces, as the reader chose last in this browser: a list unless cards were chosen. */
export function useSpaceView(): [SpaceView, (view: SpaceView) => void] {
  const [view, setView] = useState<SpaceView>(() => {
    try {
      return localStorage.getItem(viewKey) === 'cards' ? 'cards' : 'list';
    } catch {
      return 'list';
    }
  });
  const choose = (next: SpaceView) => {
    setView(next);
    try {
      localStorage.setItem(viewKey, next);
    } catch {
      // storage unavailable — the choice lasts for this page only
    }
  };
  return [view, choose];
}

function Categories({ space, onCategory }: { space: Space; onCategory: (c: string) => void }) {
  const { t } = useTranslation();
  if (space.categories.length === 0) return null;
  return (
    <Group gap={4} mt={4}>
      {space.categories.map((c) => (
        <Badge
          key={c}
          size="xs"
          variant="light"
          tt="none"
          component="button"
          type="button"
          style={{ cursor: 'pointer' }}
          onClick={() => onCategory(c)}
          aria-label={t('wiki.showCategory', { name: c })}
        >
          {c}
        </Badge>
      ))}
    </Group>
  );
}

function FavouriteButton({ space, onToggle }: { space: Space; onToggle: (s: Space) => void }) {
  const { t } = useTranslation();
  return (
    <ActionIcon
      variant="subtle"
      color="yellow"
      onClick={() => onToggle(space)}
      aria-label={t('wiki.favourite')}
      aria-pressed={space.favourite}
    >
      {space.favourite ? <IconStarFilled size={16} /> : <IconStar size={16} />}
    </ActionIcon>
  );
}

/** A header of the space table that orders the list: the first click by the column, the next one reversed. */
function SortHeader({
  column,
  label,
  sort,
  desc,
  onSort,
}: {
  column: SpaceSort;
  label: string;
  sort: SpaceSort;
  desc: boolean;
  onSort: (column: SpaceSort, desc: boolean) => void;
}) {
  const { t } = useTranslation();
  const active = sort === column;
  const icon = !active ? (
    <IconSelector size={14} aria-hidden style={{ opacity: 0.45 }} />
  ) : desc ? (
    <IconArrowDown size={14} aria-hidden />
  ) : (
    <IconArrowUp size={14} aria-hidden />
  );
  return (
    <Table.Th
      aria-sort={active ? (desc ? 'descending' : 'ascending') : undefined}
      style={{ whiteSpace: 'nowrap' }}
    >
      <UnstyledButton
        onClick={() => onSort(column, active ? !desc : column === 'updated')}
        aria-label={t('navigator.sortBy', { name: label })}
        fz="sm"
        fw={600}
      >
        <Group gap={4} wrap="nowrap">
          {label}
          {icon}
        </Group>
      </UnstyledButton>
    </Table.Th>
  );
}

function SpaceTable({
  spaces,
  sort,
  desc,
  onSort,
  onCategory,
  onFavourite,
}: {
  spaces: Space[];
  sort: SpaceSort;
  desc: boolean;
  onSort: (column: SpaceSort, desc: boolean) => void;
  onCategory: (c: string) => void;
  onFavourite: (s: Space) => void;
}) {
  const { t } = useTranslation();
  const header = { sort, desc, onSort };
  return (
    <Table.ScrollContainer minWidth={640}>
      <Table data-testid="space-table" verticalSpacing={6} highlightOnHover>
        <Table.Thead>
          <Table.Tr>
            <SortHeader column="key" label={t('wiki.spaceKey')} {...header} />
            <SortHeader column="name" label={t('common.name')} {...header} />
            <SortHeader column="type" label={t('wiki.spaceType')} {...header} />
            <SortHeader column="updated" label={t('wiki.lastUpdated')} {...header} />
            <Table.Th w={40}>
              <VisuallyHidden>{t('wiki.favourite')}</VisuallyHidden>
            </Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {spaces.map((s) => (
            <Table.Tr key={s.id}>
              <Table.Td>
                <Text size="sm" ff="monospace">
                  {s.key}
                </Text>
              </Table.Td>
              <Table.Td>
                <Group gap="sm" wrap="nowrap" style={{ minWidth: 0 }}>
                  <SpaceAvatar space={s} size={28} />
                  <Stack gap={0} style={{ minWidth: 0 }}>
                    <Anchor
                      component={Link}
                      to={pagePath(s.key, s.homePageId ?? 0)}
                      fw={600}
                      size="sm"
                      c="var(--mantine-color-text)"
                      truncate
                    >
                      {s.name}
                    </Anchor>
                    {s.description && (
                      <Text size="xs" c="dimmed" lineClamp={1}>
                        {s.description}
                      </Text>
                    )}
                    <Categories space={s} onCategory={onCategory} />
                  </Stack>
                </Group>
              </Table.Td>
              <Table.Td>
                <Group gap={4} wrap="nowrap">
                  <Badge size="sm" variant="light" color={s.type === 'personal' ? 'grape' : 'blue'} tt="none">
                    {s.type === 'personal' ? t('wiki.personal') : t('wiki.teamSpace')}
                  </Badge>
                  {s.archived && (
                    <Badge size="sm" variant="light" color="gray" tt="none">
                      {t('wiki.archived')}
                    </Badge>
                  )}
                </Group>
              </Table.Td>
              <Table.Td style={{ whiteSpace: 'nowrap' }}>
                <Text size="sm" title={formatDateTime(s.lastUpdated ?? s.updated)}>
                  {formatRelative(s.lastUpdated ?? s.updated)}
                </Text>
              </Table.Td>
              <Table.Td>
                <FavouriteButton space={s} onToggle={onFavourite} />
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}

function SpaceCards({
  spaces,
  onCategory,
  onFavourite,
}: {
  spaces: Space[];
  onCategory: (c: string) => void;
  onFavourite: (s: Space) => void;
}) {
  return (
    <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} data-testid="space-cards">
      {spaces.map((s) => (
        <Card key={s.id} withBorder padding="md">
          <Group justify="space-between" align="flex-start" wrap="nowrap">
            <Group wrap="nowrap" style={{ minWidth: 0 }}>
              <SpaceAvatar space={s} />
              <Stack gap={0} style={{ minWidth: 0 }}>
                <Anchor
                  component={Link}
                  to={pagePath(s.key, s.homePageId ?? 0)}
                  fw={700}
                  c="var(--mantine-color-text)"
                  truncate
                >
                  {s.name}
                </Anchor>
                <Text size="xs" c="dimmed">
                  {s.key}
                </Text>
              </Stack>
            </Group>
            <FavouriteButton space={s} onToggle={onFavourite} />
          </Group>
          {s.description && (
            <Text size="sm" c="dimmed" mt="xs" lineClamp={2}>
              {s.description}
            </Text>
          )}
          <Categories space={s} onCategory={onCategory} />
        </Card>
      ))}
    </SimpleGrid>
  );
}

const filters = ['all', 'favourites', 'personal', 'archived'] as const;
const sorts: SpaceSort[] = ['name', 'key', 'type', 'updated'];

/**
 * The space directory: a page of the spaces the reader may see, found and ordered by the server, as a list or as
 * cards. Filters, order and page are kept in the address, so that going back returns to the same page.
 */
export function SpaceDirectory() {
  const { t } = useTranslation();
  const { can, me } = useSession();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [params, setParams] = useSearchParams();
  const [view, setView] = useSpaceView();
  const filter = filters.find((f) => f === params.get('filter')) ?? 'all';
  const category = params.get('category') ?? '';
  const sort = sorts.find((s) => s === params.get('sort')) ?? 'name';
  const desc = params.has('order') ? params.get('order') === 'desc' : sort === 'updated';
  const page = Math.max(1, Number(params.get('page')) || 1);
  const q = params.get('q') ?? '';
  const [query, setQuery] = useState(q);
  const [debounced] = useDebouncedValue(query.trim(), 250);
  const [creating, setCreating] = useState(false);
  const size = pageSizes[view];
  const update = (patch: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    if (!('page' in patch)) next.delete('page');
    setParams(next, { replace: true });
  };
  useEffect(() => {
    if (debounced !== q) update({ q: debounced || null });
    // Only a change of what was typed updates the address; q follows it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced]);
  const spaces = useQuery({
    queryKey: ['space', 'list', 'directory', q, filter, category, sort, desc, page, size],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/wiki/spaces', {
          params: {
            query: {
              query: q || undefined,
              favourite: filter === 'favourites' || undefined,
              archived: filter === 'archived' || undefined,
              type: filter === 'personal' ? 'personal' : undefined,
              category: category || undefined,
              sort,
              order: desc ? 'desc' : 'asc',
              limit: size,
              offset: (page - 1) * size,
            },
          },
        }),
      ),
    placeholderData: (p) => p,
  });
  // Whether the reader has a personal space already, wherever it is in the directory.
  const own = useQuery({
    queryKey: ['space', 'list', 'own-personal', me.user.username],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/wiki/spaces', {
          params: { query: { type: 'personal', owner: me.user.username, limit: 1 } },
        }),
      ),
    enabled: filter === 'personal' && can('space.create-personal'),
  });
  const fav = useMutation({
    mutationFn: (s: Space) =>
      s.favourite
        ? unwrap(api.DELETE('/api/v1/wiki/spaces/{key}/favourite', { params: { path: { key: s.key } } }))
        : unwrap(api.PUT('/api/v1/wiki/spaces/{key}/favourite', { params: { path: { key: s.key } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['space'] }),
    onError: notifyError,
  });
  const personal = useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/wiki/spaces/personal')),
    onSuccess: (s) => {
      void client.invalidateQueries({ queryKey: ['space'] });
      navigate(pagePath(s.key, s.homePageId ?? 0));
    },
    onError: notifyError,
  });
  const [search, setSearch] = useState('');
  const items = spaces.data?.items ?? [];
  const total = spaces.data?.total ?? 0;
  const pages = Math.ceil(total / size);
  const onCategory = (c: string) => update({ category: c });
  const narrowed = !!q || !!category || filter !== 'all';
  return (
    <>
      <PageHeader
        title={t('wiki.spaces')}
        subtitle={t('wiki.subtitle')}
        actions={
          <>
            {can('space.create-personal') && own.data?.total === 0 && filter === 'personal' && (
              <Button variant="default" onClick={() => personal.mutate()} loading={personal.isPending}>
                {t('wiki.createPersonal')}
              </Button>
            )}
            {can('space.create') && (
              <Button onClick={() => setCreating(true)}>{t('wiki.createSpace')}</Button>
            )}
          </>
        }
      />
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (search.trim()) navigate(`/wiki/search?q=${encodeURIComponent(search.trim())}`);
        }}
      >
        <TextInput
          leftSection={<IconSearch size={16} />}
          placeholder={t('wiki.searchPages')}
          value={search}
          onChange={(e) => setSearch(e.currentTarget.value)}
          mb="md"
          aria-label={t('wiki.searchPages')}
        />
      </form>
      <Group justify="space-between" mb="md" gap="sm">
        <SegmentedControl
          value={filter}
          onChange={(v) => update({ filter: v === 'all' ? null : v })}
          aria-label={t('wiki.whichSpaces')}
          data={[
            { value: 'all', label: t('wiki.allSpaces') },
            { value: 'favourites', label: t('wiki.favourites') },
            { value: 'personal', label: t('wiki.personal') },
            { value: 'archived', label: t('wiki.archived') },
          ]}
        />
        <Group gap="sm" wrap="nowrap">
          <TextInput
            placeholder={t('wiki.filterSpaces')}
            value={query}
            onChange={(e) => setQuery(e.currentTarget.value)}
            w={240}
            aria-label={t('wiki.filterSpaces')}
          />
          <SegmentedControl
            value={view}
            onChange={(v) => {
              setView(v as SpaceView);
              update({ page: null });
            }}
            aria-label={t('wiki.spaceView')}
            data={[
              {
                value: 'list',
                label: (
                  <Group gap={4} wrap="nowrap">
                    <IconList size={16} aria-hidden />
                    <span>{t('wiki.viewList')}</span>
                  </Group>
                ),
              },
              {
                value: 'cards',
                label: (
                  <Group gap={4} wrap="nowrap">
                    <IconLayoutGrid size={16} aria-hidden />
                    <span>{t('wiki.viewCards')}</span>
                  </Group>
                ),
              },
            ]}
          />
        </Group>
      </Group>
      {category && (
        <Group mb="sm">
          <Badge
            size="lg"
            variant="light"
            tt="none"
            rightSection={
              <CloseButton
                size="xs"
                onClick={() => update({ category: null })}
                aria-label={t('wiki.allCategories')}
              />
            }
          >
            {t('wiki.categoryFilter', { name: category })}
          </Badge>
        </Group>
      )}
      <ErrorAlert error={spaces.error} />
      {spaces.isSuccess && total === 0 && (
        <Text c="dimmed">{narrowed ? t('wiki.noMatchingSpaces') : t('wiki.noSpaces')}</Text>
      )}
      {total > 0 && (
        <Text size="sm" c="dimmed" mb="xs" aria-live="polite">
          {t('wiki.spaceCount', { count: total })}
        </Text>
      )}
      {items.length > 0 &&
        (view === 'list' ? (
          <SpaceTable
            spaces={items}
            sort={sort}
            desc={desc}
            onSort={(column, d) =>
              update({ sort: column === 'name' ? null : column, order: d ? 'desc' : 'asc' })
            }
            onCategory={onCategory}
            onFavourite={(s) => fav.mutate(s)}
          />
        ) : (
          <SpaceCards spaces={items} onCategory={onCategory} onFavourite={(s) => fav.mutate(s)} />
        ))}
      {pages > 1 && (
        <Group justify="center" mt="md">
          <Pagination
            total={pages}
            value={Math.min(page, pages)}
            onChange={(p) => update({ page: String(p) })}
          />
        </Group>
      )}
      {creating && <CreateSpace onClose={() => setCreating(false)} />}
    </>
  );
}
