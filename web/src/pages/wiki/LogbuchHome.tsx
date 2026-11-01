// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Badge, Card, Divider, Grid, Group, Paper, Stack, Text, Title } from '@mantine/core';
import { IconClock, IconStar } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { Fragment } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatRelative } from '../../app/format';
import { useSession } from '../../app/session';
import { SpaceAvatar } from './SpaceDirectory';

/** What changed recently in the spaces the user can read. */
function Updates() {
  const { t } = useTranslation();
  const pages = useQuery({
    queryKey: ['page', 'recent', 'updated', 'logbuch'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/wiki/recent', { params: { query: { mode: 'updated', limit: 20 } } })),
  });
  const items = pages.data ?? [];
  if (pages.isSuccess && items.length === 0) {
    return (
      <Text c="dimmed" size="sm">
        {t('logbuch.nothingYet')}
      </Text>
    );
  }
  return (
    <Paper withBorder radius="md">
      {items.map((pg, i) => (
        <Fragment key={pg.id}>
          {i > 0 && <Divider />}
          <Stack gap={2} py="sm" px="md" className="logbuch-feed-item">
            <Group gap="xs" wrap="nowrap">
              <Anchor
                component={Link}
                to={pg.url}
                fw={650}
                c="var(--mantine-color-text)"
                className="logbuch-title"
                truncate
              >
                {pg.title}
              </Anchor>
              {pg.kind === 'blog' && (
                <Badge size="xs" variant="light">
                  {t('logbuch.blogPost')}
                </Badge>
              )}
            </Group>
            {pg.excerpt && (
              <Text size="sm" c="dimmed" lineClamp={2}>
                {pg.excerpt}
              </Text>
            )}
            <Text size="xs" c="dimmed">
              {t('logbuch.updatedIn', {
                space: pg.space.name,
                who: pg.updatedBy?.displayName ?? t('code.pr.someone'),
                when: formatRelative(pg.updated),
              })}
            </Text>
          </Stack>
        </Fragment>
      ))}
    </Paper>
  );
}

/** The spaces the user marked as favourites (or, without favourites, the first spaces). */
function Spaces() {
  const { t } = useTranslation();
  const favourites = useQuery({
    queryKey: ['space', 'list', 'logbuch-home', 'favourites'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/wiki/spaces', { params: { query: { favourite: true, limit: 8 } } })),
  });
  const first = useQuery({
    queryKey: ['space', 'list', 'logbuch-home', 'first'],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/spaces', { params: { query: { limit: 8 } } })),
    enabled: favourites.isSuccess && favourites.data.total === 0,
  });
  const list = (favourites.data?.total ? favourites.data : first.data)?.items ?? [];
  return (
    <Stack gap="sm">
      {list.map((s) => (
        <Group key={s.key} gap="sm" wrap="nowrap">
          <SpaceAvatar space={s} size={30} />
          <Anchor
            component={Link}
            to={`/wiki/spaces/${s.key}`}
            size="sm"
            fw={550}
            c="var(--mantine-color-text)"
            truncate
          >
            {s.name}
          </Anchor>
        </Group>
      ))}
      <Anchor component={Link} to="/wiki/spaces" size="sm">
        {t('logbuch.allSpaces')}
      </Anchor>
    </Stack>
  );
}

function Visited() {
  const { t } = useTranslation();
  const pages = useQuery({
    queryKey: ['page', 'recent', 'viewed', 'logbuch'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/wiki/recent', { params: { query: { mode: 'viewed', limit: 8 } } })),
  });
  const items = pages.data ?? [];
  if (items.length === 0) {
    return (
      <Text c="dimmed" size="sm">
        {t('home.nothing')}
      </Text>
    );
  }
  return (
    <Stack gap={6}>
      {items.map((pg) => (
        <Stack key={pg.id} gap={0}>
          <Anchor component={Link} to={pg.url} size="sm" c="var(--mantine-color-text)" truncate>
            {pg.title}
          </Anchor>
          <Text size="xs" c="dimmed">
            {pg.space.name}
          </Text>
        </Stack>
      ))}
    </Stack>
  );
}

/** Logbuch's start page: recent changes, and the way back to the user's spaces and pages. */
export function LogbuchHome() {
  const { t } = useTranslation();
  const { me } = useSession();
  return (
    <Grid gap="xl" className="logbuch-home">
      <Grid.Col span={{ base: 12, md: 8 }}>
        <Title order={2} className="logbuch-title" mb={4}>
          {t('logbuch.welcome', { name: me.user.displayName.split(' ')[0] })}
        </Title>
        <Text c="dimmed" mb="lg">
          {t('logbuch.welcomeSubtitle')}
        </Text>
        <Updates />
      </Grid.Col>
      <Grid.Col span={{ base: 12, md: 4 }}>
        <Stack gap="lg">
          <Card withBorder padding="lg">
            <Title order={5} mb="sm" style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
              <IconStar size={16} />
              {t('logbuch.yourSpaces')}
            </Title>
            <Spaces />
          </Card>
          <Card withBorder padding="lg">
            <Title order={5} mb="sm" style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
              <IconClock size={16} />
              {t('home.recentPages')}
            </Title>
            <Visited />
          </Card>
        </Stack>
      </Grid.Col>
    </Grid>
  );
}
