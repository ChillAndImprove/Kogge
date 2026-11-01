// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { canInsertNode, InputRule, mergeAttributes, Node, type Editor } from '@tiptap/core';
import type { Node as PMNode } from '@tiptap/pm/model';
import { NodeSelection, TextSelection } from '@tiptap/pm/state';
import { ReactNodeViewRenderer } from '@tiptap/react';
import { latexOf, latexTooLong } from '../math/latex';
import { MathNodeView } from './MathNodeView';
import { requestDialog } from './SlashMenu';

/*
 * Formulas: "math" stands on a line of its own, "inlineMath" within the text; both are atoms that keep
 * their LaTeX in "latex", typeset by KaTeX where they are shown (MathFormula.tsx). Typing $…$ makes a formula of
 * what is between the dollars, by the rules the server reads Markdown with (internal/docmodel); "$$" alone in a
 * paragraph, followed by Enter or a space, asks for one on a line of its own. Formulas the server would refuse (too
 * long) are never made: a refused change makes a shared draft load again.
 */

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    math: {
      /** Inserts a formula at the selection: within the text, or on a line of its own with the cursor after it. */
      insertMath: (latex: string, display: boolean) => ReturnType;
      /** Gives the formula at a position (by default the one selected) other LaTeX. */
      updateMath: (latex: string, pos?: number) => ReturnType;
    };
  }
}

/** Whether a node is a formula. */
function isFormula(node: PMNode | null | undefined): node is PMNode {
  return node?.type.name === 'math' || node?.type.name === 'inlineMath';
}

/** The formula selected as a whole, if one is. */
export function selectedFormula(editor: Editor): PMNode | null {
  const { selection } = editor.state;
  return selection instanceof NodeSelection && isFormula(selection.node) ? selection.node : null;
}

const space = (c: string | undefined) => c === ' ' || c === '\t' || c === '\n' || c === '\r';

/**
 * Where a formula $…$ (or $$…$$) at the start of text ends, as the server's Markdown reader finds it: the opening
 * dollars are not followed by a space, the closing ones not preceded by a space nor followed by a digit, a backslash
 * takes the next character with it, and the formula ends with its line. Returns the index after the closing
 * dollars, or -1.
 */
function formulaEnd(text: string, start: number, run: number): number {
  if (run > 2 || start + run >= text.length || space(text[start + run])) return -1;
  for (let i = start + run; i < text.length; i++) {
    const c = text[i];
    if (c === '\\') i++;
    else if (c === '\n' || c === '\r') return -1;
    else if (c === '$') {
      if (run === 2 && text[i + 1] !== '$') return -1;
      if (space(text[i - 1]) || /[0-9]/.test(text[i + run] ?? '')) return -1;
      return i + run;
    }
  }
  return -1;
}

/** A formula that the text ends with: where it starts, and its LaTeX. */
export interface TypedFormula {
  index: number;
  latex: string;
}

/**
 * The formula that the text before the cursor ends with, once its closing dollar is typed: read from the start of
 * the line as the server reads Markdown, so that dollars of formulas before it pair as there. Typing is a little
 * stricter, as $ also stands for amounts: no letter or digit right before the opening dollar (US$5/$6), and
 * nothing that only holds digits ($5-$10).
 */
export function typedFormula(text: string): TypedFormula | null {
  if (!text.endsWith('$')) return null;
  for (let i = 0; i < text.length;) {
    if (text[i] === '\\') {
      i += 2;
      continue;
    }
    if (text[i] !== '$') {
      i++;
      continue;
    }
    let run = 1;
    while (text[i + run] === '$') run++;
    const end = formulaEnd(text, i, run);
    if (end < 0) {
      i += run;
      continue;
    }
    if (end === text.length) {
      const latex = text.slice(i + run, end - run);
      if (/[\p{L}\p{N}_]/u.test(text[i - 1] ?? '') || /^[\d\s.,-]*$/.test(latex)) return null;
      return { index: i, latex };
    }
    i = end;
  }
  return null;
}

/** Kogge's formulas (its editor's HTML, its read view: MathFormula) carry their LaTeX in data-latex; one longer
 * than a formula may be stays what it shows. */
const ownFormula = (el: HTMLElement) => (latexTooLong(el.getAttribute('data-latex') ?? '') ? false : null);

/** The LaTeX of a formula from another page, as KaTeX and MathML keep it in their annotation; false where there is
 * none, or one longer than a formula may be. */
function annotated(el: HTMLElement): { latex: string } | false {
  const tex = el.querySelector('annotation[encoding="application/x-tex"]')?.textContent?.trim();
  return tex && !latexTooLong(tex) ? { latex: tex } : false;
}

/** The attribute latex, data-latex in HTML. */
const latexAttribute = () => ({
  latex: {
    default: '',
    // Null where it is missing, so that the LaTeX of an annotation (the rules' getAttrs) stays.
    parseHTML: (el: HTMLElement) => el.getAttribute('data-latex'),
    renderHTML: (attrs: Record<string, unknown>) => ({ 'data-latex': latexOf(attrs) }),
  },
});

/** Enter on a formula selected edits it, in the toolbar's dialog. */
function editSelected(editor: Editor, name: 'math' | 'inlineMath'): boolean {
  const node = selectedFormula(editor);
  return editor.isEditable && node?.type.name === name && requestDialog(editor, name);
}

/** A formula on a line of its own (display style). */
export const MathNode = Node.create({
  name: 'math',
  group: 'block',
  atom: true,
  addAttributes: latexAttribute,
  parseHTML() {
    return [
      { tag: 'div[data-math]', getAttrs: ownFormula },
      // KaTeX's and MathML's formulas from other pages.
      { tag: 'span.katex-display', getAttrs: annotated },
      { tag: 'math[display="block"]', getAttrs: annotated },
    ];
  },
  renderHTML({ node, HTMLAttributes }) {
    return [
      'div',
      mergeAttributes(HTMLAttributes, { 'data-math': '', class: 'kogge-math-block' }),
      `$$${latexOf(node.attrs)}$$`,
    ];
  },
  renderText({ node }) {
    return `$$\n${latexOf(node.attrs)}\n$$`;
  },
  addNodeView() {
    return ReactNodeViewRenderer(MathNodeView, { className: 'kogge-math-node' });
  },
  addCommands() {
    return {
      insertMath:
        (latex, display) =>
        ({ state, chain }) => {
          const type = state.schema.nodes[display ? 'math' : 'inlineMath'];
          if (!type || latexTooLong(latex) || !canInsertNode(state, type)) return false;
          const content = { type: type.name, attrs: { latex } };
          // As a divider: in place of the selection (an empty paragraph goes), after a node selected.
          const { selection } = state;
          const inserting = chain();
          if (selection instanceof NodeSelection) inserting.insertContentAt(selection.$to.pos, content);
          else inserting.insertContent(content);
          if (!display) return inserting.run();
          // The cursor goes after a formula on its own line: into the block after it, at the end of the document
          // into a new paragraph (shared drafts add none by themselves).
          return inserting
            .command(({ tr, dispatch }) => {
              if (!dispatch) return true;
              const { $to } = tr.selection;
              const after = $to.nodeAfter;
              if (after?.isTextblock) tr.setSelection(TextSelection.create(tr.doc, $to.pos + 1));
              else if (after?.isBlock) tr.setSelection(NodeSelection.create(tr.doc, $to.pos));
              else if (after) tr.setSelection(TextSelection.create(tr.doc, $to.pos));
              else {
                const end = $to.end();
                const paragraph = $to.parent.type.contentMatch.defaultType?.create();
                if (paragraph) {
                  tr.insert(end, paragraph);
                  tr.setSelection(TextSelection.create(tr.doc, end + 1));
                }
              }
              tr.scrollIntoView();
              return true;
            })
            .run();
        },
      updateMath:
        (latex, pos) =>
        ({ state, tr, dispatch }) => {
          const at = pos ?? (state.selection instanceof NodeSelection ? state.selection.from : -1);
          if (at < 0 || !isFormula(state.doc.nodeAt(at)) || latexTooLong(latex)) return false;
          if (dispatch) tr.setNodeAttribute(at, 'latex', latex);
          return true;
        },
    };
  },
  addKeyboardShortcuts() {
    return { Enter: ({ editor }) => editSelected(editor, 'math') };
  },
  addInputRules() {
    const editor = this.editor;
    return [
      new InputRule({
        find: /^\$\$[\n ]$/,
        handler: ({ state, range }) => {
          // Only where the paragraph holds nothing else; the formula comes from the dialog, in its place.
          const $from = state.doc.resolve(range.from);
          if ($from.parent.type.name !== 'paragraph' || $from.parent.textContent.trim() !== '$$') return null;
          if (!requestDialog(editor, 'math')) return null;
          state.tr.delete(range.from, range.to);
        },
      }),
    ];
  },
});

/** A formula within the text. */
export const InlineMathNode = Node.create({
  name: 'inlineMath',
  group: 'inline',
  inline: true,
  atom: true,
  addAttributes: latexAttribute,
  parseHTML() {
    return [
      { tag: 'span[data-inline-math]', getAttrs: ownFormula },
      // KaTeX's and MathML's formulas from other pages.
      { tag: 'span.katex', getAttrs: annotated },
      { tag: 'math', getAttrs: (el) => el.getAttribute('display') !== 'block' && annotated(el) },
    ];
  },
  renderHTML({ node, HTMLAttributes }) {
    return [
      'span',
      mergeAttributes(HTMLAttributes, { 'data-inline-math': '', class: 'kogge-math-inline' }),
      `$${latexOf(node.attrs)}$`,
    ];
  },
  renderText({ node }) {
    return `$${latexOf(node.attrs)}$`;
  },
  addNodeView() {
    return ReactNodeViewRenderer(MathNodeView, { as: 'span', className: 'kogge-math-node' });
  },
  addKeyboardShortcuts() {
    return { Enter: ({ editor }) => editSelected(editor, 'inlineMath') };
  },
  addInputRules() {
    const type = this.type;
    return [
      new InputRule({
        find: (text) => {
          const formula = typedFormula(text);
          return (
            formula && {
              index: formula.index,
              text: text.slice(formula.index),
              data: { latex: formula.latex },
            }
          );
        },
        handler: ({ state, range, match }) => {
          const latex = String(match.data?.latex ?? '');
          if (!latex || latexTooLong(latex)) return null;
          state.tr.replaceWith(range.from, range.to, type.create({ latex }));
        },
      }),
    ];
  },
});
