// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { type RefObject, useEffectEvent, useLayoutEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';

type Renderer = typeof import('./math/katexRender');

let renderer: Renderer | undefined;
let loading: Promise<Renderer> | undefined;

/**
 * KaTeX is large: it loads with the first formula shown, and later formulas are typeset at once. A load that
 * failed (the files of an older version are gone after an update) is tried again with the next formula.
 */
export function loadMath(): Promise<Renderer> {
  loading ??= import('./math/katexRender').then(
    (m) => (renderer = m),
    (e: unknown) => {
      loading = undefined;
      throw e;
    },
  );
  return loading;
}

/** What shows instead of a formula that cannot be typeset: its source, with the reason. */
function failure(latex: string, message: string, display: boolean, title: string): HTMLElement {
  const source = document.createElement('code');
  source.className = 'kogge-math-error';
  source.textContent = latex;
  if (!display) {
    source.title = `${title}: ${message}`;
    return source;
  }
  const note = document.createElement('div');
  note.className = 'kogge-math-error-note';
  const strong = document.createElement('strong');
  strong.textContent = `${title}:`;
  note.append(strong, ` ${message}`);
  const pre = document.createElement('pre');
  pre.append(source);
  const block = document.createElement('div');
  block.append(note, pre);
  return block;
}

export interface MathFormulaProps {
  /** The formula in LaTeX. */
  latex: string;
  /** On a line of its own (display style), else within the text. */
  display?: boolean;
  /**
   * Hears why the formula cannot be typeset, or null once it is. A formula with this (the preview of the formula
   * dialog) keeps showing the last one that could be typeset and leaves the reason to the caller.
   */
  onResult?: (error: string | null) => void;
}

/**
 * A formula, typeset by KaTeX. KaTeX builds its elements in a box that React leaves alone; until KaTeX has loaded,
 * the source shows there. A formula that cannot be typeset shows its source and why (within the text as a tip).
 * The formula's box clips what it draws (editor.css): negative spaces and lowered boxes cannot reach over the page.
 * The box's data-state tells what it shows: loading, typeset or failed.
 */
export function MathFormula({ latex, display = false, onResult }: MathFormulaProps) {
  const { t } = useTranslation();
  const box = useRef<HTMLElement>(null);
  const preview = !!onResult;
  const report = useEffectEvent((error: string | null) => onResult?.(error));
  useLayoutEffect(() => {
    const target = box.current;
    if (!target) return;
    let current = true;
    const settle = (error: string | null) => {
      target.removeAttribute('aria-busy');
      target.removeAttribute('title');
      report(error);
    };
    const fail = (message: string) => {
      if (!preview) {
        target.replaceChildren(failure(latex, message, display, t('math.error')));
        target.dataset.state = 'failed';
      } else if (target.dataset.state !== 'typeset') {
        // A preview keeps the last formula typeset, but not the source shown while KaTeX loaded.
        target.replaceChildren();
        target.dataset.state = 'failed';
      }
      settle(message);
    };
    const typeset = (m: Renderer) => {
      if (!current) return;
      const error = m.renderMath(latex, target, display);
      if (error) {
        fail(error.message);
        return;
      }
      target.dataset.state = 'typeset';
      settle(null);
    };
    if (renderer) {
      typeset(renderer);
    } else {
      target.textContent = latex;
      target.dataset.state = 'loading';
      target.setAttribute('aria-busy', 'true');
      target.title = t('math.rendering');
      loadMath().then(typeset, (e: unknown) => {
        if (current) fail(e instanceof Error ? e.message : String(e));
      });
    }
    return () => {
      current = false;
    };
  }, [latex, display, preview, t]);
  return display ? (
    <div className="kogge-math kogge-math-block" data-math="" data-latex={latex}>
      <div ref={box as RefObject<HTMLDivElement | null>} className="kogge-math-box" />
    </div>
  ) : (
    <span className="kogge-math kogge-math-inline" data-inline-math="" data-latex={latex}>
      <span ref={box as RefObject<HTMLSpanElement | null>} className="kogge-math-box" />
    </span>
  );
}
