// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap, type Schemas } from '../../api/client';
import { notifyError } from '../../components/notify';

export type Board = Schemas['Board'];
export type BoardConfig = Schemas['BoardConfig'];
export type Card = Schemas['Card'];
export type Sprint = Schemas['Sprint'];
export type BacklogView = Schemas['BacklogView'];
export type BoardView = Schemas['BoardView'];

/**
 * The boards of a project, for its navigation: a project has a few boards, and a page holds up to 500 (the
 * project's board page lists them all, page by page).
 */
export function useBoards(projectKey: string | undefined) {
  return useQuery({
    queryKey: ['board', 'list', projectKey],
    queryFn: () =>
      unwrap(api.GET('/api/v1/boards', { params: { query: { project: projectKey, limit: 500 } } })),
    enabled: !!projectKey,
  });
}

export function useBoard(id: number) {
  return useQuery({
    queryKey: ['board', id],
    queryFn: () => unwrap(api.GET('/api/v1/boards/{id}', { params: { path: { id } } })),
  });
}

export function useBoardView(id: number, quickFilters: number[], sprint?: number) {
  return useQuery({
    queryKey: ['board', id, 'view', quickFilters, sprint ?? 0],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/boards/{id}/view', {
          params: {
            path: { id },
            query: { quickFilter: quickFilters.length ? quickFilters : undefined, sprint },
          },
        }),
      ),
    placeholderData: (prev) => prev,
  });
}

export function useBacklog(id: number, quickFilters: number[], epic: string | null) {
  return useQuery({
    queryKey: ['board', id, 'backlog', quickFilters, epic],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/boards/{id}/backlog', {
          params: {
            path: { id },
            query: { quickFilter: quickFilters.length ? quickFilters : undefined, epic: epic ?? undefined },
          },
        }),
      ),
    placeholderData: (prev) => prev,
  });
}

export function useSprints(boardId: number, state?: 'future' | 'active' | 'closed', enabled = true) {
  return useQuery({
    queryKey: ['board', boardId, 'sprints', state ?? 'all'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/boards/{id}/sprints', { params: { path: { id: boardId }, query: { state } } })),
    enabled,
  });
}

/** Moves issues into a sprint (or the backlog with sprint 0) and ranks them next to an anchor. */
export function useMoveIssues(boardId: number) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (v: { sprint: number; issues: string[]; before?: string; after?: string }) =>
      v.sprint
        ? unwrap(
            api.POST('/api/v1/sprints/{id}/issues', {
              params: { path: { id: v.sprint } },
              body: { issues: v.issues, before: v.before, after: v.after },
            }),
          )
        : unwrap(
            api.POST('/api/v1/backlog/issues', {
              body: { issues: v.issues, before: v.before, after: v.after },
            }),
          ),
    onSettled: () => client.invalidateQueries({ queryKey: ['board', boardId] }),
    onError: notifyError,
  });
}

export function useRank(boardId: number) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (v: { issues: string[]; before?: string; after?: string }) =>
      unwrap(api.PUT('/api/v1/issues/rank', { body: v })),
    onSettled: () => client.invalidateQueries({ queryKey: ['board', boardId] }),
    onError: notifyError,
  });
}

const boardKey = (projectKey: string) => `kogge.board.${projectKey}`;

/** The board last used in a project (or its first board). */
export function useCurrentBoard(projectKey: string | undefined) {
  const boards = useBoards(projectKey);
  let remembered: string | null = null;
  try {
    remembered = projectKey ? localStorage.getItem(boardKey(projectKey)) : null;
  } catch {
    remembered = null;
  }
  const list = boards.data?.items ?? [];
  const board = list.find((b) => String(b.id) === remembered) ?? list[0];
  return { boards, board };
}

export function rememberBoard(projectKey: string, id: number) {
  try {
    localStorage.setItem(boardKey(projectKey), String(id));
  } catch {
    // storage unavailable — only a convenience
  }
}

/** Formats an estimate of the board's statistic. */
export function formatEstimate(value: number | undefined | null, statistic: Schemas['Estimation']): string {
  if (value == null) return '';
  if (statistic.type === 'originalEstimate') {
    const h = value / 3600;
    return h >= 1 ? `${Math.round(h * 10) / 10}h` : `${Math.round(value / 60)}m`;
  }
  return String(Math.round(value * 100) / 100);
}

export const epicColors = ['grape', 'violet', 'indigo', 'cyan', 'teal', 'lime', 'orange', 'pink'];
export const epicColor = (id: number) => epicColors[id % epicColors.length];
