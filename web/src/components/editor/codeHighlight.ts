// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Extension } from '@tiptap/core';
import type { Node as PMNode } from '@tiptap/pm/model';
import { Plugin, PluginKey, type EditorState } from '@tiptap/pm/state';
import { Decoration, DecorationSet, type EditorView } from '@tiptap/pm/view';
import { api, unwrap } from '../../api/client';

/*
 * Highlighting of code blocks while they are edited, by the highlighter that pages show code blocks with: the
 * server's (chroma, POST /api/v1/code/highlight), with its token classes and colours (styles/highlight.css). So
 * the editor knows the same languages and colours as the page, and the browser loads no highlighter of its own.
 * A block is highlighted again a moment after it changed; until then its old highlighting moves along with the
 * text. Highlights are decorations: they are not part of the document and travel to nobody.
 */

/** A line of highlighted code: tokens with chroma's class ("" for plain text). */
export type HighlightedLine = { c?: string; t: string }[];

export type Highlighter = (language: string, text: string) => Promise<HighlightedLine[] | null>;

const serverHighlighter: Highlighter = async (language, text) =>
  (await unwrap(api.POST('/api/v1/code/highlight', { body: { language, text } }))).lines;

/** The server highlights up to 64 KiB; longer code stays plain, as on pages. */
const maxLength = 65536;

/** Results by language and text, shared by the editors of a page; a few hundred at most. */
const results = new Map<string, HighlightedLine[] | null>();
const maxResults = 300;

function remember(key: string, lines: HighlightedLine[] | null) {
  if (results.size >= maxResults) results.delete(results.keys().next().value as string);
  results.set(key, lines);
}

const keyOf = (language: string, text: string) => `${language}\u0000${text}`;

function languageOf(node: PMNode): string {
  const l = String(node.attrs.language ?? '')
    .trim()
    .toLowerCase();
  // Diagrams are drawn, not highlighted.
  return l === 'mermaid' ? '' : l;
}

/** The decorations of a code block at pos from its highlighted lines; none if they are not of its text. */
export function tokenDecorations(pos: number, node: PMNode, lines: HighlightedLine[]): Decoration[] {
  const out: Decoration[] = [];
  let offset = pos + 1;
  let text = '';
  lines.forEach((line, i) => {
    if (i > 0) {
      offset++;
      text += '\n';
    }
    for (const tok of line) {
      if (tok.c)
        out.push(Decoration.inline(offset, offset + tok.t.length, { class: tok.c }, { token: tok.c }));
      offset += tok.t.length;
      text += tok.t;
    }
  });
  // The server drops a last line break; anything else that differs means the result is of other text.
  return text === node.textContent.replace(/\n$/, '') ? out : [];
}

export const highlightKey = new PluginKey<DecorationSet>('koggeCodeHighlight');

/** The decorations of a document: of the results there are, else those of before, moved with the text. */
function build(doc: PMNode, previous: DecorationSet | null): DecorationSet {
  const decos: Decoration[] = [];
  doc.descendants((node, pos) => {
    if (node.type.name !== 'codeBlock') return true;
    const language = languageOf(node);
    const text = node.textContent;
    if (language && text && text.length <= maxLength) {
      const lines = results.get(keyOf(language, text));
      if (lines) decos.push(...tokenDecorations(pos, node, lines));
      else if (lines === undefined && previous) decos.push(...previous.find(pos, pos + node.nodeSize));
    }
    return false;
  });
  return decos.length ? DecorationSet.create(doc, decos) : DecorationSet.empty;
}

/** The code blocks that need highlighting. */
function wanted(state: EditorState): { language: string; text: string }[] {
  const out: { language: string; text: string }[] = [];
  state.doc.descendants((node) => {
    if (node.type.name !== 'codeBlock') return true;
    const language = languageOf(node);
    const text = node.textContent;
    if (language && text && text.length <= maxLength && !results.has(keyOf(language, text)))
      out.push({ language, text });
    return false;
  });
  return out;
}

export function codeHighlightPlugin(highlight: Highlighter, delay = 250) {
  const pending = new Set<string>();
  return new Plugin<DecorationSet>({
    key: highlightKey,
    state: {
      init: (_, state) => build(state.doc, null),
      apply(tr, set) {
        if (tr.getMeta(highlightKey)) return build(tr.doc, set.map(tr.mapping, tr.doc));
        if (!tr.docChanged) return set;
        return build(tr.doc, set.map(tr.mapping, tr.doc));
      },
    },
    view(view: EditorView) {
      let timer: ReturnType<typeof setTimeout> | undefined;
      let destroyed = false;
      // A few requests at a time, so that a page with many code blocks does not ask for all of them at once.
      const run = () => {
        for (const { language, text } of wanted(view.state)) {
          const key = keyOf(language, text);
          if (pending.has(key)) continue;
          if (pending.size >= 4) return;
          pending.add(key);
          highlight(language, text)
            .then((lines) => remember(key, lines && lines.length ? lines : null))
            // Highlighting is a nicety: without the server, code stays plain.
            .catch(() => remember(key, null))
            .finally(() => {
              pending.delete(key);
              if (destroyed) return;
              view.dispatch(view.state.tr.setMeta(highlightKey, true).setMeta('addToHistory', false));
              run();
            });
        }
      };
      const schedule = () => {
        clearTimeout(timer);
        timer = setTimeout(run, delay);
      };
      schedule();
      return {
        update(v, prev) {
          if (v.state.doc !== prev.doc) schedule();
        },
        destroy() {
          destroyed = true;
          clearTimeout(timer);
        },
      };
    },
    props: {
      decorations: (state) => highlightKey.getState(state),
    },
  });
}

export interface CodeHighlightOptions {
  highlight: Highlighter;
  /** How long after a change the block is highlighted again (milliseconds). */
  delay: number;
}

/** Highlights code blocks as pages show them. */
export const CodeHighlight = Extension.create<CodeHighlightOptions>({
  name: 'koggeCodeHighlight',
  addOptions: () => ({ highlight: serverHighlighter, delay: 250 }),
  addProseMirrorPlugins() {
    return [codeHighlightPlugin(this.options.highlight, this.options.delay)];
  },
});

/** Forgets the results (for tests). */
export function forgetHighlights() {
  results.clear();
}
