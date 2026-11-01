// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { mergeAttributes, Node, type Editor } from '@tiptap/core';
import { Fragment, Slice, type Node as PMNode, type ResolvedPos } from '@tiptap/pm/model';
import { GapCursor } from '@tiptap/pm/gapcursor';
import {
  Plugin,
  PluginKey,
  Selection,
  TextSelection,
  type EditorState,
  type Transaction,
} from '@tiptap/pm/state';

/*
 * Layouts: a "columns" block with two or three columns side by side, each holding ordinary blocks.
 * Layouts stand at the top level of a document only — the document is "(block | columns)+" and a column holds
 * "block+", which has no columns — as the server's document model requires (internal/docmodel). Columns stack on
 * narrow screens (editor.css).
 */

export type ColumnLayout = 'equal' | 'left' | 'right' | 'sides';

/** The layouts of two and three columns: equal widths, or a narrow column on the left, right or both sides. */
export const columnLayouts: Record<number, ColumnLayout[]> = {
  2: ['equal', 'left', 'right'],
  3: ['equal', 'sides'],
};

/** The document: blocks and layouts. */
export const KoggeDocument = Node.create({
  name: 'doc',
  topNode: true,
  content: '(block | columns)+',
});

export const Column = Node.create({
  name: 'column',
  content: 'block+',
  // Backspace at the start of a column, or Delete at its end, stays in the column.
  isolating: true,
  parseHTML() {
    return [{ tag: 'div[data-column]' }];
  },
  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-column': '', class: 'kogge-column' }), 0];
  },
});

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    columns: {
      /** Inserts a layout below the block of the cursor (in its place if that is an empty paragraph). */
      insertColumns: (count: 2 | 3, layout?: ColumnLayout) => ReturnType;
      /** Changes the widths of the layout of the cursor. */
      setColumnsLayout: (layout: ColumnLayout) => ReturnType;
      /** Gives the layout of the cursor two or three columns; the content of a third column moves to the second. */
      setColumnCount: (count: 2 | 3) => ReturnType;
      /** Removes the layout of the cursor and keeps its content, one column after another. */
      unsetColumns: () => ReturnType;
      /** Deletes the layout of the cursor with its content. */
      deleteColumns: () => ReturnType;
    };
  }
}

/** The layout around a position: the columns node, where it starts, and the column the position is in. */
export interface ColumnsAt {
  node: PMNode;
  pos: number;
  column: number;
}

export function columnsAt($pos: ResolvedPos): ColumnsAt | null {
  for (let d = $pos.depth; d > 0; d--) {
    if ($pos.node(d).type.name === 'columns') {
      return { node: $pos.node(d), pos: $pos.before(d), column: d < $pos.depth ? $pos.index(d) : 0 };
    }
  }
  return null;
}

/** The start of the content of a column of a layout that starts at pos. */
function columnStart(layout: PMNode, pos: number, index: number): number {
  let p = pos + 1;
  for (let i = 0; i < index; i++) p += layout.child(i).nodeSize;
  return p + 1;
}

function newColumn(state: EditorState): PMNode {
  return state.schema.nodes.column.create(null, state.schema.nodes.paragraph.create());
}

/** Whether every column of a layout holds nothing but an empty paragraph. */
function emptyLayout(layout: PMNode): boolean {
  let empty = true;
  layout.forEach((col) => {
    if (col.childCount !== 1 || col.firstChild?.type.name !== 'paragraph' || col.firstChild.content.size > 0)
      empty = false;
  });
  return empty;
}

/** The selection after a layout: in the block that follows, or a gap cursor where nothing follows (layouts are
 * isolating and the document's default block is a paragraph, so a gap cursor is valid there). */
function selectionAfter(doc: PMNode, after: number): Selection {
  const $after = doc.resolve(after);
  return $after.nodeAfter ? Selection.near($after, 1) : new GapCursor($after);
}

function selectionBefore(doc: PMNode, before: number): Selection {
  const $before = doc.resolve(before);
  return $before.nodeBefore ? Selection.near($before, -1) : new GapCursor($before);
}

/** Pasted layouts lose their columns where layouts cannot stand: anywhere but between top-level blocks. */
export function flattenColumns(fragment: Fragment): Fragment {
  const out: PMNode[] = [];
  fragment.forEach((node) => {
    if (node.type.name === 'columns') node.forEach((col) => col.content.forEach((b) => out.push(b)));
    else if (node.type.name === 'column') node.content.forEach((b) => out.push(b));
    else out.push(node.copy(flattenColumns(node.content)));
  });
  return Fragment.fromArray(out);
}

function containsColumns(fragment: Fragment): boolean {
  let found = false;
  fragment.descendants((n) => {
    if (n.type.name === 'columns' || n.type.name === 'column') found = true;
    return !found;
  });
  return found;
}

export function flattenSlice(slice: Slice): Slice {
  if (!containsColumns(slice.content)) return slice;
  // Flattening removes the levels of the layout: a slice open into them is open less deeply.
  const removed = (n: PMNode | null) => (n?.type.name === 'columns' ? 2 : n?.type.name === 'column' ? 1 : 0);
  return new Slice(
    flattenColumns(slice.content),
    Math.max(0, slice.openStart - removed(slice.content.firstChild)),
    Math.max(0, slice.openEnd - removed(slice.content.lastChild)),
  );
}

/** Moves the cursor to the start of another column; false when there is none. */
function goToColumn(state: EditorState, dispatch: ((tr: Transaction) => void) | undefined, step: 1 | -1) {
  const { $from } = state.selection;
  const at = columnsAt($from);
  if (!at) return false;
  // Lists and tables have their own Tab.
  for (let d = $from.depth; d > 0; d--) {
    const name = $from.node(d).type.name;
    if (name === 'listItem' || name === 'taskItem' || name === 'tableCell' || name === 'tableHeader')
      return false;
    if (name === 'column') break;
  }
  const target = at.column + step;
  if (target < 0 || target >= at.node.childCount) return false;
  if (dispatch) {
    const start = columnStart(at.node, at.pos, target);
    dispatch(state.tr.setSelection(Selection.near(state.doc.resolve(start), 1)).scrollIntoView());
  }
  return true;
}

export const Columns = Node.create({
  name: 'columns',
  content: 'column{2,3}',
  isolating: true,
  addAttributes() {
    return {
      layout: {
        default: 'equal',
        parseHTML: (el) => el.getAttribute('data-layout') ?? 'equal',
        renderHTML: (a) => ({ 'data-layout': a.layout }),
      },
    };
  },
  parseHTML() {
    return [{ tag: 'div[data-columns]', contentElement: '.kogge-columns-row' }];
  },
  renderHTML({ HTMLAttributes }) {
    // The row is the flex box; the layout around it measures the width it has, for stacking (editor.css).
    return [
      'div',
      mergeAttributes(HTMLAttributes, { 'data-columns': '', class: 'kogge-columns' }),
      ['div', { class: 'kogge-columns-row' }, 0],
    ];
  },
  addCommands() {
    return {
      insertColumns:
        (count, layout = 'equal') =>
        ({ state, tr, dispatch }) => {
          const { $from } = state.selection;
          if (columnsAt($from)) return false; // layouts do not nest
          const cols = Array.from({ length: count }, () => newColumn(state));
          const node = state.schema.nodes.columns.create(
            { layout: columnLayouts[count].includes(layout) ? layout : 'equal' },
            cols,
          );
          if (dispatch) {
            let at: number;
            if ($from.depth === 0) {
              at = $from.pos;
              tr.insert(at, node);
            } else {
              const block = $from.node(1);
              const before = $from.before(1);
              if (block.type.name === 'paragraph' && block.content.size === 0) {
                at = before;
                tr.replaceWith(before, before + block.nodeSize, node);
              } else {
                at = before + block.nodeSize;
                tr.insert(at, node);
              }
            }
            // Into the paragraph of the first column: columns, column, paragraph.
            tr.setSelection(TextSelection.create(tr.doc, at + 3)).scrollIntoView();
          }
          return true;
        },
      setColumnsLayout:
        (layout) =>
        ({ state, tr, dispatch }) => {
          const at = columnsAt(state.selection.$from);
          if (!at || !columnLayouts[at.node.childCount]?.includes(layout)) return false;
          if (at.node.attrs.layout === layout) return true;
          if (dispatch) tr.setNodeAttribute(at.pos, 'layout', layout);
          return true;
        },
      setColumnCount:
        (count) =>
        ({ state, tr, dispatch }) => {
          const at = columnsAt(state.selection.$from);
          if (!at) return false;
          const n = at.node.childCount;
          if (n === count) return true;
          if (dispatch) {
            if (count > n) {
              tr.insert(at.pos + at.node.nodeSize - 1, newColumn(state));
            } else {
              // The third column's content moves to the end of the second; nothing is lost.
              const third = at.node.child(2);
              const secondEnd = columnStart(at.node, at.pos, 1) + at.node.child(1).content.size;
              const thirdStart = secondEnd + 1;
              const emptyThird =
                third.childCount === 1 &&
                third.firstChild?.type.name === 'paragraph' &&
                !third.firstChild.content.size;
              tr.delete(thirdStart, thirdStart + third.nodeSize);
              if (!emptyThird) tr.insert(secondEnd, third.content);
            }
            if (!columnLayouts[count].includes(at.node.attrs.layout as ColumnLayout))
              tr.setNodeAttribute(at.pos, 'layout', 'equal');
          }
          return true;
        },
      unsetColumns:
        () =>
        ({ state, tr, dispatch }) => {
          const at = columnsAt(state.selection.$from);
          if (!at) return false;
          if (dispatch) {
            const blocks: PMNode[] = [];
            at.node.forEach((col) => col.content.forEach((b) => blocks.push(b)));
            tr.replaceWith(at.pos, at.pos + at.node.nodeSize, Fragment.fromArray(blocks));
            tr.setSelection(Selection.near(tr.doc.resolve(at.pos + 1), 1)).scrollIntoView();
          }
          return true;
        },
      deleteColumns:
        () =>
        ({ state, tr, dispatch }) => {
          const at = columnsAt(state.selection.$from);
          if (!at) return false;
          if (dispatch) {
            tr.replaceWith(at.pos, at.pos + at.node.nodeSize, state.schema.nodes.paragraph.create());
            tr.setSelection(TextSelection.create(tr.doc, at.pos + 1)).scrollIntoView();
          }
          return true;
        },
    };
  },
  addKeyboardShortcuts() {
    const verticalExit = (editor: Editor, dir: 'up' | 'down') => {
      const { state, view } = editor;
      const { $from, empty } = state.selection;
      if (!empty || !(state.selection instanceof TextSelection)) return false;
      const at = columnsAt($from);
      if (!at || !view.endOfTextblock(dir)) return false;
      // Only from the first text of a column upwards, or from its last text downwards: the cursor's block and
      // each block around it are the first (last) of their parents, up to the column.
      for (let d = $from.depth; d > 0 && $from.node(d - 1).type.name !== 'columns'; d--) {
        const index = $from.index(d - 1);
        if (dir === 'up' ? index !== 0 : index !== $from.node(d - 1).childCount - 1) return false;
      }
      const sel =
        dir === 'down'
          ? selectionAfter(state.doc, at.pos + at.node.nodeSize)
          : selectionBefore(state.doc, at.pos);
      view.dispatch(state.tr.setSelection(sel).scrollIntoView());
      return true;
    };
    return {
      Tab: ({ editor }) => goToColumn(editor.state, editor.view.dispatch, 1),
      'Shift-Tab': ({ editor }) => goToColumn(editor.state, editor.view.dispatch, -1),
      ArrowDown: ({ editor }) => verticalExit(editor, 'down'),
      ArrowUp: ({ editor }) => verticalExit(editor, 'up'),
      // Backspace in an empty layout removes it.
      Backspace: ({ editor }) => {
        const { $from, empty } = editor.state.selection;
        const at = columnsAt($from);
        if (!at || !empty || $from.parentOffset !== 0 || !emptyLayout(at.node)) return false;
        return editor.commands.deleteColumns();
      },
    };
  },
  addProseMirrorPlugins() {
    return [
      new Plugin({
        key: new PluginKey('koggeColumns'),
        props: {
          // Layouts pasted or dropped into a list, table, panel or column become their content.
          transformPasted: (slice, view) =>
            view.state.selection.$from.depth > 1 ? flattenSlice(slice) : slice,
        },
      }),
    ];
  },
});
