// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Formulas in documents: the nodes math (on a line of its own) and inlineMath (within the text) keep
// their LaTeX in the attribute latex. KaTeX is not needed here; katexRender.ts typesets them.

/** The longest formula the server keeps, in bytes of UTF-8 (docmodel.MaxLatex): it refuses longer ones. */
export const maxLatexBytes = 16 << 10;

/** Whether a formula is longer than the server keeps. */
export function latexTooLong(latex: string): boolean {
  // UTF-8 takes at most three bytes per UTF-16 unit: most formulas need no counting.
  return latex.length * 3 > maxLatexBytes && new TextEncoder().encode(latex).length > maxLatexBytes;
}

/** The LaTeX of a formula node from its attributes; other values than text (others' changes may set any) count as
 * none. */
export function latexOf(attrs: { readonly [name: string]: unknown } | null | undefined): string {
  const latex = attrs?.latex;
  return typeof latex === 'string' ? latex : '';
}
