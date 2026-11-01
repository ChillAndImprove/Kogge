// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Card, Group, SimpleGrid, Stack, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../api/client';
import { useSession } from '../app/session';
import { ActivityStream } from '../components/ActivityStream';
import { Logo } from '../components/Logo';
import { products, productList, type ProductId } from '../products';

const assignedQuery = 'assignee = currentUser() AND statusCategory != Done';

/** Reviews counted exactly; beyond, the line says "more than". */
const countedReviews = 50;

/** One line of what waits for the user in a product. */
function Status({ id }: { id: ProductId }) {
  const { t } = useTranslation();
  const issues = useQuery({
    queryKey: ['launcher', 'kontor'],
    queryFn: () => unwrap(api.GET('/api/v1/search', { params: { query: { q: assignedQuery, limit: 1 } } })),
    enabled: id === 'kontor',
  });
  const pages = useQuery({
    queryKey: ['launcher', 'logbuch'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/wiki/recent', { params: { query: { mode: 'viewed', limit: 1 } } })),
    enabled: id === 'logbuch',
  });
  // One more than are counted, to say "more than" instead of a number that stops at the limit.
  const reviews = useQuery({
    queryKey: ['launcher', 'hafen'],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/pull-requests', {
          params: { query: { role: 'reviewer', state: 'open', limit: countedReviews + 1 } },
        }),
      ),
    enabled: id === 'hafen',
  });
  const failed = useQuery({
    queryKey: ['launcher', 'helling'],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/builds/runs', { params: { query: { mine: true, state: 'failed', limit: 1 } } }),
      ),
    enabled: id === 'helling',
  });
  let line: ReactNode = null;
  switch (id) {
    case 'kontor':
      if (issues.data) line = t('launcher.kontorStatus', { count: issues.data.total });
      break;
    case 'logbuch':
      if (pages.data) {
        line = pages.data[0]
          ? t('launcher.logbuchStatus', { title: pages.data[0].title })
          : t('launcher.logbuchEmpty');
      }
      break;
    case 'hafen':
      if (reviews.data) {
        line =
          reviews.data.length > countedReviews
            ? t('launcher.hafenStatusMore', { total: countedReviews })
            : t('launcher.hafenStatus', { count: reviews.data.length });
      }
      break;
    case 'helling':
      if (failed.data) line = t('launcher.hellingStatus', { count: failed.data.total });
      break;
  }
  return (
    <Text size="sm" c="dimmed" mih={20} lineClamp={1}>
      {line}
    </Text>
  );
}

/** The platform's start page: the products, each with what waits for the user there, and what happened recently. */
export function Launcher() {
  const { t } = useTranslation();
  const { me } = useSession();
  return (
    <Stack maw={1100} mx="auto" pt="md" pb="xl">
      <Group gap="md" mb="lg">
        <Logo size={48} />
        <Stack gap={0}>
          <Title order={2}>{t('home.greeting', { name: me.user.displayName })}</Title>
          <Text c="dimmed">{t('launcher.subtitle', { name: me.instanceName })}</Text>
        </Stack>
      </Group>
      <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="lg">
        {productList.map((p) => (
          <Card
            key={p.id}
            component={Link}
            to={p.home}
            withBorder
            padding="xl"
            className="kogge-product-card"
            style={{ borderTop: `4px solid var(--mantine-color-${products[p.id].color}-6)` }}
          >
            <Group gap="lg" wrap="nowrap" align="flex-start">
              <p.Logo size={56} />
              <Stack gap={4} style={{ minWidth: 0 }}>
                <Title order={3}>{p.name}</Title>
                <Text size="sm">{t(`products.${p.id}.tagline`)}</Text>
                <Text size="xs" c="dimmed">
                  {t(`products.${p.id}.features`)}
                </Text>
                <Status id={p.id} />
              </Stack>
            </Group>
          </Card>
        ))}
      </SimpleGrid>
      <Card withBorder padding="lg" mt="lg" component="section" aria-labelledby="launcher-activity">
        <Title order={3} id="launcher-activity" mb="sm">
          {t('activity.recent')}
        </Title>
        <ActivityStream productFilter />
      </Card>
    </Stack>
  );
}
