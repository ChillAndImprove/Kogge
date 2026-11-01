// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import type { DiffLine } from './diffRows';
import { changedRanges, markHtml, wordChanges } from './wordDiff';

/** The marked parts of a text. */
const parts = (text: string, ranges: [number, number][]) => ranges.map(([a, b]) => text.slice(a, b));

describe('changedRanges', () => {
  it('marks the words that changed on each side', () => {
    const a = 'const VAT = 16';
    const b = 'const VAT = 19';
    const [ra, rb] = changedRanges(a, b)!;
    expect(parts(a, ra)).toEqual(['16']);
    expect(parts(b, rb)).toEqual(['19']);
  });

  it('marks several changes of a line, joining neighbouring tokens', () => {
    const a = 'return rate(a, b) * 100;';
    const b = 'return rates(a, c) * 100;';
    const [ra, rb] = changedRanges(a, b)!;
    expect(parts(a, ra)).toEqual(['rate', 'b']);
    expect(parts(b, rb)).toEqual(['rates', 'c']);
  });

  it('marks only the added side when words were inserted', () => {
    const a = 'if (x) {';
    const b = 'if (x && y) {';
    const [ra, rb] = changedRanges(a, b)!;
    expect(ra).toEqual([]);
    expect(parts(b, rb)).toEqual([' && y']);
  });

  it('marks changed indentation', () => {
    const [ra, rb] = changedRanges('  foo();', '    foo();')!;
    expect(parts('  foo();', ra)).toEqual(['  ']);
    expect(parts('    foo();', rb)).toEqual(['    ']);
  });

  it('leaves rewritten lines without marks', () => {
    expect(changedRanges('const VAT = 16', 'func main() { run() }')).toBeUndefined();
    expect(changedRanges('', '')).toBeUndefined();
  });

  it('compares long lines by their common beginning and end', () => {
    const line = (changed: Record<number, string>) =>
      `x(${Array.from({ length: 400 }, (_, i) => changed[i] ?? `a${i}`).join(', ')})`;
    const a = line({});
    const b = line({ 7: 'changed' });
    const [ra, rb] = changedRanges(a, b)!;
    expect(parts(a, ra)).toEqual(['a7']);
    expect(parts(b, rb)).toEqual(['changed']);
    // Changes far apart would leave a middle too long to compare and almost all of the line to mark.
    expect(changedRanges(a, line({ 7: 'changed', 390: 'too' }))).toBeUndefined();
  });

  it('counts letters of every script as words', () => {
    const [ra, rb] = changedRanges('Größe = 5', 'Größe = 6')!;
    expect(parts('Größe = 5', ra)).toEqual(['5']);
    expect(parts('Größe = 6', rb)).toEqual(['6']);
  });
});

describe('wordChanges', () => {
  const ctx = (n: number, text: string): DiffLine => ({ kind: 'context', old: n, new: n, text });
  const del = (n: number, text: string): DiffLine => ({ kind: 'delete', old: n, text });
  const add = (n: number, text: string): DiffLine => ({ kind: 'add', new: n, text });

  it('compares removed lines with the added lines beside them', () => {
    const removed = del(2, 'const VAT = 16');
    const added = add(2, 'const VAT = 19');
    const alone = add(3, 'const Reduced = 7');
    const marks = wordChanges([ctx(1, 'package rates'), removed, added, alone]);
    expect(parts(removed.text, marks.get(removed)!)).toEqual(['16']);
    expect(parts(added.text, marks.get(added)!)).toEqual(['19']);
    expect(marks.has(alone)).toBe(false);
  });
});

describe('markHtml', () => {
  it('wraps the ranges in marks within the spans of tokens', () => {
    const html = '<span class="kd">const</span> VAT = <span class="mi">16</span>';
    expect(markHtml(html, 'const VAT = 16', [[12, 14]])).toBe(
      '<span class="kd">const</span> VAT = <span class="mi"><span class="kogge-word">16</span></span>',
    );
  });

  it('closes a mark before a tag and opens it again after it', () => {
    const html = 'a<span class="o">+</span>b';
    expect(markHtml(html, 'a+b', [[0, 3]])).toBe(
      '<span class="kogge-word">a</span><span class="o"><span class="kogge-word">+</span></span><span class="kogge-word">b</span>',
    );
  });

  it('counts an entity as the character it stands for', () => {
    const html = 'x &lt; y &amp;&amp; z == &#39;a&#39;';
    const text = "x < y && z == 'a'";
    expect(
      markHtml(html, text, [
        [2, 3],
        [14, 17],
      ]),
    ).toBe(
      'x <span class="kogge-word">&lt;</span> y &amp;&amp; z == <span class="kogge-word">&#39;a&#39;</span>',
    );
  });

  it('leaves HTML whose text is not the line as it is', () => {
    expect(markHtml('<b>other</b>', 'text', [[0, 2]])).toBe('<b>other</b>');
    expect(markHtml('a &bogus; b', 'a ? b', [[0, 1]])).toBe('a &bogus; b');
  });
});
