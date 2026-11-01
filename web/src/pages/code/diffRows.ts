// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { DiffFile } from './hooks';

export type DiffLine = DiffFile['hunks'][number]['lines'][number];

/** A row of a side-by-side diff: a line of the old version (left) and of the new one (right). */
export interface SplitRow {
  left?: DiffLine;
  right?: DiffLine;
}

/**
 * Pairs the lines of a hunk for a side-by-side view: lines that stayed stand on both sides; in a change, the removed
 * lines stand on the left next to the added lines on the right, in their order, and the longer side goes on alone.
 */
export function splitRows(lines: DiffLine[]): SplitRow[] {
  const rows: SplitRow[] = [];
  let removed: DiffLine[] = [];
  let added: DiffLine[] = [];
  const change = () => {
    for (let i = 0; i < Math.max(removed.length, added.length); i++) {
      rows.push({ left: removed[i], right: added[i] });
    }
    removed = [];
    added = [];
  };
  for (const l of lines) {
    if (l.kind === 'delete') {
      // Removed lines after added ones begin the next change.
      if (added.length > 0) change();
      removed.push(l);
    } else if (l.kind === 'add') {
      added.push(l);
    } else {
      change();
      rows.push({ left: l, right: l });
    }
  }
  change();
  return rows;
}

/** Where comments on a line go: removed lines are commented on the old side, the others on the new one. */
export function lineAnchor(l: DiffLine): { side: 'old' | 'new'; line: number } {
  return l.kind === 'delete' ? { side: 'old', line: l.old ?? 0 } : { side: 'new', line: l.new ?? 0 };
}
