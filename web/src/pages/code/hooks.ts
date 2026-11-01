// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useQuery } from '@tanstack/react-query';
import { createContext, useContext } from 'react';
import { api, unwrap, type Schemas } from '../../api/client';
import type { paths } from '../../api/schema';

export type CodeProject = Schemas['CodeProject'];
export type Repository = Schemas['Repository'];
export type Branch = Schemas['Branch'];
export type Tag = Schemas['Tag'];
export type CommitInfo = Schemas['CommitInfo'];
export type Diff = Schemas['Diff'];
export type DiffFile = Schemas['GitdiffFile'];
export type PullRequest = Schemas['PullRequest'];
export type PRComment = Schemas['PRComment'];
export type Activity = Schemas['Activity'];
export type Grant = Schemas['CodeGrant'];
export type BranchPermission = Schemas['BranchPermission'];

/** The repository a page belongs to (set by RepoLayout). */
export const RepoContext = createContext<Repository | null>(null);

export function useRepo(): Repository {
  const r = useContext(RepoContext);
  if (!r) throw new Error('useRepo outside of RepoLayout');
  return r;
}

/** Path of a repository page: /code/<project>/<repo>/… */
export function repoPath(
  repo: Pick<Repository, 'project' | 'slug'> | { projectKey: string; slug: string },
  ...rest: string[]
) {
  const key = 'project' in repo ? repo.project.key : repo.projectKey;
  return ['/code', key.toLowerCase(), repo.slug, ...rest].filter(Boolean).join('/');
}

/** Encodes a path inside the repository for the address (keeping slashes). */
export function encodePath(path: string) {
  return path
    .split('/')
    .filter(Boolean)
    .map((p) => encodeURIComponent(p))
    .join('/');
}

export function srcPath(repo: Repository, ref: string | undefined, path: string) {
  const at = ref && ref !== repo.defaultBranch ? `?at=${encodeURIComponent(ref)}` : '';
  return repoPath(repo, 'src', encodePath(path)) + at;
}

export function rawUrl(repo: Repository, ref: string, path: string, download = false) {
  const q = new URLSearchParams({ ref, path });
  if (download) q.set('download', '1');
  return `/api/v1/code/repos/${repo.project.key}/${repo.slug}/raw?${q}`;
}

export function shortSha(sha: string) {
  return sha.slice(0, 10);
}

export function repoParams(repo: Pick<Repository, 'project' | 'slug'>) {
  return { key: repo.project.key, slug: repo.slug };
}

export function useRepository(key: string | undefined, slug: string | undefined) {
  return useQuery({
    queryKey: ['repository', 'path', key?.toUpperCase(), slug],
    queryFn: () =>
      unwrap(api.GET('/api/v1/code/repos/{key}/{slug}', { params: { path: { key: key!, slug: slug! } } })),
    enabled: !!key && !!slug,
    retry: false,
  });
}

export type RepositoryQuery = NonNullable<paths['/api/v1/code/repos']['get']['parameters']['query']>;

/** A page of the repositories the user can read, with their total; the server filters and orders them. */
export function useRepositoryPage(query: RepositoryQuery) {
  return useQuery({
    queryKey: ['repository', 'list', query],
    queryFn: () => unwrap(api.GET('/api/v1/code/repos', { params: { query } })),
    placeholderData: (p) => p,
  });
}

/** A page of the branches whose names contain filter (one named like it first), with their total. */
export function useBranches(repo: Repository, filter = '', limit = 100, offset = 0) {
  return useQuery({
    queryKey: ['repository', repo.id, 'branches', filter, limit, offset],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/branches', {
          params: { path: repoParams(repo), query: { filter, limit, offset } },
        }),
      ),
    placeholderData: (p) => p,
  });
}

/** A page of the tags whose names contain filter (one named like it first), with their total. */
export function useTags(repo: Repository, filter = '', limit = 100, offset = 0, enabled = true) {
  return useQuery({
    queryKey: ['repository', repo.id, 'tags', filter, limit, offset],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/tags', {
          params: { path: repoParams(repo), query: { filter, limit, offset } },
        }),
      ),
    placeholderData: (p) => p,
    enabled,
  });
}

/** A label for a person of a commit: the Kogge user's name if known. */
export function personName(p: Schemas['Person']) {
  return p.user?.displayName ?? p.name;
}
