// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { createInstance } from 'i18next';
import { describe, expect, it } from 'vitest';
import de from '../../i18n/de';
import en from '../../i18n/en';
import { nextOffset, uniqueItems } from './paging';

async function translator(lng: 'de' | 'en') {
  const i18n = createInstance();
  await i18n.init({
    resources: { de: { translation: de }, en: { translation: en } },
    lng,
    fallbackLng: 'en',
    interpolation: { escapeValue: false },
  });
  return i18n.t;
}

describe('the counts of paged lists', () => {
  it('are written with the separators of the language', async () => {
    const shown = { shown: 100, total: 4800 };
    expect((await translator('en'))('wiki.treeShowMore', shown)).toBe('Show more – 100 of 4,800 shown');
    expect((await translator('de'))('wiki.treeShowMore', shown)).toBe(
      'Mehr anzeigen – 100 von 4.800 angezeigt',
    );
    expect((await translator('de'))('wiki.treeShowMoreIn', { ...shown, title: 'Protokolle' })).toBe(
      'Mehr anzeigen – 100 von 4.800 angezeigt, Seiten in „Protokolle“',
    );
    expect((await translator('en'))('wiki.trashCount', { count: 1 })).toBe('1 page in the trash');
    expect((await translator('de'))('wiki.blogPostCount', { count: 1200 })).toBe('1.200 Blogbeiträge');
  });
});

const page = (ids: number[], total: number) => ({ items: ids.map((id) => ({ id })), total });

describe('nextOffset', () => {
  it('starts at the beginning', () => {
    expect(nextOffset([])).toBe(0);
  });
  it('continues after the items loaded so far', () => {
    expect(nextOffset([page([1, 2], 5)])).toBe(2);
    expect(nextOffset([page([1, 2], 5), page([3, 4], 5)])).toBe(4);
  });
  it('stops when everything is loaded', () => {
    expect(nextOffset([page([1, 2], 5), page([3, 4], 5), page([5], 5)])).toBeUndefined();
    expect(nextOffset([page([], 0)])).toBeUndefined();
  });
  it('stops at an empty page although the total promises more', () => {
    expect(nextOffset([page([1, 2], 5), page([], 5)])).toBeUndefined();
  });
  it('follows the total of the latest page', () => {
    expect(nextOffset([page([1, 2], 2), page([3], 4)])).toBe(3);
  });
});

describe('uniqueItems', () => {
  it('keeps the order of the pages', () => {
    expect(uniqueItems([page([1, 2], 4), page([3, 4], 4)], (i) => i.id).map((i) => i.id)).toEqual([
      1, 2, 3, 4,
    ]);
  });
  it('keeps an item that came on two pages once, where it came first', () => {
    expect(uniqueItems([page([1, 2], 4), page([2, 3], 4)], (i) => i.id).map((i) => i.id)).toEqual([1, 2, 3]);
  });
});
