// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { lineAnchor, splitRows, type DiffLine } from './diffRows';

const ctx = (old: number, nu: number, text = `c${old}`): DiffLine => ({
  kind: 'context',
  old,
  new: nu,
  text,
});
const del = (old: number, text = `d${old}`): DiffLine => ({ kind: 'delete', old, text });
const add = (nu: number, text = `a${nu}`): DiffLine => ({ kind: 'add', new: nu, text });

/** The rows as "left|right" texts, "-" for an empty side. */
const shown = (lines: DiffLine[]) =>
  splitRows(lines).map((r) => `${r.left?.text ?? '-'}|${r.right?.text ?? '-'}`);

describe('splitRows', () => {
  it('puts lines that stayed on both sides', () => {
    expect(shown([ctx(1, 1), ctx(2, 2)])).toEqual(['c1|c1', 'c2|c2']);
  });

  it('pairs removed and added lines of a change in their order', () => {
    expect(shown([ctx(1, 1), del(2), del(3), add(2), add(3), ctx(4, 4)])).toEqual([
      'c1|c1',
      'd2|a2',
      'd3|a3',
      'c4|c4',
    ]);
  });

  it('lets the longer side of a change go on alone', () => {
    expect(shown([del(1), del(2), del(3), add(1)])).toEqual(['d1|a1', 'd2|-', 'd3|-']);
    expect(shown([del(1), add(1), add(2), add(3), ctx(2, 4)])).toEqual(['d1|a1', '-|a2', '-|a3', 'c2|c2']);
  });

  it('shows lines only added or only removed on their side', () => {
    expect(shown([ctx(1, 1), add(2), add(3)])).toEqual(['c1|c1', '-|a2', '-|a3']);
    expect(shown([del(1), ctx(2, 1)])).toEqual(['d1|-', 'c2|c2']);
  });

  it('starts a new change when removed lines follow added ones', () => {
    expect(shown([del(1), add(1), del(2), add(2)])).toEqual(['d1|a1', 'd2|a2']);
    expect(shown([add(1), del(1), add(2)])).toEqual(['-|a1', 'd1|a2']);
  });

  it('keeps every line once on each side', () => {
    const lines = [ctx(1, 1), del(2), add(2), add(3), ctx(3, 4), del(4), del(5), ctx(6, 5)];
    const rows = splitRows(lines);
    const left = rows.flatMap((r) => (r.left ? [r.left] : []));
    const right = rows.flatMap((r) => (r.right ? [r.right] : []));
    expect(left.map((l) => l.old)).toEqual([1, 2, 3, 4, 5, 6]);
    expect(right.map((l) => l.new)).toEqual([1, 2, 3, 4, 5]);
  });

  it('has no rows for no lines', () => {
    expect(splitRows([])).toEqual([]);
  });
});

describe('lineAnchor', () => {
  it('anchors comments on removed lines on the old side, others on the new one', () => {
    expect(lineAnchor(del(7))).toEqual({ side: 'old', line: 7 });
    expect(lineAnchor(add(9))).toEqual({ side: 'new', line: 9 });
    expect(lineAnchor(ctx(3, 5))).toEqual({ side: 'new', line: 5 });
  });
});
