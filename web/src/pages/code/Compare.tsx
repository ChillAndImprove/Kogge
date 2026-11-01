// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Group, Stack, Tabs, Text } from '@mantine/core';
import { IconGitPullRequest } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { CommitList, RefSelect } from './components';
import { DiffView } from './DiffView';
import { repoParams, repoPath, useRepo } from './hooks';

/** Compares two refs: the commits and changes of "to" since it left "from". */
export function Compare() {
  const { t } = useTranslation();
  const repo = useRepo();
  const navigate = useNavigate();
  const [search, setSearch] = useSearchParams();
  const from = search.get('from') ?? repo.defaultBranch;
  const to = search.get('to') ?? '';
  const [tab, setTab] = useState<string | null>('diff');
  const compare = useQuery({
    queryKey: ['repository', repo.id, 'compare', from, to],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/compare', {
          params: { path: repoParams(repo), query: { from, to } },
        }),
      ),
    enabled: !!to && from !== to,
    retry: false,
  });
  return (
    <Stack>
      <Group align="flex-end" justify="space-between">
        <Group align="flex-end">
          <RefSelect
            repo={repo}
            value={to || '…'}
            onChange={(v) => setSearch({ from, to: v })}
            label={t('code.from')}
          />
          <Text pb={6}>→</Text>
          <RefSelect
            repo={repo}
            value={from}
            onChange={(v) => setSearch({ from: v, to })}
            label={t('code.to')}
          />
        </Group>
        {to && (
          <Button
            leftSection={<IconGitPullRequest size={16} />}
            onClick={() =>
              navigate(
                `${repoPath(repo, 'pull-requests', 'new')}?source=${encodeURIComponent(to)}&target=${encodeURIComponent(from)}`,
              )
            }
          >
            {t('code.openPullRequest')}
          </Button>
        )}
      </Group>
      <ErrorAlert error={compare.error} />
      {compare.data && (
        <Tabs value={tab} onChange={setTab}>
          <Tabs.List mb="md">
            <Tabs.Tab value="diff">{t('code.pr.diff')}</Tabs.Tab>
            <Tabs.Tab value="commits">
              {t('code.commits')} ({compare.data.commits.length}
              {compare.data.more ? '+' : ''})
            </Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="diff">
            {compare.data.diff && <DiffView repo={repo} diff={compare.data.diff} viewRef={to} />}
          </Tabs.Panel>
          <Tabs.Panel value="commits">
            <CommitList repo={repo} commits={compare.data.commits} />
            {compare.data.more && (
              <Text size="sm" c="dimmed" mt="xs">
                {t('code.newestCommitsOnly', { shown: compare.data.commits.length })}
              </Text>
            )}
          </Tabs.Panel>
        </Tabs>
      )}
    </Stack>
  );
}
