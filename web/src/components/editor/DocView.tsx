// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Badge, Checkbox, Code, Text } from '@mantine/core';
import {
  IconAlertOctagon,
  IconAlertTriangle,
  IconChevronRight,
  IconCircleCheck,
  IconInfoCircle,
  IconNote,
} from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { createElement, Fragment, type ReactNode } from 'react';
import { Link } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatDay } from '../../app/format';
import { useOptionalSession } from '../../app/session';
import { CodeBlock } from './CodeBlock';
import { emojiGlyph } from './emoji';
import { latexOf } from '../math/latex';
import { MathFormula } from '../MathFormula';
import { MermaidDiagram } from '../MermaidDiagram';
import type { Doc } from './doc';
import './editor.css';

export type DocNode = NonNullable<Doc['content']>[number];
type Node = DocNode;

/** Optional rendering of wiki content: macros, inline comment anchors and attachments. */
export interface DocContext {
  macro?: (node: DocNode, inline: boolean) => ReactNode;
  anchor?: (id: string, child: ReactNode) => ReactNode;
  attachmentUrl?: (id: number) => string;
  /** Images from other servers may load (the instance allows it); set by DocView from the session. */
  externalImages?: boolean;
}

/** Whether an image may load: from the instance itself, or over HTTPS from other servers where the instance
 * allows it. */
function imageAllowed(src: string, ctx: DocContext): boolean {
  if ([...src].some((c) => c === '\\' || c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f)) return false;
  if (/^\/(?!\/)/.test(src)) return true;
  return !!ctx.externalImages && /^https:\/\/[^/]/i.test(src);
}

/** The plain text of a node (for heading ids and tables of contents). */
export function nodeText(node: DocNode): string {
  if (node.type === 'text') return node.text ?? '';
  return (node.content ?? []).map(nodeText).join('');
}

/** A stable id for a heading, used by tables of contents. */
export function headingId(node: DocNode): string {
  if (node.attrs?.id) return String(node.attrs.id);
  return (
    'h-' +
    nodeText(node)
      .toLowerCase()
      .replace(/[^a-z0-9äöüß]+/g, '-')
      .replace(/^-|-$/g, '')
      .slice(0, 60)
  );
}

/** An issue reference with live summary and status; issues the reader may not see show their key only. */
export function IssueChip({ issueKey }: { issueKey: string }) {
  const issue = useQuery({
    queryKey: ['issue', issueKey, 'chip'],
    queryFn: () => unwrap(api.GET('/api/v1/issues/{key}', { params: { path: { key: issueKey } } })),
    staleTime: 60_000,
    retry: false,
  });
  const done = issue.data?.status?.category === 'done';
  return (
    <Anchor
      component={Link}
      to={`/browse/${issueKey}`}
      className="kogge-issue-chip"
      title={issue.data?.summary}
    >
      <span style={{ textDecoration: done ? 'line-through' : undefined }}>{issueKey}</span>
      {issue.data && (
        <>
          {' '}
          <span className="kogge-issue-chip-summary">{issue.data.summary}</span>{' '}
          <Badge
            size="xs"
            variant="light"
            color={done ? 'teal' : issue.data.status?.category === 'inprogress' ? 'blue' : 'gray'}
          >
            {issue.data.status?.name}
          </Badge>
        </>
      )}
    </Anchor>
  );
}

function marks(node: Node, child: ReactNode, ctx: DocContext): ReactNode {
  let out = child;
  for (const m of node.marks ?? []) {
    switch (m.type) {
      case 'textColor':
        // The editor's colours (editor.css), which read on light and dark pages.
        out = m.attrs?.color ? (
          <span className="kogge-color" data-color={String(m.attrs.color)}>
            {out}
          </span>
        ) : (
          out
        );
        break;
      case 'anchor':
        out = ctx.anchor ? ctx.anchor(String(m.attrs?.id ?? ''), out) : out;
        break;
      case 'bold':
        out = <strong>{out}</strong>;
        break;
      case 'italic':
        out = <em>{out}</em>;
        break;
      case 'underline':
        out = <u>{out}</u>;
        break;
      case 'strike':
        out = <s>{out}</s>;
        break;
      case 'code':
        out = <Code>{out}</Code>;
        break;
      case 'subscript':
        out = <sub>{out}</sub>;
        break;
      case 'superscript':
        out = <sup>{out}</sup>;
        break;
      case 'highlight':
        out = <mark>{out}</mark>;
        break;
      case 'link': {
        const href = String(m.attrs?.href ?? '');
        const safe = safeHref(href);
        out = safe ? (
          <Anchor
            href={href}
            target={href.startsWith('/') || href.startsWith('#') ? undefined : '_blank'}
            rel="noopener noreferrer"
          >
            {out}
          </Anchor>
        ) : (
          out
        );
        break;
      }
    }
  }
  return out;
}

function children(node: Node, ctx: DocContext): ReactNode {
  return (node.content ?? []).map((c, i) => <Fragment key={i}>{render(c, ctx)}</Fragment>);
}

function render(node: Node, ctx: DocContext): ReactNode {
  const a = node.attrs ?? {};
  const kids = () => children(node, ctx);
  switch (node.type) {
    case 'text':
      return marks(node, node.text, ctx);
    case 'paragraph':
      return <p style={a.align ? { textAlign: a.align } : undefined}>{kids()}</p>;
    case 'heading': {
      const id = headingId(node);
      // Headings carry a link to themselves, to point others at a section.
      return createElement(
        `h${Math.min(Math.max(Number(a.level) || 2, 1), 6)}`,
        { id, style: a.align ? { textAlign: a.align } : undefined, className: 'kogge-heading' },
        kids(),
        <a key="anchor" href={`#${id}`} className="kogge-heading-anchor" aria-hidden tabIndex={-1}>
          #
        </a>,
      );
    }
    case 'blockquote':
      return <blockquote>{kids()}</blockquote>;
    case 'horizontalRule':
      return <hr />;
    case 'hardBreak':
      return <br />;
    case 'bulletList':
      return <ul>{kids()}</ul>;
    case 'orderedList':
      return <ol start={a.start || undefined}>{kids()}</ol>;
    case 'listItem':
      return <li>{kids()}</li>;
    case 'taskList':
      return <ul data-type="taskList">{kids()}</ul>;
    case 'taskItem':
      return (
        <li data-type="taskItem" data-checked={a.checked ? 'true' : 'false'}>
          <Checkbox checked={!!a.checked} readOnly size="xs" aria-label="task" />
          <div>{kids()}</div>
        </li>
      );
    case 'codeBlock': {
      const code = (node.content ?? []).map((c) => c.text).join('');
      // Mermaid code blocks are diagrams, as in Markdown files elsewhere.
      if (
        String(a.language ?? '')
          .trim()
          .toLowerCase() === 'mermaid'
      )
        return <MermaidDiagram code={code} />;
      return <CodeBlock code={code} language={a.language || undefined} title={a.title || undefined} />;
    }
    case 'table':
      return (
        <div className="kogge-doc-table">
          <table>
            <tbody>{kids()}</tbody>
          </table>
        </div>
      );
    case 'tableRow':
      return <tr>{kids()}</tr>;
    case 'tableHeader':
      return (
        <th
          colSpan={a.colspan || undefined}
          rowSpan={a.rowspan || undefined}
          data-bg={cellColor(a.background)}
        >
          {kids()}
        </th>
      );
    case 'tableCell':
      return (
        <td
          colSpan={a.colspan || undefined}
          rowSpan={a.rowspan || undefined}
          data-bg={cellColor(a.background)}
        >
          {kids()}
        </td>
      );
    case 'image': {
      const src =
        a.attachment && ctx.attachmentUrl ? ctx.attachmentUrl(Number(a.attachment)) : String(a.src ?? '');
      return imageAllowed(src, ctx) ? (
        <img
          src={src}
          alt={a.alt ?? ''}
          referrerPolicy="no-referrer"
          style={{ maxWidth: '100%', width: a.width || undefined }}
        />
      ) : null;
    }
    case 'inlineImage': {
      // Images within text, as Markdown has them: badges, icons, screenshots between paragraphs.
      const src = String(a.src ?? '');
      const alt = String(a.alt ?? '');
      if (!imageAllowed(src, ctx)) return alt ? <span>{alt}</span> : null;
      const img = (
        <img
          className="kogge-inline-image"
          src={src}
          alt={alt}
          title={a.title ? String(a.title) : undefined}
          loading="lazy"
          referrerPolicy="no-referrer"
        />
      );
      const href = String(a.href ?? '');
      return href && safeHref(href) ? (
        <Anchor
          href={href}
          target={href.startsWith('/') || href.startsWith('#') ? undefined : '_blank'}
          rel="noopener noreferrer"
        >
          {img}
        </Anchor>
      ) : (
        img
      );
    }
    case 'macro':
      return ctx.macro ? (
        ctx.macro(node, false)
      ) : (
        <Text c="dimmed" size="sm" className="kogge-unsupported">
          [{String(a.name)}]
        </Text>
      );
    case 'inlineMacro':
      return ctx.macro ? ctx.macro(node, true) : <span>[{String(a.name)}]</span>;
    case 'panel': {
      const kind = panelIcons[a.kind as string] ? (a.kind as string) : 'info';
      const Icon = panelIcons[kind];
      return (
        <div className="kogge-panel kogge-panel-view" data-kind={kind}>
          <Icon size={18} className="kogge-panel-icon" aria-hidden />
          <div className="kogge-panel-body">{kids()}</div>
        </div>
      );
    }
    case 'columns':
      // The widths come from the layout (editor.css); the row stacks where the page is narrow.
      return (
        <div className="kogge-columns" data-layout={columnLayout(a.layout)}>
          <div className="kogge-columns-row">{kids()}</div>
        </div>
      );
    case 'column':
      return <div className="kogge-column">{kids()}</div>;
    case 'expand':
      return (
        <details className="kogge-expand-block">
          <summary>
            <IconChevronRight size={14} className="kogge-expand-chevron" aria-hidden />
            {a.title}
          </summary>
          <div className="kogge-expand-body">{kids()}</div>
        </details>
      );
    case 'mention':
      return <span className="kogge-mention">@{a.label}</span>;
    case 'issue':
      return <IssueChip issueKey={String(a.key)} />;
    case 'status':
      return (
        <Badge size="sm" variant="light" color={a.color || 'gray'} radius="sm">
          {a.text}
        </Badge>
      );
    case 'date':
      return (
        <time className="kogge-date" dateTime={String(a.value ?? '')}>
          {formatDay(String(a.value ?? '')) || a.value}
        </time>
      );
    case 'emoji':
      return <span title={`:${a.name}:`}>{emojiGlyph(String(a.name ?? '')) ?? `:${a.name}:`}</span>;
    // Formulas, typeset by KaTeX once it has loaded; the server keeps no marks on them. Empty ones show nothing.
    case 'math':
    case 'inlineMath': {
      const latex = latexOf(a);
      return latex.trim() ? <MathFormula latex={latex} display={node.type === 'math'} /> : null;
    }
    case 'unsupported':
      return (
        <Text c="dimmed" size="sm" className="kogge-unsupported">
          [{a.source} {a.name}]
        </Text>
      );
    default:
      return kids();
  }
}

const panelIcons: Record<string, typeof IconInfoCircle> = {
  info: IconInfoCircle,
  note: IconNote,
  warning: IconAlertTriangle,
  success: IconCircleCheck,
  error: IconAlertOctagon,
};

/** The layout of columns, if it is one Kogge knows. */
function columnLayout(l: unknown): string {
  return typeof l === 'string' && /^(equal|left|right|sides)$/.test(l) ? l : 'equal';
}

/** The colour of a table cell, if it is one of the palette's. */
function cellColor(c: unknown): string | undefined {
  return typeof c === 'string' && /^(gray|red|orange|yellow|green|teal|blue|violet|pink)$/.test(c)
    ? c
    : undefined;
}

/** Link targets shown as links: like the server, no backslashes or control characters, which browsers turn into
 * other hosts. */
function safeHref(href: string): boolean {
  if ([...href].some((c) => c === '\\' || c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f)) return false;
  return /^(https?:\/\/[^/]|mailto:|\/(?!\/)|#)/i.test(href);
}

/** Renders a document read-only as React elements (no HTML is injected). */
export function DocView({ doc, context }: { doc: Doc | null | undefined; context?: DocContext }) {
  const externalImages = useOptionalSession()?.me.externalImages ?? false;
  if (!doc?.content?.length) return null;
  return <div className="kogge-doc">{children(doc as Node, { externalImages, ...context })}</div>;
}

/** Renders a list of nodes (e.g. the body of a macro) with a context. */
export function DocNodes({ nodes, context }: { nodes: DocNode[] | undefined; context?: DocContext }) {
  const externalImages = useOptionalSession()?.me.externalImages ?? false;
  return (
    <>
      {(nodes ?? []).map((n, i) => (
        <Fragment key={i}>{render(n, { externalImages, ...context })}</Fragment>
      ))}
    </>
  );
}
