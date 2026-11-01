// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Editor, type JSONContent } from '@tiptap/core';
import { Step } from '@tiptap/pm/transform';
import { receiveTransaction, sendableSteps } from 'prosemirror-collab';
import { afterEach, describe, expect, it } from 'vitest';
import { collaboration } from './collab';
import { editorExtensions } from './extensions';
import { findMatches, findState } from './findReplace';

const t = (text: string, marks?: JSONContent['marks']): JSONContent => ({ type: 'text', text, marks });
const p = (...content: JSONContent[]): JSONContent => ({ type: 'paragraph', content });

const editors: Editor[] = [];
afterEach(() => {
  for (const e of editors.splice(0)) e.destroy();
});

function make(content: JSONContent[], collab?: { version: number; client: string }) {
  const e = new Editor({
    element: document.createElement('div'),
    extensions: [
      ...editorExtensions('', { collaborative: !!collab, wiki: true }),
      ...(collab ? collaboration(collab.version, collab.client) : []),
    ],
    content: { type: 'doc', content },
  });
  editors.push(e);
  return e;
}

/** Steps of one editor as another receives them: as JSON, read with its own schema (the server sends JSON). */
function deliver(from: Editor, to: Editor[]) {
  const sent = sendableSteps(from.state);
  if (!sent) throw new Error('nothing to send');
  const json = sent.steps.map((st) => st.toJSON());
  for (const e of to) {
    const steps = json.map((j) => Step.fromJSON(e.schema, j));
    e.view.dispatch(
      receiveTransaction(
        e.state,
        steps,
        steps.map(() => sent.clientID),
      ),
    );
  }
  return json.length;
}

const texts = (e: Editor) => findState(e.state).matches.map((m) => e.state.doc.textBetween(m.from, m.to));
const any = { caseSensitive: false, wholeWord: false };

describe('find', () => {
  it('finds text across marks, within blocks, and not where inline nodes stand between', () => {
    const e = make([
      p(t('Release the '), t('Rel', [{ type: 'bold' }]), t('ease notes. release!')),
      p(t('Rele')),
      p(
        t('ase '),
        { type: 'mention', attrs: { user: 1, label: 'x' } },
        t('re'),
        { type: 'hardBreak' },
        t('lease'),
      ),
      { type: 'codeBlock', content: [t('release()')] },
    ]);
    const doc = e.state.doc;
    const found = findMatches(doc, 'release', any).map((m) => doc.textBetween(m.from, m.to));
    expect(found).toEqual(['Release', 'Release', 'release', 'release']);
    expect(findMatches(doc, 'release', { caseSensitive: true, wholeWord: false })).toHaveLength(2);
    // Whole words: not within other words; punctuation and spaces bound words.
    expect(findMatches(doc, 'lease', { caseSensitive: false, wholeWord: true })).toHaveLength(1);
    expect(findMatches(doc, 'lease', any)).toHaveLength(5);
    // The query is text, not a pattern.
    expect(findMatches(doc, '()', any)).toHaveLength(1);
    expect(findMatches(doc, '.', any)).toHaveLength(1);
    expect(findMatches(doc, '', any)).toEqual([]);
  });

  it('ignores case of umlauts and finds words with them', () => {
    const e = make([p(t('Größe, GRÖSSE und größere Größen'))]);
    expect(findMatches(e.state.doc, 'grösse', any)).toHaveLength(1);
    expect(findMatches(e.state.doc, 'größe', { caseSensitive: false, wholeWord: true })).toHaveLength(1);
    expect(findMatches(e.state.doc, 'größe', any)).toHaveLength(3);
  });

  it('highlights the matches, moves between them and follows changes of the document', () => {
    const e = make([p(t('one two one')), p(t('two one'))]);
    e.commands.setTextSelection(1);
    e.commands.setSearch('one', any);
    expect(texts(e)).toEqual(['one', 'one', 'one']);
    expect(e.view.dom.querySelectorAll('.kogge-find-match')).toHaveLength(3);
    expect(findState(e.state).current).toBe(0);
    e.commands.findNext();
    e.commands.findNext();
    expect(findState(e.state).current).toBe(1);
    expect(e.view.dom.querySelector('.kogge-find-current')?.textContent).toBe('one');
    e.commands.findNext(-1);
    expect(findState(e.state).current).toBe(0);
    e.commands.findNext(-1);
    expect(findState(e.state).current).toBe(2); // around to the last one
    // Typing elsewhere keeps the current match and finds what the new text adds.
    e.commands.insertContentAt(1, 'one ');
    expect(texts(e)).toHaveLength(4);
    expect(findState(e.state).current).toBe(3);
    e.commands.clearSearch();
    expect(e.view.dom.querySelectorAll('.kogge-find-match')).toHaveLength(0);
  });

  it('replaces one match in its marks, and all of them as one change that undo takes back', () => {
    const e = make([p(t('Kogge ', [{ type: 'bold' }]), t('and kogge and KOGGE'))]);
    e.commands.setTextSelection(1);
    e.commands.setSearch('kogge', any);
    e.commands.findNext();
    e.commands.replaceCurrent('Logbuch');
    expect(e.state.doc.textContent).toBe('Logbuch and kogge and KOGGE');
    // The bold of the replaced text stays; the next match is current.
    expect(e.getJSON().content?.[0].content?.[0]).toMatchObject({
      text: 'Logbuch ',
      marks: [{ type: 'bold' }],
    });
    expect(texts(e)).toEqual(['kogge', 'KOGGE']);
    expect(findState(e.state).current).toBe(0);
    e.commands.replaceAll('Hafen');
    expect(e.state.doc.textContent).toBe('Logbuch and Hafen and Hafen');
    expect(findState(e.state).matches).toHaveLength(0);
    e.commands.undo();
    expect(e.state.doc.textContent).toBe('Logbuch and kogge and KOGGE');
    e.commands.undo();
    expect(e.state.doc.textContent).toBe('Kogge and kogge and KOGGE');
  });

  it('updates its matches while someone else edits the same draft, and replaces through the shared draft', () => {
    const doc = [p(t('Draft: plan the plan'))];
    const mine = make(doc, { version: 0, client: '1:mine' });
    const theirs = make(doc, { version: 0, client: '2:theirs' });
    mine.commands.setTextSelection(1);
    mine.commands.setSearch('plan', any);
    expect(texts(mine)).toEqual(['plan', 'plan']);
    // The other person types a match; the server confirms their steps and sends them to me.
    theirs.commands.insertContentAt(theirs.state.doc.content.size - 1, ', then plan more');
    deliver(theirs, [theirs, mine]);
    expect(texts(mine)).toEqual(['plan', 'plan', 'plan']);
    // Replacing all is my change: steps for the server, as typing is.
    mine.commands.replaceAll('draft');
    expect(mine.state.doc.textContent).toBe('Draft: draft the draft, then draft more');
    expect(deliver(mine, [mine, theirs])).toBe(3);
    expect(theirs.state.doc.textContent).toBe('Draft: draft the draft, then draft more');
  });
});
