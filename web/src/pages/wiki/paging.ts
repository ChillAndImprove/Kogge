// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/** A page of a list with the total of the list, as the paged wiki lists return it. */
export interface ListPage<T> {
  items: T[];
  total: number;
}

/**
 * The offset of the next page of a list that is loaded a page at a time ("show more"), or undefined when the list is
 * complete. An empty page ends the list even if its total says otherwise: items went away while it was loaded.
 */
export function nextOffset<T>(pages: ListPage<T>[]): number | undefined {
  const last = pages.at(-1);
  if (!last) return 0;
  const loaded = pages.reduce((n, p) => n + p.items.length, 0);
  return last.items.length > 0 && loaded < last.total ? loaded : undefined;
}

/**
 * The items of the pages of a list in their order, each once: an item that moved while the list was loaded comes on
 * two pages.
 */
export function uniqueItems<T>(pages: ListPage<T>[], key: (item: T) => number): T[] {
  const seen = new Set<number>();
  const out: T[] = [];
  for (const page of pages) {
    for (const item of page.items) {
      const k = key(item);
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(item);
    }
  }
  return out;
}
