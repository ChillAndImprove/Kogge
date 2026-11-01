// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Badge, Loader, ThemeIcon, Tooltip } from '@mantine/core';
import {
  IconCircleCheck,
  IconCircleDashed,
  IconCircleMinus,
  IconCircleX,
  IconClockPause,
  IconPlayerSkipForward,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { combinedState, runColor, type CommitStatus } from './hooks';

// States of runs, jobs and commit statuses; also used by the code pages.

/** An icon for the state of a run, job or commit status. */
export function StateIcon({ state, size = 18 }: { state: string; size?: number }) {
  const { t } = useTranslation();
  const icon = {
    succeeded: IconCircleCheck,
    success: IconCircleCheck,
    failed: IconCircleX,
    failure: IconCircleX,
    error: IconCircleX,
    cancelled: IconCircleMinus,
    skipped: IconPlayerSkipForward,
    waiting: IconClockPause,
    created: IconCircleDashed,
    queued: IconCircleDashed,
    pending: IconCircleDashed,
  }[state];
  const label = t(`builds.state.${state}` as 'builds.state.queued', { defaultValue: state });
  if (state === 'running') {
    return (
      <Tooltip label={label}>
        <Loader size={size - 4} color="blue" role="img" aria-label={label} />
      </Tooltip>
    );
  }
  const Icon = icon ?? IconCircleDashed;
  return (
    <Tooltip label={label}>
      <ThemeIcon
        variant="transparent"
        color={runColor[state] ?? 'gray'}
        size={size}
        role="img"
        aria-label={label}
      >
        <Icon size={size} />
      </ThemeIcon>
    </Tooltip>
  );
}

export function StateBadge({ state }: { state: string }) {
  const { t } = useTranslation();
  return (
    <Badge color={runColor[state] ?? 'gray'} variant="light">
      {t(`builds.state.${state}` as 'builds.state.queued', { defaultValue: state })}
    </Badge>
  );
}

/** The combined commit status, linking to its run. */
export function CommitStatusIcon({ statuses }: { statuses: CommitStatus[] | undefined }) {
  const state = combinedState(statuses);
  if (!state || !statuses) return null;
  const run = statuses.find((s) => s.url && s.context.startsWith('kogge/')) ?? statuses.find((s) => s.url);
  const icon = <StateIcon state={state} size={16} />;
  if (!run?.url) return icon;
  const url = new URL(run.url, window.location.origin);
  return url.origin === window.location.origin ? (
    <Anchor component={Link} to={url.pathname} aria-label={run.context}>
      {icon}
    </Anchor>
  ) : (
    <Anchor href={run.url} target="_blank" rel="noreferrer" aria-label={run.context}>
      {icon}
    </Anchor>
  );
}
