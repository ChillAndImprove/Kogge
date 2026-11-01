// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Card, Group, Select, Stack, Text, TextInput } from '@mantine/core';
import { IconFilePlus, IconGitBranch, IconPlayerPlay } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Pagination } from '../../components/Pagination';
import { ErrorAlert } from '../../components/ErrorAlert';
import { useRepo } from '../code/hooks';
import { RunPipelineModal, RunTable } from './components';
import { useRuns } from './hooks';
import { SetUpPipelines } from './Templates';

const pageSize = 30;

/** The pipeline runs of a repository. */
export function Runs() {
  const { t } = useTranslation();
  const repo = useRepo();
  const [branch, setBranch] = useState('');
  const [state, setState] = useState<string | null>(null);
  const [page, setPage] = useState(1);
  const [running, setRunning] = useState(false);
  const [creating, setCreating] = useState(false);
  const runs = useRuns(repo, {
    branch: branch.trim() || undefined,
    state: state ?? undefined,
    limit: pageSize,
    offset: (page - 1) * pageSize,
  });
  const canRun = repo.permission === 'write' || repo.permission === 'admin';
  return (
    <Stack>
      <Group justify="space-between">
        <Group>
          <TextInput
            leftSection={<IconGitBranch size={14} />}
            placeholder={t('builds.branch')}
            aria-label={t('builds.branch')}
            value={branch}
            onChange={(e) => {
              setBranch(e.currentTarget.value);
              setPage(1);
            }}
            w={220}
          />
          <Select
            placeholder={t('builds.allStates')}
            aria-label={t('builds.stateFilter')}
            clearable
            value={state}
            onChange={(v) => {
              setState(v);
              setPage(1);
            }}
            data={(['queued', 'running', 'succeeded', 'failed', 'cancelled', 'error'] as const).map((s) => ({
              value: s,
              label: t(`builds.state.${s}`),
            }))}
            w={180}
          />
        </Group>
        {canRun && !repo.empty && !repo.archived && (
          <Button leftSection={<IconPlayerPlay size={14} />} onClick={() => setRunning(true)}>
            {t('builds.runPipeline')}
          </Button>
        )}
      </Group>
      <ErrorAlert error={runs.error} />
      <Card withBorder padding="xs">
        <RunTable runs={runs.data?.items ?? []} />
      </Card>
      {(runs.data?.total ?? 0) > pageSize && (
        <Group justify="center">
          <Pagination total={Math.ceil((runs.data?.total ?? 0) / pageSize)} value={page} onChange={setPage} />
        </Group>
      )}
      {runs.data && runs.data.total === 0 && !branch && !state && (
        <Stack align="center" gap="xs">
          <Text c="dimmed" size="sm" ta="center">
            {t('builds.noPipelineFileHelp')}
          </Text>
          {canRun && !repo.archived && (
            <Button
              variant="light"
              leftSection={<IconFilePlus size={14} />}
              onClick={() => setCreating(true)}
            >
              {t('builds.setUpPipelines')}
            </Button>
          )}
        </Stack>
      )}
      {creating && <SetUpPipelines repo={repo} onClose={() => setCreating(false)} />}
      {running && <RunPipelineModal repo={repo} opened={running} onClose={() => setRunning(false)} />}
    </Stack>
  );
}
