// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Badge, Card, Group, Text, Tooltip, UnstyledButton, VisuallyHidden } from '@mantine/core';
import { IconPlayerPlay, IconRotateClockwise } from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import type { TFunction } from 'i18next';
import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { formatBytes, formatNumber } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { useRepo } from '../code/hooks';
import { Duration, StateIcon, WaitingLabel } from './components';
import { runKey, type JobInfo } from './hooks';

/** CPUs and memory that one container asks for or may have, for people: "2 CPUs, 4 GB". */
export function resourcesText(t: TFunction, r: { milliCpus?: number; memory?: number }) {
  const parts: string[] = [];
  if (r.milliCpus)
    parts.push(t('builds.cpus', { count: r.milliCpus / 1000, n: formatNumber(r.milliCpus / 1000) }));
  if (r.memory) parts.push(formatBytes(r.memory));
  return parts.join(', ');
}

/** Why a job was skipped, for people. */
export function skipText(t: TFunction, skip: NonNullable<JobInfo['skip']>) {
  return t(`builds.skip.${skip.reason}` as 'builds.skip.condition', { detail: skip.detail ?? '' });
}

/** A job of a run: its state, name and duration, with a button to start or retry it. */
export function JobCard({
  job,
  label,
  caption,
  description,
  selected,
  canRun,
  onSelect,
}: {
  job: JobInfo;
  label?: string;
  /** A short line under the name (the stage in the graph). */
  caption?: string;
  /** What screen readers hear about the job besides its name (what it waits for). */
  description?: string;
  selected: boolean;
  canRun: boolean;
  onSelect: () => void;
}) {
  const { t } = useTranslation();
  const repo = useRepo();
  const client = useQueryClient();
  const describedBy = useId();
  const action = useMutation({
    mutationFn: (kind: 'play' | 'retry') =>
      unwrap(
        kind === 'play'
          ? api.POST('/api/v1/builds/jobs/{id}/play', { params: { path: { id: job.id } } })
          : api.POST('/api/v1/builds/jobs/{id}/retry', { params: { path: { id: job.id } } }),
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: runKey(repo.id) }),
  });
  const playable = job.state === 'waiting' && (job.waitingFor === 'manual' || job.waitingFor === 'deployer');
  const retryable = ['succeeded', 'failed', 'cancelled'].includes(job.state);
  return (
    <Card withBorder padding="xs" className="kogge-job" data-selected={selected} onClick={onSelect}>
      <Group justify="space-between" wrap="nowrap" gap={6}>
        <Group gap={6} wrap="nowrap" style={{ minWidth: 0 }}>
          <StateIcon state={job.state} size={16} />
          <UnstyledButton
            onClick={onSelect}
            aria-pressed={selected}
            aria-label={label ? job.name : undefined}
            aria-describedby={description ? describedBy : undefined}
            style={{ minWidth: 0 }}
          >
            <Text size="sm" fw={500} truncate title={job.name}>
              {label ?? job.name}
            </Text>
          </UnstyledButton>
          {description && <VisuallyHidden id={describedBy}>{description}</VisuallyHidden>}
        </Group>
        {canRun && (playable || retryable) && (
          <Tooltip label={playable ? t('builds.play') : t('builds.retry')}>
            <ActionIcon
              size="sm"
              variant="subtle"
              loading={action.isPending}
              aria-label={playable ? t('builds.play') : t('builds.retry')}
              onClick={(e) => {
                e.stopPropagation();
                action.mutate(playable ? 'play' : 'retry');
              }}
            >
              {playable ? <IconPlayerPlay size={14} /> : <IconRotateClockwise size={14} />}
            </ActionIcon>
          </Tooltip>
        )}
      </Group>
      <Group gap={6} mt={2} wrap="nowrap" style={{ minWidth: 0 }}>
        {caption && (
          <Text size="xs" c="dimmed" truncate style={{ flexShrink: 1 }}>
            {caption}
          </Text>
        )}
        {job.environment && (
          <Badge size="xs" variant="light" color="teal" tt="none">
            {job.environment}
          </Badge>
        )}
        <Text size="xs" c="dimmed" style={{ flexShrink: 0 }}>
          <Duration started={job.started} finished={job.finished} />
        </Text>
        {job.state === 'waiting' && <WaitingLabel waitingFor={job.waitingFor} />}
        {job.state === 'skipped' && job.skip && (
          <Text size="xs" c="dimmed" truncate title={skipText(t, job.skip)}>
            {t(`builds.skipShort.${job.skip.reason}` as 'builds.skipShort.condition')}
          </Text>
        )}
      </Group>
      {action.error && <ErrorAlert error={action.error} />}
    </Card>
  );
}
