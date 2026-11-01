// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Editor, type JSONContent } from '@tiptap/core';
import { closeHistory } from '@tiptap/pm/history';
import type { Slice } from '@tiptap/pm/model';
import { TextSelection } from '@tiptap/pm/state';
import { afterEach, describe, expect, it } from 'vitest';
import { columnsAt, flattenSlice } from './columns';
import { editorExtensions } from './extensions';

const t = (text: string): JSONContent => ({ type: 'text', text });
const p = (...content: JSONContent[]): JSONContent => ({ type: 'paragraph', content });
const col = (...content: JSONContent[]): JSONContent => ({ type: 'column', content });

const editors: Editor[] = [];
afterEach(() => {
  for (const e of editors.splice(0)) e.destroy();
});

function make(content: JSONContent[]) {
  const e = new Editor({
    element: document.createElement('div'),
    extensions: editorExtensions('', { collaborative: true, wiki: true }),
    content: { type: 'doc', content },
  });
  editors.push(e);
  return e;
}

/** The text of each column, in document order. */
function columnTexts(e: Editor): string[] {
  const out: string[] = [];
  e.state.doc.descendants((n) => {
    if (n.type.name === 'column') {
      out.push(n.textContent);
      return false;
    }
    return true;
  });
  return out;
}

/** Presses a key as the browser does (TipTap's keyboardShortcut command keeps the steps of what a key does, but
 * not where the cursor went). */
function press(e: Editor, key: string, shiftKey = false) {
  e.view.someProp('handleKeyDown', (f) => f(e.view, new KeyboardEvent('keydown', { key, shiftKey })));
}

/** Puts the cursor into the first text that contains text, offset characters into it. */
function place(e: Editor, text: string, offset = 0) {
  let at = -1;
  e.state.doc.descendants((n, pos) => {
    if (at < 0 && n.isText && n.text?.includes(text)) at = pos + n.text.indexOf(text) + offset;
    return at < 0;
  });
  e.view.dispatch(e.state.tr.setSelection(TextSelection.create(e.state.doc, at)));
}

describe('columns', () => {
  it('are inserted in place of an empty paragraph, or below the block of the cursor, and do not nest', () => {
    const e = make([p(t('Intro')), p()]);
    e.commands.setTextSelection(e.state.doc.content.size - 1);
    expect(e.commands.insertColumns(2)).toBe(true);
    expect(e.getJSON().content?.map((n) => n.type)).toEqual(['paragraph', 'columns']);
    // The cursor is in the first column, where typing goes.
    e.commands.insertContent('Left');
    expect(columnTexts(e)).toEqual(['Left', '']);
    expect(e.can().insertColumns(3)).toBe(false);

    place(e, 'Intro', 2);
    e.commands.insertColumns(3, 'sides');
    const doc = e.getJSON().content ?? [];
    expect(doc.map((n) => n.type)).toEqual(['paragraph', 'columns', 'columns']);
    expect(doc[1].attrs?.layout).toBe('sides');
    expect(doc[1].content).toHaveLength(3);
  });

  it('move between columns with Tab, and Backspace removes an empty layout', () => {
    const e = make([{ type: 'columns', content: [col(p(t('one'))), col(p(t('two'))), col(p(t('three')))] }]);
    place(e, 'one', 1);
    press(e, 'Tab');
    expect(e.state.selection.$from.parent.textContent).toBe('two');
    press(e, 'Tab');
    expect(e.state.selection.$from.parent.textContent).toBe('three');
    press(e, 'Tab', true);
    expect(e.state.selection.$from.parent.textContent).toBe('two');

    const empty = make([p(t('before')), { type: 'columns', content: [col(p()), col(p())] }]);
    // The paragraph of the second column: before the end of the layout, the column and the paragraph.
    empty.commands.setTextSelection(empty.state.doc.content.size - 3);
    expect(columnsAt(empty.state.selection.$from)?.column).toBe(1);
    press(empty, 'Backspace');
    expect(empty.getJSON().content?.map((n) => n.type)).toEqual(['paragraph', 'paragraph']);
  });

  it('change their number and widths without losing content, each as one change', () => {
    const e = make([
      { type: 'columns', content: [col(p(t('a'))), col(p(t('b'))), col(p(t('c')), p(t('d')))] },
    ]);
    place(e, 'a');
    expect(e.can().setColumnsLayout('left')).toBe(false); // three columns have no sidebar on one side
    e.commands.setColumnsLayout('sides');
    e.commands.setColumnCount(2);
    expect(columnTexts(e)).toEqual(['a', 'bcd']);
    // The layout of three columns does not fit two.
    expect(e.getJSON().content?.[0].attrs?.layout).toBe('equal');
    e.commands.setColumnsLayout('right');
    e.view.dispatch(closeHistory(e.state.tr));
    e.commands.setColumnCount(3);
    expect(columnTexts(e)).toEqual(['a', 'bcd', '']);
    e.commands.undo();
    expect(columnTexts(e)).toEqual(['a', 'bcd']);
    expect(e.getJSON().content?.[0].attrs?.layout).toBe('right');
  });

  it('can be removed with or without their content', () => {
    const e = make([
      p(t('top')),
      { type: 'columns', attrs: { layout: 'left' }, content: [col(p(t('x'))), col(p(t('y')), p(t('z')))] },
    ]);
    place(e, 'y');
    e.commands.unsetColumns();
    expect(e.state.doc.content.content.map((n) => n.textContent)).toEqual(['top', 'x', 'y', 'z']);
    e.commands.undo();
    place(e, 'x');
    e.commands.deleteColumns();
    expect(e.getJSON().content?.map((n) => n.type)).toEqual(['paragraph', 'paragraph']);
    expect(e.state.doc.textContent).toBe('top');
  });

  it('lose their columns where they are pasted into a column, list or table', () => {
    const e = make([
      p(t('top')),
      { type: 'columns', content: [col(p(t('first'))), col(p(t('second')))] },
      { type: 'bulletList', content: [{ type: 'listItem', content: [p(t('item'))] }] },
    ]);
    const start = e.state.doc.child(0).nodeSize;
    const layout = e.state.doc.slice(start, start + e.state.doc.child(1).nodeSize);
    expect(layout.content.firstChild?.type.name).toBe('columns');
    const flat = flattenSlice(layout);
    expect(flat.content.childCount).toBe(2);
    expect(flat.content.firstChild?.textContent).toBe('first');
    // A slice from one column into the other is open into both; flattened, it is open into their paragraphs.
    const inner = e.state.doc.slice(start + 4, start + 14);
    expect([inner.openStart, inner.openEnd, inner.content.firstChild?.type.name]).toEqual([2, 2, 'column']);
    const flatInner = flattenSlice(inner);
    expect([flatInner.openStart, flatInner.openEnd, flatInner.content.childCount]).toEqual([1, 1, 2]);

    const paste = () => e.view.someProp('transformPasted', (f) => f(layout, e.view, false)) as Slice;
    const hasLayout = (s: Slice) => {
      let found = false;
      s.content.descendants((n) => {
        found ||= n.type.name === 'columns';
      });
      return found;
    };
    place(e, 'item', 2);
    expect(hasLayout(paste())).toBe(false);
    place(e, 'first', 2);
    expect(hasLayout(paste())).toBe(false);
    // Between top-level blocks a layout stays one.
    place(e, 'top', 1);
    expect(hasLayout(paste())).toBe(true);
  });
});
