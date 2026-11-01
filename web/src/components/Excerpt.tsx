// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Text } from '@mantine/core';
import { Fragment } from 'react';

const startMark = String.fromCharCode(1);
const endMark = String.fromCharCode(2);

/** The parts of an excerpt: text, and the matches of a search (between the control characters 1 and 2). */
export function excerptParts(text: string): { text: string; match: boolean }[] {
  // The server joins the blocks of a text into clauses; blocks still on lines of their own do not run together.
  let rest = text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
    .join(' · ');
  const parts: { text: string; match: boolean }[] = [];
  while (rest) {
    const s = rest.indexOf(startMark);
    if (s < 0) {
      parts.push({ text: rest, match: false });
      break;
    }
    const e = rest.indexOf(endMark, s);
    if (e < 0) {
      parts.push({ text: rest.replace(startMark, ''), match: false });
      break;
    }
    if (s > 0) parts.push({ text: rest.slice(0, s), match: false });
    parts.push({ text: rest.slice(s + 1, e), match: true });
    rest = rest.slice(e + 1);
  }
  return parts;
}

/**
 * An excerpt of a text in a list or a search: its clauses as the server cut them (with … where the text goes on),
 * the matches of a search highlighted.
 */
export function Excerpt({ text, lines = 3 }: { text: string; lines?: number }) {
  return (
    <Text size="sm" c="dimmed" lineClamp={lines}>
      {excerptParts(text).map((p, i) =>
        p.match ? <mark key={i}>{p.text}</mark> : <Fragment key={i}>{p.text}</Fragment>,
      )}
    </Text>
  );
}
