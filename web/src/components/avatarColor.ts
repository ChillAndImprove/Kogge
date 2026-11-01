// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/**
 * The twelve mid-tone colours of avatars, of people and of containers (projects, spaces, code projects): white
 * initials read on each at 4.5:1 in both colour schemes. Their order puts different hues on the keys of the sample
 * content, so that neighbours seldom look alike.
 */
export const avatarColors = [
  'blue.8',
  'orange.9',
  'grape.8',
  'cyan.9',
  'pink.8',
  'indigo.8',
  'teal.9',
  'violet.8',
  'leather.8',
  'petrol.7',
  'red.9',
  'navy.8',
] as const;

/** A stable number for a text (sdbm): it spreads short keys such as "SHOP" or "ENG" evenly. */
function hash(text: string): number {
  let h = 0;
  for (const c of text) h = ((c.codePointAt(0) ?? 0) + (h << 6) + (h << 16) - h) >>> 0;
  return h;
}

/** The colour of a key (project, space, code project): the same each time. */
export function avatarColor(key: string): (typeof avatarColors)[number] {
  return avatarColors[hash(key.toUpperCase()) % avatarColors.length];
}

/** The colour of a person, by the name shown (some places, such as notifications, know only the name). */
export function personColor(name: string): (typeof avatarColors)[number] {
  return avatarColors[hash(name.trim()) % avatarColors.length];
}

/** The initials of a name: the first letters of its first two words ("Mara Lindner" → "ML"), "?" for none. */
export function initials(name: string | null | undefined): string {
  const words = (name ?? '').trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return '?';
  return words
    .slice(0, 2)
    .map((w) => Array.from(w)[0])
    .join('')
    .toUpperCase();
}

/** The letters of a container's avatar: the first two of its key ("SHOP" → "SH"). */
export function keyLetters(key: string): string {
  return Array.from(key.trim()).slice(0, 2).join('').toUpperCase() || '?';
}
