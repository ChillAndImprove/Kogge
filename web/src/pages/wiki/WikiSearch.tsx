// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Avatar,
  Badge,
  Box,
  Button,
  Collapse,
  Grid,
  Group,
  NavLink,
  SegmentedControl,
  Skeleton,
  Stack,
  Text,
  TextInput,
} from '@mantine/core';
import {
  IconAdjustmentsHorizontal,
  IconCheck,
  IconFileText,
  IconNews,
  IconSearch,
  IconX,
} from '@tabler/icons-react';
import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { Excerpt } from '../../components/Excerpt';
import { PageHeader } from '../../components/PageHeader';
import { HitCount } from '../../components/SearchHit';
import { Pagination } from '../../components/Pagination';

const pageSize = 20;

/** Half the smallest gap of the theme. */
const half = 'calc(var(--mantine-spacing-xs) / 2)';

type PageSummary = Schemas['PageSummary'];

/** The choices of the filter by the date of the last change, in days (0: any time). */
const periods = [
  { days: 0, label: 'wiki.find.anyTime' },
  { days: 7, label: 'wiki.find.pastWeek' },
  { days: 30, label: 'wiki.find.pastMonth' },
  { days: 365, label: 'wiki.find.pastYear' },
] as const;

function initials(name: string) {
  return name
    .split(/\s+/)
    .map((p) => p[0])
    .join('')
    .slice(0, 2)
    .toUpperCase();
}

function Facet({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Box component="section" aria-label={title}>
      <Text size="xs" fw={600} c="dimmed" px="sm" pb={half}>
        {title}
      </Text>
      {children}
    </Box>
  );
}

/** A choice of a filter: the chosen one in the accent with a check mark, the others plain. */
function Choice({
  label,
  count,
  chosen,
  disabled,
  onChoose,
}: {
  label: string;
  count?: number;
  chosen: boolean;
  disabled?: boolean;
  onChoose: () => void;
}) {
  return (
    <NavLink
      component="button"
      type="button"
      variant="subtle"
      label={label}
      leftSection={<IconCheck size={14} style={{ visibility: chosen ? 'visible' : 'hidden' }} />}
      rightSection={count !== undefined && <HitCount n={count} />}
      active={chosen}
      aria-pressed={chosen}
      disabled={disabled}
      aria-disabled={disabled || undefined}
      onClick={disabled ? undefined : onChoose}
      fw={chosen ? 600 : undefined}
    />
  );
}

/** A page found: its kind, title and place, the text around the matches, who changed it last and when. */
function Result({ page, onLabel }: { page: PageSummary; onLabel: (label: string) => void }) {
  const { t } = useTranslation();
  const Icon = page.kind === 'blog' ? IconNews : IconFileText;
  const place = [page.space.name, ...(page.path ?? [])].join(' › ');
  return (
    <Group component="li" gap="sm" wrap="nowrap" align="flex-start" py="sm">
      <Box
        c="dimmed"
        display="flex"
        style={{
          alignItems: 'center',
          height: 'calc(var(--mantine-font-size-md) * var(--mantine-line-height-md))',
        }}
        aria-hidden
      >
        <Icon size={18} />
      </Box>
      <Stack gap={half} miw={0} style={{ flex: 1 }}>
        <Group gap="xs" wrap="wrap" miw={0}>
          <Anchor
            component={Link}
            to={page.url}
            fw={600}
            underline="hover"
            style={{ overflowWrap: 'anywhere' }}
          >
            {page.title}
          </Anchor>
          {page.kind === 'blog' && (
            <Badge size="sm" variant="light" tt="none">
              {t('logbuch.blogPost')}
            </Badge>
          )}
        </Group>
        <Text size="xs" c="dimmed" truncate>
          {place}
        </Text>
        {page.excerpt && <Excerpt text={page.excerpt} lines={2} />}
        <Group gap="xs" wrap="wrap">
          {page.updatedBy && (
            <Group gap="xs" wrap="nowrap" miw={0}>
              <Avatar size={20} radius="xl" color="initials" name={page.updatedBy.displayName} aria-hidden>
                {initials(page.updatedBy.displayName)}
              </Avatar>
              <Text size="xs" c="dimmed" truncate>
                {t('searchAll.changedBy', { name: page.updatedBy.displayName })}
              </Text>
            </Group>
          )}
          <Text size="xs" c="dimmed">
            {formatRelative(page.updated)}
          </Text>
          {page.labels.map((l) => (
            <Badge
              key={l}
              size="sm"
              variant="outline"
              color="gray"
              tt="none"
              component="button"
              type="button"
              style={{ cursor: 'pointer' }}
              onClick={() => onLabel(l)}
            >
              {l}
            </Badge>
          ))}
        </Group>
      </Stack>
    </Group>
  );
}

/**
 * The wiki's search: pages and blog posts by their texts, titles, labels and comments, with the
 * text around the matches, filtered by space, kind, label and the date of the last change, by relevance or newest
 * first. Each filter counts what choosing it would find.
 */
export function WikiSearch() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const q = params.get('q') ?? '';
  const label = params.get('label') ?? '';
  const space = params.get('space') ?? '';
  const kind = (params.get('kind') ?? '') as '' | 'page' | 'blog';
  const sort = params.get('sort') === 'updated' ? 'updated' : 'relevance';
  const days = Number(params.get('updated') ?? 0) || 0;
  const page = Math.max(1, Number(params.get('page') ?? '1') || 1);
  const [draft, setDraft] = useState(q);
  const [filtersShown, setFiltersShown] = useState(false);
  const res = useQuery({
    queryKey: ['page', 'search', q, label, space, kind, sort, days, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/wiki/search', {
          params: {
            query: {
              q,
              label: label || undefined,
              space: space || undefined,
              kind: kind || undefined,
              sort: q && sort === 'updated' ? 'updated' : undefined,
              updatedWithin: days || undefined,
              facets: true,
              limit: pageSize,
              offset: (page - 1) * pageSize,
            },
          },
        }),
      ),
    placeholderData: keepPreviousData,
  });
  const update = (patch: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    next.delete('page');
    setParams(next);
  };
  const facets = res.data?.facets;
  const spaces = facets?.spaces ?? [];
  const kinds = facets?.kinds ?? [];
  const kindCount = (k: string) => kinds.find((x) => x.kind === k)?.count ?? 0;
  const total = res.data?.total ?? 0;
  // The space chosen stays in the list when nothing matches there.
  const spaceList =
    space && !spaces.some((s) => s.key === space)
      ? [{ key: space, name: space, count: 0 }, ...spaces]
      : spaces;
  const filters = (
    <Stack gap="md" component="nav" aria-label={t('wiki.find.filters')}>
      <Facet title={t('wiki.find.space')}>
        <Choice
          label={t('wiki.allSpaces')}
          count={facets && spaces.reduce((n, s) => n + s.count, 0)}
          chosen={!space}
          onChoose={() => update({ space: null })}
        />
        {spaceList.map((s) => (
          <Choice
            key={s.key}
            label={s.name}
            count={s.count}
            chosen={space === s.key}
            onChoose={() => update({ space: s.key })}
          />
        ))}
      </Facet>
      <Facet title={t('wiki.find.type')}>
        {(
          [
            ['', t('wiki.find.allTypes'), kindCount('page') + kindCount('blog')],
            ['page', t('wiki.find.pages'), kindCount('page')],
            ['blog', t('wiki.find.blogPosts'), kindCount('blog')],
          ] as const
        ).map(([k, name, n]) => (
          <Choice
            key={k || 'all'}
            label={name}
            count={facets && n}
            chosen={kind === k}
            disabled={!!k && n === 0 && kind !== k}
            onChoose={() => update({ kind: k || null })}
          />
        ))}
      </Facet>
      <Facet title={t('wiki.find.updated')}>
        {periods.map((p) => (
          <Choice
            key={p.days}
            label={t(p.label)}
            chosen={days === p.days}
            onChoose={() => update({ updated: p.days ? String(p.days) : null })}
          />
        ))}
      </Facet>
    </Stack>
  );
  return (
    <>
      <PageHeader title={t('wiki.search')} />
      <form
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
          update({ q: draft.trim() || null });
        }}
      >
        <Group gap="xs" mb="lg" wrap="nowrap">
          <TextInput
            style={{ flex: 1, maxWidth: 640 }}
            leftSection={<IconSearch size={16} />}
            value={draft}
            onChange={(e) => setDraft(e.currentTarget.value)}
            placeholder={t('wiki.searchPages')}
            aria-label={t('wiki.searchPages')}
          />
          <Button type="submit">{t('searchAll.submit')}</Button>
        </Group>
      </form>
      <Grid gap="xl">
        <Grid.Col span={{ base: 12, md: 3 }}>
          <Box visibleFrom="md">{filters}</Box>
          <Box hiddenFrom="md">
            <Button
              variant="default"
              size="xs"
              leftSection={<IconAdjustmentsHorizontal size={16} />}
              onClick={() => setFiltersShown((v) => !v)}
              aria-expanded={filtersShown}
            >
              {t('wiki.find.filters')}
            </Button>
            <Collapse
              expanded={filtersShown}
              mt="sm"
              transitionDuration={150}
              transitionTimingFunction="ease-out"
            >
              {filters}
            </Collapse>
          </Box>
        </Grid.Col>
        <Grid.Col span={{ base: 12, md: 9 }}>
          <Group justify="space-between" gap="sm" mb="xs" wrap="wrap">
            <Group gap="xs" wrap="wrap">
              <Text size="sm" c="dimmed" aria-live="polite">
                {t('wiki.resultCount', { count: total })}
              </Text>
              {label && (
                <Badge
                  size="lg"
                  variant="light"
                  tt="none"
                  rightSection={
                    <Anchor
                      component="button"
                      type="button"
                      c="inherit"
                      display="flex"
                      aria-label={t('wiki.find.removeLabel', { label })}
                      onClick={() => update({ label: null })}
                    >
                      <IconX size={12} />
                    </Anchor>
                  }
                >
                  {t('wiki.label')}: {label}
                </Badge>
              )}
            </Group>
            {q && (
              <SegmentedControl
                size="xs"
                aria-label={t('wiki.find.sort')}
                value={sort}
                onChange={(v) => update({ sort: v === 'updated' ? 'updated' : null })}
                data={[
                  { value: 'relevance', label: t('wiki.find.relevance') },
                  { value: 'updated', label: t('wiki.find.newest') },
                ]}
              />
            )}
          </Group>
          <ErrorAlert error={res.error} />
          {res.isPending && (
            <Stack gap="md" aria-hidden>
              {[0, 1, 2].map((i) => (
                <Stack key={i} gap="xs">
                  <Skeleton height="var(--mantine-font-size-md)" width="40%" />
                  <Skeleton height="var(--mantine-font-size-sm)" width="85%" />
                </Stack>
              ))}
            </Stack>
          )}
          {res.isSuccess && total === 0 && (
            <Text size="sm" c="dimmed">
              {t('wiki.find.nothing')}
            </Text>
          )}
          <Box component="ul" m={0} p={0} style={{ listStyle: 'none' }}>
            {(res.data?.pages ?? []).map((p) => (
              <Result key={p.id} page={p} onLabel={(l) => update({ label: l })} />
            ))}
          </Box>
          {total > pageSize && (
            <Pagination
              mt="md"
              total={Math.ceil(total / pageSize)}
              value={page}
              onChange={(v) => {
                const next = new URLSearchParams(params);
                next.set('page', String(v));
                setParams(next);
              }}
            />
          )}
          {q && (
            <Anchor
              component={Link}
              to={`/search?q=${encodeURIComponent(q)}`}
              size="sm"
              mt="md"
              display="block"
            >
              {t('wiki.find.everywhere', { query: q })}
            </Anchor>
          )}
        </Grid.Col>
      </Grid>
    </>
  );
}
