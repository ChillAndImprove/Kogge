// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Box,
  Divider,
  Group,
  Loader,
  SegmentedControl,
  Text,
  Tooltip,
} from '@mantine/core';
import { useDebouncedValue, useMediaQuery } from '@mantine/hooks';
import {
  IconArrowBackUp,
  IconArrowForwardUp,
  IconBlockquote,
  IconBold,
  IconCode,
  IconH1,
  IconH2,
  IconH3,
  IconInfoCircle,
  IconItalic,
  IconLink,
  IconList,
  IconListCheck,
  IconListNumbers,
  IconMath,
  IconMathFunction,
  IconMaximize,
  IconMinimize,
  IconPhoto,
  IconSearch,
  IconSeparatorHorizontal,
  IconSourceCode,
  IconStrikethrough,
  IconTable,
  type Icon,
} from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import type { Editor, JSONContent } from '@tiptap/core';
import { Node as PMNode, Slice } from '@tiptap/pm/model';
import { lazy, Suspense, useEffect, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../ErrorAlert';
import type { Doc } from './doc';
import { DocView, type DocContext } from './DocView';
import type { MarkdownAction } from './markdownActions';
import type { MarkdownCodeApi } from './MarkdownCode';
import { safeSlice } from './safe';

// The code editor loads when Markdown is chosen: writing formatted does without it.
const MarkdownCode = lazy(() => import('./MarkdownCode').then((m) => ({ default: m.MarkdownCode })));

/** What Markdown holds as the server reads it: a document of only these reads back as it was written. */
const markdownNodes = new Set([
  'doc',
  'paragraph',
  'text',
  'heading',
  'horizontalRule',
  'blockquote',
  'codeBlock',
  'bulletList',
  'orderedList',
  'listItem',
  'taskList',
  'taskItem',
  'table',
  'tableRow',
  'tableHeader',
  'tableCell',
  'hardBreak',
  'image',
  'inlineImage',
  'math',
  'inlineMath',
]);
const markdownMarks = new Set(['bold', 'italic', 'strike', 'code', 'link']);

/** Whether a document has content that Markdown cannot hold: macros, panels, columns, mentions, colours … */
export function beyondMarkdown(doc: JSONContent | undefined): boolean {
  if (!doc) return false;
  if (doc.type && !markdownNodes.has(doc.type)) return true;
  if (doc.marks?.some((m) => !markdownMarks.has(m.type))) return true;
  return (doc.content ?? []).some(beyondMarkdown);
}

const flushers = new WeakMap<Editor, () => Promise<void>>();

/** Brings Markdown that is still being typed into the editor: before its draft is published. */
export function flushMarkdown(editor: Editor | null | undefined): Promise<void> {
  return (editor && flushers.get(editor)?.()) || Promise.resolve();
}

async function markdownDoc(markdown: string): Promise<Doc> {
  return (await unwrap(api.POST('/api/v1/code/markdown', { body: { markdown } }))).doc as Doc;
}

/** Replaces the editor's content with a document of the server, keeping out links and images it would refuse. */
function replaceContent(editor: Editor, doc: Doc, externalImages: boolean) {
  if (editor.isDestroyed) return;
  const content = safeSlice(
    new Slice(PMNode.fromJSON(editor.schema, doc).content, 0, 0),
    externalImages,
  ).content;
  const filled = editor.schema.topNodeType.createAndFill(null, content);
  if (!filled) return;
  const { tr } = editor.state;
  editor.view.dispatch(tr.replaceWith(0, tr.doc.content.size, filled.content));
}

/** A button of the Markdown toolbar; it leaves the focus in the source. */
function Tool({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <Tooltip label={label} openDelay={400}>
      <ActionIcon
        variant="subtle"
        color="gray"
        size="md"
        onMouseDown={(e) => e.preventDefault()}
        onClick={onClick}
        aria-label={label}
      >
        {children}
      </ActionIcon>
    </Tooltip>
  );
}

/** "Strg" or "Ctrl" or "⌘", for the keys in tooltips. */
function useModKey() {
  const { t } = useTranslation();
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
    ? '⌘'
    : t('editor.ctrlKey');
}

/** Lines, words and characters of a text, for the status line. */
function counts(text: string) {
  const words = text.match(/[\p{L}\p{N}][\p{L}\p{N}'’_-]*/gu)?.length ?? 0;
  return { lines: text === '' ? 0 : text.split('\n').length, words, chars: text.length };
}

/**
 * Writing a document as Markdown, as in a Markdown editor: a toolbar for its marks, the source with line numbers
 * and highlighting (CodeMirror) beside a live preview that follows its scrolling, a status line, and a full-screen
 * view. Narrow screens show the source or the preview. The server writes the document as Markdown and reads what is
 * typed back into the editor, so the draft and its collaborators follow. The document stays untouched until the
 * Markdown changes: content that Markdown cannot hold is only lost by editing it here, and the mode says so.
 */
export function MarkdownMode({
  editor,
  externalImages,
  context,
  switcher,
}: {
  editor: Editor;
  externalImages: boolean;
  context?: DocContext;
  /** The switch back to formatted writing, at the end of the toolbar. */
  switcher?: ReactNode;
}) {
  const { t } = useTranslation();
  const mod = useModKey();
  const wide = useMediaQuery('(min-width: 62em)');
  const [tab, setTab] = useState<'write' | 'preview'>('write');
  const [full, setFull] = useState(false);
  const [start, setStart] = useState<{ markdown: string; doc: Doc; lossy: boolean } | null>(null);
  const [failed, setFailed] = useState<unknown>(null);
  const [text, setText] = useState('');
  const [touched, setTouched] = useState(false);
  const code = useRef<MarkdownCodeApi | null>(null);
  const previewPane = useRef<HTMLDivElement>(null);
  const latest = useRef({ text, touched });
  useEffect(() => {
    latest.current = { text, touched };
  });
  const applied = useRef<string | null>(null);

  // The document as Markdown, once, when the mode opens.
  useEffect(() => {
    let live = true;
    const doc = editor.getJSON();
    unwrap(api.POST('/api/v1/code/markdown/source', { body: { doc } })).then(
      (r) => {
        if (!live) return;
        const markdown = r.markdown.replace(/\n$/, '');
        setStart({ markdown, doc, lossy: beyondMarkdown(doc) });
        setText(markdown);
      },
      (e: unknown) => live && setFailed(e),
    );
    return () => {
      live = false;
    };
  }, [editor]);

  const [settled] = useDebouncedValue(text, 200);
  const preview = useQuery({
    queryKey: ['markdown-preview', settled],
    queryFn: () => markdownDoc(settled),
    enabled: touched,
    staleTime: Infinity,
    placeholderData: (previous) => previous,
  });

  // What was typed goes into the editor once the server has read it.
  useEffect(() => {
    if (!touched || !preview.data || preview.isPlaceholderData || applied.current === settled) return;
    applied.current = settled;
    replaceContent(editor, preview.data, externalImages);
  }, [touched, preview.data, preview.isPlaceholderData, settled, editor, externalImages]);

  // Publishing waits for the latest Markdown to be in the editor.
  useEffect(() => {
    const flush = async () => {
      const { text: now, touched: changed } = latest.current;
      if (!changed || applied.current === now) return;
      const doc = await markdownDoc(now);
      applied.current = now;
      replaceContent(editor, doc, externalImages);
    };
    flushers.set(editor, flush);
    return () => {
      flushers.delete(editor);
    };
  }, [editor, externalImages]);

  // Escape leaves the full-screen view.
  useEffect(() => {
    if (!full) return;
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !e.defaultPrevented) setFull(false);
    };
    window.addEventListener('keydown', key);
    return () => window.removeEventListener('keydown', key);
  }, [full]);

  const change = (value: string) => {
    setText(value);
    if (start && value !== start.markdown) setTouched(true);
  };
  // The preview follows the source: the same share of each is scrolled.
  const follow = (share: number) => {
    const pane = previewPane.current;
    if (pane) pane.scrollTop = share * (pane.scrollHeight - pane.clientHeight);
  };
  const tool = (action: MarkdownAction, label: string, Glyph: Icon, keys?: string) => (
    <Tool
      key={action}
      label={keys ? `${label} (${mod}+${keys})` : label}
      onClick={() => code.current?.run(action)}
    >
      <Glyph size={16} />
    </Tool>
  );
  const shown = touched ? preview.data : start?.doc;
  const n = counts(text);
  return (
    <Box className="kogge-markdown" data-full={full || undefined}>
      <div className="kogge-editor-toolbar">
        <Group gap={2} px={6} py={4} wrap="wrap">
          <Tool label={`${t('editor.undo')} (${mod}+Z)`} onClick={() => code.current?.undo()}>
            <IconArrowBackUp size={16} />
          </Tool>
          <Tool label={`${t('editor.redo')} (${mod}+Y)`} onClick={() => code.current?.redo()}>
            <IconArrowForwardUp size={16} />
          </Tool>
          <Divider orientation="vertical" />
          {tool('bold', t('editor.bold'), IconBold, 'B')}
          {tool('italic', t('editor.italic'), IconItalic, 'I')}
          {tool('strike', t('editor.strike'), IconStrikethrough)}
          {tool('code', t('editor.code'), IconCode, 'E')}
          {tool('math', t('editor.formula'), IconMathFunction)}
          <Divider orientation="vertical" />
          {tool('h1', t('editor.h1'), IconH1, 'Alt+1')}
          {tool('h2', t('editor.h2'), IconH2, 'Alt+2')}
          {tool('h3', t('editor.h3'), IconH3, 'Alt+3')}
          <Divider orientation="vertical" />
          {tool('quote', t('editor.quote'), IconBlockquote)}
          {tool('bullet', t('editor.bulletList'), IconList)}
          {tool('ordered', t('editor.orderedList'), IconListNumbers)}
          {tool('task', t('editor.taskList'), IconListCheck)}
          <Divider orientation="vertical" />
          {tool('link', t('editor.link'), IconLink, 'K')}
          {tool('image', t('editor.image'), IconPhoto)}
          {tool('codeBlock', t('editor.codeBlock'), IconSourceCode)}
          {tool('mathBlock', t('editor.formulaBlock'), IconMath)}
          {tool('table', t('editor.table'), IconTable)}
          {tool('rule', t('editor.rule'), IconSeparatorHorizontal)}
          <Divider orientation="vertical" />
          <Tool label={`${t('editor.markdownSearch')} (${mod}+F)`} onClick={() => code.current?.search()}>
            <IconSearch size={16} />
          </Tool>
          <Group gap={6} ml="auto" wrap="nowrap">
            {!wide && (
              <SegmentedControl
                size="xs"
                value={tab}
                onChange={(v) => setTab(v as 'write' | 'preview')}
                data={[
                  { value: 'write', label: t('editor.markdownWrite') },
                  { value: 'preview', label: t('editor.markdownPreview') },
                ]}
              />
            )}
            <Tool
              label={full ? t('editor.markdownExitFull') : t('editor.markdownFull')}
              onClick={() => setFull(!full)}
            >
              {full ? <IconMinimize size={16} /> : <IconMaximize size={16} />}
            </Tool>
            {switcher}
          </Group>
        </Group>
      </div>
      {start?.lossy && (
        <Alert variant="light" color="yellow" icon={<IconInfoCircle size={16} />} radius={0} py={6}>
          <Text size="xs">{t('editor.markdownLossy')}</Text>
        </Alert>
      )}
      {failed ? (
        <Box p="sm">
          <ErrorAlert error={failed} />
        </Box>
      ) : !start ? (
        <Loader size="sm" m="md" />
      ) : (
        <div className="kogge-markdown-split" data-tab={wide ? undefined : tab}>
          <div className="kogge-markdown-pane kogge-markdown-source">
            <Suspense fallback={<Loader size="sm" m="md" />}>
              <MarkdownCode
                initial={start.markdown}
                label={t('editor.markdownSource')}
                placeholderText={t('editor.markdownPlaceholder')}
                words={{ column: t('editor.markdownColumn'), image: t('editor.image') }}
                phrases={t('editor.markdownPhrases', { returnObjects: true }) as Record<string, string>}
                onChange={change}
                onScroll={follow}
                onReady={(a) => {
                  code.current = a;
                }}
              />
            </Suspense>
          </div>
          <div
            ref={previewPane}
            className="kogge-markdown-pane kogge-markdown-preview"
            role="region"
            aria-label={t('editor.markdownPreview')}
          >
            {shown?.content?.length ? (
              <DocView doc={shown} context={context} />
            ) : (
              <Text size="sm" c="dimmed">
                {t('editor.markdownEmpty')}
              </Text>
            )}
          </div>
        </div>
      )}
      <Group className="kogge-markdown-status" justify="space-between" gap="xs" px="sm" py={3} wrap="nowrap">
        <Text size="xs" c="dimmed">
          {t('editor.markdownCounts', { lines: n.lines, words: n.words, chars: n.chars })}
        </Text>
        <Text size="xs" c="dimmed">
          {touched && (preview.isFetching || settled !== text)
            ? t('editor.markdownUpdating')
            : t('editor.markdownLive')}
        </Text>
      </Group>
    </Box>
  );
}
