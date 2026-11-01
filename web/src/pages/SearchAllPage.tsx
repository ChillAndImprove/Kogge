// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Box,
  Button,
  Card,
  Grid,
  Group,
  Loader,
  NavLink as MantineNavLink,
  Stack,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { IconLayoutGrid, IconSearch, IconUserCircle } from '@tabler/icons-react';
import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { api, unwrap, type Schemas } from '../api/client';
import { formatRelative } from '../app/format';
import { ErrorAlert } from '../components/ErrorAlert';
import { Excerpt } from '../components/Excerpt';
import { PageHeader } from '../components/PageHeader';
import { Pagination } from '../components/Pagination';
import {
  HitBadges,
  HitCount,
  HitIcon,
  HitPerson,
  iconSpeaks,
  showsKey,
  type Hit,
} from '../components/SearchHit';
import { products, type ProductId } from '../products';

type Group = Schemas['SearchGroup'];
type Kind = Group['kind'];
type Product = Group['product'];

const pageSize = 20;

/** The products and their kinds of things, in the order of the page. */
const productKinds: [Product, Kind[]][] = [
  ['kontor', ['issue', 'project']],
  ['logbuch', ['page', 'blog', 'space']],
  ['hafen', ['pull-request', 'repository', 'code-project']],
  ['helling', ['pipelines']],
  ['people', ['user']],
];

function useProductName() {
  const { t } = useTranslation();
  return (p: Product) => (p === 'people' ? t('searchAll.people') : products[p as ProductId].name);
}

function ProductIcon({ product }: { product: Product }) {
  if (product === 'people') return <IconUserCircle size={16} />;
  const Logo = products[product as ProductId].Logo;
  return <Logo size={16} />;
}

/** A thing found: its link, where it is, what matched. */
function SearchHit({ hit }: { hit: Hit }) {
  const { t } = useTranslation();
  return (
    <Group component="li" gap="sm" wrap="nowrap" align="flex-start" py="xs" className="kogge-search-hit">
      <Box pt={2} aria-hidden={!iconSpeaks(hit)} className="kogge-qs-icon">
        <HitIcon hit={hit} />
      </Box>
      <Stack gap={2} miw={0} style={{ flex: 1 }}>
        <Group gap="xs" wrap="wrap" miw={0}>
          <Anchor
            component={Link}
            to={hit.url}
            fw={600}
            underline="hover"
            style={{ overflowWrap: 'anywhere' }}
          >
            {showsKey(hit) ? `${hit.key} ${hit.title}` : hit.title}
          </Anchor>
          <HitBadges hit={hit} />
        </Group>
        {hit.excerpt && <Excerpt text={hit.excerpt} />}
        <Group gap="xs" wrap="wrap">
          <Text size="xs" c="dimmed">
            {[
              hit.kind === 'user' ? hit.key : hit.context,
              hit.archived ? t('quickSearch.archived') : '',
              hit.updated ? t('searchAll.updated', { when: formatRelative(hit.updated) }) : '',
            ]
              .filter(Boolean)
              .join(' · ')}
          </Text>
          <HitPerson hit={hit} />
        </Group>
      </Stack>
    </Group>
  );
}

function HitList({ hits }: { hits: Hit[] }) {
  return (
    <Box component="ul" m={0} p={0} style={{ listStyle: 'none' }}>
      {hits.map((h) => (
        <SearchHit key={`${h.kind}-${h.id}`} hit={h} />
      ))}
    </Box>
  );
}

/** The search field; it starts with the text searched for (and starts anew when that changes). */
function SearchForm({ initial, onSearch }: { initial: string; onSearch: (text: string) => void }) {
  const { t } = useTranslation();
  const [text, setText] = useState(initial);
  return (
    <form
      role="search"
      onSubmit={(e) => {
        e.preventDefault();
        onSearch(text.trim());
      }}
    >
      <Group gap="xs" mb="lg" align="flex-end" wrap="nowrap">
        <TextInput
          style={{ flex: 1, maxWidth: 640 }}
          leftSection={<IconSearch size={16} />}
          aria-label={t('searchAll.field')}
          placeholder={t('searchAll.field')}
          value={text}
          onChange={(e) => setText(e.currentTarget.value)}
        />
        <Button type="submit">{t('searchAll.submit')}</Button>
      </Group>
    </form>
  );
}

/**
 * The search page: the titles and texts of everything the user may see, in all products. All
 * products at once show the first hits of each kind; a product or kind shows its hits a page at a time.
 */
export function SearchAllPage() {
  const { t } = useTranslation();
  const productName = useProductName();
  const [params, setParams] = useSearchParams();
  const q = (params.get('q') ?? '').trim();
  const product = params.get('product') as Product | null;
  const pageNumber = Math.max(1, Number(params.get('page') ?? 1) || 1);
  const kindParam = params.get('kind') as Kind | null;
  // All kinds at once, with their totals (for the filters).
  const overview = useQuery({
    queryKey: ['search-all', q],
    queryFn: ({ signal }) =>
      unwrap(api.GET('/api/v1/search-all', { params: { query: { q, limit: 5 } }, signal })),
    enabled: q !== '',
    placeholderData: keepPreviousData,
  });
  const groups = overview.data?.groups ?? [];
  const total = (k: Kind) => groups.find((g) => g.kind === k)?.total ?? 0;
  const productTotal = (p: Product) => groups.filter((g) => g.product === p).reduce((n, g) => n + g.total, 0);
  // A product shows its first kind that found something (or its first kind).
  const kinds = productKinds.find(([p]) => p === product)?.[1] ?? [];
  const kind: Kind | null =
    kindParam ?? (product ? (kinds.find((k) => total(k) > 0) ?? kinds[0] ?? null) : null);
  const paged = useQuery({
    queryKey: ['search-all', q, kind, pageNumber],
    queryFn: ({ signal }) =>
      unwrap(
        api.GET('/api/v1/search-all', {
          params: { query: { q, kind: kind!, limit: pageSize, offset: (pageNumber - 1) * pageSize } },
          signal,
        }),
      ),
    enabled: q !== '' && kind !== null,
    placeholderData: keepPreviousData,
  });

  const select = (p: Product | null, k: Kind | null) => {
    const next = new URLSearchParams({ q });
    if (p) next.set('product', p);
    if (k) next.set('kind', k);
    setParams(next);
  };
  const visibleProducts = productKinds.filter(([p]) => groups.some((g) => g.product === p));
  const incomplete = [...(overview.data?.incomplete ?? []), ...(paged.data?.incomplete ?? [])];

  let main: ReactNode;
  if (q === '') {
    main = <Text c="dimmed">{t('searchAll.noQuery')}</Text>;
  } else if (kind) {
    const g = paged.data?.groups[0];
    main = (
      <Stack gap="xs">
        <Title order={3}>{t(`searchAll.kind.${kind}`)}</Title>
        <ErrorAlert error={paged.error} />
        {paged.isPending && <Loader size="sm" />}
        {g && g.hits.length === 0 && <Text c="dimmed">{t('searchAll.nothing', { query: q })}</Text>}
        {g && <HitList hits={g.hits} />}
        {g && g.total > pageSize && (
          <Pagination
            total={Math.ceil(g.total / pageSize)}
            value={pageNumber}
            onChange={(n) => {
              const next = new URLSearchParams(params);
              next.set('page', String(n));
              setParams(next);
            }}
          />
        )}
      </Stack>
    );
  } else {
    const found = groups.filter((g) => g.total > 0);
    main = (
      <Stack gap="lg">
        <ErrorAlert error={overview.error} />
        {overview.isPending && <Loader size="sm" />}
        {overview.isSuccess && found.length === 0 && (
          <Text c="dimmed">{t('searchAll.nothing', { query: q })}</Text>
        )}
        {productKinds.map(([p, ks]) => {
          const own = found.filter((g) => g.product === p);
          if (own.length === 0) return null;
          return (
            <Card key={p} withBorder component="section" aria-labelledby={`search-${p}`}>
              <Group gap="xs" mb="xs">
                <ProductIcon product={p} />
                <Title order={3} id={`search-${p}`}>
                  {productName(p)}
                </Title>
              </Group>
              <Stack gap="sm">
                {ks
                  .map((k) => own.find((g) => g.kind === k))
                  .filter((g): g is Group => !!g)
                  .map((g) => (
                    <Box key={g.kind}>
                      <Group justify="space-between" gap="xs">
                        <Text size="sm" fw={650}>
                          {t(`searchAll.kind.${g.kind}`)}
                        </Text>
                        {g.total > g.hits.length && (
                          <Anchor
                            component="button"
                            type="button"
                            size="sm"
                            onClick={() => select(p, g.kind)}
                          >
                            {t('searchAll.showAll', { count: g.total })}
                          </Anchor>
                        )}
                      </Group>
                      <HitList hits={g.hits} />
                    </Box>
                  ))}
              </Stack>
            </Card>
          );
        })}
      </Stack>
    );
  }

  return (
    <>
      <PageHeader
        title={t('searchAll.title')}
        subtitle={q ? t('searchAll.results', { query: q }) : undefined}
      />
      <SearchForm
        key={q}
        initial={q}
        onSearch={(text) => {
          const next = new URLSearchParams();
          if (text) next.set('q', text);
          if (product) next.set('product', product);
          if (kindParam) next.set('kind', kindParam);
          setParams(next);
        }}
      />
      {incomplete.length > 0 && (
        <Text size="sm" c="dimmed" mb="md">
          {t('searchAll.incomplete', {
            products: [...new Set(incomplete)].map((p) => productName(p as Product)).join(', '),
          })}
        </Text>
      )}
      <Grid gap="xl">
        {q !== '' && (
          <Grid.Col span={{ base: 12, md: 3 }}>
            <Box component="nav" aria-label={t('searchAll.filters')}>
              <MantineNavLink
                component="button"
                type="button"
                label={t('searchAll.allProducts')}
                leftSection={<IconLayoutGrid size={16} />}
                rightSection={overview.data && <HitCount n={groups.reduce((n, g) => n + g.total, 0)} />}
                active={!product && !kindParam}
                onClick={() => select(null, null)}
              />
              {visibleProducts.map(([p, ks]) => {
                // A product or kind that found nothing is listed, but cannot be chosen.
                const none = productTotal(p) === 0 && product !== p;
                return (
                  <MantineNavLink
                    key={p}
                    component="button"
                    type="button"
                    label={productName(p)}
                    leftSection={<ProductIcon product={p} />}
                    rightSection={<HitCount n={productTotal(p)} />}
                    active={product === p && !kindParam}
                    opened={product === p}
                    disabled={none}
                    aria-disabled={none || undefined}
                    onClick={none ? undefined : () => select(p, null)}
                  >
                    {ks.length > 1 &&
                      ks.map((k) => {
                        const empty = total(k) === 0 && kind !== k;
                        return (
                          <MantineNavLink
                            key={k}
                            component="button"
                            type="button"
                            label={t(`searchAll.kind.${k}`)}
                            rightSection={<HitCount n={total(k)} />}
                            active={kind === k}
                            disabled={empty}
                            aria-disabled={empty || undefined}
                            onClick={empty ? undefined : () => select(p, k)}
                          />
                        );
                      })}
                  </MantineNavLink>
                );
              })}
            </Box>
          </Grid.Col>
        )}
        <Grid.Col span={{ base: 12, md: q !== '' ? 9 : 12 }}>{main}</Grid.Col>
      </Grid>
    </>
  );
}
