// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Editor, type JSONContent } from '@tiptap/core';
import { afterEach, describe, expect, it } from 'vitest';
import { CodeHighlight, forgetHighlights, highlightKey, type HighlightedLine } from './codeHighlight';
import { editorExtensions } from './extensions';

const editors: Editor[] = [];
afterEach(() => {
  for (const e of editors.splice(0)) e.destroy();
  forgetHighlights();
});

/** A highlighter like the server's: keywords and strings of Go, the rest plain. */
function fakeHighlighter(calls: string[]) {
  return async (language: string, text: string): Promise<HighlightedLine[]> => {
    calls.push(`${language}:${text}`);
    return text.split('\n').map((line) =>
      line
        .split(/(\bfunc\b|"[^"]*")/)
        .filter(Boolean)
        .map((t) => (t === 'func' ? { c: 'kd', t } : t.startsWith('"') ? { c: 's', t } : { t })),
    );
  };
}

function make(content: JSONContent[], calls: string[]) {
  const e = new Editor({
    element: document.createElement('div'),
    extensions: [
      ...editorExtensions('').filter((x) => x.name !== 'koggeCodeHighlight'),
      CodeHighlight.configure({ highlight: fakeHighlighter(calls), delay: 0 }),
    ],
    content: { type: 'doc', content },
  });
  editors.push(e);
  return e;
}

const settle = () => new Promise((r) => setTimeout(r, 20));
/** The token classes the editor decorates its code with, and the text of each. */
const tokens = (e: Editor) =>
  (highlightKey.getState(e.state)?.find() ?? []).map(
    (d) => `${(d.spec as { token: string }).token}:${e.state.doc.textBetween(d.from, d.to)}`,
  );
const classes = (e: Editor) => tokens(e).map((t) => t.slice(0, t.indexOf(':')));
const code = (language: string | null, text: string): JSONContent => ({
  type: 'codeBlock',
  attrs: { language },
  content: text ? [{ type: 'text', text }] : [],
});

describe('code highlighting while editing', () => {
  it('colours code blocks with the classes of the server, and only blocks of a language', async () => {
    const calls: string[] = [];
    const e = make(
      [
        code('go', 'func main() {\n\tprintln("hi")\n}'),
        code(null, 'plain text'),
        code('mermaid', 'graph TD'),
      ],
      calls,
    );
    await settle();
    expect(calls).toEqual(['go:func main() {\n\tprintln("hi")\n}']);
    expect(tokens(e)).toEqual(['kd:func', 's:"hi"']);
  });

  it('highlights a block again after it changed, keeping the old colours until then', async () => {
    const calls: string[] = [];
    const e = make([code('go', 'x := "a"')], calls);
    await settle();
    expect(classes(e)).toEqual(['s']);
    // Typing before the string moves its colour along at once; the new text is highlighted a moment later.
    e.commands.insertContentAt(1, 'func ');
    expect(classes(e)).toEqual(['s']);
    await settle();
    expect(calls).toHaveLength(2);
    expect(classes(e).sort()).toEqual(['kd', 's']);
    // Highlighting changes nothing of the document: there is nothing to undo but the typing.
    e.commands.undo();
    expect(e.state.doc.textContent).toBe('x := "a"');
    await settle();
    expect(calls).toHaveLength(2); // the earlier result is remembered
    expect(classes(e)).toEqual(['s']);
  });

  it('shows no colours from a result for other text', async () => {
    const e = new Editor({
      element: document.createElement('div'),
      extensions: [
        ...editorExtensions('').filter((x) => x.name !== 'koggeCodeHighlight'),
        CodeHighlight.configure({ highlight: async () => [[{ c: 'k', t: 'something else' }]], delay: 0 }),
      ],
      content: { type: 'doc', content: [code('go', 'func f()')] },
    });
    editors.push(e);
    await settle();
    expect(classes(e)).toEqual([]);
  });
});
