// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { avatarColor, avatarColors, initials, keyLetters, personColor } from './avatarColor';

describe('avatars', () => {
  it('give a key the same colour each time, whatever its case', () => {
    expect(avatarColor('SHOP')).toBe(avatarColor('shop'));
    expect(avatarColors).toContain(avatarColor('SHOP'));
  });

  it('tell the containers of the sample content apart', () => {
    // The two spaces, the two projects: neighbours in lists that once shared one colour.
    expect(avatarColor('ENG')).not.toBe(avatarColor('SUP'));
    expect(avatarColor('SHOP')).not.toBe(avatarColor('APP'));
  });

  it('spread keys over all colours', () => {
    const counts = new Map<string, number>();
    const letters = Array.from({ length: 26 }, (_, i) => String.fromCharCode(65 + i));
    for (const a of letters)
      for (const b of letters)
        for (const c of letters) {
          const color = avatarColor(a + b + c);
          counts.set(color, (counts.get(color) ?? 0) + 1);
        }
    expect(counts.size).toBe(avatarColors.length);
    const even = 26 ** 3 / avatarColors.length;
    expect(Math.min(...counts.values())).toBeGreaterThan(even * 0.9);
    expect(Math.max(...counts.values())).toBeLessThan(even * 1.1);
  });

  it('give a person a colour by name', () => {
    expect(avatarColors).toContain(personColor('Mara Lindner'));
    expect(personColor(' Mara Lindner ')).toBe(personColor('Mara Lindner'));
  });

  it('write initials of up to two words', () => {
    expect(initials('Mara Lindner')).toBe('ML');
    expect(initials('Anna Maria Schulz')).toBe('AM');
    expect(initials('wisee')).toBe('W');
    expect(initials('  Jonas   Weber ')).toBe('JW');
    expect(initials('Élodie Ørsted')).toBe('ÉØ');
    expect(initials('')).toBe('?');
    expect(initials(undefined)).toBe('?');
  });

  it('write two letters of a key', () => {
    expect(keyLetters('SHOP')).toBe('SH');
    expect(keyLetters('web')).toBe('WE');
    expect(keyLetters('')).toBe('?');
  });
});
