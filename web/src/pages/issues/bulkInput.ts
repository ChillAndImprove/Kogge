// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { Schemas } from '../../api/client';

/** The actions of bulk changes. */
export const bulkActions = ['edit', 'transition', 'move', 'watch', 'unwatch', 'delete'] as const;
export type BulkAction = (typeof bulkActions)[number];

/** Fields whose values are added to, removed from or replace those of the issues. */
export const modeFields = ['labels', 'components', 'fixVersions', 'affectsVersions'] as const;
export type Mode = 'add' | 'remove' | 'replace';

/** What the user chose to change in a bulk edit. */
export interface EditState {
  on: Record<string, boolean>;
  values: Record<string, unknown>;
  modes: Record<string, Mode>;
}

export const emptyEdit: EditState = { on: {}, values: {}, modes: {} };

/** The edit of a bulk change as the API takes it; undefined while nothing is chosen. */
export function bulkEditInput(st: EditState): Schemas['BulkEditInput'] | undefined {
  const out: Schemas['BulkEditInput'] = {};
  const fields: Record<string, unknown> = {};
  let any = false;
  for (const [key, on] of Object.entries(st.on)) {
    if (!on) continue;
    any = true;
    const value = st.values[key] ?? null;
    const mode = st.modes[key] ?? 'add';
    switch (key) {
      case 'labels':
        out.labels = { mode, values: Array.isArray(value) ? (value as string[]) : [] };
        break;
      case 'components':
      case 'fixVersions':
      case 'affectsVersions':
        out[key] = { mode, ids: Array.isArray(value) ? (value as number[]) : [] };
        break;
      default:
        fields[key] = value;
    }
  }
  if (!any) return undefined;
  if (Object.keys(fields).length) out.fields = fields as Record<string, never>;
  return out;
}

/** The issues a bulk change is for: their keys, or a query (whose first 1000 results they are). */
export interface BulkTarget {
  issues?: string[];
  query?: string;
  /** How many issues that is (for the query: how many it finds, at most 1000 are changed). */
  count: number;
}

/** The selection part of the API's bulk requests. */
export function selectionOf(target: BulkTarget): { issues?: string[]; query?: string } {
  return target.query !== undefined ? { query: target.query } : { issues: target.issues ?? [] };
}
