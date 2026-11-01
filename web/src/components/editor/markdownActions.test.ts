// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { EditorSelection, EditorState } from '@codemirror/state';
import { describe, expect, it } from 'vitest';
import { markdownTransaction, type MarkdownAction } from './markdownActions';

const words = { column: 'Column', image: 'Image' };

/** Applies an action to a text whose selection is marked with « and » (a cursor with |); returns it marked so. */
function run(marked: string, action: MarkdownAction): string {
  const cursor = marked.indexOf('|');
  const from = cursor >= 0 ? cursor : marked.indexOf('«');
  const to = cursor >= 0 ? cursor : marked.indexOf('»') - 1;
  const doc = cursor >= 0 ? marked.replace('|', '') : marked.replace('«', '').replace('»', '');
  const state = EditorState.create({ doc, selection: EditorSelection.range(from, to) });
  const spec = markdownTransaction(state, action, words);
  if (!spec) return marked;
  const next = state.update(spec).state;
  const sel = next.selection.main;
  const text = next.doc.toString();
  return sel.empty
    ? text.slice(0, sel.from) + '|' + text.slice(sel.from)
    : text.slice(0, sel.from) + '«' + text.slice(sel.from, sel.to) + '»' + text.slice(sel.to);
}

describe('markdownTransaction', () => {
  it('puts marks around the selection, and takes them away again', () => {
    expect(run('a «word» b', 'bold')).toBe('a **«word»** b');
    expect(run('a **«word»** b', 'bold')).toBe('a «word» b');
    expect(run('a ~~«word»~~ b', 'strike')).toBe('a «word» b');
    expect(run('run «ls»', 'code')).toBe('run `«ls»`');
  });

  it('puts marks around the cursor, to type between them', () => {
    expect(run('a | b', 'italic')).toBe('a *|* b');
  });

  it('reads one "*" next to another as part of bold, not as italics', () => {
    expect(run('**«bold»**', 'italic')).toBe('***«bold»***');
  });

  it('makes lines headings of a level, changes the level and takes it away', () => {
    expect(run('Ti|tle', 'h2')).toBe('## Ti|tle');
    expect(run('# Ti|tle', 'h3')).toBe('### Ti|tle');
    expect(run('## Ti|tle', 'h2')).toBe('Ti|tle');
    expect(run('### Ti|tle', 'h2')).toBe('## Ti|tle');
  });

  it('makes the selected lines a list, skipping blank lines, and turns one kind of list into another', () => {
    expect(run('«one\n\ntwo»', 'bullet')).toBe('- «one\n\n- two»');
    expect(run('«one\ntwo\nthree»', 'ordered')).toBe('1. «one\n2. two\n3. three»');
    expect(run('«- one\n- two»', 'task')).toBe('«- [ ] one\n- [ ] two»');
    expect(run('«- [ ] one\n- [x] two»', 'task')).toBe('«one\ntwo»');
    expect(run('  - in|ner', 'ordered')).toBe('  1. in|ner');
  });

  it('quotes lines and unquotes them', () => {
    expect(run('«a\nb»', 'quote')).toBe('> «a\n> b»');
    expect(run('«> a\n>b»', 'quote')).toBe('«a\nb»');
  });

  it('makes a link of the selection with the address to type selected', () => {
    expect(run('see «docs» here', 'link')).toBe('see [docs](«https://») here');
    expect(run('|', 'image')).toBe('![Image](«https://»)');
  });

  it('puts blocks between blank lines', () => {
    expect(run('Text|', 'rule')).toBe('Text\n\n---|\n');
    expect(run('a\n|\nb', 'rule')).toBe('a\n\n---|\n\nb');
    expect(run('|', 'table')).toBe('| «Column 1» | Column 2 |\n| --- | --- |\n|  |  |\n');
    expect(run('|', 'codeBlock')).toBe('```\n|\n```\n');
    expect(run('«x := 1\ny := 2»', 'codeBlock')).toBe('```|\nx := 1\ny := 2\n```');
  });

  it('makes the selection a formula within the line, and a formula text again', () => {
    expect(run('a «x^2» b', 'math')).toBe('a $«x^2»$ b');
    expect(run('a $«x^2»$ b', 'math')).toBe('a «x^2» b');
    expect(run('a | b', 'math')).toBe('a $|$ b');
    expect(run('a $|$ b', 'math')).toBe('a | b');
    // "$$x$$" within a line is a formula as well.
    expect(run('a $$«x»$$ b', 'math')).toBe('a «x» b');
    // Next to a formula, a new one starts: "$x$$y$" are two.
    expect(run('$x$|', 'math')).toBe('$x$$|$');
  });

  it('puts a formula of its own around the selected lines, or an empty one between blank lines', () => {
    expect(run('|', 'mathBlock')).toBe('$$\n|\n$$\n');
    expect(run('Text|', 'mathBlock')).toBe('Text\n\n$$\n|\n$$\n');
    expect(run('«a = b\nc = d»', 'mathBlock')).toBe('$$|\na = b\nc = d\n$$');
  });
});
