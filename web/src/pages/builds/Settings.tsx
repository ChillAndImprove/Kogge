// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Checkbox, Divider, Group, NumberInput, Stack, Text } from '@mantine/core';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifySuccess } from '../../components/notify';
import { repoParams, type Repository } from '../code/hooks';
import { runKey, type BuildSettings } from './hooks';
import { PipelineTriggers } from './Triggers';
import { VariablesEditor } from './Variables';

/** Pipeline settings of a repository: run limits and the repository's variables and secrets. */
export function PipelineSettings({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const settings = useQuery({
    queryKey: runKey(repo.id, 'settings'),
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/build-settings', { params: { path: repoParams(repo) } }),
      ),
  });
  return (
    <Stack maw={720}>
      <ErrorAlert error={settings.error} />
      {settings.data && <LimitsForm repo={repo} initial={settings.data} />}
      <Divider label={t('builds.variables')} labelPosition="left" mt="md" />
      <Text size="sm" c="dimmed">
        {t('builds.variablesHelp')}
      </Text>
      <VariablesEditor scope={{ scope: 'repository', project: repo.project.key, repository: repo.slug }} />
      <Divider label={t('builds.triggers')} labelPosition="left" mt="md" />
      <PipelineTriggers repo={repo} />
    </Stack>
  );
}

function LimitsForm({ repo, initial }: { repo: Repository; initial: BuildSettings }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [autoCancel, setAutoCancel] = useState(initial.autoCancel);
  const [maxParallel, setMaxParallel] = useState(initial.maxParallelJobs);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/code/repos/{key}/{slug}/build-settings', {
          params: { path: repoParams(repo) },
          body: { autoCancel, maxParallelJobs: maxParallel },
        }),
      ),
    onSuccess: () => {
      notifySuccess(t('common.saved'));
      void client.invalidateQueries({ queryKey: runKey(repo.id, 'settings') });
    },
  });
  return (
    <>
      <Checkbox
        label={t('builds.autoCancel')}
        description={t('builds.autoCancelHelp')}
        checked={autoCancel}
        onChange={(e) => setAutoCancel(e.currentTarget.checked)}
      />
      <NumberInput
        label={t('builds.maxParallelJobs')}
        description={t('builds.maxParallelJobsHelp')}
        min={0}
        max={1000}
        value={maxParallel}
        onChange={(v) => setMaxParallel(Number(v) || 0)}
        w={260}
      />
      <ErrorAlert error={save.error} />
      <Group>
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          {t('common.save')}
        </Button>
      </Group>
    </>
  );
}

/** The variables and secrets of a code project, for all its repositories. */
export function ProjectVariables({ projectKey }: { projectKey: string }) {
  const { t } = useTranslation();
  return (
    <Stack maw={720}>
      <Text size="sm" c="dimmed">
        {t('builds.projectVariablesHelp')}
      </Text>
      <VariablesEditor scope={{ scope: 'project', project: projectKey }} />
    </Stack>
  );
}
