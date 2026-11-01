// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useQuery } from '@tanstack/react-query';
import { api, unwrap, type Schemas } from '../../api/client';
import type { Repository } from '../code/hooks';
import { repoParams } from '../code/hooks';

export type RunSummary = Schemas['RunSummary'];
export type RunDetail = Schemas['RunDetail'];
export type JobInfo = Schemas['JobInfo'];
export type StageInfo = Schemas['StageInfo'];
export type Environment = Schemas['Environment'];
export type Deployment = Schemas['Deployment'];
export type Variable = Schemas['Variable'];
export type CommitStatus = Schemas['CommitStatus'];
export type AgentInfo = Schemas['AgentInfo'];
export type AgentToken = Schemas['AgentToken'];
export type PipelineInfo = Schemas['PipelineInfo'];
export type BuildSettings = Schemas['BuildSettings'];

export type RunState = RunSummary['state'];
export type JobState = JobInfo['state'];

/** Live messages of type "run" refresh every query whose key starts with "run". */
export const runKey = (...rest: unknown[]) => ['run', ...rest];

export function useRuns(
  repo: Repository,
  filter: { branch?: string; state?: string; limit?: number; offset?: number },
) {
  return useQuery({
    queryKey: runKey(repo.id, 'list', filter),
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/runs', {
          params: {
            path: repoParams(repo),
            query: {
              branch: filter.branch,
              state: (filter.state || undefined) as RunState | undefined,
              limit: filter.limit ?? 50,
              offset: filter.offset ?? 0,
            },
          },
        }),
      ),
    placeholderData: (p) => p,
  });
}

export function useRun(repo: Repository, number: number) {
  return useQuery({
    queryKey: runKey(repo.id, 'detail', number),
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/runs/{number}', {
          params: { path: { ...repoParams(repo), number } },
        }),
      ),
    retry: false,
  });
}

export function useEnvironments(repo: Repository) {
  return useQuery({
    queryKey: runKey(repo.id, 'environments'),
    queryFn: () =>
      unwrap(api.GET('/api/v1/code/repos/{key}/{slug}/environments', { params: { path: repoParams(repo) } })),
  });
}

/** Commit statuses of the shown commits (one request for all). */
export function useStatuses(repo: Repository, shas: string[]) {
  const list = [...new Set(shas)].slice(0, 100).sort();
  return useQuery({
    queryKey: runKey(repo.id, 'statuses', list),
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/statuses', {
          params: { path: repoParams(repo), query: { sha: list } },
        }),
      ),
    enabled: list.length > 0,
    staleTime: 10_000,
  });
}

/** The combined state of the statuses of a commit: failure wins, then running, then success. */
export function combinedState(statuses: CommitStatus[] | undefined): CommitStatus['state'] | null {
  if (!statuses?.length) return null;
  const states = statuses.map((s) => s.state);
  for (const s of ['failure', 'error', 'running', 'pending', 'cancelled'] as const) {
    if (states.includes(s)) return s;
  }
  // Pipelines that their paths filters left out do not make a commit look built.
  return states.every((s) => s === 'skipped') ? 'skipped' : 'success';
}

export const runColor: Record<string, string> = {
  queued: 'gray',
  pending: 'gray',
  created: 'gray',
  waiting: 'yellow',
  running: 'blue',
  succeeded: 'green',
  success: 'green',
  failed: 'red',
  failure: 'red',
  error: 'red',
  cancelled: 'gray',
  skipped: 'gray',
};

/** Formats a duration in seconds as "1 h 4 min", "3 min 12 s" or "45 s". */
export function formatDuration(seconds: number) {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
  return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
}

/** Seconds a run or job took (or takes so far). */
export function elapsed(started?: string | null, finished?: string | null, now = Date.now()) {
  if (!started) return null;
  const end = finished ? new Date(finished).getTime() : now;
  return (end - new Date(started).getTime()) / 1000;
}

/** Path of a run page. */
export function runPath(
  repo: { projectKey: string; slug: string } | Pick<Repository, 'project' | 'slug'>,
  number: number,
) {
  return pipelinesPath(repo, 'runs', String(number));
}

/** Path of a repository's pages in Helling: /builds/<project>/<repo>/… */
export function pipelinesPath(
  repo: { projectKey: string; slug: string } | Pick<Repository, 'project' | 'slug'>,
  ...rest: string[]
) {
  const key = 'project' in repo ? repo.project.key : repo.projectKey;
  return ['/builds', key.toLowerCase(), repo.slug, ...rest].join('/');
}

/** A branch or tag without its refs/heads/ or refs/tags/ prefix. */
export function shortRef(ref: string) {
  return ref.replace(/^refs\/(heads|tags)\//, '');
}
