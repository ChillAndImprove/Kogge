// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Editor } from '@tiptap/core';
import { afterEach, describe, expect, it } from 'vitest';
import { editorExtensions } from './extensions';
import { safeImage, safeLink } from './safe';

const editors: Editor[] = [];
afterEach(() => editors.splice(0).forEach((e) => e.destroy()));

function editor(externalImages = false) {
  const e = new Editor({
    extensions: editorExtensions('', { wiki: true, images: true, externalImages }),
    content: { type: 'doc', content: [{ type: 'paragraph' }] },
  });
  editors.push(e);
  return e;
}

type Json = {
  type: string;
  text?: string;
  attrs?: Record<string, unknown>;
  marks?: Json[];
  content?: Json[];
};

/** The nodes of a document in order, flattened. */
function nodes(doc: Json): Json[] {
  return [doc, ...(doc.content ?? []).flatMap(nodes)];
}

/** Pastes HTML as the browser does (jsdom has no ClipboardEvent; ProseMirror only needs an event). */
const paste = (e: Editor, html: string) => e.view.pasteHTML(html, new Event('paste') as ClipboardEvent);

const links = (e: Editor) =>
  nodes(e.getJSON() as Json).flatMap((n) =>
    (n.marks ?? []).filter((m) => m.type === 'link').map((m) => `${n.text} → ${m.attrs?.href}`),
  );
const images = (e: Editor) =>
  nodes(e.getJSON() as Json)
    .filter((n) => n.type === 'image' || n.type === 'inlineImage')
    .map((n) => String(n.attrs?.src));

describe('safeLink', () => {
  // The cases of the server's TestLinksAndAlignment, and what pasted content brings along.
  it.each([
    ['https://example.com/x', true],
    ['/browse/SHOP-1', true],
    ['#top', true],
    ['mailto:a@example.com', true],
    ['//evil.example', false],
    ['/\\evil.example', false],
    ['/\t/evil.example', false],
    ['javascript:alert(1)', false],
    ['https://ok.example/a\\b', false],
    ['https:evil', false],
    ['tel:+4989123456', false],
    ['ftp://files.example.com/a', false],
    ['docs/guide.md', false],
    ['/files/%zz', false],
  ])('%s → %s', (href, allowed) => {
    expect(safeLink(href)).toBe(allowed);
  });
});

describe('safeImage', () => {
  it('allows paths of this server, and other servers only where the administration allows them', () => {
    expect(safeImage('/api/v1/wiki/attachments/3', false)).toBe(true);
    expect(safeImage('//evil.example/a.png', false)).toBe(false);
    expect(safeImage('https://cdn.example.com/a.png', false)).toBe(false);
    expect(safeImage('https://cdn.example.com/a.png', true)).toBe(true);
    expect(safeImage('data:image/png;base64,AAAA', true)).toBe(false);
  });
});

describe('the editor', () => {
  it('keeps pasted links and images the server would refuse out of the document', () => {
    const e = editor();
    paste(
      e,
      '<p><a href="tel:+4989123456">Call us</a>, <a href="https://kogge.example.com/x">read</a>, ' +
        '<a href="docs/guide.md">guide</a> <img src="https://cdn.example.com/logo.png" alt="Logo"></p>',
    );
    expect(links(e)).toEqual([
      'read → https://kogge.example.com/x',
      'Logo → https://cdn.example.com/logo.png',
    ]);
    expect(images(e)).toEqual([]);
    expect(e.getText()).toContain('Call us');
    expect(e.getText()).toContain('guide');
  });

  it('keeps pasted images of this server, as paths, and those of other servers where they are allowed', () => {
    const own = `${window.location.origin}/api/v1/wiki/attachments/7/download`;
    const e = editor();
    paste(e, `<img src="${own}" alt="Chart">`);
    expect(images(e)).toEqual(['/api/v1/wiki/attachments/7/download']);

    const external = editor(true);
    paste(external, '<img src="https://cdn.example.com/logo.png" alt="Logo">');
    expect(images(external)).toEqual(['https://cdn.example.com/logo.png']);
  });

  it('makes no links or images of what the server refuses while typing or by command', () => {
    const e = editor();
    e.commands.insertContent('Call');
    e.commands.selectAll();
    expect(e.commands.setLink({ href: 'tel:+4989123456' })).toBe(false);
    expect(e.commands.setLink({ href: 'https://example.com' })).toBe(true);
    expect(links(e)).toEqual(['Call → https://example.com']);

    const typed = editor();
    typed.commands.focus('end');
    for (const ch of '![Logo](https://cdn.example.com/logo.png)') {
      const { from, to } = typed.state.selection;
      if (
        !typed.view.someProp('handleTextInput', (f) =>
          f(typed.view, from, to, ch, () => typed.state.tr.insertText(ch, from, to)),
        )
      ) {
        typed.view.dispatch(typed.state.tr.insertText(ch, from, to));
      }
    }
    expect(images(typed)).toEqual([]);
    expect(typed.getText()).toContain('![Logo](https://cdn.example.com/logo.png)');
  });
});
