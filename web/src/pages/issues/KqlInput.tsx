// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Box, Group, Paper, Text, Textarea, VisuallyHidden } from '@mantine/core';
import { useDebouncedValue, useId } from '@mantine/hooks';
import { useQuery } from '@tanstack/react-query';
import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import {
  apply,
  contextAt,
  staticSuggestions,
  valueField,
  valueSuggestion,
  type Catalog,
  type Suggestion,
} from './kqlComplete';

/** The fields, operators and functions of KQL (from the server, which knows its grammar). */
export function useKqlCatalog() {
  return useQuery({
    queryKey: ['kql-catalog'],
    queryFn: () => unwrap(api.GET('/api/v1/search/autocomplete')) as Promise<Catalog>,
    staleTime: 10 * 60_000,
  });
}

/** How many suggestions the list shows at most. */
const shownMax = 50;

/**
 * The query box of the navigator: KQL with suggestions at the caret for fields, operators, values, functions and
 * keywords. It is a combobox: the arrow keys choose a suggestion, Enter or Tab inserts it (Tab the first one if none
 * is chosen), Escape closes the list and Ctrl+Space opens it; Enter without a chosen suggestion runs the query, as
 * before, and Shift+Enter starts a new line.
 */
export function KqlInput({
  value,
  onChange,
  onSubmit,
  label,
  placeholder,
  error,
}: {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  label: string;
  placeholder?: string;
  error?: ReactNode;
}) {
  const { t } = useTranslation();
  const ref = useRef<HTMLTextAreaElement>(null);
  const listId = useId();
  const helpId = useId();
  const [caret, setCaret] = useState(value.length);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const catalog = useKqlCatalog();
  const ctx = useMemo(() => contextAt(value, Math.min(caret, value.length)), [value, caret]);
  const field = valueField(catalog.data, ctx);
  const [request] = useDebouncedValue({ field, prefix: ctx.prefix }, 150);
  const values = useQuery({
    queryKey: ['kql-suggestions', request.field, request.prefix],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/search/suggestions', {
          params: { query: { field: request.field!, prefix: request.prefix } },
        }),
      ),
    enabled: open && !!request.field,
    staleTime: 30_000,
    placeholderData: (prev) => prev,
  });
  const items = useMemo(() => {
    const fromServer =
      field && field === request.field
        ? (values.data ?? []).map((v) => valueSuggestion(v, ctx.expect.kind === 'argument'))
        : [];
    return [...fromServer, ...staticSuggestions(catalog.data, ctx)].slice(0, shownMax);
  }, [catalog.data, ctx, field, request.field, values.data]);
  const shown = open && items.length > 0;
  const current = active >= 0 && active < items.length ? active : -1;
  const optionId = (i: number) => `${listId}-${i}`;

  // The chosen suggestion stays in view.
  useEffect(() => {
    if (current >= 0) document.getElementById(optionId(current))?.scrollIntoView({ block: 'nearest' });
    // optionId depends on listId only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [current, listId]);

  const follow = () => setCaret(ref.current?.selectionStart ?? value.length);
  const accept = (s: Suggestion) => {
    const next = apply(value, ctx, s);
    onChange(next.text);
    setCaret(next.caret);
    setActive(-1);
    setOpen(true); // what comes next is suggested at once
    requestAnimationFrame(() => {
      const el = ref.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(next.caret, next.caret);
    });
  };
  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (shown) {
      switch (e.key) {
        case 'ArrowDown':
          e.preventDefault();
          setActive(current + 1 >= items.length ? 0 : current + 1);
          return;
        case 'ArrowUp':
          e.preventDefault();
          setActive(current <= 0 ? items.length - 1 : current - 1);
          return;
        case 'Tab':
          if (e.shiftKey) break;
          e.preventDefault();
          accept(items[current >= 0 ? current : 0]);
          return;
        case 'Enter':
          if (current < 0 || e.shiftKey) break;
          e.preventDefault();
          accept(items[current]);
          return;
        case 'Escape':
          e.preventDefault();
          e.stopPropagation();
          setOpen(false);
          setActive(-1);
          return;
      }
    }
    if (
      (e.key === ' ' && e.ctrlKey) ||
      (e.key === 'ArrowDown' && !shown && !value.slice(caret).includes('\n'))
    ) {
      e.preventDefault();
      setOpen(true);
      setActive(e.key === 'ArrowDown' ? 0 : -1);
      return;
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      setOpen(false);
      onSubmit();
    }
  };

  const list = shown && (
    <Paper
      id={listId}
      role="listbox"
      aria-label={t('kql.suggestions')}
      shadow="md"
      withBorder
      radius="sm"
      className="kogge-kql-list"
      // Clicks on suggestions must not take the focus from the query.
      onMouseDown={(e) => e.preventDefault()}
    >
      {items.map((s, i) => (
        <Group
          key={`${s.kind}:${s.label}:${s.detail ?? ''}`}
          id={optionId(i)}
          role="option"
          aria-selected={i === current}
          data-active={i === current || undefined}
          className="kogge-kql-option"
          gap="xs"
          wrap="nowrap"
          justify="space-between"
          onMouseEnter={() => setActive(i)}
          onClick={() => accept(s)}
        >
          <Text size="sm" ff="monospace" truncate>
            {s.label}
          </Text>
          <Text size="xs" c="dimmed" truncate>
            {s.detail ? s.detail : t(`kql.kinds.${s.kind}`)}
          </Text>
        </Group>
      ))}
    </Paper>
  );
  return (
    <Box style={{ flex: 1, minWidth: 0 }}>
      <Textarea
        ref={ref}
        value={value}
        onChange={(e) => {
          onChange(e.currentTarget.value);
          setCaret(e.currentTarget.selectionStart);
          setOpen(true);
          setActive(-1);
        }}
        onKeyDown={onKeyDown}
        onKeyUp={(e) => {
          if (['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) follow();
        }}
        onClick={follow}
        onBlur={() => {
          setOpen(false);
          setActive(-1);
        }}
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={shown}
        aria-controls={shown ? listId : undefined}
        aria-activedescendant={shown && current >= 0 ? optionId(current) : undefined}
        aria-describedby={helpId}
        aria-label={label}
        placeholder={placeholder}
        autosize
        minRows={1}
        maxRows={6}
        spellCheck={false}
        autoComplete="off"
        styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
        error={error}
        inputContainer={(children) => (
          <div style={{ position: 'relative' }}>
            {children}
            {list}
          </div>
        )}
      />
      <VisuallyHidden id={helpId}>{t('kql.help')}</VisuallyHidden>
      <VisuallyHidden aria-live="polite">
        {shown ? t('kql.count', { count: items.length }) : ''}
      </VisuallyHidden>
    </Box>
  );
}
