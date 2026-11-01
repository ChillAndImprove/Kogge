// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Extension, Mark, mergeAttributes, Node } from '@tiptap/core';
import Mention from '@tiptap/extension-mention';
import { ReactNodeViewRenderer } from '@tiptap/react';
import { formatDay } from '../../app/format';
import { emojiGlyph } from './emoji';
import { InlineMacroNodeView, MacroNodeView } from './MacroNodeView';

/** Info, note, warning, success and error panels. */
export const Panel = Node.create({
  name: 'panel',
  group: 'block',
  content: 'block+',
  defining: true,
  addAttributes() {
    return {
      kind: {
        default: 'info',
        parseHTML: (el) => el.getAttribute('data-kind') ?? 'info',
        renderHTML: (attrs) => ({ 'data-kind': attrs.kind }),
      },
    };
  },
  parseHTML() {
    return [{ tag: 'div[data-panel]' }];
  },
  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-panel': '', class: 'kogge-panel' }), 0];
  },
});

/** An issue reference rendered as a chip. */
export const IssueNode = Node.create({
  name: 'issue',
  group: 'inline',
  inline: true,
  atom: true,
  addAttributes() {
    return {
      key: {
        default: null,
        parseHTML: (el) => el.getAttribute('data-issue'),
        renderHTML: (a) => ({ 'data-issue': a.key }),
      },
      display: { default: 'inline' },
    };
  },
  parseHTML() {
    return [{ tag: 'span[data-issue]' }];
  },
  renderHTML({ node, HTMLAttributes }) {
    return ['span', mergeAttributes(HTMLAttributes, { class: 'kogge-issue-chip' }), node.attrs.key];
  },
  renderText({ node }) {
    return node.attrs.key;
  },
});

/** A coloured status lozenge with text. */
export const StatusNode = Node.create({
  name: 'status',
  group: 'inline',
  inline: true,
  atom: true,
  addAttributes() {
    return { text: { default: '' }, color: { default: 'gray' } };
  },
  parseHTML() {
    return [{ tag: 'span[data-status]' }];
  },
  renderHTML({ node, HTMLAttributes }) {
    return [
      'span',
      mergeAttributes(HTMLAttributes, { 'data-status': node.attrs.color, class: 'kogge-status' }),
      node.attrs.text,
    ];
  },
});

/** Mentions store the user id in "user", as the server's document model expects. */
export const UserMention = Mention.extend({
  addAttributes() {
    return {
      user: {
        default: null,
        parseHTML: (el) => Number(el.getAttribute('data-user')),
        renderHTML: (a) => ({ 'data-user': a.user }),
      },
      label: {
        default: null,
        parseHTML: (el) => el.getAttribute('data-label'),
        renderHTML: (a) => ({ 'data-label': a.label }),
      },
    };
  },
});

/** Marks the text an inline comment refers to. Several anchors may overlap. */
export const AnchorMark = Mark.create({
  name: 'anchor',
  inclusive: false,
  excludes: '',
  addAttributes() {
    return {
      id: {
        default: null,
        parseHTML: (el) => el.getAttribute('data-anchor'),
        renderHTML: (a) => ({ 'data-anchor': a.id }),
      },
    };
  },
  parseHTML() {
    return [{ tag: 'span[data-anchor]' }];
  },
  renderHTML({ HTMLAttributes }) {
    return ['span', mergeAttributes(HTMLAttributes, { class: 'kogge-anchor' }), 0];
  },
});

/** Text colour from the document palette. */
export const TextColor = Mark.create({
  name: 'textColor',
  addAttributes() {
    return {
      color: {
        default: '',
        parseHTML: (el) => el.getAttribute('data-color'),
        renderHTML: (a) => ({ 'data-color': a.color }),
      },
    };
  },
  parseHTML() {
    return [{ tag: 'span[data-color]' }];
  },
  renderHTML({ HTMLAttributes }) {
    return ['span', mergeAttributes(HTMLAttributes, { class: 'kogge-color' }), 0];
  },
});

/** A collapsible section with a title. */
export const Expand = Node.create({
  name: 'expand',
  group: 'block',
  content: 'block+',
  defining: true,
  addAttributes() {
    return {
      title: {
        default: '',
        parseHTML: (el) => el.getAttribute('data-title') ?? '',
        renderHTML: (a) => ({ 'data-title': a.title }),
      },
    };
  },
  parseHTML() {
    return [{ tag: 'div[data-expand]' }];
  },
  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-expand': '', class: 'kogge-expand' }), 0];
  },
});

/** A date lozenge (YYYY-MM-DD), shown as readers see it. */
export const DateNode = Node.create({
  name: 'date',
  group: 'inline',
  inline: true,
  atom: true,
  addAttributes() {
    return { value: { default: '' } };
  },
  parseHTML() {
    return [{ tag: 'time[data-date]' }];
  },
  renderHTML({ node, HTMLAttributes }) {
    return [
      'time',
      mergeAttributes(HTMLAttributes, { 'data-date': node.attrs.value, class: 'kogge-date' }),
      formatDay(node.attrs.value) || node.attrs.value,
    ];
  },
});

/** An emoji by name: its character where Kogge knows the name, else :name:. */
export const EmojiNode = Node.create({
  name: 'emoji',
  group: 'inline',
  inline: true,
  atom: true,
  addAttributes() {
    return { name: { default: '' } };
  },
  parseHTML() {
    return [{ tag: 'span[data-emoji]' }];
  },
  renderHTML({ node, HTMLAttributes }) {
    return [
      'span',
      mergeAttributes(HTMLAttributes, { 'data-emoji': node.attrs.name, title: `:${node.attrs.name}:` }),
      emojiGlyph(node.attrs.name) ?? `:${node.attrs.name}:`,
    ];
  },
});

/** Content from a migration that Kogge cannot show; kept with its source. */
/** Images within text, as Markdown has them (badges, icons); href is the link around the image. Editing keeps
 * them; new ones come from Markdown. */
export const InlineImageNode = Node.create({
  name: 'inlineImage',
  group: 'inline',
  inline: true,
  atom: true,
  addAttributes() {
    return { src: { default: '' }, alt: { default: '' }, title: { default: null }, href: { default: null } };
  },
  parseHTML() {
    return [
      {
        tag: 'img[data-inline-image]',
        priority: 60, // before the block image
        getAttrs: (el) => ({
          src: el.getAttribute('src') ?? '',
          alt: el.getAttribute('alt') ?? '',
          title: el.getAttribute('title'),
          href: el.getAttribute('data-href'),
        }),
      },
    ];
  },
  renderHTML({ node }) {
    return [
      'img',
      {
        'data-inline-image': '',
        class: 'kogge-inline-image',
        src: node.attrs.src,
        alt: node.attrs.alt,
        title: node.attrs.title,
        'data-href': node.attrs.href,
        referrerpolicy: 'no-referrer',
      },
    ];
  },
});

export const UnsupportedNode = Node.create({
  name: 'unsupported',
  group: 'block',
  atom: true,
  addAttributes() {
    return { source: { default: '' }, name: { default: '' }, raw: { default: '' } };
  },
  parseHTML() {
    return [{ tag: 'div[data-unsupported]' }];
  },
  renderHTML({ node, HTMLAttributes }) {
    return [
      'div',
      mergeAttributes(HTMLAttributes, { 'data-unsupported': '', class: 'kogge-unsupported' }),
      `[${node.attrs.source} ${node.attrs.name}]`,
    ];
  },
});

/** An inline macro (parameters only). */
export const InlineMacroNode = Node.create({
  name: 'inlineMacro',
  group: 'inline',
  inline: true,
  atom: true,
  addAttributes() {
    return { name: { default: '' }, params: { default: {} } };
  },
  parseHTML() {
    return [{ tag: 'span[data-inline-macro]' }];
  },
  renderHTML({ node, HTMLAttributes }) {
    return [
      'span',
      mergeAttributes(HTMLAttributes, { 'data-inline-macro': node.attrs.name, class: 'kogge-inline-macro' }),
      node.attrs.name,
    ];
  },
  addNodeView() {
    return ReactNodeViewRenderer(InlineMacroNodeView, { as: 'span' });
  },
});

/** A block macro with parameters and, for body macros, content. */
export const MacroNode = Node.create({
  name: 'macro',
  group: 'block',
  content: 'block*',
  isolating: true,
  addAttributes() {
    return { name: { default: '' }, params: { default: {} } };
  },
  parseHTML() {
    return [{ tag: 'div[data-macro]' }];
  },
  renderHTML({ node, HTMLAttributes }) {
    return [
      'div',
      mergeAttributes(HTMLAttributes, { 'data-macro': node.attrs.name, class: 'kogge-macro' }),
      0,
    ];
  },
  addNodeView() {
    return ReactNodeViewRenderer(MacroNodeView);
  },
});

/** The alignments of paragraphs and headings; anything else would become CSS in the editor. */
function alignOf(v: unknown): string | null {
  return v === 'left' || v === 'center' || v === 'right' || v === 'justify' ? v : null;
}

/** Attributes of the document model that the standard extensions do not know; kept so that nothing is lost. */
export const ModelAttributes = Extension.create({
  name: 'koggeAttributes',
  addGlobalAttributes() {
    return [
      {
        types: ['paragraph', 'heading'],
        attributes: {
          align: {
            default: null,
            parseHTML: (el) => alignOf((el as HTMLElement).style.textAlign),
            renderHTML: (a) => (alignOf(a.align) ? { style: `text-align: ${alignOf(a.align)}` } : {}),
          },
        },
      },
      {
        types: ['heading'],
        attributes: { id: { default: null, renderHTML: (a) => (a.id ? { id: a.id } : {}) } },
      },
      {
        types: ['codeBlock'],
        attributes: {
          title: { default: null, renderHTML: (a) => (a.title ? { 'data-title': a.title } : {}) },
        },
      },
      {
        types: ['taskItem'],
        attributes: {
          assignee: { default: null, renderHTML: () => ({}) },
          due: { default: null, renderHTML: () => ({}) },
        },
      },
      {
        types: ['tableCell', 'tableHeader'],
        attributes: {
          background: {
            default: null,
            parseHTML: (el) => el.getAttribute('data-background'),
            renderHTML: (a) =>
              a.background ? { 'data-background': a.background, 'data-bg': a.background } : {},
          },
        },
      },
      {
        types: ['image'],
        attributes: {
          attachment: {
            default: null,
            renderHTML: (a) => (a.attachment ? { 'data-attachment': a.attachment } : {}),
          },
          width: { default: null, renderHTML: (a) => (a.width ? { width: a.width } : {}) },
          align: { default: null, renderHTML: () => ({}) },
        },
      },
    ];
  },
});
