// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { Editor } from '@tiptap/core';
import { Node as PMNode, Slice } from '@tiptap/pm/model';
import { Mapping } from '@tiptap/pm/transform';
import { api, unwrap } from '../../api/client';
import { safeSlice } from './safe';

// Lines that only Markdown starts so: headings, lists, tasks, quotes, code fences, tables, rules.
const markdownBlock =
  /^(#{1,6}\s+\S|\s*([-*+]|\d{1,9}[.)])\s+\S|\s*[-*+]\s+\[[ xX]\]\s|>\s?\S|```|~~~|\|.*\|\s*$|(\*\s*){3,}$|(-\s*){3,}$)/m;
// Marks within text: bold, strike, code, links and images.
const markdownInline = /(\*\*|__)\S.*?\S?\1|~~\S.*?~~|`[^`\n]+`|!?\[[^\]\n]+\]\([^)\s]+\)/;
/*
 * Formulas: $…$ and $$…$$ within a line, and lines "$$" or "$$…$$" of their own. Stricter than the server, which
 * reads "$x$" between any characters: here no word character may come before the opening dollars or after the
 * closing ones, so that amounts ("$5 and $10", "$5-$10") and shell variables ("cp $HOME/$USER .") are pasted as
 * they are. Spaces are those of the server: space, tab, line feed and carriage return.
 */
const markdownMath =
  /(?:^|[^\\$\w])(\$\$?)(?:[^ \t\r\n$\\]|[^ \t\r\n$][^$\r\n]*[^ \t\r\n$\\])\1(?![\w$])|^ {0,3}\$\$(?:[ \t]*$|(?=[ \t]*[^ \t\r\n$])[^$\r\n]*(?<!\\)\$\$[ \t]*$)/m;

/** Whether pasted text is Markdown worth reading as such (plain sentences are pasted as they are). */
export function looksLikeMarkdown(text: string): boolean {
  return markdownBlock.test(text) || markdownInline.test(text) || markdownMath.test(text);
}

/**
 * The content of a document of the server as pasted content. A paragraph at its start or end is open, so that its
 * text joins the paragraph pasted into; headings, lists and code stay blocks of their own.
 */
export function markdownSlice(editor: Editor, doc: unknown, externalImages: boolean): Slice | null {
  try {
    const content = PMNode.fromJSON(editor.schema, doc).content;
    const open = Slice.maxOpen(content);
    const paragraph = (n: PMNode | null) => n?.type.name === 'paragraph';
    const slice = new Slice(
      content,
      paragraph(content.firstChild) ? open.openStart : 0,
      paragraph(content.lastChild) ? open.openEnd : 0,
    );
    return safeSlice(slice, externalImages);
  } catch {
    return null; // content this editor does not know: pasted as text
  }
}

/**
 * Pastes Markdown as formatted content: the server reads it as it reads Markdown files. Its answer comes after other
 * changes may have happened (other editors of a shared draft keep typing), so the place of the paste is mapped
 * through them; without an answer, the text is pasted as it is.
 */
export function pasteMarkdown(editor: Editor, text: string, externalImages: boolean) {
  const bookmark = editor.state.selection.getBookmark();
  const mapping = new Mapping();
  const track = ({ transaction }: { transaction: { docChanged: boolean; mapping: Mapping } }) => {
    if (transaction.docChanged) mapping.appendMapping(transaction.mapping);
  };
  editor.on('transaction', track);
  void unwrap(api.POST('/api/v1/code/markdown', { body: { markdown: text } }))
    .then(
      (res) => markdownSlice(editor, res.doc, externalImages),
      () => null,
    )
    .then((slice) => {
      editor.off('transaction', track);
      if (editor.isDestroyed) return;
      const tr = editor.state.tr.setSelection(bookmark.map(mapping).resolve(editor.state.doc));
      if (slice) tr.replaceSelection(slice);
      else tr.insertText(text);
      editor.view.dispatch(tr.scrollIntoView());
    });
}
