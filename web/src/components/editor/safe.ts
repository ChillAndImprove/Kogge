// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Extension, InputRule } from '@tiptap/core';
import Image, { type ImageOptions } from '@tiptap/extension-image';
import { Fragment, type Mark, type Node as PMNode, Slice } from '@tiptap/pm/model';
import { Plugin, PluginKey } from '@tiptap/pm/state';

/*
 * The server refuses every change that brings a link or an image into a document that its rules do not allow
 * (internal/docmodel: SafeLink, safeImage), and a shared draft whose change was refused has to be loaded again,
 * without the changes not sent yet. So the editor keeps such links and images out: pasted ones become what the
 * server accepts, and typing or commands do not create them.
 */

/** Browsers drop tabs and newlines from addresses and read backslashes as slashes: "/\\host" leads elsewhere. */
function misleading(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x20 || c === 0x7f || c === 0x5c) return true;
  }
  return false;
}
const badEscape = /%(?![0-9a-f]{2})/i;

/**
 * Whether a link target is allowed in documents: http(s) with a host, mailto, local paths and anchors — as the
 * server's SafeLink decides, which parses addresses as Go does ("https:host" has no host there, and paths and
 * fragments with broken %-escapes do not parse).
 */
export function safeLink(href: string): boolean {
  href = href.trim();
  if (href === '' || misleading(href)) return false;
  if (href.startsWith('#')) return true;
  const hash = href.indexOf('#');
  if (badEscape.test(href.split(/[?#]/)[0]) || (hash >= 0 && badEscape.test(href.slice(hash + 1))))
    return false;
  if (href.startsWith('/')) return !href.startsWith('//');
  const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(href)?.[1].toLowerCase();
  if (scheme === 'mailto') return true;
  if ((scheme !== 'http' && scheme !== 'https') || !/^[a-z]+:\/\//i.test(href)) return false;
  try {
    return new URL(href).host !== '';
  } catch {
    return false;
  }
}

/** Whether an image source is allowed: a path of this server, or http(s) where external images are allowed. */
export function safeImage(src: string, external: boolean): boolean {
  if (misleading(src)) return false;
  if (src.startsWith('/') && !src.startsWith('//')) return true;
  if (!external) return false;
  try {
    const u = new URL(src);
    return (u.protocol === 'https:' || u.protocol === 'http:') && u.host !== '';
  } catch {
    return false;
  }
}

/** The address an automatic link gets for the text it was found in (as linkify completes it). */
export function autolinkTarget(text: string): string {
  if (/^[a-z][a-z0-9+.-]*:/i.test(text)) return text;
  if (/^[^\s@/]+@[^\s@/]+$/.test(text)) return `mailto:${text}`;
  return `http://${text}`;
}

/** An address of this server as a path, which documents keep (absolute ones are other servers to them). */
function local(src: string): string {
  try {
    const u = new URL(src, window.location.href);
    if (u.origin === window.location.origin) return u.pathname + u.search + u.hash;
  } catch {
    // not an address
  }
  return src;
}

/** Makes pasted or dropped content acceptable: images the server refuses become links, unsafe links plain text. */
export function safeSlice(slice: Slice, external: boolean): Slice {
  const schema = slice.content.firstChild?.type.schema;
  if (!schema) return slice;
  const linkType = schema.marks.link;
  const cleanMarks = (marks: readonly Mark[]) =>
    marks.filter((m) => m.type !== linkType || safeLink(String(m.attrs.href ?? '')));
  const fix = (fragment: Fragment): Fragment => {
    const out: PMNode[] = [];
    fragment.forEach((node) => {
      if (node.type.name === 'image' || node.type.name === 'inlineImage') {
        const src = local(String(node.attrs.src ?? ''));
        if (node.attrs.attachment || safeImage(src, external)) {
          out.push(
            src === node.attrs.src
              ? node
              : node.type.create({ ...node.attrs, src }, node.content, node.marks),
          );
          return;
        }
        // An image from elsewhere: a link to it, which readers can follow.
        const text = String(node.attrs.alt || node.attrs.title || src || '').trim();
        if (!text) return;
        const marks = safeLink(src) && linkType ? [linkType.create({ href: src })] : [];
        const inline = schema.text(text, marks);
        out.push(node.isInline ? inline : schema.nodes.paragraph.create(null, inline));
        return;
      }
      if (node.isText) {
        out.push(node.mark(cleanMarks(node.marks)));
        return;
      }
      out.push(node.copy(fix(node.content)));
    });
    return Fragment.fromArray(out);
  };
  return new Slice(fix(slice.content), slice.openStart, slice.openEnd);
}

/** Paste and drop: content the server would refuse is made acceptable before it reaches the document. */
export const SafeContent = Extension.create<{ externalImages: boolean }>({
  name: 'koggeSafeContent',
  addOptions() {
    return { externalImages: false };
  },
  addProseMirrorPlugins() {
    const external = this.options.externalImages;
    return [
      new Plugin({
        key: new PluginKey('koggeSafeContent'),
        props: { transformPasted: (slice) => safeSlice(slice, external) },
      }),
    ];
  },
});

/** Images, whose Markdown shortcut ![alt](src) only applies to sources the server accepts. */
export const SafeImage = Image.extend<ImageOptions & { externalImages: boolean }>({
  addOptions() {
    return { ...this.parent!(), externalImages: false };
  },
  addInputRules() {
    const external = this.options.externalImages;
    return (this.parent?.() ?? []).map(
      (rule) =>
        new InputRule({
          find: rule.find,
          handler: (props) => (safeImage(props.match[3] ?? '', external) ? rule.handler(props) : null),
          undoable: rule.undoable,
        }),
    );
  },
});
