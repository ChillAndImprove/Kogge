// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Extension } from '@tiptap/core';
import { closeHistory } from '@tiptap/pm/history';
import type { Node as PMNode } from '@tiptap/pm/model';
import { Plugin, PluginKey, TextSelection, type EditorState, type Transaction } from '@tiptap/pm/state';
import { Decoration, DecorationSet } from '@tiptap/pm/view';

/*
 * Find and replace in the editor. Matches are found in the text of each text block — across marks, never across
 * blocks or inline nodes such as mentions — and highlighted with decorations, which follow the document as it
 * changes: by one's own typing or by the steps of others editing the same draft. Replacing is an ordinary change
 * of the document (one change for all matches), which shared drafts send like typing and undo takes back at once.
 */

export interface FindOptions {
  caseSensitive: boolean;
  wholeWord: boolean;
}

export interface Match {
  from: number;
  to: number;
}

const escapeRegExp = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** The regular expression of a query: its text literally, ignoring case unless asked, whole words if asked. */
export function searchPattern(query: string, options: FindOptions): RegExp {
  let source = escapeRegExp(query);
  if (options.wholeWord) source = `(?<![\\p{L}\\p{N}_])${source}(?![\\p{L}\\p{N}_])`;
  return new RegExp(source, options.caseSensitive ? 'gu' : 'giu');
}

/** Stands for inline nodes other than text in the text of a block: no query matches it. */
const objectReplacement = '￼';

/** The matches of a query in a document, in document order. */
export function findMatches(doc: PMNode, query: string, options: FindOptions): Match[] {
  if (!query) return [];
  const pattern = searchPattern(query, options);
  const out: Match[] = [];
  doc.descendants((node, pos) => {
    if (!node.isTextblock) return true;
    let text = '';
    const positions: number[] = []; // the document position of each character of text
    node.forEach((child, offset) => {
      const start = pos + 1 + offset;
      if (child.isText && child.text) {
        text += child.text;
        for (let i = 0; i < child.text.length; i++) positions.push(start + i);
      } else {
        text += objectReplacement;
        positions.push(start);
      }
    });
    pattern.lastIndex = 0;
    for (let m = pattern.exec(text); m; m = pattern.exec(text)) {
      if (m[0].length === 0) {
        pattern.lastIndex++;
        continue;
      }
      out.push({ from: positions[m.index], to: positions[m.index + m[0].length - 1] + 1 });
    }
    return false;
  });
  return out;
}

export interface FindState {
  query: string;
  options: FindOptions;
  matches: Match[];
  /** The index of the current match, or -1. */
  current: number;
  decorations: DecorationSet;
}

type FindMeta =
  | { type: 'search'; query: string; options: FindOptions; from: number }
  | { type: 'select'; index: number }
  | { type: 'clear' };

export const findKey = new PluginKey<FindState>('koggeFind');

const emptyState: FindState = {
  query: '',
  options: { caseSensitive: false, wholeWord: false },
  matches: [],
  current: -1,
  decorations: DecorationSet.empty,
};

function decorate(doc: PMNode, matches: Match[], current: number): DecorationSet {
  if (!matches.length) return DecorationSet.empty;
  return DecorationSet.create(
    doc,
    matches.map((m, i) =>
      Decoration.inline(m.from, m.to, {
        class: i === current ? 'kogge-find-match kogge-find-current' : 'kogge-find-match',
      }),
    ),
  );
}

/** The index of the first match at or after a position (wrapping around), or -1. */
function matchAtOrAfter(matches: Match[], pos: number): number {
  if (!matches.length) return -1;
  const i = matches.findIndex((m) => m.from >= pos);
  return i < 0 ? 0 : i;
}

function withMatches(state: FindState, doc: PMNode, matches: Match[], current: number): FindState {
  return { ...state, matches, current, decorations: decorate(doc, matches, current) };
}

export function findPlugin() {
  return new Plugin<FindState>({
    key: findKey,
    state: {
      init: () => emptyState,
      apply(tr, prev, _old, next) {
        const meta = tr.getMeta(findKey) as FindMeta | undefined;
        if (meta?.type === 'clear') return emptyState;
        if (meta?.type === 'search') {
          const matches = findMatches(next.doc, meta.query, meta.options);
          return withMatches(
            { ...prev, query: meta.query, options: meta.options },
            next.doc,
            matches,
            matchAtOrAfter(matches, meta.from),
          );
        }
        if (meta?.type === 'select') {
          return withMatches(prev, next.doc, prev.matches, meta.index);
        }
        if (!tr.docChanged || !prev.query) return prev;
        // The document changed (here or in another's editor): the matches are those of the new text, and the
        // current one stays where it was.
        const matches = findMatches(next.doc, prev.query, prev.options);
        const was = prev.matches[prev.current];
        const current = was ? matchAtOrAfter(matches, tr.mapping.map(was.from, -1)) : matches.length ? 0 : -1;
        return withMatches(prev, next.doc, matches, current);
      },
    },
    props: {
      decorations: (state) => findKey.getState(state)?.decorations,
    },
  });
}

export function findState(state: EditorState): FindState {
  return findKey.getState(state) ?? emptyState;
}

/** Selects a match and scrolls to it. */
function selectMatch(tr: Transaction, m: Match, index: number): Transaction {
  return tr
    .setSelection(TextSelection.create(tr.doc, m.from, m.to))
    .setMeta(findKey, { type: 'select', index } satisfies FindMeta)
    .scrollIntoView();
}

/** Replaces a match with text in the marks of its first character (a replacement keeps bold, links, comments). */
function replaceMatch(tr: Transaction, m: Match, replacement: string) {
  if (!replacement) {
    tr.delete(m.from, m.to);
    return;
  }
  const marks = tr.doc.nodeAt(m.from)?.marks ?? [];
  tr.replaceWith(m.from, m.to, tr.doc.type.schema.text(replacement, marks));
}

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    findReplace: {
      /** Searches the document; the current match is the first at or after the cursor. */
      setSearch: (query: string, options: FindOptions) => ReturnType;
      /** Selects the next (or previous) match and scrolls to it. */
      findNext: (direction?: 1 | -1) => ReturnType;
      /** Replaces the current match, then selects the next one. */
      replaceCurrent: (replacement: string) => ReturnType;
      /** Replaces every match, as one change. */
      replaceAll: (replacement: string) => ReturnType;
      /** Ends the search: no more highlights. */
      clearSearch: () => ReturnType;
    };
  }
}

export const FindReplace = Extension.create({
  name: 'koggeFindReplace',
  addProseMirrorPlugins() {
    return [findPlugin()];
  },
  addCommands() {
    return {
      setSearch:
        (query, options) =>
        ({ state, tr, dispatch }) => {
          if (dispatch) {
            tr.setMeta(findKey, {
              type: 'search',
              query,
              options,
              from: state.selection.from,
            } satisfies FindMeta);
            tr.setMeta('addToHistory', false);
          }
          return true;
        },
      findNext:
        (direction = 1) =>
        ({ state, tr, dispatch }) => {
          const s = findState(state);
          if (!s.matches.length) return false;
          let index = s.current;
          if (index < 0) index = 0;
          else {
            // Next from the current match, or from where the cursor moved to since.
            const sel = state.selection;
            const cur = s.matches[index];
            const onCurrent = sel.from === cur.from && sel.to === cur.to;
            if (onCurrent) index = (index + direction + s.matches.length) % s.matches.length;
            else if (direction > 0) index = matchAtOrAfter(s.matches, sel.to);
            else {
              const before = s.matches.findLastIndex((m) => m.to <= sel.from);
              index = before < 0 ? s.matches.length - 1 : before;
            }
          }
          if (dispatch) selectMatch(tr, s.matches[index], index);
          return true;
        },
      replaceCurrent:
        (replacement) =>
        ({ state, tr, dispatch }) => {
          const s = findState(state);
          const m = s.matches[s.current];
          if (!m) return false;
          if (dispatch) {
            replaceMatch(tr, m, replacement);
            closeHistory(tr);
            // The next match after the replacement becomes current.
            const after = tr.mapping.map(m.to);
            const next = findMatches(tr.doc, s.query, s.options);
            const index = matchAtOrAfter(next, after);
            tr.setMeta(findKey, {
              type: 'search',
              query: s.query,
              options: s.options,
              from: after,
            } satisfies FindMeta);
            if (index >= 0)
              tr.setSelection(
                TextSelection.create(tr.doc, next[index].from, next[index].to),
              ).scrollIntoView();
          }
          return true;
        },
      replaceAll:
        (replacement) =>
        ({ state, tr, dispatch }) => {
          const s = findState(state);
          if (!s.matches.length) return false;
          if (dispatch) {
            // From the last match to the first, so that the positions of the others stay as they were.
            for (let i = s.matches.length - 1; i >= 0; i--) replaceMatch(tr, s.matches[i], replacement);
            closeHistory(tr);
            tr.setMeta(findKey, {
              type: 'search',
              query: s.query,
              options: s.options,
              from: 0,
            } satisfies FindMeta);
          }
          return true;
        },
      clearSearch:
        () =>
        ({ tr, dispatch }) => {
          if (dispatch)
            tr.setMeta(findKey, { type: 'clear' } satisfies FindMeta).setMeta('addToHistory', false);
          return true;
        },
    };
  },
});
