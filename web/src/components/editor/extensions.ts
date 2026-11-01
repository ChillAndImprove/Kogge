// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { AnyExtension } from '@tiptap/core';
import Highlight from '@tiptap/extension-highlight';
import { TaskItem, TaskList } from '@tiptap/extension-list';
import Placeholder from '@tiptap/extension-placeholder';
import Subscript from '@tiptap/extension-subscript';
import Superscript from '@tiptap/extension-superscript';
import { Table, TableCell, TableHeader, TableRow } from '@tiptap/extension-table';
import { ReactRenderer } from '@tiptap/react';
import StarterKit from '@tiptap/starter-kit';
import { api, unwrap } from '../../api/client';
import { CodeHighlight } from './codeHighlight';
import { EditorCodeBlock } from './CodeBlockView';
import { Column, Columns, KoggeDocument } from './columns';
import { FindReplace } from './findReplace';
import { InlineMathNode, MathNode } from './math';
import { MentionList, type MentionItem, type MentionListHandle } from './MentionList';
import {
  AnchorMark,
  DateNode,
  EmojiNode,
  InlineImageNode,
  Expand,
  InlineMacroNode,
  IssueNode,
  MacroNode,
  ModelAttributes,
  Panel,
  StatusNode,
  TextColor,
  UnsupportedNode,
  UserMention,
} from './nodes';
import { autolinkTarget, SafeContent, SafeImage, safeLink } from './safe';
import { SlashCommands } from './SlashMenu';

async function findUsers(query: string): Promise<MentionItem[]> {
  try {
    const res = await unwrap(api.GET('/api/v1/users', { params: { query: { query, limit: 8 } } }));
    return res.items.map((u) => ({ id: u.id, label: u.displayName, username: u.username }));
  } catch {
    return [];
  }
}

/**
 * The editor's extensions; node and mark names match the server's document model. Editors of shared
 * drafts add no paragraph after a last table or macro by themselves: every editor would add one. The "/" menu
 * offers the wiki's blocks where the editor has them, and images where files can be uploaded. Links and images
 * follow the server's rules (safe.ts); images from other servers only where the administration allows them.
 */
export function editorExtensions(
  placeholder: string,
  options: { collaborative?: boolean; wiki?: boolean; images?: boolean; externalImages?: boolean } = {},
): AnyExtension[] {
  return [
    StarterKit.configure({
      // The document of columns.ts, which also holds layouts.
      document: false,
      heading: { levels: [1, 2, 3, 4, 5, 6] },
      link: {
        openOnClick: false,
        autolink: true,
        protocols: ['http', 'https', 'mailto'],
        isAllowedUri: (url) => safeLink(url),
        shouldAutoLink: (text) => safeLink(autolinkTarget(text)),
      },
      codeBlock: false,
      ...(options.collaborative ? { trailingNode: false } : {}),
    }),
    KoggeDocument,
    EditorCodeBlock,
    CodeHighlight,
    SlashCommands.configure({ wiki: !!options.wiki, images: !!options.images }),
    Placeholder.configure({ placeholder }),
    Table.configure({ resizable: false }),
    TableRow,
    TableHeader,
    TableCell,
    TaskList,
    TaskItem.configure({ nested: true }),
    SafeImage.configure({ inline: false, allowBase64: false, externalImages: !!options.externalImages }),
    SafeContent.configure({ externalImages: !!options.externalImages }),
    Highlight,
    Subscript,
    Superscript,
    Panel,
    IssueNode,
    StatusNode,
    AnchorMark,
    TextColor,
    Expand,
    DateNode,
    EmojiNode,
    InlineImageNode,
    UnsupportedNode,
    InlineMacroNode,
    MacroNode,
    // Formulas in every editor (descriptions and comments, too): all editors and the server share one schema.
    MathNode,
    InlineMathNode,
    Columns,
    Column,
    FindReplace,
    ModelAttributes,
    UserMention.configure({
      HTMLAttributes: { class: 'kogge-mention' },
      renderText: ({ node }) => `@${node.attrs.label ?? ''}`,
      renderHTML: ({ node }) => [
        'span',
        { class: 'kogge-mention', 'data-user': node.attrs.user },
        `@${node.attrs.label ?? ''}`,
      ],
      suggestion: {
        char: '@',
        items: ({ query }) => findUsers(query),
        command: ({ editor, range, props }) => {
          const p = props as unknown as { user: number; label: string };
          editor
            .chain()
            .focus()
            .insertContentAt(range, [
              { type: 'mention', attrs: { user: p.user, label: p.label } },
              { type: 'text', text: ' ' },
            ])
            .run();
        },
        render: () => {
          let renderer: ReactRenderer<MentionListHandle> | null = null;
          let element: HTMLDivElement | null = null;
          const place = (rect: DOMRect | null | undefined) => {
            if (!element || !rect) return;
            element.style.left = `${rect.left + window.scrollX}px`;
            element.style.top = `${rect.bottom + window.scrollY + 4}px`;
          };
          return {
            onStart: (props) => {
              renderer = new ReactRenderer(MentionList, { props, editor: props.editor });
              element = document.createElement('div');
              element.style.position = 'absolute';
              element.style.zIndex = '1000';
              element.appendChild(renderer.element);
              document.body.appendChild(element);
              place(props.clientRect?.());
            },
            onUpdate: (props) => {
              renderer?.updateProps(props);
              place(props.clientRect?.());
            },
            onKeyDown: (props) => {
              if (props.event.key === 'Escape') {
                element?.remove();
                return true;
              }
              return renderer?.ref?.onKeyDown(props.event) ?? false;
            },
            onExit: () => {
              renderer?.destroy();
              element?.remove();
              renderer = null;
              element = null;
            },
          };
        },
      },
    }),
  ];
}
