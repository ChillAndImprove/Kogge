// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Editor, type JSONContent } from '@tiptap/core';
import { NodeSelection } from '@tiptap/pm/state';
import { afterEach, describe, expect, it } from 'vitest';
import { maxLatexBytes } from '../math/latex';
import { editorExtensions } from './extensions';
import { typedFormula } from './math';
import { onSlashRequest, type SlashRequest } from './SlashMenu';

const editors: Editor[] = [];
afterEach(() => editors.splice(0).forEach((e) => e.destroy()));

function editor(content: JSONContent = { type: 'doc', content: [{ type: 'paragraph' }] }) {
  const e = new Editor({ extensions: editorExtensions(''), content });
  editors.push(e);
  return e;
}

/** Types text as the browser does: through the input rules, or plainly where none applies. */
function type(e: Editor, text: string) {
  for (const ch of text) {
    const { from, to } = e.state.selection;
    const insert = () => e.state.tr.insertText(ch, from, to);
    if (!e.view.someProp('handleTextInput', (f) => f(e.view, from, to, ch, insert)))
      e.view.dispatch(insert());
  }
}

const key = (e: Editor, k: string) =>
  e.view.someProp('handleKeyDown', (f) => f(e.view, new KeyboardEvent('keydown', { key: k })));

/** Pastes HTML as the browser does (jsdom has no ClipboardEvent; ProseMirror only needs an event). */
const paste = (e: Editor, html: string) => e.view.pasteHTML(html, new Event('paste') as ClipboardEvent);

/** The document as JSON, without the attributes that are not set (alignments). */
function json(e: Editor): JSONContent {
  const clean = (n: JSONContent): JSONContent => {
    const attrs = Object.entries(n.attrs ?? {}).filter(([, v]) => v !== null);
    return {
      ...n,
      attrs: attrs.length ? Object.fromEntries(attrs) : undefined,
      content: n.content?.map(clean),
    };
  };
  return JSON.parse(JSON.stringify(clean(e.getJSON()))) as JSONContent;
}

const p = (...content: JSONContent[]): JSONContent =>
  content.length ? { type: 'paragraph', content } : { type: 'paragraph' };
const t = (text: string): JSONContent => ({ type: 'text', text });
const doc = (...content: JSONContent[]): JSONContent => ({ type: 'doc', content });

describe('typedFormula', () => {
  it('finds the formula that the text ends with, by the rules of Markdown', () => {
    expect(typedFormula('$x$')).toEqual({ index: 0, latex: 'x' });
    expect(typedFormula('Area: $\\pi r^2$')).toEqual({ index: 6, latex: '\\pi r^2' });
    expect(typedFormula('see $$a+b$$')).toEqual({ index: 4, latex: 'a+b' });
    // A backslash takes the next character with it, also a dollar.
    expect(typedFormula('$a\\$b$')).toEqual({ index: 0, latex: 'a\\$b' });
    // Formulas before it pair their dollars first.
    expect(typedFormula('$a$ and $b$')).toEqual({ index: 8, latex: 'b' });
    expect(typedFormula('($x$')).toEqual({ index: 1, latex: 'x' });
  });

  it('leaves dollars that do not make a formula', () => {
    for (const text of [
      '$x', // not closed yet
      '$$x$', // $$ waits for $$
      '$$',
      '\\$x$', // escaped
      '$ x$', // a space after the opening dollar
      '$x $', // a space before the closing one
      'costs $5 and $',
      'from $5-$',
      'US$5/$', // a letter before the opening dollar
      'a$b$',
      '$1,000$',
      'line\n$x\ny$', // a formula ends with its line
    ]) {
      expect(typedFormula(text), text).toBeNull();
    }
  });
});

describe('formulas in the editor', () => {
  it('come of typed $…$, and Backspace takes the conversion back', () => {
    const e = editor();
    e.commands.focus('end');
    type(e, 'Energy $E=mc^2$');
    expect(json(e)).toEqual(doc(p(t('Energy '), { type: 'inlineMath', attrs: { latex: 'E=mc^2' } })));
    key(e, 'Backspace');
    expect(json(e)).toEqual(doc(p(t('Energy $E=mc^2$'))));
  });

  it('do not come of amounts, escaped dollars or code', () => {
    for (const text of ['from $5-$10', 'pay $5$', 'an escaped \\$x$', 'US$5/$6']) {
      const e = editor();
      e.commands.focus('end');
      type(e, text);
      expect(json(e), text).toEqual(doc(p(t(text))));
    }

    const code = editor(doc({ type: 'codeBlock', attrs: { language: 'tex' }, content: [t('a ')] }));
    code.commands.focus('end');
    type(code, '$x$');
    expect(code.state.doc.firstChild?.textContent).toBe('a $x$');
  });

  it('ask for a formula on its own line where $$ is followed by Enter or a space', () => {
    const e = editor(doc(p(t('Before')), p()));
    const requests: SlashRequest[] = [];
    onSlashRequest(e, (r) => requests.push(r));
    e.commands.focus('end');
    type(e, '$$');
    key(e, 'Enter');
    expect(requests).toEqual(['math']);
    expect(json(e)).toEqual(doc(p(t('Before')), p()));
    // The dialog inserts the formula in place of the empty paragraph, the cursor after it.
    e.commands.insertMath('x^2', true);
    expect(json(e)).toEqual(doc(p(t('Before')), { type: 'math', attrs: { latex: 'x^2' } }, p()));
    expect(e.state.selection.$from.parent.type.name).toBe('paragraph');

    type(e, '$$ ');
    expect(requests).toEqual(['math', 'math']);
    expect(e.state.selection.$from.parent.textContent).toBe('');
  });

  it('leave $$ to Enter where it is not alone or no toolbar asks', () => {
    const e = editor();
    e.commands.focus('end');
    type(e, '$$');
    key(e, 'Enter');
    expect(json(e)).toEqual(doc(p(t('$$')), p()));

    const other = editor(doc(p(t('a'))));
    const requests: SlashRequest[] = [];
    onSlashRequest(other, (r) => requests.push(r));
    other.commands.focus('end');
    type(other, '$$');
    key(other, 'Enter');
    expect(requests).toEqual([]);
  });

  it('are edited with Enter while selected', () => {
    const e = editor(doc(p(t('a'), { type: 'inlineMath', attrs: { latex: 'x' } })));
    const requests: SlashRequest[] = [];
    onSlashRequest(e, (r) => requests.push(r));
    e.view.dispatch(e.state.tr.setSelection(NodeSelection.create(e.state.doc, 2)));
    key(e, 'Enter');
    expect(requests).toEqual(['inlineMath']);
    expect(e.commands.updateMath('y^2')).toBe(true);
    expect(json(e)).toEqual(doc(p(t('a'), { type: 'inlineMath', attrs: { latex: 'y^2' } })));
    // Nothing selected, nothing to update.
    e.commands.setTextSelection(1);
    expect(e.commands.updateMath('z')).toBe(false);
  });

  it('are inserted within the text, and never longer than the server keeps', () => {
    const e = editor(doc(p(t('ab'))));
    e.commands.setTextSelection(2);
    expect(e.commands.insertMath('\\alpha', false)).toBe(true);
    expect(json(e)).toEqual(doc(p(t('a'), { type: 'inlineMath', attrs: { latex: '\\alpha' } }, t('b'))));
    expect(e.commands.insertMath('x'.repeat(maxLatexBytes + 1), false)).toBe(false);
    // Bytes of UTF-8 count, not characters.
    expect(e.commands.insertMath('ä'.repeat(maxLatexBytes / 2 + 1), true)).toBe(false);
    expect(e.commands.insertMath('ä'.repeat(maxLatexBytes / 2), true)).toBe(true);
  });

  it('are written as $…$ and $$ lines in text, and keep their LaTeX through HTML', () => {
    const content = doc(
      p(t('Euler: '), { type: 'inlineMath', attrs: { latex: 'e^{i\\pi}+1=0' } }),
      { type: 'math', attrs: { latex: '\\sum_{k=1}^n k = \\frac{n(n+1)}{2}' } },
      p(t('The end.')),
    );
    const e = editor(content);
    expect(e.getText()).toBe(
      'Euler: $e^{i\\pi}+1=0$\n\n$$\n\\sum_{k=1}^n k = \\frac{n(n+1)}{2}\n$$\n\nThe end.',
    );
    const again = editor();
    again.commands.setContent(e.getHTML());
    expect(json(again)).toEqual(content);
  });

  it('come of formulas pasted from pages that KaTeX typesets, and of the read view', () => {
    const annotation = (latex: string) =>
      `<span class="katex-mathml"><math><semantics><mrow><mi>x</mi></mrow><annotation encoding="application/x-tex">${latex}</annotation></semantics></math></span><span class="katex-html" aria-hidden="true">x</span>`;
    const katex = editor();
    paste(
      katex,
      `<p>Inline <span class="katex">${annotation('x^2')}</span> here</p><span class="katex-display"><span class="katex">${annotation('\\int f')}</span></span>`,
    );
    const pasted = JSON.stringify(json(katex));
    expect(pasted).toContain('{"type":"inlineMath","attrs":{"latex":"x^2"}}');
    expect(pasted).toContain('{"type":"math","attrs":{"latex":"\\\\int f"}}');

    // Kogge's read view: the source is on the formula's element.
    const read = editor();
    paste(
      read,
      `<p>A <span class="kogge-math kogge-math-inline" data-inline-math="" data-latex="a&amp;b"><span class="kogge-math-box"><span class="katex">${annotation('ignored')}</span></span></span></p><div class="kogge-math kogge-math-block" data-math="" data-latex="\\frac 1 2"></div>`,
    );
    const fromRead = JSON.stringify(json(read));
    expect(fromRead).toContain('{"type":"inlineMath","attrs":{"latex":"a&b"}}');
    expect(fromRead).toContain('{"type":"math","attrs":{"latex":"\\\\frac 1 2"}}');

    // Longer than the server keeps: what it shows stays, as text.
    const long = editor();
    paste(long, `<p><span class="katex">${annotation('y'.repeat(maxLatexBytes + 1))}</span></p>`);
    expect(JSON.stringify(json(long))).not.toContain('Math');
  });
});
