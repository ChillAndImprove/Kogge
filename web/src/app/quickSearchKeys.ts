// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/** The keys of the quick search: what opens it, and how its options are chosen with the keyboard. */

/** The parts of a keyboard event the quick search looks at. */
export interface KeyPress {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  defaultPrevented: boolean;
  isComposing?: boolean;
}

/** Whether the platform's command key is ⌘ rather than Ctrl. */
export function isApple(platform: string): boolean {
  return /mac|iphone|ipad|ipod/i.test(platform);
}

/**
 * Whether a key press opens the quick search: ⌘K on Apple devices, Ctrl+K elsewhere. A press that something on
 * the page already handled (an editor's Ctrl+K for links, for example) is left to it, and so is Ctrl+K on Apple
 * devices, where it deletes to the end of the line in text fields.
 */
export function opensQuickSearch(e: KeyPress, apple: boolean): boolean {
  if (e.defaultPrevented || e.isComposing || e.altKey || e.shiftKey || e.key.toLowerCase() !== 'k')
    return false;
  return apple ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
}

/** The label of the quick search's key on the platform. */
export function quickSearchKey(apple: boolean): string {
  return apple ? '⌘K' : 'Ctrl+K';
}

/**
 * The option that a key makes active in a list of count options whose groups start at groupStarts (ascending,
 * the first 0): the next or previous option (around the ends), or the first option of the next or previous group
 * with PageDown and PageUp. It returns null for keys that do not move.
 */
export function moveActive(
  active: number,
  count: number,
  key: string,
  groupStarts: number[] = [0],
): number | null {
  if (count === 0) return null;
  const current = Math.min(Math.max(active, 0), count - 1);
  switch (key) {
    case 'ArrowDown':
      return (current + 1) % count;
    case 'ArrowUp':
      return (current - 1 + count) % count;
    case 'PageDown': {
      const next = groupStarts.find((s) => s > current);
      return next ?? groupStarts[0] ?? 0;
    }
    case 'PageUp': {
      const starts = groupStarts.filter((s) => s < count);
      const own = [...starts].reverse().find((s) => s <= current) ?? 0;
      if (own < current) return own;
      const previous = [...starts].reverse().find((s) => s < own);
      return previous ?? starts[starts.length - 1] ?? 0;
    }
  }
  return null;
}

const wordsOf = (s: string) =>
  s
    .toLocaleLowerCase()
    .split(/[^\p{L}\p{N}]+/u)
    .filter(Boolean);

/**
 * Whether a text names an action: every word typed begins a word of the action's text ("new is" finds "Create an
 * issue" with its further word "new").
 */
export function matchesWords(text: string, typed: string): boolean {
  const words = wordsOf(text);
  const parts = wordsOf(typed);
  return parts.length > 0 && parts.every((p) => words.some((w) => w.startsWith(p)));
}

/** Whether a press of Enter asks to open the option in a new tab (Ctrl or ⌘ held). */
export function opensInNewTab(e: Pick<KeyPress, 'ctrlKey' | 'metaKey'>, apple: boolean): boolean {
  return apple ? e.metaKey : e.ctrlKey;
}
