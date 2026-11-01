// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Extension, type AnyExtension } from '@tiptap/core';
import { collab } from 'prosemirror-collab';
import { RemoteCarets } from './carets';

/**
 * The extensions of an editor of a shared draft: ProseMirror's collaborative editing from a version of the draft,
 * as the editor with an id, and the carets of the others.
 */
export function collaboration(version: number, clientID: string): AnyExtension[] {
  return [
    Extension.create({
      name: 'koggeCollaboration',
      addProseMirrorPlugins: () => [collab({ version, clientID })],
    }),
    RemoteCarets,
  ];
}
