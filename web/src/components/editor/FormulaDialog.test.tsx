// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { act, cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeAll, describe, expect, it } from 'vitest';
import { loadMath } from '../MathFormula';
import { renderWithApp, setViewport } from '../testing';
import type { Doc } from './doc';
import { Editor } from './Editor';

beforeAll(async () => {
  setViewport(1440);
  // jsdom loads no fonts and observes no sizes; Mantine's growing text areas and segmented controls ask for both.
  if (!('fonts' in document)) {
    Object.defineProperty(document, 'fonts', {
      configurable: true,
      value: { addEventListener: () => {}, removeEventListener: () => {} },
    });
  }
  window.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  // Nor does it lay out text, where ProseMirror measures what it scrolls into view.
  Range.prototype.getClientRects ??= () => [] as unknown as DOMRectList;
  Range.prototype.getBoundingClientRect ??= () => new DOMRect();
  await loadMath();
});
afterEach(cleanup);

/** The formulas of a document, as type and LaTeX. */
function formulas(doc: Doc | undefined): string[] {
  const out: string[] = [];
  const walk = (n: Doc) => {
    if (n.type === 'math' || n.type === 'inlineMath') out.push(`${n.type}:${n.attrs?.latex}`);
    n.content?.forEach(walk);
  };
  if (doc) walk(doc);
  return out;
}

describe('the formula dialog', () => {
  it('inserts formulas from the toolbar, with a preview, and edits them on a double click', async () => {
    let doc: Doc | undefined;
    const { container } = renderWithApp(
      <Editor
        value={{
          type: 'doc',
          content: [
            {
              type: 'paragraph',
              content: [
                { type: 'text', text: 'Mass ' },
                { type: 'inlineMath', attrs: { latex: 'm' } },
              ],
            },
          ],
        }}
        onChange={(d) => (doc = d)}
        placeholder="Write"
      />,
    );
    // Formulas show in the editor as readers see them.
    await waitFor(() => expect(container.querySelector('.kogge-math-node .katex')).not.toBeNull());

    fireEvent.click(screen.getByRole('button', { name: 'Formula' }));
    const dialog = await screen.findByRole('dialog', { name: 'Formula' });
    const latex = screen.getByRole('textbox', { name: 'LaTeX' });
    fireEvent.change(latex, { target: { value: '\\frac{1' } });
    expect(await screen.findByText(/^The formula cannot be typeset: /)).toBeTruthy();
    fireEvent.change(latex, { target: { value: 'E = mc^2' } });
    await waitFor(() => expect(dialog.querySelector('.kogge-formula-preview .katex')).not.toBeNull());
    expect(screen.queryByText(/^The formula cannot be typeset: /)).toBeNull();
    fireEvent.click(screen.getByRole('radio', { name: 'On its own line' }));
    await waitFor(() => expect(dialog.querySelector('.kogge-formula-preview .katex-display')).not.toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'Insert' }));
    // Where the cursor was: at the start, as nothing was clicked.
    await waitFor(() => expect(formulas(doc)).toEqual(['math:E = mc^2', 'inlineMath:m']));

    // A double click on a formula edits it; Ctrl+Enter saves.
    const inline = container.querySelector('span.kogge-math-node .kogge-math-edit');
    act(() => {
      fireEvent.doubleClick(inline!);
    });
    const edit = screen.getByRole('textbox', { name: 'LaTeX' });
    expect((edit as HTMLTextAreaElement).value).toBe('m');
    // Where a formula stands is chosen when it is inserted.
    expect(screen.queryByRole('radio', { name: 'On its own line' })).toBeNull();
    fireEvent.change(edit, { target: { value: 'm_0' } });
    fireEvent.keyDown(edit, { key: 'Enter', ctrlKey: true });
    await waitFor(() => expect(formulas(doc)).toEqual(['math:E = mc^2', 'inlineMath:m_0']));
  });

  it('refuses formulas longer than the server keeps', async () => {
    renderWithApp(<Editor value={{ type: 'doc', content: [{ type: 'paragraph' }] }} placeholder="Write" />);
    fireEvent.click(screen.getByRole('button', { name: 'Formula' }));
    const latex = await screen.findByRole('textbox', { name: 'LaTeX' });
    fireEvent.change(latex, { target: { value: 'x'.repeat(16 * 1024 + 1) } });
    expect(screen.getByText('The formula is too long.')).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Insert' }) as HTMLButtonElement).disabled).toBe(true);
  });
});
