// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import {
  isApple,
  matchesWords,
  moveActive,
  opensInNewTab,
  opensQuickSearch,
  quickSearchKey,
  type KeyPress,
} from './quickSearchKeys';

const press = (key: string, mods: Partial<KeyPress> = {}): KeyPress => ({
  key,
  ctrlKey: false,
  metaKey: false,
  altKey: false,
  shiftKey: false,
  defaultPrevented: false,
  ...mods,
});

describe('opening the quick search', () => {
  it('opens with Ctrl+K, and with ⌘K on Apple devices', () => {
    expect(opensQuickSearch(press('k', { ctrlKey: true }), false)).toBe(true);
    expect(opensQuickSearch(press('K', { ctrlKey: true }), false)).toBe(true);
    expect(opensQuickSearch(press('k', { metaKey: true }), true)).toBe(true);
    expect(opensQuickSearch(press('k', { metaKey: true }), false)).toBe(false);
    // On Apple devices Ctrl+K deletes to the end of the line in text fields.
    expect(opensQuickSearch(press('k', { ctrlKey: true }), true)).toBe(false);
  });

  it('leaves other keys and other combinations alone', () => {
    expect(opensQuickSearch(press('k'), false)).toBe(false);
    expect(opensQuickSearch(press('j', { ctrlKey: true }), false)).toBe(false);
    expect(opensQuickSearch(press('k', { ctrlKey: true, shiftKey: true }), false)).toBe(false);
    expect(opensQuickSearch(press('k', { ctrlKey: true, altKey: true }), false)).toBe(false);
    expect(opensQuickSearch(press('k', { ctrlKey: true, metaKey: true }), false)).toBe(false);
  });

  it('leaves Ctrl+K to whatever handled it first, such as an editor inserting a link', () => {
    expect(opensQuickSearch(press('k', { ctrlKey: true, defaultPrevented: true }), false)).toBe(false);
    expect(opensQuickSearch(press('k', { ctrlKey: true, isComposing: true }), false)).toBe(false);
  });

  it('knows the platforms', () => {
    expect(isApple('MacIntel')).toBe(true);
    expect(isApple('iPhone')).toBe(true);
    expect(isApple('Linux x86_64')).toBe(false);
    expect(isApple('Win32')).toBe(false);
    expect(quickSearchKey(true)).toBe('⌘K');
    expect(quickSearchKey(false)).toBe('Ctrl+K');
    expect(opensInNewTab({ ctrlKey: true, metaKey: false }, false)).toBe(true);
    expect(opensInNewTab({ ctrlKey: true, metaKey: false }, true)).toBe(false);
  });
});

describe('finding actions', () => {
  it('matches the beginnings of words, in any order and case', () => {
    expect(matchesWords('Create an issue new', 'new iss')).toBe(true);
    expect(matchesWords('Create an issue new', 'ISSUE create')).toBe(true);
    expect(matchesWords('Vorgang anlegen neu', 'vorg')).toBe(true);
    expect(matchesWords('Go to Logbuch', 'ssue')).toBe(false);
    expect(matchesWords('Go to Logbuch', 'logbuch kontor')).toBe(false);
    expect(matchesWords('Go to Logbuch', '  ')).toBe(false);
  });
});

describe('moving through the options', () => {
  it('moves down and up, around the ends', () => {
    expect(moveActive(0, 3, 'ArrowDown')).toBe(1);
    expect(moveActive(2, 3, 'ArrowDown')).toBe(0);
    expect(moveActive(0, 3, 'ArrowUp')).toBe(2);
    expect(moveActive(1, 3, 'ArrowUp')).toBe(0);
  });

  it('jumps between groups with PageDown and PageUp', () => {
    const starts = [0, 3, 5];
    expect(moveActive(1, 8, 'PageDown', starts)).toBe(3);
    expect(moveActive(3, 8, 'PageDown', starts)).toBe(5);
    expect(moveActive(6, 8, 'PageDown', starts)).toBe(0);
    expect(moveActive(4, 8, 'PageUp', starts)).toBe(3);
    expect(moveActive(3, 8, 'PageUp', starts)).toBe(0);
    expect(moveActive(0, 8, 'PageUp', starts)).toBe(5);
  });

  it('does nothing without options or for other keys', () => {
    expect(moveActive(0, 0, 'ArrowDown')).toBeNull();
    expect(moveActive(1, 3, 'Enter')).toBeNull();
    expect(moveActive(1, 3, 'Home')).toBeNull();
    expect(moveActive(7, 3, 'ArrowDown')).toBe(0);
  });
});
