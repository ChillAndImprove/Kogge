// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { excerptParts } from './Excerpt';

const m = (s: string) => `\u0001${s}\u0002`;

describe('excerpts', () => {
  it('marks the matches of a search', () => {
    expect(excerptParts(`…pay with a ${m('card')} I saved`)).toEqual([
      { text: '…pay with a ', match: false },
      { text: 'card', match: true },
      { text: ' I saved', match: false },
    ]);
    expect(excerptParts(`${m('Card')} list`)[0]).toEqual({ text: 'Card', match: true });
  });

  it('keeps blocks on lines of their own apart', () => {
    expect(excerptParts('Acceptance criteria\nSaved cards first\n\n')).toEqual([
      { text: 'Acceptance criteria · Saved cards first', match: false },
    ]);
  });

  it('shows a mark without its end as text', () => {
    expect(excerptParts('a \u0001b')).toEqual([{ text: 'a b', match: false }]);
  });
});
