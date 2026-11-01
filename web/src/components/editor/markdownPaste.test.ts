// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Editor, type JSONContent } from '@tiptap/core';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { editorExtensions } from './extensions';
import { looksLikeMarkdown, markdownSlice, pasteMarkdown } from './markdownPaste';

// The server reads Markdown into a document; the tests answer for it when they want to.
const answers = vi.hoisted(() => ({ next: null as null | ((doc: unknown) => void) }));
vi.mock('../../api/client', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api/client')>();
  return {
    ...real,
    api: {
      ...real.api,
      POST: () =>
        new Promise((resolve) => {
          answers.next = (doc) => resolve({ data: { doc }, response: new Response(null, { status: 200 }) });
        }),
    },
  };
});

const editors: Editor[] = [];
afterEach(() => editors.splice(0).forEach((e) => e.destroy()));

function editor(text: string) {
  const e = new Editor({
    extensions: editorExtensions('', { wiki: true }),
    content: { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text }] }] },
  });
  editors.push(e);
  return e;
}

const t = (text: string) => ({ type: 'text', text });
// What the server answers for "## Steps\n\n- Build it\n- Ship it".
const steps = {
  type: 'doc',
  content: [
    { type: 'heading', attrs: { level: 2 }, content: [t('Steps')] },
    {
      type: 'bulletList',
      content: [
        { type: 'listItem', content: [{ type: 'paragraph', content: [t('Build it')] }] },
        { type: 'listItem', content: [{ type: 'paragraph', content: [t('Ship it')] }] },
      ],
    },
  ],
};

describe('looksLikeMarkdown', () => {
  it.each([
    ['## Steps\nBuild it.', true],
    ['- Build it\n- Ship it', true],
    ['1. Build it', true],
    ['- [ ] Ship it', true],
    ['> Quoted', true],
    ['```go\nfunc main() {}\n```', true],
    ['| A | B |\n|---|---|\n| 1 | 2 |', true],
    ['Use **bold** here', true],
    ['See [the guide](https://example.com/guide)', true],
    ['Run `make test` first', true],
    ['Hello world.', false],
    ['5 * 3 - 2 = 13', false],
    ['UTF-8 and SHA-256', false],
    ['Price: 10 - 5 EUR', false],
    // Formulas.
    ['Euler: $e^{i\\pi}+1=0$', true],
    ['Area $\\pi r^2$.', true],
    ['$x$', true],
    ['a $$x$$ b', true],
    ['$$\n\\int_0^1 x\\,dx\n$$', true],
    ['  $$x^2$$', true],
    ['$$ x^2 $$', true],
    // Dollars that are no formulas, or that the server would read as one only by chance.
    ['Price: $5 and $10', false],
    ['From $5-$10', false],
    ['US$5/$6', false],
    ['50 $ pro Monat', false],
    ['Costs $$$ a lot', false],
    ['$$ dollars', false],
    ['$$5 a month', false],
    ['$a = $b', false],
    ['echo $PATH', false],
    ['cp $HOME/$USER .', false],
    ['\\$x$', false],
    // The server reads the formula "n" here; pasted text keeps it, as it keeps anything doubtful.
    ['the $n$th element', false],
  ])('%j → %s', (text, markdown) => {
    expect(looksLikeMarkdown(text)).toBe(markdown);
  });
});

describe('pasting Markdown', () => {
  it('turns the server’s document into content that joins the paragraph, with links and images as allowed', () => {
    const e = editor('');
    const doc = {
      type: 'doc',
      content: [
        {
          type: 'paragraph',
          content: [
            t('Logo '),
            { type: 'inlineImage', attrs: { src: 'https://cdn.example.com/logo.png', alt: 'Kogge' } },
          ],
        },
      ],
    };
    const slice = markdownSlice(e, doc, false)!;
    expect(slice.openStart).toBeGreaterThan(0);
    expect(JSON.stringify(slice.content.toJSON())).not.toContain('inlineImage');
    expect(JSON.stringify(slice.content.toJSON())).toContain('https://cdn.example.com/logo.png');
    expect(markdownSlice(e, { type: 'doc', content: [{ type: 'nonsense' }] }, false)).toBeNull();
  });

  it('pastes where it was pasted, though the document changed until the server answered', async () => {
    const e = editor('Plan: ');
    e.commands.focus('end');
    pasteMarkdown(e, '## Steps\n\n- Build it\n- Ship it', false);
    // Another editor of the shared draft writes before the answer comes, in front of the paste.
    e.view.dispatch(e.state.tr.insertText('Release ', 1));
    answers.next!(steps);
    await vi.waitFor(() => expect(e.getText()).toContain('Ship it'));
    const json = e.getJSON() as { content: { type: string }[] };
    expect(e.getText()).toMatch(/^Release Plan: /);
    // The heading and the list are blocks of their own; writing goes on in the rest of the paragraph.
    expect(json.content.map((n) => n.type)).toEqual(['paragraph', 'heading', 'bulletList', 'paragraph']);
  });

  it('pastes formulas as formulas: within the paragraph pasted into, and of their own', async () => {
    const e = editor('Note: ');
    e.commands.focus('end');
    pasteMarkdown(e, 'Area $\\pi r^2$\n\n$$\n\\int_0^1 x\\,dx\n$$', false);
    answers.next!({
      type: 'doc',
      content: [
        { type: 'paragraph', content: [t('Area '), { type: 'inlineMath', attrs: { latex: '\\pi r^2' } }] },
        { type: 'math', attrs: { latex: '\\int_0^1 x\\,dx' } },
      ],
    });
    await vi.waitFor(() => expect(e.getText()).toContain('Area'));
    const [first, second] = (e.getJSON().content ?? []) as JSONContent[];
    expect(first.content).toEqual([t('Note: Area '), { type: 'inlineMath', attrs: { latex: '\\pi r^2' } }]);
    expect(second).toEqual({ type: 'math', attrs: { latex: '\\int_0^1 x\\,dx' } });
  });

  it('pastes the text as it is when the server cannot read it', async () => {
    const e = editor('');
    e.commands.focus('end');
    pasteMarkdown(e, '**bold**', false);
    answers.next!({ type: 'doc', content: [{ type: 'nonsense' }] });
    await vi.waitFor(() => expect(e.getText()).toBe('**bold**'));
  });
});
