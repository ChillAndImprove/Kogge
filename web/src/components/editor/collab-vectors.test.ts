// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Editor, type JSONContent } from '@tiptap/core';
import { Fragment, Slice, type Node as PMNode } from '@tiptap/pm/model';
import { NodeSelection } from '@tiptap/pm/state';
import { CellSelection } from '@tiptap/pm/tables';
import { canJoin, canSplit, findWrapping, liftTarget, Step } from '@tiptap/pm/transform';
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { editorExtensions } from './extensions';

/*
 * Editing sessions for the server's port of ProseMirror (internal/docmodel/prosemirror): random edits in the
 * editor, recorded as the steps the editor sends, with the document after each transaction (as a hash), and probes —
 * broken variants of steps with ProseMirror's verdict. The Go tests replay them and must reach the same documents
 * and verdicts. KOGGE_UPDATE_VECTORS=1 writes the file; otherwise the test checks that it is current.
 */

const file = resolve(__dirname, '../../../../internal/docmodel/prosemirror/testdata/sessions.json');

/** A small, seeded random generator (mulberry32), so that the sessions are the same on every run. */
function random(seed: number) {
  let a = seed >>> 0;
  const next = () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  return {
    next,
    int: (n: number) => Math.floor(next() * n),
    pick: <T>(list: readonly T[]) => list[Math.floor(next() * list.length)],
    chance: (p: number) => next() < p,
  };
}
type Random = ReturnType<typeof random>;

/** JSON with sorted keys, hashed: the Go tests compute the same for their documents. */
function canonical(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(canonical);
  if (v && typeof v === 'object') {
    return Object.fromEntries(
      Object.keys(v)
        .sort()
        .map((k) => [k, canonical((v as Record<string, unknown>)[k])]),
    );
  }
  return v;
}
const hash = (doc: PMNode) =>
  createHash('sha256')
    .update(JSON.stringify(canonical(doc.toJSON())))
    .digest('hex')
    .slice(0, 16);

const t = (text: string, marks?: JSONContent['marks']): JSONContent => ({ type: 'text', text, marks });
const p = (...content: JSONContent[]): JSONContent => ({ type: 'paragraph', content });

/** A document with every kind of node and mark of the editor. */
const fixture: JSONContent = {
  type: 'doc',
  content: [
    { type: 'heading', attrs: { level: 1 }, content: [t('Release handbook')] },
    p(
      t('Plain, '),
      t('bold', [{ type: 'bold' }]),
      t(' and '),
      t('linked', [{ type: 'link', attrs: { href: 'https://kogge.example/docs' } }, { type: 'italic' }]),
      t(' text with 😀 and ü. '),
      { type: 'mention', attrs: { user: 7, label: 'Anna' } },
      t(' sees '),
      { type: 'issue', attrs: { key: 'SHOP-12', display: 'inline' } },
      { type: 'hardBreak' },
      { type: 'status', attrs: { text: 'DONE', color: 'green' } },
      t(' on '),
      { type: 'date', attrs: { value: '2026-10-07' } },
      { type: 'emoji', attrs: { name: 'rocket' } },
      t(' where '),
      { type: 'inlineMath', attrs: { latex: 'e^{i\\pi}+1=0' } },
    ),
    { type: 'math', attrs: { latex: '\\int_0^1 x^2\\,dx = \\frac{1}{3}' } },
    {
      type: 'bulletList',
      content: [
        { type: 'listItem', content: [p(t('first'))] },
        {
          type: 'listItem',
          content: [
            p(t('second', [{ type: 'textColor', attrs: { color: 'red' } }])),
            {
              type: 'orderedList',
              attrs: { start: 3 },
              content: [{ type: 'listItem', content: [p(t('nested'))] }],
            },
          ],
        },
      ],
    },
    {
      type: 'taskList',
      content: [
        { type: 'taskItem', attrs: { checked: true }, content: [p(t('done task'))] },
        { type: 'taskItem', attrs: { checked: false }, content: [p(t('open task'))] },
      ],
    },
    { type: 'codeBlock', attrs: { language: 'go' }, content: [t('func main() {\n\tprintln("hi")\n}')] },
    { type: 'blockquote', content: [p(t('quoted '), t('code', [{ type: 'code' }]))] },
    { type: 'panel', attrs: { kind: 'warning' }, content: [p(t('careful'))] },
    { type: 'expand', attrs: { title: 'More' }, content: [p(t('hidden'))] },
    {
      type: 'table',
      content: [
        {
          type: 'tableRow',
          content: [
            { type: 'tableHeader', content: [p(t('Name'))] },
            { type: 'tableHeader', content: [p(t('Owner'))] },
          ],
        },
        {
          type: 'tableRow',
          content: [
            { type: 'tableCell', attrs: { background: 'blue' }, content: [p(t('Shop'))] },
            {
              type: 'tableCell',
              content: [
                p(t('Ben'), t(' x', [{ type: 'highlight' }])),
                { type: 'math', attrs: { latex: 'a < b & c' } },
              ],
            },
          ],
        },
      ],
    },
    { type: 'macro', attrs: { name: 'toc', params: { maxLevel: 3 } } },
    { type: 'macro', attrs: { name: 'excerpt', params: {} }, content: [p(t('excerpt text'))] },
    { type: 'image', attrs: { src: '/files/a.png', alt: 'diagram', attachment: 4 } },
    { type: 'horizontalRule' },
    p(
      t('Sub', [{ type: 'subscript' }]),
      t('Sup', [{ type: 'superscript' }]),
      t('anchored', [{ type: 'anchor', attrs: { id: 'c1' } }]),
      { type: 'inlineImage', attrs: { src: '/badge.svg', alt: 'badge' } },
      { type: 'inlineMacro', attrs: { name: 'status', params: { title: 'X' } } },
    ),
    { type: 'unsupported', attrs: { source: 'confluence', name: 'gadget', raw: '<x/>' } },
    {
      type: 'columns',
      attrs: { layout: 'left' },
      content: [
        {
          type: 'column',
          content: [p(t('Side note '), { type: 'inlineMath', attrs: { latex: '\\sqrt{2}' } })],
        },
        {
          type: 'column',
          content: [
            p(t('Main '), t('column', [{ type: 'bold' }])),
            { type: 'bulletList', content: [{ type: 'listItem', content: [p(t('point'))] }] },
          ],
        },
      ],
    },
    p(t('The end.', [{ type: 'strike' }, { type: 'underline' }])),
  ],
};

const words = ['a', 'ab', 'Hallo', ' ', 'ß', '😀', 'x y', 'Zahlungserinnerung', '12'];

function textPositions(doc: PMNode): number[] {
  const out: number[] = [];
  doc.descendants((node, pos) => {
    if (node.isTextblock) for (let i = 0; i <= node.content.size; i++) out.push(pos + 1 + i);
    return true;
  });
  return out;
}

/** A position in text that does not cut a surrogate pair. */
function textPos(doc: PMNode, r: Random): number | null {
  const all = textPositions(doc).filter((pos) => {
    const $pos = doc.resolve(pos);
    const after = $pos.nodeAfter;
    if (!after?.isText || $pos.textOffset === 0) return true;
    const code = (after.text ?? '').charCodeAt(0);
    return !(code >= 0xdc00 && code <= 0xdfff);
  });
  return all.length ? r.pick(all) : null;
}

/** A position moved off the middle of a surrogate pair: editors never cut one. */
function whole(doc: PMNode, pos: number): number {
  const $pos = doc.resolve(pos);
  const after = $pos.nodeAfter;
  if (after?.isText && $pos.textOffset > 0) {
    const code = (after.text ?? '').charCodeAt(0);
    if (code >= 0xdc00 && code <= 0xdfff) return pos - 1;
  }
  return pos;
}

function nodePositions(doc: PMNode, pred: (n: PMNode) => boolean): number[] {
  const out: number[] = [];
  doc.descendants((node, pos) => {
    if (pred(node)) out.push(pos);
    return true;
  });
  return out;
}

type Op = (editor: Editor, r: Random) => void;

function select(editor: Editor, r: Random, range = true) {
  const a = textPos(editor.state.doc, r);
  if (a === null) return false;
  let b = a;
  if (range && r.chance(0.7)) {
    const c = textPos(editor.state.doc, r);
    if (c !== null && Math.abs(c - a) < 60) b = c;
  }
  editor.commands.setTextSelection({ from: Math.min(a, b), to: Math.max(a, b) });
  return true;
}

function cell(editor: Editor, r: Random) {
  const cells = nodePositions(
    editor.state.doc,
    (n) => n.type.name === 'tableCell' || n.type.name === 'tableHeader',
  );
  if (!cells.length) return false;
  const pos = r.pick(cells);
  editor.commands.setTextSelection(pos + 2);
  return true;
}

const ops: [string, number, Op][] = [
  ['type', 12, (e, r) => select(e, r, false) && e.commands.insertContent(r.pick(words))],
  ['typeOver', 3, (e, r) => select(e, r) && e.commands.insertContent(r.pick(words))],
  ['delete', 5, (e, r) => select(e, r) && e.commands.deleteSelection()],
  ['enter', 4, (e, r) => select(e, r, false) && e.commands.keyboardShortcut('Enter')],
  ['backspace', 4, (e, r) => select(e, r) && e.commands.keyboardShortcut('Backspace')],
  ['deleteForward', 3, (e, r) => select(e, r) && e.commands.keyboardShortcut('Delete')],
  [
    'blockStartBackspace',
    3,
    (e, r) => {
      const starts = nodePositions(e.state.doc, (n) => n.isTextblock);
      if (!starts.length) return;
      e.commands.setTextSelection(r.pick(starts) + 1);
      e.commands.keyboardShortcut('Backspace');
    },
  ],
  [
    'mark',
    8,
    (e, r) =>
      select(e, r) &&
      r.pick([
        () => e.commands.toggleBold(),
        () => e.commands.toggleItalic(),
        () => e.commands.toggleStrike(),
        () => e.commands.toggleCode(),
        () => e.commands.toggleUnderline(),
        () => e.commands.toggleHighlight(),
        () => e.commands.toggleSubscript(),
        () => e.commands.toggleSuperscript(),
        () => e.commands.setLink({ href: 'https://example.org/' + r.int(5) }),
        () => e.commands.unsetLink(),
        () => e.commands.setMark('textColor', { color: r.pick(['red', 'blue', '']) }),
        () => e.commands.unsetMark('textColor'),
        () => e.commands.setMark('anchor', { id: 'a' + r.int(3) }),
        () => e.commands.unsetAllMarks(),
      ])(),
  ],
  [
    'block',
    6,
    (e, r) =>
      select(e, r) &&
      r.pick([
        () => e.commands.setParagraph(),
        () => e.commands.toggleHeading({ level: r.pick([1, 2, 3] as const) }),
        () => e.commands.setCodeBlock({ language: 'ts' }),
        () => e.commands.toggleBlockquote(),
        () => e.commands.wrapIn('panel', { kind: 'info' }),
        () => e.commands.lift('panel'),
        () => e.commands.wrapIn('expand', { title: 'Details' }),
        () => e.commands.toggleBulletList(),
        () => e.commands.toggleOrderedList(),
        () => e.commands.toggleTaskList(),
        () => e.commands.sinkListItem('listItem'),
        () => e.commands.liftListItem('listItem'),
        () => e.commands.sinkListItem('taskItem'),
        () => e.commands.liftListItem('taskItem'),
        () => e.commands.setHorizontalRule(),
        () => e.commands.setHardBreak(),
        () => e.commands.updateAttributes('paragraph', { align: r.pick(['center', 'right', null]) }),
        () => e.commands.updateAttributes('heading', { level: r.pick([2, 4]) }),
        () => e.commands.updateAttributes('codeBlock', { language: r.pick(['go', 'sql', null]) }),
        () => e.commands.updateAttributes('orderedList', { start: r.int(9) + 1 }),
      ])(),
  ],
  [
    'table',
    5,
    (e, r) => {
      if (r.chance(0.2)) {
        if (select(e, r, false))
          e.commands.insertTable({ rows: 2 + r.int(2), cols: 2 + r.int(2), withHeaderRow: r.chance(0.5) });
        return;
      }
      if (!cell(e, r)) return;
      r.pick([
        () => e.commands.addColumnBefore(),
        () => e.commands.addColumnAfter(),
        () => e.commands.deleteColumn(),
        () => e.commands.addRowBefore(),
        () => e.commands.addRowAfter(),
        () => e.commands.deleteRow(),
        () => e.commands.toggleHeaderRow(),
        () => e.commands.toggleHeaderColumn(),
        () => e.commands.toggleHeaderCell(),
        () => e.commands.setCellAttribute('background', r.pick(['red', 'teal', null])),
        () => e.commands.splitCell(),
        () => {
          const cells = nodePositions(
            e.state.doc,
            (n) => n.type.name === 'tableCell' || n.type.name === 'tableHeader',
          );
          if (cells.length < 2) return;
          const a = r.pick(cells);
          const b = r.pick(cells);
          try {
            e.view.dispatch(e.state.tr.setSelection(CellSelection.create(e.state.doc, a, b)));
            e.commands.mergeCells();
          } catch {
            // cells of different tables
          }
        },
        () => r.chance(0.2) && e.commands.deleteTable(),
      ])();
    },
  ],
  [
    'columns',
    4,
    (e, r) => {
      if (r.chance(0.3)) {
        if (select(e, r, false))
          e.commands.insertColumns(r.pick([2, 3] as const), r.pick(['equal', 'left', 'sides'] as const));
        return;
      }
      const layouts = nodePositions(e.state.doc, (n) => n.type.name === 'columns');
      if (!layouts.length) return;
      // Into a column of a layout, then a change of the layout.
      e.commands.setTextSelection(r.pick(layouts) + 3);
      r.pick([
        () => e.commands.setColumnsLayout(r.pick(['equal', 'left', 'right', 'sides'] as const)),
        () => e.commands.setColumnCount(r.pick([2, 3] as const)),
        () => e.commands.unsetColumns(),
        () => e.commands.deleteColumns(),
        () => e.commands.insertContent(r.pick(words)),
        () => e.commands.keyboardShortcut('Backspace'),
      ])();
    },
  ],
  [
    'atom',
    5,
    (e, r) =>
      select(e, r, false) &&
      e.commands.insertContent(
        r.pick<JSONContent>([
          { type: 'mention', attrs: { user: r.int(50), label: 'Ben' } },
          { type: 'status', attrs: { text: 'OPEN', color: 'blue' } },
          { type: 'date', attrs: { value: '2026-01-0' + (1 + r.int(9)) } },
          { type: 'issue', attrs: { key: 'OPS-' + (1 + r.int(99)) } },
          { type: 'emoji', attrs: { name: 'tada' } },
          { type: 'image', attrs: { src: '/files/b.png', alt: 'b', attachment: 9 } },
          { type: 'macro', attrs: { name: 'issue-list', params: { query: 'project = OPS', max: 5 } } },
          { type: 'macro', attrs: { name: 'excerpt', params: {} }, content: [p(t('ex'))] },
          { type: 'horizontalRule' },
          { type: 'inlineImage', attrs: { src: '/i.svg', alt: 'i' } },
          { type: 'math', attrs: { latex: `x_${r.int(9)}` } },
          { type: 'inlineMath', attrs: { latex: `\\sqrt{${r.int(9)}}` } },
        ]),
      ),
  ],
  [
    'nodeAttrs',
    4,
    (e, r) => {
      const doc = e.state.doc;
      const targets = nodePositions(doc, (n) => !n.isText && n.type.name !== 'doc');
      if (!targets.length) return;
      const pos = r.pick(targets);
      const node = doc.nodeAt(pos)!;
      const tr = e.state.tr;
      r.pick([
        () =>
          tr.setNodeAttribute(
            pos,
            r.pick(Object.keys(node.attrs).concat(['unknown'])) ?? 'x',
            r.pick([1, 'two', null, true, { a: [1] }]),
          ),
        () => tr.setNodeMarkup(pos, undefined, { ...node.attrs }),
        () => {
          if (node.type.name === 'macro') tr.setNodeAttribute(pos, 'params', { maxLevel: r.int(6) });
        },
        () => {
          if (node.type.name === 'taskItem') tr.setNodeAttribute(pos, 'checked', !node.attrs.checked);
        },
        () => {
          if (node.isInline) tr.addNodeMark(pos, e.schema.marks.bold.create());
        },
        () => {
          if (node.isInline) tr.removeNodeMark(pos, e.schema.marks.bold);
        },
        () => tr.setDocAttribute('unknown', 1),
      ])();
      if (tr.docChanged) e.view.dispatch(tr);
    },
  ],
  [
    'structure',
    6,
    (e, r) => {
      const doc = e.state.doc;
      const tr = e.state.tr;
      const pos = textPos(doc, r);
      if (pos === null) return;
      const $pos = doc.resolve(pos);
      r.pick([
        () => {
          const depth = 1 + r.int(Math.max(1, $pos.depth));
          if (canSplit(doc, pos, depth)) tr.split(pos, depth);
        },
        () => {
          const before = $pos.before(Math.max(1, $pos.depth));
          if (canJoin(doc, before)) tr.join(before);
        },
        () => {
          const range = $pos.blockRange();
          const target = range && liftTarget(range);
          if (range && target !== null && target !== undefined) tr.lift(range, target);
        },
        () => {
          const range = $pos.blockRange();
          const wrapping =
            range &&
            findWrapping(
              range,
              r.pick([e.schema.nodes.blockquote, e.schema.nodes.bulletList, e.schema.nodes.panel]),
            );
          if (range && wrapping) tr.wrap(range, wrapping);
        },
        () => {
          const end = Math.min(doc.content.size, pos + r.int(40));
          tr.setBlockType(
            pos,
            end,
            r.pick([e.schema.nodes.paragraph, e.schema.nodes.heading, e.schema.nodes.codeBlock]),
            { level: 2 },
          );
        },
        () => tr.deleteRange(whole(doc, Math.max(0, pos - r.int(30))), pos),
        () => {
          // A paste: a slice of the document elsewhere, fitted by ProseMirror.
          const a = textPos(doc, r) ?? 0;
          const b = whole(doc, Math.min(doc.content.size, a + 1 + r.int(50)));
          tr.replaceRange(pos, pos, doc.slice(a, b));
        },
        () => {
          const a = textPos(doc, r) ?? 0;
          const b = whole(doc, Math.min(doc.content.size, a + 1 + r.int(30)));
          tr.replace(pos, pos, doc.slice(a, b));
        },
        () =>
          tr.replaceRangeWith(
            pos,
            whole(doc, Math.min(doc.content.size, pos + r.int(10))),
            e.schema.nodes.horizontalRule.create(),
          ),
        () => tr.insertText(r.pick(words), pos),
        () => {
          // A drag: a block moves elsewhere.
          const blocks = nodePositions(doc, (n) => n.isBlock && !n.isTextblock && n.type.name !== 'doc');
          if (!blocks.length) return;
          const from = r.pick(blocks);
          const node = doc.nodeAt(from)!;
          tr.delete(from, from + node.nodeSize);
          const to = tr.mapping.map(pos);
          tr.replaceRange(to, to, new Slice(Fragment.from(node), 0, 0));
        },
      ])();
      if (tr.docChanged) e.view.dispatch(tr);
    },
  ],
  [
    'nodeSelection',
    2,
    (e, r) => {
      const atoms = nodePositions(e.state.doc, (n) => n.isAtom && !n.isText);
      if (!atoms.length) return;
      e.view.dispatch(e.state.tr.setSelection(NodeSelection.create(e.state.doc, r.pick(atoms))));
      r.pick([
        () => e.commands.deleteSelection(),
        () => e.commands.keyboardShortcut('Enter'),
        () => e.commands.insertContent('z'),
      ])();
    },
  ],
  [
    'nodeMark',
    3,
    (e, r) => {
      const atoms = nodePositions(e.state.doc, (n) => n.isInline && !n.isText);
      if (!atoms.length) return;
      const pos = r.pick(atoms);
      const mark = r.pick([
        e.schema.marks.bold.create(),
        e.schema.marks.italic.create(),
        e.schema.marks.link.create({ href: 'https://example.org' }),
      ]);
      const tr = e.state.tr;
      if (r.chance(0.6)) tr.addNodeMark(pos, mark);
      else tr.removeNodeMark(pos, r.chance(0.5) ? mark : mark.type);
      if (tr.docChanged) e.view.dispatch(tr);
    },
  ],
  [
    'attrStep',
    3,
    (e, r) => {
      const nodes = nodePositions(e.state.doc, (n) => !n.isText && Object.keys(n.attrs).length > 0);
      if (!nodes.length) return;
      const pos = r.pick(nodes);
      const node = e.state.doc.nodeAt(pos)!;
      const name = r.pick(Object.keys(node.attrs));
      // Values of the attribute's kind: the editor's views render them.
      const current = node.attrs[name] as unknown;
      const value =
        typeof current === 'number'
          ? 1 + r.int(4)
          : typeof current === 'boolean'
            ? !current
            : current && typeof current === 'object'
              ? { maxLevel: r.int(4) }
              : r.pick([null, 'center', 'teal']);
      e.view.dispatch(e.state.tr.setNodeAttribute(pos, name, value));
    },
  ],
  [
    'selectAll',
    1,
    (e, r) =>
      e.commands.selectAll() &&
      r.pick([
        () => e.commands.deleteSelection(),
        () => e.commands.toggleBold(),
        () => e.commands.insertContent('all'),
      ])(),
  ],
  ['undo', 4, (e) => e.commands.undo()],
  ['redo', 2, (e) => e.commands.redo()],
];
const weighted = ops.flatMap(([name, weight, op]) =>
  Array.from({ length: weight }, () => [name, op] as const),
);

/** Variants of a step that may or may not apply: shifted positions, other open depths, foreign content. */
function mutate(json: Record<string, unknown>, r: Random, docSize: number): Record<string, unknown> {
  const out = JSON.parse(JSON.stringify(json)) as Record<string, unknown>;
  const numeric = ['from', 'to', 'gapFrom', 'gapTo', 'insert', 'pos'].filter(
    (k) => typeof out[k] === 'number',
  );
  const choice = r.int(6);
  if (choice <= 2 && numeric.length) {
    const k = r.pick(numeric);
    out[k] = Math.max(0, (out[k] as number) + r.pick([-3, -2, -1, 1, 2, 3, docSize]));
  } else if (choice === 3 && out.slice && typeof out.slice === 'object') {
    const slice = out.slice as Record<string, unknown>;
    const k = r.pick(['openStart', 'openEnd']);
    slice[k] = Math.max(0, ((slice[k] as number) ?? 0) + r.pick([-1, 1, 2]));
  } else if (choice === 4) {
    out.slice = {
      content: [
        r.pick<JSONContent>([
          { type: 'tableRow', content: [{ type: 'tableCell', content: [p(t('cell'))] }] },
          { type: 'listItem', content: [p(t('item'))] },
          p(t('para')),
          t('text'),
          { type: 'horizontalRule' },
          { type: 'text', text: '' },
          { type: 'nope' },
          { type: 'paragraph', content: [p(t('nested'))] },
          { type: 'codeBlock', content: [t('code', [{ type: 'bold' }])] },
          { type: 'column', content: [p(t('col'))] },
          { type: 'math', attrs: { latex: 'x' } },
          { type: 'inlineMath', attrs: { latex: 'y' } },
          // A formula with content: ProseMirror does not check a leaf's content (the server's document model does).
          { type: 'math', content: [t('z')] },
          { type: 'columns', content: [{ type: 'column', content: [p(t('alone'))] }] },
          {
            type: 'columns',
            content: [
              { type: 'column', content: [p(t('a'))] },
              { type: 'column', content: [p(t('b'))] },
            ],
          },
        ]),
      ],
      openStart: r.int(2),
      openEnd: r.int(2),
    };
    out.stepType = r.pick(['replace', 'replaceAround', out.stepType as string]);
    out.from ??= 1;
    out.to ??= out.from;
    out.gapFrom ??= out.from;
    out.gapTo ??= out.to;
    out.insert ??= 0;
  } else {
    out.mark = r.pick([
      { type: 'bold' },
      { type: 'link', attrs: { href: 'https://x' } },
      { type: 'nope' },
      null,
    ]);
    out.stepType = r.pick(['addMark', 'removeMark', 'addNodeMark', 'removeNodeMark']);
    out.from ??= 1;
    out.to ??= (out.from as number) + 3;
    out.pos ??= out.from;
  }
  return out;
}

interface Tx {
  steps: unknown[];
  hash: string;
}
interface Probe {
  probe: unknown;
  hash: string | null; // null: ProseMirror refuses the step
}

function session(seed: number, length: number) {
  const r = random(seed);
  const editor = new Editor({
    element: document.createElement('div'),
    extensions: editorExtensions(''),
    content: fixture,
  });
  const initial = editor.state.doc;
  const entries: (Tx | Probe)[] = [];
  let current = initial;
  const schema = editor.schema;
  editor.on('transaction', ({ transaction, appendedTransactions }) => {
    for (const tr of [transaction, ...(appendedTransactions ?? [])]) {
      if (!tr.docChanged) continue;
      const steps = tr.steps.map((s) => s.toJSON());
      // The steps, read back from JSON, must give the editor's document: what the server receives is complete.
      for (const json of steps) {
        const result = Step.fromJSON(schema, json).apply(current);
        if (!result.doc) throw new Error(`recorded step fails: ${result.failed}`);
        current = result.doc;
      }
      if (!current.eq(tr.doc)) throw new Error('recorded steps do not give the document');
      entries.push({ steps, hash: hash(current) });
      // Probes: broken variants of the transaction's first step, applied to the document before it.
      if (r.chance(0.5)) {
        const before = tr.before;
        const variant = mutate(steps[0] as Record<string, unknown>, r, before.content.size);
        let verdict: string | null;
        try {
          const result = Step.fromJSON(schema, variant).apply(before);
          verdict = result.doc ? hash(result.doc) : null;
        } catch {
          verdict = null;
        }
        // Probes apply to the document before the transaction, which the Go test keeps for them.
        entries.push({ probe: variant, hash: verdict });
      }
    }
  });
  for (let i = 0; i < length; i++) {
    const [name, op] = r.pick(weighted);
    const before = current;
    try {
      op(editor, r);
    } catch (e) {
      // Commands may refuse positions they cannot use; the session goes on, unless the recording failed.
      if (!current.eq(editor.state.doc)) {
        throw new Error(
          `${name} in session ${seed}; before: ${JSON.stringify(before.toJSON()).slice(0, 300)}`,
          {
            cause: e,
          },
        );
      }
    }
  }
  if (!current.eq(editor.state.doc)) throw new Error('session out of step');
  editor.destroy();
  return { seed, doc: initial.toJSON(), entries, final: current.toJSON() };
}

describe('editing sessions for the server', () => {
  it('are current (KOGGE_UPDATE_VECTORS=1 writes them)', () => {
    const sessions = Array.from({ length: 40 }, (_, i) => session(1000 + i, 80));
    const out = JSON.stringify({ sessions }) + '\n';
    if (process.env.KOGGE_UPDATE_VECTORS) writeFileSync(file, out);
    expect(readFileSync(file, 'utf8') === out, 'sessions.json is out of date: KOGGE_UPDATE_VECTORS=1').toBe(
      true,
    );
  }, 120_000);
});
