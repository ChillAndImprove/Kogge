// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Button, Card, Grid, Group, Stack, Tabs, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { IconBug, IconLayoutDashboard, IconLayoutKanban, IconTimeline } from '@tabler/icons-react';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../../api/client';
import { useSession } from '../../app/session';
import { PageHeader } from '../../components/PageHeader';
import { DashboardView } from '../dashboards/DashboardView';
import { useFavouriteDashboards } from '../dashboards/DashboardsPage';
import { WidgetBody } from '../dashboards/widgets';
import { ProjectAvatar } from './ProjectLayout';

function Section({
  title,
  icon,
  action,
  children,
}: {
  title: string;
  icon: ReactNode;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <Card withBorder padding="lg">
      <Group justify="space-between" mb="sm">
        <Title order={4} style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          {icon}
          {title}
        </Title>
        {action}
      </Group>
      {children}
    </Card>
  );
}

/** The projects the user works in, with their boards. */
function Projects() {
  const { t } = useTranslation();
  const projects = useQuery({ queryKey: ['projects'], queryFn: () => unwrap(api.GET('/api/v1/projects')) });
  const list = (projects.data ?? []).filter((p) => !p.archived).slice(0, 8);
  if (list.length === 0) {
    return (
      <Text c="dimmed" size="sm">
        {t('kontor.noProjects')}
      </Text>
    );
  }
  return (
    <Stack gap="sm">
      {list.map((p) => (
        <Group key={p.key} gap="sm" wrap="nowrap">
          <ProjectAvatar projectKey={p.key} size={32} />
          <Stack gap={0} style={{ minWidth: 0, flex: 1 }}>
            <Anchor component={Link} to={`/projects/${p.key}`} fw={600} size="sm" truncate>
              {p.name}
            </Anchor>
            <Text size="xs" c="dimmed">
              {p.key}
            </Text>
          </Stack>
          <Anchor
            component={Link}
            to={`/projects/${p.key}/boards`}
            size="xs"
            c="dimmed"
            aria-label={t('kontor.boardsOf', { project: p.name })}
          >
            <IconLayoutKanban size={16} />
          </Anchor>
        </Group>
      ))}
    </Stack>
  );
}

function Work() {
  const { t } = useTranslation();
  return (
    <Grid gap="lg">
      <Grid.Col span={{ base: 12, md: 8 }}>
        <Stack gap="lg">
          <Section
            title={t('home.assigned')}
            icon={<IconBug size={20} />}
            action={
              <Anchor
                component={Link}
                size="sm"
                to={`/issues/search?q=${encodeURIComponent('assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC')}`}
              >
                {t('kontor.showAll')}
              </Anchor>
            }
          >
            <WidgetBody
              widget={{
                id: 'assigned',
                type: 'assigned-to-me',
                column: 0,
                position: 0,
                config: { limit: 12 },
              }}
            />
          </Section>
          <Section title={t('home.activity')} icon={<IconTimeline size={20} />}>
            <WidgetBody
              widget={{ id: 'activity', type: 'activity', column: 1, position: 0, config: { limit: 12 } }}
            />
          </Section>
        </Stack>
      </Grid.Col>
      <Grid.Col span={{ base: 12, md: 4 }}>
        <Section
          title={t('kontor.projects')}
          icon={<IconLayoutKanban size={20} />}
          action={
            <Anchor component={Link} to="/issues/projects" size="sm">
              {t('kontor.allProjects')}
            </Anchor>
          }
        >
          <Projects />
        </Section>
      </Grid.Col>
    </Grid>
  );
}

/** Kontor's start page: the user's issues, the activity in their projects, and their favourite dashboards. */
export function KontorHome() {
  const { t } = useTranslation();
  const { me } = useSession();
  const dashboards = useFavouriteDashboards();
  const favourites = dashboards.data?.items ?? [];
  const [tab, setTab] = useState<string | null>('work');
  return (
    <>
      <PageHeader
        title={t('kontor.yourWork')}
        subtitle={t('home.greeting', { name: me.user.displayName })}
        actions={
          <Button
            component={Link}
            to="/dashboards"
            variant="default"
            leftSection={<IconLayoutDashboard size={16} />}
          >
            {t('dashboards.heading')}
          </Button>
        }
      />
      {favourites.length === 0 ? (
        <Work />
      ) : (
        <Tabs value={tab} onChange={setTab} keepMounted={false}>
          <Tabs.List mb="md">
            <Tabs.Tab value="work">{t('kontor.yourWork')}</Tabs.Tab>
            {favourites.map((d) => (
              <Tabs.Tab key={d.id} value={String(d.id)}>
                {d.name}
              </Tabs.Tab>
            ))}
          </Tabs.List>
          <Tabs.Panel value="work">
            <Work />
          </Tabs.Panel>
          {favourites.map((d) => (
            <Tabs.Panel key={d.id} value={String(d.id)}>
              <Group justify="flex-end" mb="xs">
                <Anchor component={Link} to={`/dashboards/${d.id}`} size="sm">
                  {t('dashboards.open')}
                </Anchor>
              </Group>
              <DashboardView dashboard={d} editing={false} />
            </Tabs.Panel>
          ))}
        </Tabs>
      )}
    </>
  );
}
