// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Extension } from '@tiptap/core';
import { Plugin, PluginKey } from '@tiptap/pm/state';
import type { Node as PMNode } from '@tiptap/pm/model';
import { Decoration, DecorationSet } from '@tiptap/pm/view';

/** Where another editor's selection is, in positions of this editor's document. */
export interface RemoteCaret {
  client: string;
  name: string;
  /** A colour of the palette (blue, teal, …). */
  color: string;
  anchor: number;
  head: number;
}

interface CaretsState {
  carets: Map<string, RemoteCaret>;
  decorations: DecorationSet;
}

/** Changes of the carets, given as the meta of a transaction. */
export type CaretsMeta = { set: RemoteCaret } | { remove: string } | { clear: true };

export const caretsKey = new PluginKey<CaretsState>('koggeCarets');

// The name is drawn by CSS (from data-name): it is not text of the document, for copying or reading aloud.
function caretElement(c: RemoteCaret): HTMLElement {
  const caret = document.createElement('span');
  caret.className = 'kogge-remote-caret';
  caret.style.setProperty('--caret-color', `var(--mantine-color-${c.color}-6)`);
  caret.dataset.name = c.name;
  caret.setAttribute('aria-hidden', 'true');
  return caret;
}

function decorate(doc: PMNode, carets: Map<string, RemoteCaret>): DecorationSet {
  const size = doc.content.size;
  const decorations: Decoration[] = [];
  for (const c of carets.values()) {
    const anchor = Math.max(0, Math.min(size, c.anchor));
    const head = Math.max(0, Math.min(size, c.head));
    if (anchor !== head) {
      decorations.push(
        Decoration.inline(Math.min(anchor, head), Math.max(anchor, head), {
          class: 'kogge-remote-selection',
          style: `--caret-color: var(--mantine-color-${c.color}-6)`,
        }),
      );
    }
    decorations.push(
      Decoration.widget(head, () => caretElement(c), {
        key: `${c.client}:${c.name}:${c.color}`,
        side: 1,
        ignoreSelection: true,
      }),
    );
  }
  return DecorationSet.create(doc, decorations);
}

/**
 * Shows the carets and selections of the other editors of a shared draft. They follow every change of the document
 * (the editor's own and the others'); the shared draft's connection sets and removes them.
 */
export const RemoteCarets = Extension.create({
  name: 'koggeRemoteCarets',
  addProseMirrorPlugins() {
    return [
      new Plugin<CaretsState>({
        key: caretsKey,
        state: {
          init: (_, state) => ({ carets: new Map(), decorations: DecorationSet.create(state.doc, []) }),
          apply(tr, value) {
            const meta = tr.getMeta(caretsKey) as CaretsMeta | undefined;
            if (!tr.docChanged && !meta) return value;
            let carets = value.carets;
            if (tr.docChanged) {
              carets = new Map(
                [...carets].map(([k, c]) => [
                  k,
                  { ...c, anchor: tr.mapping.map(c.anchor), head: tr.mapping.map(c.head) },
                ]),
              );
            }
            if (meta) {
              carets = new Map(carets);
              if ('set' in meta) carets.set(meta.set.client, meta.set);
              else if ('remove' in meta) carets.delete(meta.remove);
              else carets.clear();
            }
            return { carets, decorations: decorate(tr.doc, carets) };
          },
        },
        props: {
          decorations: (state) => caretsKey.getState(state)?.decorations,
        },
      }),
    ];
  },
});
