// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { DiffFile } from './hooks';

type Hunk = DiffFile['hunks'][number];

/** Lines of a file that stayed and that its diff leaves out: before a hunk, or after the last one. */
export interface Gap {
  /** Its first line in the new version. */
  start: number;
  /** The line of the new version after its last; after the last hunk, the file's end decides. */
  end?: number;
  /** What to add to the number of one of its lines in the new version for the number in the old one. */
  offset: number;
}

/** Lines that stayed which diffs show after a change, where the file has them: fewer mean that the file ends. */
const context = 3;

/**
 * The gaps of a diff: one before each hunk and one after the last. A range of no lines (`-5,0`) stands for the
 * place after its line. After the last hunk, the gap is known to be empty where the file ends with the hunk.
 */
export function diffGaps(hunks: Hunk[]): Gap[] {
  const gaps: Gap[] = [];
  let start = 1;
  let offset = 0;
  for (const h of hunks) {
    gaps.push({ start, end: h.newLines > 0 ? h.newStart : h.newStart + 1, offset });
    start = h.newStart + Math.max(h.newLines, 1);
    offset = h.oldStart + Math.max(h.oldLines, 1) - start;
  }
  const lines = hunks[hunks.length - 1]?.lines ?? [];
  let after = 0;
  while (after < lines.length && lines[lines.length - 1 - after].kind === 'context') after++;
  const ends = lines.length > 0 && (after < context || !!lines[lines.length - 1].noNewline);
  gaps.push(ends ? { start, end: start, offset } : { start, offset });
  return gaps;
}

/** Lines shown of a gap: so many from its beginning (below the hunk above) and so many from its end. */
export interface Shown {
  top: number;
  bottom: number;
}

/**
 * The lines of a gap to show, as numbers of the new version: those from its beginning, then those from its end, and
 * how many stay hidden between them. `length` is the number of lines of the new version once it is loaded. Nothing
 * when the size of the gap is not known yet, or when the loaded lines do not reach its end (a file too large to load
 * whole).
 */
export function gapLines(gap: Gap, shown: Shown, length?: number) {
  const end = gap.end ?? (length === undefined ? undefined : length + 1);
  if (end === undefined || (length !== undefined && end > length + 1)) return undefined;
  const size = Math.max(0, end - gap.start);
  const top = Math.min(shown.top, size);
  const bottom = Math.min(shown.bottom, size - top);
  const range = (from: number, n: number) => Array.from({ length: n }, (_, i) => from + i);
  return { top: range(gap.start, top), bottom: range(end - bottom, bottom), hidden: size - top - bottom };
}
