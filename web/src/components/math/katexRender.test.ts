// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { renderMath } from './katexRender';

function typeset(latex: string, display = false) {
  const target = document.createElement(display ? 'div' : 'span');
  const error = renderMath(latex, target, display);
  return { target, error };
}

describe('renderMath', () => {
  it('typesets formulas within the text and on a line of their own, with MathML for screen readers', () => {
    const inline = typeset('x^2');
    expect(inline.error).toBeNull();
    expect(inline.target.querySelector('.katex')).not.toBeNull();
    expect(inline.target.querySelector('.katex-display')).toBeNull();
    // What is seen is hidden from screen readers, which read the MathML with the source.
    expect(inline.target.querySelector('.katex-html')?.getAttribute('aria-hidden')).toBe('true');
    expect(inline.target.querySelector('.katex-mathml annotation')?.textContent).toBe('x^2');

    const display = typeset('\\int_0^1 x\\,dx', true);
    expect(display.error).toBeNull();
    expect(display.target.querySelector('.katex-display .katex')).not.toBeNull();
  });

  it('says why a formula cannot be typeset and keeps what was there', () => {
    const { target } = typeset('a+b');
    const before = target.innerHTML;
    const error = renderMath('\\frac{', target, false);
    expect(error?.message).toMatch(/expected '}'/);
    expect(target.innerHTML).toBe(before);
    expect(renderMath('\\nosuchcommand', target, false)?.message).toMatch(/Undefined control sequence/);
    expect(target.innerHTML).toBe(before);
  });

  it('catches formulas nested too deeply for the stack', () => {
    const { target, error } = typeset('{'.repeat(5000) + 'x' + '}'.repeat(5000));
    expect(error?.message).toBeTruthy();
    expect(target.childNodes).toHaveLength(0);
  });

  it('makes no links, images, styles or classes of what formulas ask for', () => {
    for (const latex of [
      '\\href{javascript:alert(1)}{x}',
      '\\url{https://example.com}',
      '\\includegraphics{https://example.com/a.png}',
      '\\htmlStyle{position:fixed;top:0}{x}',
      '\\htmlClass{kogge-editor}{x}',
      '\\htmlId{root}{x}',
    ]) {
      const { target, error } = typeset(latex);
      expect(error, latex).toBeNull();
      expect(target.querySelector('a, img, #root, .kogge-editor'), latex).toBeNull();
      const styled = [...target.querySelectorAll<HTMLElement>('[style]')];
      expect(
        styled.some((el) => el.style.position || el.style.top),
        latex,
      ).toBe(false);
      // Refused commands show as their name, in the colour of errors.
      expect(target.querySelector('.katex-html')?.textContent, latex).toMatch(/^\\/);
    }
  });

  it('keeps definitions within their formula', () => {
    const pi = typeset('\\pi').target.querySelector('.katex-html')?.textContent;
    expect(pi).toBe('π');
    expect(typeset('\\gdef\\pi{X}\\pi').target.querySelector('.katex-html')?.textContent).toBe('X');
    expect(typeset('\\pi').target.querySelector('.katex-html')?.textContent).toBe('π');
  });

  it('bounds sizes and macro expansions', () => {
    const { target } = typeset('\\rule{500em}{1em}');
    const rule = target.querySelector<HTMLElement>('.katex-html [style*="border-right-width"]');
    expect(rule?.style.borderRightWidth).toBe('20em');
    expect(typeset('\\def\\a{\\a\\a}\\a').error?.message).toMatch(/expansion/i);
  });
});
