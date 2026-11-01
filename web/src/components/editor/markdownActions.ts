// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  EditorSelection,
  type ChangeSpec,
  type EditorState,
  type Line,
  type TransactionSpec,
} from '@codemirror/state';

/** What the Markdown toolbar and its keys do to the text. */
export type MarkdownAction =
  | 'bold'
  | 'italic'
  | 'strike'
  | 'code'
  | 'math'
  | 'h1'
  | 'h2'
  | 'h3'
  | 'quote'
  | 'bullet'
  | 'ordered'
  | 'task'
  | 'link'
  | 'image'
  | 'codeBlock'
  | 'mathBlock'
  | 'table'
  | 'rule';

/** Words the inserted Markdown needs, in the reader's language. */
export interface MarkdownWords {
  /** The heading of a column of a new table ("Column" → "Column 1"). */
  column: string;
  /** The description of a new image. */
  image: string;
}

const marks: Partial<Record<MarkdownAction, string>> = { bold: '**', italic: '*', strike: '~~', code: '`' };

/**
 * Puts marks around each selection, or around the cursor so that typing goes between them; where the selection
 * already has the marks around it, they are taken away. A single "*" next to another is part of "**", not italics.
 */
function toggleMarks(state: EditorState, mark: string): TransactionSpec {
  const doc = state.doc;
  return state.changeByRange((range) => {
    const { from, to } = range;
    const before = doc.sliceString(Math.max(0, from - mark.length), from);
    const after = doc.sliceString(to, to + mark.length);
    const single = mark.length === 1;
    const doubled =
      single &&
      (doc.sliceString(Math.max(0, from - 2), from - 1) === mark || doc.sliceString(to + 1, to + 2) === mark);
    if (before === mark && after === mark && !doubled) {
      return {
        changes: [
          { from: from - mark.length, to: from },
          { from: to, to: to + mark.length },
        ],
        range: EditorSelection.range(from - mark.length, to - mark.length),
      };
    }
    return {
      changes: [
        { from, insert: mark },
        { from: to, insert: mark },
      ],
      range: EditorSelection.range(from + mark.length, to + mark.length),
    };
  });
}

/**
 * Makes each selection a formula within the line ($…$), or the cursor an empty one to type it in. Where the
 * selection is a formula already, between "$" or "$$" (a formula, too), its dollars are taken away.
 */
function toggleFormula(state: EditorState): TransactionSpec {
  const doc = state.doc;
  return state.changeByRange(({ from, to }) => {
    // The dollars right before and after the selection, up to three ("$$$" is no formula).
    const before = /\$*$/.exec(doc.sliceString(Math.max(0, from - 3), from))![0].length;
    const after = /^\$*/.exec(doc.sliceString(to, to + 3))![0].length;
    if (before > 0 && before < 3 && after === before) {
      return {
        changes: [
          { from: from - before, to: from },
          { from: to, to: to + after },
        ],
        range: EditorSelection.range(from - before, to - before),
      };
    }
    return {
      changes: [
        { from, insert: '$' },
        { from: to, insert: '$' },
      ],
      range: EditorSelection.range(from + 1, to + 1),
    };
  });
}

/** The lines the selection touches, in order; a selection ending at a line's start leaves that line out. */
function selectedLines(state: EditorState): Line[] {
  const seen = new Map<number, Line>();
  for (const r of state.selection.ranges) {
    const first = state.doc.lineAt(r.from);
    let last = state.doc.lineAt(r.to);
    if (r.to > r.from && last.from === r.to && last.number > first.number)
      last = state.doc.line(last.number - 1);
    for (let n = first.number; n <= last.number; n++) seen.set(n, state.doc.line(n));
  }
  return [...seen.values()].sort((a, b) => a.number - b.number);
}

const headingMark = /^#{1,6}(?:[ \t]+|$)/;
const quoteMark = /^>[ \t]?/;
/** A list item's marker after its indentation: a task, a bullet or a number. */
const listMark = /^(\s*)([-*+][ \t]+\[[ xX]\][ \t]+|[-*+][ \t]+|\d{1,9}[.)][ \t]+)/;
const kinds: Record<string, RegExp> = {
  task: /^[-*+][ \t]+\[[ xX]\]/,
  bullet: /^[-*+][ \t]+(?!\[[ xX]\])/,
  ordered: /^\d/,
};

/**
 * Makes the selected lines headings, quotes or list items; lines that all are of that kind already become plain
 * text again. List items keep their indentation, one kind of list becomes another, and blank lines among several
 * stay blank.
 */
function toggleLines(
  state: EditorState,
  action: 'h1' | 'h2' | 'h3' | 'quote' | 'bullet' | 'ordered' | 'task',
) {
  const all = selectedLines(state);
  const lines = all.length > 1 ? all.filter((l) => l.text.trim() !== '') : all;
  if (lines.length === 0) return null;
  const changes: ChangeSpec[] = [];
  if (action === 'h1' || action === 'h2' || action === 'h3') {
    const want = `${'#'.repeat(Number(action[1]))} `;
    const off = lines.every((l) => l.text.startsWith(want) && !l.text.startsWith(`${want.trim()}#`));
    for (const l of lines) {
      const old = headingMark.exec(l.text)?.[0] ?? '';
      changes.push({ from: l.from, to: l.from + old.length, insert: off ? '' : want });
    }
  } else if (action === 'quote') {
    const off = lines.every((l) => l.text.startsWith('>'));
    for (const l of lines) {
      const old = quoteMark.exec(l.text)?.[0] ?? '';
      changes.push({ from: l.from, to: l.from + old.length, insert: off ? '' : '> ' });
    }
  } else {
    const off = lines.every((l) => {
      const m = listMark.exec(l.text);
      return !!m && kinds[action].test(m[2]);
    });
    lines.forEach((l, i) => {
      const m = listMark.exec(l.text);
      const indent = m ? m[1] : (/^\s*/.exec(l.text)?.[0] ?? '');
      const old = m ? m[2] : '';
      const want = action === 'bullet' ? '- ' : action === 'task' ? '- [ ] ' : `${i + 1}. `;
      changes.push({
        from: l.from + indent.length,
        to: l.from + indent.length + old.length,
        insert: off ? '' : want,
      });
    });
  }
  return { changes };
}

/** A block of its own: separated by blank lines from the text before and after the cursor's line. */
function insertBlock(state: EditorState, block: string, select?: [number, number]): TransactionSpec {
  const line = state.doc.lineAt(state.selection.main.head);
  const prev = line.number > 1 ? state.doc.line(line.number - 1).text : '';
  const blank = line.text.trim() === '';
  // On a blank line the block takes its place; after text it follows the line.
  const at = blank ? line.from : line.to;
  const lead = blank ? (line.number > 1 && prev.trim() !== '' ? '\n' : '') : '\n\n';
  const nextLine = line.number < state.doc.lines ? state.doc.line(line.number + 1).text : null;
  const trail = nextLine === null ? '\n' : nextLine.trim() === '' ? '' : '\n';
  const insert = lead + block + trail;
  const start = at + lead.length;
  return {
    changes: { from: at, to: line.to, insert },
    selection: select
      ? EditorSelection.range(start + select[0], start + select[1])
      : EditorSelection.cursor(start + block.length),
  };
}

/**
 * A block between fences ("```" for code, "$$" for a formula) around the selected lines, with the cursor after the
 * opening one; without a selection an empty block, with the cursor inside.
 */
function fenced(state: EditorState, fence: string): TransactionSpec {
  const sel = state.selection.main;
  if (sel.empty) return insertBlock(state, `${fence}\n\n${fence}`, [fence.length + 1, fence.length + 1]);
  const first = state.doc.lineAt(sel.from);
  const last = state.doc.lineAt(
    sel.to > sel.from && state.doc.lineAt(sel.to).from === sel.to ? sel.to - 1 : sel.to,
  );
  return {
    changes: [
      { from: first.from, insert: `${fence}\n` },
      { from: last.to, insert: `\n${fence}` },
    ],
    selection: EditorSelection.cursor(first.from + fence.length),
  };
}

/** A link or an image around the selected text, with the address selected to type it. */
function link(state: EditorState, image: boolean, imageText: string): TransactionSpec {
  return state.changeByRange((range) => {
    const text = state.doc.sliceString(range.from, range.to) || (image ? imageText : '');
    const lead = image ? '![' : '[';
    const insert = `${lead}${text}](https://)`;
    const url = range.from + lead.length + text.length + 2;
    return {
      changes: { from: range.from, to: range.to, insert },
      range: text
        ? EditorSelection.range(url, url + 'https://'.length)
        : EditorSelection.cursor(range.from + 1),
    };
  });
}

/** The change an action makes to the text, or nothing where it has nothing to do. */
export function markdownTransaction(
  state: EditorState,
  action: MarkdownAction,
  words: MarkdownWords,
): TransactionSpec | null {
  const mark = marks[action];
  if (mark) return toggleMarks(state, mark);
  switch (action) {
    case 'math':
      return toggleFormula(state);
    case 'h1':
    case 'h2':
    case 'h3':
    case 'quote':
    case 'bullet':
    case 'ordered':
    case 'task':
      return toggleLines(state, action);
    case 'link':
      return link(state, false, words.image);
    case 'image':
      return link(state, true, words.image);
    case 'codeBlock':
      return fenced(state, '```');
    case 'mathBlock':
      return fenced(state, '$$');
    case 'table': {
      const head = `| ${words.column} 1 | ${words.column} 2 |`;
      return insertBlock(state, `${head}\n| --- | --- |\n|  |  |`, [2, 2 + words.column.length + 2]);
    }
    case 'rule':
      return insertBlock(state, '---');
  }
  return null;
}
