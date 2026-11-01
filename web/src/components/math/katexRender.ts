// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// The only module that imports KaTeX and its stylesheet (with its fonts): it loads with the first formula shown
// (MathFormula.tsx), so that pages without formulas never fetch them.
import 'katex/dist/katex.min.css';
import katex, { type KatexOptions } from 'katex';

/** Why a formula cannot be typeset: KaTeX's message (English, as Mermaid's), and where in the source, if known. */
export interface MathError {
  message: string;
  position?: number;
  length?: number;
}

/**
 * KaTeX's options for formulas that anyone may have written, made anew for every formula: \gdef writes into the
 * macros, which would otherwise carry one formula's definitions into the next. Commands that link, load images or
 * set classes and styles stay inert (trust); sizes and macro expansions are bounded; LaTeX that KaTeX accepts but
 * LaTeX would not (umlauts in math mode) is no reason to complain.
 */
function options(display: boolean): KatexOptions {
  return {
    displayMode: display,
    output: 'htmlAndMathml',
    throwOnError: true,
    // What trust refuses is shown as its command in this colour.
    errorColor: 'var(--mantine-color-error)',
    trust: false,
    strict: 'ignore',
    maxSize: 20,
    maxExpand: 1000,
    macros: {},
  };
}

/**
 * Typesets a formula into target. KaTeX builds the elements itself (no HTML is parsed) into an element of its own
 * first, which replaces the target's content only once the formula is typeset: a formula that fails leaves what
 * the target showed. Returns why it failed, or null.
 */
export function renderMath(latex: string, target: HTMLElement, display: boolean): MathError | null {
  const box = document.createElement(display ? 'div' : 'span');
  try {
    katex.render(latex, box, options(display));
  } catch (e) {
    if (e instanceof katex.ParseError)
      return { message: e.rawMessage, position: e.position, length: e.length };
    // Formulas nested too deeply exhaust the stack (RangeError) before KaTeX can complain.
    return { message: e instanceof Error ? e.message : String(e) };
  }
  target.replaceChildren(...box.childNodes);
  return null;
}
