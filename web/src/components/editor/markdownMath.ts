// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Tag, tags } from '@lezer/highlight';
import type { Line, MarkdownConfig } from '@lezer/markdown';

/*
 * Formulas in the Markdown source, by the rules the server reads them with (internal/docmodel), so that the source
 * marks as a formula what the preview shows as one:
 * - $…$ within a line: the opening dollar is not followed by a space, the closing one is not preceded by a space
 *   and not followed by a digit, so "$5 and $10" stays text. A backslash escapes the character after it: "\$"
 *   closes nothing. "$$…$$" within a line is a formula, too.
 * - A formula of its own: a line "$$" up to a line that ends with "$$", or a line "$$…$$". Without the closing
 *   line it ends with its quote or list item, or with the text. Other lines starting with "$$" are text.
 * Code spans and code blocks come first, as on the server, and their dollars are code. The dollars of a formula
 * are MathMark, its LaTeX is MathText. A fenced block of the language math, which the server reads as a formula,
 * too, shows as code here.
 */

const dollar = 36;
const backslash = 92;

/** Spaces as the server counts them: space, tab, line feed and carriage return, nothing else. */
const space = (c: number) => c === 32 || c === 9 || c === 10 || c === 13;
const newline = (c: number) => c === 10 || c === 13;
const digit = (c: number) => c >= 48 && c <= 57;

/** Whether text from `from` to `to` is only spaces. */
function blank(text: string, from: number, to: number): boolean {
  for (let i = from; i < to; i++) if (!space(text.charCodeAt(i))) return false;
  return true;
}

/** The end of a text without the spaces at its end, not going back before `from`. */
function trimmedEnd(text: string, from = 0): number {
  let end = text.length;
  while (end > from && space(text.charCodeAt(end - 1))) end--;
  return end;
}

/**
 * The formula whose dollars start at `pos`: its end, or -1 if they open none, and the number of dollars. The scan
 * stops at the first dollar that is not escaped and at the end of the line, so that it stays linear.
 */
function inlineMath(text: string, pos: number): { end: number; run: number } {
  let run = 0;
  while (text.charCodeAt(pos + run) === dollar) run++;
  const none = { end: -1, run };
  if (run > 2 || pos + run >= text.length || space(text.charCodeAt(pos + run))) return none;
  for (let i = pos + run; i < text.length; i++) {
    const c = text.charCodeAt(i);
    if (c === backslash) {
      // A backslash escapes the character after it, but a formula still ends with its line.
      if (newline(text.charCodeAt(++i))) return none;
    } else if (newline(c)) {
      return none;
    } else if (c === dollar) {
      if (run === 2 && text.charCodeAt(i + 1) !== dollar) return none;
      if (space(text.charCodeAt(i - 1)) || digit(text.charCodeAt(i + run))) return none;
      return { end: i + run, run };
    }
  }
  return none;
}

/** Where the first "$$" between `from` and `to` that is not escaped starts, or -1. */
function displayClose(text: string, from: number, to: number): number {
  for (let i = from; i + 1 < to; i++) {
    const c = text.charCodeAt(i);
    if (c === backslash) i++;
    else if (c === dollar && text.charCodeAt(i + 1) === dollar) return i;
  }
  return -1;
}

/**
 * Where the "$$" that ends a line (spaces aside) starts, or -1: also if a backslash escapes it, i.e. an odd number
 * of them comes before it.
 */
function closingAt(text: string, from: number): number {
  const end = trimmedEnd(text, from);
  if (end - 2 < from || text.charCodeAt(end - 1) !== dollar || text.charCodeAt(end - 2) !== dollar) return -1;
  let i = end - 3;
  while (i >= from && text.charCodeAt(i) === backslash) i--;
  return (end - 3 - i) % 2 ? -1 : end - 2;
}

/**
 * Whether a line starts a formula of its own (and so ends a paragraph before it): "$$" with at most three spaces
 * before it, and then nothing, or the whole formula and its closing "$$".
 */
function opensDisplay(line: Line): boolean {
  const { text, pos } = line;
  if (
    line.indent - line.baseIndent >= 4 ||
    text.charCodeAt(pos) !== dollar ||
    text.charCodeAt(pos + 1) !== dollar ||
    text.charCodeAt(pos + 2) === dollar
  )
    return false;
  const end = trimmedEnd(text);
  const close = displayClose(text, pos + 2, end);
  return close >= 0 ? close + 2 === end && !blank(text, pos + 2, close) : pos + 2 === end;
}

/** How many of the open blocks (quotes, list items) a line goes on with: Line.depth, missing from the typings. */
const depth = (line: Line) => (line as unknown as { depth: number }).depth;

/** The LaTeX of a formula; a style without a rule of its own shows it as code. */
export const mathTag = Tag.define('math', tags.monospace);

/** Formulas for the Markdown parser (@lezer/markdown), next to GitHub's extensions: InlineMath and BlockMath. */
export const MathMarkdown: MarkdownConfig = {
  defineNodes: [
    { name: 'InlineMath' },
    { name: 'BlockMath', block: true },
    { name: 'MathMark', style: tags.processingInstruction },
    { name: 'MathText', style: mathTag },
  ],
  parseInline: [
    {
      name: 'InlineMath',
      parse(cx, next, pos) {
        if (next !== dollar) return -1;
        const { end, run } = inlineMath(cx.text, pos - cx.offset);
        // Dollars that open no formula are text; several at once stay together, so "$$x$" opens nothing.
        if (end < 0) return run > 1 ? pos + run : -1;
        const to = end + cx.offset;
        return cx.addElement(
          cx.elt('InlineMath', pos, to, [
            cx.elt('MathMark', pos, pos + run),
            cx.elt('MathText', pos + run, to - run),
            cx.elt('MathMark', to - run, to),
          ]),
        );
      },
      after: 'InlineCode',
    },
  ],
  parseBlock: [
    {
      name: 'BlockMath',
      parse(cx, line) {
        if (!opensDisplay(line)) return false;
        const from = cx.lineStart + line.pos;
        const marks = [cx.elt('MathMark', from, from + 2)];
        const end = trimmedEnd(line.text);
        const close = displayClose(line.text, line.pos + 2, end);
        if (close >= 0) {
          marks.push(
            cx.elt('MathText', from + 2, cx.lineStart + close),
            cx.elt('MathMark', cx.lineStart + close, cx.lineStart + close + 2),
          );
          cx.nextLine();
        } else {
          // Like a code block: the lines of the formula go on as long as its quote or list item does.
          while (cx.nextLine() && depth(line) >= cx.depth) {
            marks.push(...line.markers);
            const at = closingAt(line.text, line.basePos);
            const latexEnd = at >= 0 ? at : line.text.length;
            if (!blank(line.text, line.basePos, latexEnd))
              marks.push(cx.elt('MathText', cx.lineStart + line.basePos, cx.lineStart + latexEnd));
            if (at >= 0) {
              marks.push(cx.elt('MathMark', cx.lineStart + at, cx.lineStart + at + 2));
              cx.nextLine();
              break;
            }
          }
        }
        cx.addElement(cx.elt('BlockMath', from, cx.prevLineEnd(), marks));
        return true;
      },
      endLeaf: (_cx, line) => opensDisplay(line),
      before: 'FencedCode',
    },
  ],
};
