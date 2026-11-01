// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';
import '../i18n';
import { DocView } from './editor/DocView';
import type { Doc } from './editor/doc';
import { loadMath, MathFormula } from './MathFormula';

// KaTeX itself is tested in math/katexRender.test.ts: here a stand-in typesets "[latex]", and fails on "\bad",
// keeping what the target showed, as KaTeX does.
const renderMath = vi.hoisted(() =>
  vi.fn((latex: string, target: HTMLElement, display: boolean) => {
    if (latex.includes('\\bad')) return { message: 'Undefined control sequence: \\bad' };
    const out = document.createElement('span');
    out.className = display ? 'typeset-display' : 'typeset';
    out.textContent = `[${latex}]`;
    target.replaceChildren(out);
    return null;
  }),
);
vi.mock('./math/katexRender', () => ({ renderMath }));

// jsdom has no media queries, which Mantine asks for the colour scheme.
window.matchMedia ??= (query: string) =>
  ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }) as MediaQueryList;

function view(children: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    </MantineProvider>,
  );
}

const text = (t: string) => ({ type: 'text', text: t });

describe('formulas in documents', () => {
  it('are typeset within the text and on lines of their own, carrying their source', async () => {
    const { container } = view(
      <DocView
        doc={
          {
            type: 'doc',
            content: [
              {
                type: 'paragraph',
                content: [
                  text('Energy '),
                  { type: 'inlineMath', attrs: { latex: 'E=mc^2' }, marks: [{ type: 'bold' }] },
                  text(' holds.'),
                ],
              },
              { type: 'math', attrs: { latex: '\\int_0^1 x\\,dx' } },
            ],
          } as Doc
        }
      />,
    );
    const inline = container.querySelector('p .kogge-math-inline');
    expect(inline?.getAttribute('data-latex')).toBe('E=mc^2');
    expect(inline?.hasAttribute('data-inline-math')).toBe(true);
    const block = container.querySelector('div.kogge-math-block');
    expect(block?.getAttribute('data-latex')).toBe('\\int_0^1 x\\,dx');
    expect(block?.hasAttribute('data-math')).toBe(true);
    await waitFor(() => expect(inline?.querySelector('.typeset')?.textContent).toBe('[E=mc^2]'));
    await waitFor(() => expect(block?.querySelector('.typeset-display')).not.toBeNull());
    expect(inline?.querySelector('.kogge-math-box')?.getAttribute('data-state')).toBe('typeset');
    // Marks on formulas are not shown: the server keeps none.
    expect(container.querySelector('strong')).toBeNull();
    expect(container.querySelector('p')?.textContent).toBe('Energy [E=mc^2] holds.');
  });

  it('show their source with the reason where they cannot be typeset', async () => {
    await loadMath();
    const { container } = view(
      <DocView
        doc={
          {
            type: 'doc',
            content: [
              { type: 'paragraph', content: [{ type: 'inlineMath', attrs: { latex: 'x+\\bad' } }] },
              { type: 'math', attrs: { latex: '\\bad{y}' } },
            ],
          } as Doc
        }
      />,
    );
    const inline = container.querySelector('.kogge-math-inline code.kogge-math-error');
    expect(inline?.textContent).toBe('x+\\bad');
    expect(inline?.getAttribute('title')).toBe(
      'The formula cannot be typeset: Undefined control sequence: \\bad',
    );
    const block = container.querySelector('.kogge-math-block');
    expect(block?.querySelector('pre code.kogge-math-error')?.textContent).toBe('\\bad{y}');
    expect(block?.querySelector('.kogge-math-error-note')?.textContent).toBe(
      'The formula cannot be typeset: Undefined control sequence: \\bad',
    );
    expect(block?.querySelector('.kogge-math-box')?.getAttribute('data-state')).toBe('failed');
  });

  it('show nothing for empty formulas or LaTeX that is no text', async () => {
    await loadMath();
    const { container } = view(
      <DocView
        doc={
          {
            type: 'doc',
            content: [
              { type: 'paragraph', content: [text('a'), { type: 'inlineMath', attrs: { latex: '  ' } }] },
              { type: 'math', attrs: { latex: { a: [1] } } },
              { type: 'math' },
              { type: 'paragraph', content: [text('b')] },
            ],
          } as Doc
        }
      />,
    );
    expect(container.querySelector('.kogge-math')).toBeNull();
    expect(container.querySelector('.kogge-doc')?.textContent).toBe('ab');
  });
});

describe('MathFormula', () => {
  it('shows the source until KaTeX has loaded, then the formula', async () => {
    const { container } = view(<MathFormula latex="a^2" />);
    const box = container.querySelector('.kogge-math-box');
    // Whether KaTeX is there already depends on the tests before: it is typeset now or after loading.
    if (box?.getAttribute('data-state') === 'loading') {
      expect(box.textContent).toBe('a^2');
      expect(box.getAttribute('aria-busy')).toBe('true');
    }
    await waitFor(() => expect(box?.getAttribute('data-state')).toBe('typeset'));
    expect(box?.textContent).toBe('[a^2]');
    expect(box?.hasAttribute('aria-busy')).toBe(false);
  });

  it('as a preview, keeps the last formula typeset and reports why the next cannot be', async () => {
    await loadMath();
    const results: (string | null)[] = [];
    const preview = (latex: string) => (
      <MantineProvider>
        <MathFormula latex={latex} display onResult={(e) => results.push(e)} />
      </MantineProvider>
    );
    const { container, rerender } = render(preview('\\bad'));
    const box = container.querySelector('.kogge-math-box');
    // Nothing typeset yet: nothing shown, not even the source.
    expect(box?.textContent).toBe('');
    rerender(preview('x^2'));
    expect(box?.textContent).toBe('[x^2]');
    rerender(preview('x^2+\\bad'));
    expect(box?.textContent).toBe('[x^2]');
    expect(box?.querySelector('.kogge-math-error')).toBeNull();
    expect(container.querySelector('.kogge-math-box')).toBe(box);
    expect(results).toEqual(['Undefined control sequence: \\bad', null, 'Undefined control sequence: \\bad']);
  });
});
