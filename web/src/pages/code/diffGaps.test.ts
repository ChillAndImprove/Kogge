// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { diffGaps, gapLines } from './diffGaps';
import type { DiffLine } from './diffRows';

/** A hunk whose change is followed by so many lines that stayed (three: the file may go on). */
const hunk = (oldStart: number, oldLines: number, newStart: number, newLines: number, after = 3) => ({
  oldStart,
  oldLines,
  newStart,
  newLines,
  header: '',
  lines: [
    { kind: 'add', new: newStart, text: '' },
    ...Array.from({ length: after }, (): DiffLine => ({
      kind: 'context',
      old: oldStart,
      new: newStart,
      text: '',
    })),
  ] as DiffLine[],
});

describe('diffGaps', () => {
  it('finds the lines before, between and after the hunks with their old numbers', () => {
    // Lines 1–9 stayed; 10–12 became 10–14; 13–29 stayed as 15–31; then a change at 30 (new: 32).
    expect(diffGaps([hunk(10, 3, 10, 5), hunk(30, 4, 32, 4)])).toEqual([
      { start: 1, end: 10, offset: 0 },
      { start: 15, end: 32, offset: -2 },
      { start: 36, offset: -2 },
    ]);
  });

  it('places ranges of no lines after their line', () => {
    // Lines 5 and 6 removed (new: nothing after line 4); then two lines added after old line 20.
    expect(diffGaps([hunk(5, 2, 4, 0), hunk(20, 0, 19, 2)])).toEqual([
      { start: 1, end: 5, offset: 0 },
      { start: 5, end: 19, offset: 2 },
      { start: 21, offset: 0 },
    ]);
  });

  it('knows that the file ends with a hunk that has fewer lines after its change than diffs show', () => {
    expect(diffGaps([hunk(10, 3, 10, 5, 1)])[1]).toEqual({ start: 15, end: 15, offset: -2 });
    const noNewline = hunk(10, 3, 10, 5);
    noNewline.lines[noNewline.lines.length - 1].noNewline = true;
    expect(diffGaps([noNewline])[1]).toEqual({ start: 15, end: 15, offset: -2 });
  });

  it('has no gap before a hunk at the top', () => {
    expect(diffGaps([hunk(1, 4, 1, 5)])[0]).toEqual({ start: 1, end: 1, offset: 0 });
  });
});

describe('gapLines', () => {
  const gap = { start: 15, end: 32, offset: -2 };

  it('shows lines from the beginning and from the end of a gap', () => {
    expect(gapLines(gap, { top: 2, bottom: 3 })).toEqual({ top: [15, 16], bottom: [29, 30, 31], hidden: 12 });
  });

  it('shows a gap once and whole when more is asked than it has', () => {
    expect(gapLines(gap, { top: 20, bottom: 20 })).toEqual({
      top: Array.from({ length: 17 }, (_, i) => 15 + i),
      bottom: [],
      hidden: 0,
    });
  });

  it('knows the size of the gap after the last hunk from the file', () => {
    const last = { start: 36, offset: -2 };
    expect(gapLines(last, { top: 0, bottom: 0 })).toBeUndefined();
    expect(gapLines(last, { top: 3, bottom: 0 }, 40)).toEqual({ top: [36, 37, 38], bottom: [], hidden: 2 });
    expect(gapLines(last, { top: 0, bottom: 0 }, 35)).toEqual({ top: [], bottom: [], hidden: 0 });
  });

  it('shows nothing of a gap the loaded lines do not reach', () => {
    expect(gapLines(gap, { top: 2, bottom: 0 }, 20)).toBeUndefined();
  });
});
