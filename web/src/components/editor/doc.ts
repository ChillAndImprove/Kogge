// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { JSONContent } from '@tiptap/react';

// Documents without the editor, for pages that only show or check them.

export type Doc = JSONContent;

/** Whether a document has no text or other content. */
export function isEmptyDoc(doc: Doc | null | undefined): boolean {
  if (!doc?.content?.length) return true;
  return doc.content.every((n) => n.type === 'paragraph' && !n.content?.length);
}
