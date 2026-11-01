// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { diffWordsWithSpace } from 'diff';
import { describe, expect, it } from 'vitest';
import type { Doc } from '../../components/editor/doc';
import { versionText } from './PageHistory';

const text = (t: string) => ({ type: 'text', text: t });
const formula = (latex: unknown) => ({ type: 'inlineMath', attrs: { latex } });
const block = (latex: string) => ({ type: 'math', attrs: { latex } });
const doc = (...content: unknown[]) => ({ type: 'doc', content }) as Doc;

/** What changed between two versions, as the comparison marks it. */
const changes = (a: Doc, b: Doc) =>
  diffWordsWithSpace(versionText(a), versionText(b))
    .filter((p) => p.added || p.removed)
    .map((p) => `${p.added ? '+' : '-'}${p.value}`);

describe('versionText', () => {
  it('writes formulas as in Markdown, so that a changed formula shows', () => {
    const before = doc(
      { type: 'paragraph', content: [text('Energy '), formula('E=mc^2'), text(' holds.')] },
      block('a^2+b^2=c^2\n\\quad\\text{(Pythagoras)}'),
    );
    expect(versionText(before)).toBe(
      'Energy $E=mc^2$ holds.\n$$\na^2+b^2=c^2\n\\quad\\text{(Pythagoras)}\n$$',
    );
    const after = doc(
      { type: 'paragraph', content: [text('Energy '), formula('E=mc^3'), text(' holds.')] },
      block('a^2+b^2=c^2\n\\quad\\text{(Pythagoras)}'),
    );
    expect(changes(before, after)).toEqual(['-2', '+3']);
  });

  it('shows formulas in lists, tables and columns, and LaTeX that is no text as none', () => {
    const v = versionText(
      doc(
        {
          type: 'bulletList',
          content: [{ type: 'listItem', content: [{ type: 'paragraph', content: [formula('x')] }] }],
        },
        {
          type: 'table',
          content: [
            {
              type: 'tableRow',
              content: [
                { type: 'tableCell', content: [{ type: 'paragraph', content: [formula('y')] }] },
                { type: 'tableCell', content: [block('z')] },
              ],
            },
          ],
        },
        { type: 'columns', content: [{ type: 'column', content: [block('w')] }] },
        { type: 'paragraph', content: [formula(null)] },
      ),
    );
    expect(v).toBe('  $x$\n$y$ | $$z$$\n│ $$\n│ w\n│ $$\n$$');
  });

  it('shows what else stands in the text as a whole', () => {
    expect(
      versionText(
        doc({
          type: 'paragraph',
          content: [
            { type: 'mention', attrs: { user: 7, label: 'Anna' } },
            text(' '),
            { type: 'issue', attrs: { key: 'SHOP-12' } },
            text(' '),
            { type: 'status', attrs: { text: 'DONE', color: 'green' } },
            text(' '),
            { type: 'date', attrs: { value: '2026-10-07' } },
            text(' '),
            { type: 'emoji', attrs: { name: 'rocket' } },
          ],
        }),
      ),
    ).toBe('@Anna SHOP-12 [DONE] 2026-10-07 :rocket:');
  });
});
