// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { splitRows, type DiffLine } from './diffRows';

/** A changed part of a line: the offset of its first character and of the one after its last. */
export type Range = [number, number];

/** Words, runs of whitespace and single other characters: the units in which a line's changes are found. */
function tokenize(s: string): string[] {
  return s.match(/[\p{L}\p{N}_]+|\s+|[^\p{L}\p{N}_\s]/gu) ?? [];
}

/** Lines with more tokens than this compare only their common beginning and end. */
const maxCells = 40_000;

/** Lines that keep less of their text than this count as rewritten: marks would cover almost all of them. */
const minKept = 0.4;

function visible(s: string) {
  return s.replace(/\s+/g, '').length;
}

/**
 * The parts of a removed line and of the added line in its place that changed, found word by word; nothing when the
 * two have too little in common for marks to help.
 */
export function changedRanges(a: string, b: string): [Range[], Range[]] | undefined {
  const ta = tokenize(a);
  const tb = tokenize(b);
  let pre = 0;
  while (pre < ta.length && pre < tb.length && ta[pre] === tb[pre]) pre++;
  let suf = 0;
  while (
    suf < ta.length - pre &&
    suf < tb.length - pre &&
    ta[ta.length - 1 - suf] === tb[tb.length - 1 - suf]
  ) {
    suf++;
  }
  const ma = ta.slice(pre, ta.length - suf);
  const mb = tb.slice(pre, tb.length - suf);
  const keptA = new Array<boolean>(ma.length).fill(false);
  const keptB = new Array<boolean>(mb.length).fill(false);
  if (ma.length * mb.length <= maxCells) {
    // The longest common subsequence of the tokens between the common beginning and end.
    const w = mb.length + 1;
    const len = new Uint16Array((ma.length + 1) * w);
    for (let i = ma.length - 1; i >= 0; i--) {
      for (let j = mb.length - 1; j >= 0; j--) {
        len[i * w + j] =
          ma[i] === mb[j] ? len[(i + 1) * w + j + 1] + 1 : Math.max(len[(i + 1) * w + j], len[i * w + j + 1]);
      }
    }
    for (let i = 0, j = 0; i < ma.length && j < mb.length;) {
      if (ma[i] === mb[j]) {
        keptA[i++] = true;
        keptB[j++] = true;
      } else if (len[(i + 1) * w + j] >= len[i * w + j + 1]) {
        i++;
      } else {
        j++;
      }
    }
  }
  const kept = (tokens: string[], k: boolean[]) =>
    visible(tokens.slice(0, pre).join('') + tokens.slice(tokens.length - suf).join('')) +
    visible(k.reduce((s, yes, i) => (yes ? s + tokens[pre + i] : s), ''));
  const most = Math.max(visible(a), visible(b));
  if (most === 0 || kept(ta, keptA) / most < minKept) return undefined;
  return [ranges(ta, pre, keptA, a.length), ranges(tb, pre, keptB, b.length)];
}

/** The ranges of the tokens after the first `pre` that were not kept, adjacent ones joined; none for a whole line. */
function ranges(tokens: string[], pre: number, kept: boolean[], length: number): Range[] {
  const out: Range[] = [];
  let at = tokens.slice(0, pre).join('').length;
  kept.forEach((k, i) => {
    const end = at + tokens[pre + i].length;
    if (!k) {
      const last = out[out.length - 1];
      if (last && last[1] === at) last[1] = end;
      else out.push([at, end]);
    }
    at = end;
  });
  // A line that changed entirely is marked as a whole already.
  return out.length === 1 && out[0][0] === 0 && out[0][1] === length ? [] : out;
}

/** The changed words of a hunk's lines: each removed line is compared with the added line beside it in the split view. */
export function wordChanges(lines: DiffLine[]): Map<DiffLine, Range[]> {
  const marks = new Map<DiffLine, Range[]>();
  for (const { left, right } of splitRows(lines)) {
    if (left?.kind !== 'delete' || right?.kind !== 'add') continue;
    const r = changedRanges(left.text, right.text);
    if (!r) continue;
    if (r[0].length) marks.set(left, r[0]);
    if (r[1].length) marks.set(right, r[1]);
  }
  return marks;
}

const entities: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ' };

function decodeEntity(body: string): string | undefined {
  if (body.startsWith('#')) {
    const n = /^#x/i.test(body) ? parseInt(body.slice(2), 16) : parseInt(body.slice(1), 10);
    return Number.isInteger(n) && n >= 0 && n <= 0x10ffff ? String.fromCodePoint(n) : undefined;
  }
  return entities[body];
}

/**
 * Marks ranges of a line's text in its highlighted HTML (escaped text and the spans of its tokens). A mark closes
 * before each tag and opens again after it, so that it nests. Where the HTML's text is not the line's, the HTML is
 * returned as it is.
 */
export function markHtml(html: string, text: string, marks: Range[], className = 'kogge-word'): string {
  if (marks.length === 0) return html;
  let out = '';
  let decoded = '';
  let r = 0;
  let open = false;
  for (let i = 0; i < html.length;) {
    if (html[i] === '<') {
      const end = html.indexOf('>', i);
      if (end < 0) return html;
      if (open) out += '</span>';
      open = false;
      out += html.slice(i, end + 1);
      i = end + 1;
      continue;
    }
    let chunk = html[i];
    let chars = chunk;
    if (chunk === '&') {
      const end = html.indexOf(';', i);
      const decodedEntity = end > i ? decodeEntity(html.slice(i + 1, end)) : undefined;
      if (decodedEntity === undefined) return html;
      chunk = html.slice(i, end + 1);
      chars = decodedEntity;
    }
    i += chunk.length;
    const at = decoded.length;
    while (r < marks.length && marks[r][1] <= at) r++;
    const inside = r < marks.length && marks[r][0] <= at;
    if (inside && !open) out += `<span class="${className}">`;
    if (!inside && open) out += '</span>';
    open = inside;
    out += chunk;
    decoded += chars;
  }
  if (open) out += '</span>';
  return decoded === text ? out : html;
}
