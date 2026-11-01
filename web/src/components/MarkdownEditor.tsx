// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Box, Group, Loader, Tabs, Text, Textarea, Tooltip } from '@mantine/core';
import {
  IconBold,
  IconCode,
  IconHeading,
  IconItalic,
  IconLink,
  IconList,
  IconListCheck,
  IconListNumbers,
  IconMarkdown,
  IconQuote,
} from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useId, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../api/client';
import { DocView } from './editor/DocView';
import { ErrorAlert } from './ErrorAlert';

/**
 * Replaces the selection of a text area. The browser's command keeps the change in its undo history; where it is
 * missing, the text is set directly.
 */
function replaceSelection(el: HTMLTextAreaElement, text: string) {
  el.focus();
  if (!document.execCommand?.('insertText', false, text)) {
    el.setRangeText(text, el.selectionStart, el.selectionEnd, 'end');
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }
}

/** Puts marks around the selection (or a placeholder) and selects what is between them. */
function wrap(el: HTMLTextAreaElement, before: string, after: string, placeholder: string) {
  const { selectionStart: s, selectionEnd: e, value } = el;
  const inner = value.slice(s, e) || placeholder;
  replaceSelection(el, before + inner + after);
  el.setSelectionRange(s + before.length, s + before.length + inner.length);
}

/** Starts each selected line (or the line of the cursor) with a prefix. */
function prefixLines(el: HTMLTextAreaElement, prefix: (line: number) => string) {
  const { selectionStart: s, selectionEnd: e, value } = el;
  const from = value.lastIndexOf('\n', s - 1) + 1;
  // A selection that ends at the start of a line does not include that line.
  const last = e > s && value[e - 1] === '\n' ? e - 1 : e;
  const end = value.indexOf('\n', last) === -1 ? value.length : value.indexOf('\n', last);
  const lines = value.slice(from, end).split('\n');
  const text = lines.map((l, i) => prefix(i) + l).join('\n');
  el.setSelectionRange(from, end);
  replaceSelection(el, text);
  el.setSelectionRange(from, from + text.length);
}

/** A link around the selection; the address is selected to be typed. */
function link(el: HTMLTextAreaElement, placeholder: string) {
  const { selectionStart: s, selectionEnd: e, value } = el;
  const label = value.slice(s, e) || placeholder;
  const address = 'https://';
  replaceSelection(el, `[${label}](${address})`);
  const at = s + label.length + 3;
  el.setSelectionRange(at, at + address.length);
}

interface MarkdownEditorProps {
  value: string;
  onChange: (value: string) => void;
  /** The visible label; without it, `ariaLabel` names the text area. */
  label?: string;
  ariaLabel?: string;
  description?: ReactNode;
  placeholder?: string;
  minRows?: number;
  autoFocus?: boolean;
  /** Called with Ctrl/Cmd+Enter. */
  onSubmit?: () => void;
}

/**
 * Markdown text with a toolbar and a preview that shows the text as the server will store it (the preview is
 * rendered by the server, so it matches what readers will see).
 */
export function MarkdownEditor({
  value,
  onChange,
  label,
  ariaLabel,
  description,
  placeholder,
  minRows = 3,
  autoFocus,
  onSubmit,
}: MarkdownEditorProps) {
  const { t } = useTranslation();
  const area = useRef<HTMLTextAreaElement>(null);
  const id = useId();
  const [mode, setMode] = useState<'write' | 'preview'>('write');
  const [height, setHeight] = useState<number>();
  const empty = value.trim() === '';
  const preview = useQuery({
    queryKey: ['markdown-preview', value],
    queryFn: () => unwrap(api.POST('/api/v1/code/markdown', { body: { markdown: value } })),
    enabled: mode === 'preview' && !empty,
    staleTime: Infinity,
    gcTime: 60_000,
  });
  const tool = (label: string, icon: ReactNode, run: (el: HTMLTextAreaElement) => void) => (
    <Tooltip label={label} openDelay={500}>
      <ActionIcon
        variant="subtle"
        color="gray"
        size="sm"
        aria-label={label}
        onMouseDown={(e) => e.preventDefault()}
        onClick={() => area.current && run(area.current)}
      >
        {icon}
      </ActionIcon>
    </Tooltip>
  );
  return (
    <Box className="kogge-markdown">
      {label && (
        <Text component="label" htmlFor={id} size="sm" fw={500} display="block" mb={4}>
          {label}
        </Text>
      )}
      <Tabs
        value={mode}
        onChange={(v) => {
          if (v === 'preview') setHeight(area.current?.offsetHeight);
          setMode(v === 'preview' ? 'preview' : 'write');
        }}
        variant="outline"
        keepMounted
      >
        <div className="kogge-markdown-head">
          <Tabs.List>
            <Tabs.Tab value="write">{t('markdown.write')}</Tabs.Tab>
            <Tabs.Tab value="preview">{t('markdown.preview')}</Tabs.Tab>
          </Tabs.List>
          {mode === 'write' && (
            <Group
              gap={2}
              pr={4}
              wrap="wrap"
              className="kogge-markdown-tools"
              role="toolbar"
              aria-label={t('markdown.formatting')}
            >
              {tool(t('editor.h3'), <IconHeading size={16} />, (el) => prefixLines(el, () => '### '))}
              {tool(t('editor.bold'), <IconBold size={16} />, (el) =>
                wrap(el, '**', '**', t('markdown.text')),
              )}
              {tool(t('editor.italic'), <IconItalic size={16} />, (el) =>
                wrap(el, '_', '_', t('markdown.text')),
              )}
              {tool(t('editor.code'), <IconCode size={16} />, (el) =>
                el.value.slice(el.selectionStart, el.selectionEnd).includes('\n')
                  ? wrap(el, '```\n', '\n```', '')
                  : wrap(el, '`', '`', t('markdown.code')),
              )}
              {tool(t('editor.link'), <IconLink size={16} />, (el) => link(el, t('markdown.text')))}
              {tool(t('editor.quote'), <IconQuote size={16} />, (el) => prefixLines(el, () => '> '))}
              {tool(t('editor.bulletList'), <IconList size={16} />, (el) => prefixLines(el, () => '- '))}
              {tool(t('editor.orderedList'), <IconListNumbers size={16} />, (el) =>
                prefixLines(el, (i) => `${i + 1}. `),
              )}
              {tool(t('editor.taskList'), <IconListCheck size={16} />, (el) =>
                prefixLines(el, () => '- [ ] '),
              )}
            </Group>
          )}
        </div>
        <Tabs.Panel value="write" pt="xs">
          <Textarea
            ref={area}
            id={id}
            value={value}
            onChange={(e) => onChange(e.currentTarget.value)}
            placeholder={placeholder}
            aria-label={label ? undefined : ariaLabel}
            description={description}
            autosize
            minRows={minRows}
            maxRows={30}
            autoFocus={autoFocus}
            onKeyDown={(e) => {
              const el = e.currentTarget;
              const mod = e.ctrlKey || e.metaKey;
              if (mod && e.key === 'Enter' && onSubmit) {
                e.preventDefault();
                onSubmit();
              } else if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'b') {
                e.preventDefault();
                wrap(el, '**', '**', t('markdown.text'));
              } else if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'i') {
                e.preventDefault();
                wrap(el, '_', '_', t('markdown.text'));
              } else if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'k') {
                e.preventDefault();
                link(el, t('markdown.text'));
              }
            }}
          />
          <Group gap={4} mt={4} c="dimmed">
            <IconMarkdown size={14} aria-hidden />
            <Text size="xs">{t('markdown.supported')}</Text>
          </Group>
        </Tabs.Panel>
        <Tabs.Panel value="preview" pt="xs">
          <Box className="kogge-markdown-preview" mih={height} data-markdown-preview>
            {mode !== 'preview' ? null : empty ? (
              <Text size="sm" c="dimmed">
                {t('markdown.nothing')}
              </Text>
            ) : preview.error ? (
              <ErrorAlert error={preview.error} />
            ) : !preview.data ? (
              <Loader size="sm" aria-label={t('markdown.rendering')} />
            ) : (
              <DocView doc={preview.data.doc} />
            )}
          </Box>
        </Tabs.Panel>
      </Tabs>
    </Box>
  );
}
