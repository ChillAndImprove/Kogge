// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Button, Group, Stack, Tabs, Text, Title } from '@mantine/core';
import { IconPlayerPlay, IconRocket, IconSettings } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link, Outlet, useLocation, useNavigate, useParams } from 'react-router';
import { ErrorAlert } from '../../components/ErrorAlert';
import { products } from '../../products';
import { RepoContext, repoPath, useRepo, useRepository } from '../code/hooks';
import { BadgeButton } from './Badge';
import { pipelinesPath } from './hooks';
import { PipelineSettings } from './Settings';

/** The frame of a repository's pipelines in Helling: its runs, environments and settings. */
export function PipelineLayout() {
  const { t } = useTranslation();
  const { key = '', repo: slug = '' } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const repo = useRepository(key, slug);
  if (repo.error) return <ErrorAlert error={repo.error} />;
  if (!repo.data) return null;
  const r = repo.data;
  const base = pipelinesPath(r);
  const rest = location.pathname.slice(base.length).split('/')[1] ?? '';
  const tab = rest === 'environments' || rest === 'settings' ? rest : 'runs';
  const Hafen = products.hafen.Logo;
  return (
    <RepoContext.Provider value={r}>
      <Group justify="space-between" mb="sm" wrap="nowrap" align="flex-start">
        <Stack gap={2}>
          <Text size="sm" c="dimmed">
            <Anchor component={Link} to="/builds" c="dimmed">
              {products.helling.name}
            </Anchor>
            {' / '}
            <Anchor component={Link} to={`/builds/${r.project.key.toLowerCase()}`} c="dimmed">
              {r.project.name}
            </Anchor>
          </Text>
          <Title order={2}>{r.name}</Title>
        </Stack>
        <Group gap="xs" wrap="nowrap">
          <BadgeButton repo={r} />
          <Button variant="default" component={Link} to={repoPath(r)} leftSection={<Hafen size={18} />}>
            {t('helling.openInHafen')}
          </Button>
        </Group>
      </Group>
      <Tabs value={tab} onChange={(v) => navigate(v === 'runs' ? base : `${base}/${v}`)} mb="md">
        <Tabs.List>
          <Tabs.Tab value="runs" leftSection={<IconPlayerPlay size={14} />}>
            {t('helling.runs')}
          </Tabs.Tab>
          <Tabs.Tab value="environments" leftSection={<IconRocket size={14} />}>
            {t('builds.environments')}
          </Tabs.Tab>
          {r.permission === 'admin' && (
            <Tabs.Tab value="settings" leftSection={<IconSettings size={14} />}>
              {t('helling.settings')}
            </Tabs.Tab>
          )}
        </Tabs.List>
      </Tabs>
      <Outlet />
    </RepoContext.Provider>
  );
}

/** The build settings of a repository: limits and variables. */
export function PipelineSettingsPage() {
  const repo = useRepo();
  return <PipelineSettings repo={repo} />;
}
