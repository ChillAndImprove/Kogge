// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Button, Group, Text, TextInput, Tooltip } from '@mantine/core';
import { IconArrowDown, IconArrowUp, IconLetterCase, IconLetterW, IconX } from '@tabler/icons-react';
import { useEditorState, type Editor } from '@tiptap/react';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { findState } from './findReplace';

function Toggle({
  label,
  active,
  onClick,
  children,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <Tooltip label={label} openDelay={400}>
      <ActionIcon
        variant={active ? 'light' : 'subtle'}
        color={active ? undefined : 'gray'}
        size="md"
        onClick={onClick}
        aria-label={label}
        aria-pressed={active}
      >
        {children}
      </ActionIcon>
    </Tooltip>
  );
}

/**
 * Find and replace below the editor's toolbar (Ctrl/Cmd+F): Enter finds the next match, Shift+Enter the previous
 * one, Escape closes it and returns to the text at the current match.
 */
export function FindReplacePanel({
  editor,
  initial,
  focusKey,
  onClose,
}: {
  editor: Editor;
  /** The text the search starts with (what was selected). */
  initial: string;
  /** Changes when Ctrl/Cmd+F is pressed again, which focuses the search. */
  focusKey: number;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [query, setQuery] = useState(initial);
  const [replacement, setReplacement] = useState('');
  const [caseSensitive, setCaseSensitive] = useState(false);
  const [wholeWord, setWholeWord] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const s = useEditorState({
    editor,
    selector: ({ editor: e }) => {
      const f = findState(e.state);
      return { total: f.matches.length, current: f.current, editable: e.isEditable };
    },
  });
  useEffect(() => {
    editor.commands.setSearch(query, { caseSensitive, wholeWord });
  }, [editor, query, caseSensitive, wholeWord]);
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, [focusKey]);
  // Closing ends the highlights.
  useEffect(() => () => void editor.commands.clearSearch(), [editor]);
  const close = () => {
    onClose();
    editor.commands.focus();
  };
  const onKey = (e: React.KeyboardEvent, enter: () => void) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      enter();
    }
  };
  return (
    <Group
      gap={6}
      px={6}
      py={4}
      wrap="wrap"
      className="kogge-editor-find"
      role="search"
      aria-label={t('editor.find.label')}
    >
      <TextInput
        ref={input}
        size="xs"
        w={220}
        placeholder={t('editor.find.find')}
        aria-label={t('editor.find.find')}
        value={query}
        onChange={(e) => setQuery(e.currentTarget.value)}
        onKeyDown={(e) => onKey(e, () => editor.commands.findNext(e.shiftKey ? -1 : 1))}
        rightSectionWidth={64}
        rightSection={
          query ? (
            <Text size="xs" c={s.total ? 'dimmed' : 'red'} data-find-count>
              {s.total
                ? t('editor.find.count', { current: s.current + 1, total: s.total })
                : t('editor.find.none')}
            </Text>
          ) : null
        }
      />
      <Toggle
        label={t('editor.find.caseSensitive')}
        active={caseSensitive}
        onClick={() => setCaseSensitive((v) => !v)}
      >
        <IconLetterCase size={16} />
      </Toggle>
      <Toggle label={t('editor.find.wholeWord')} active={wholeWord} onClick={() => setWholeWord((v) => !v)}>
        <IconLetterW size={16} />
      </Toggle>
      <Tooltip label={t('editor.find.previous')} openDelay={400}>
        <ActionIcon
          variant="subtle"
          color="gray"
          size="md"
          disabled={!s.total}
          onClick={() => editor.commands.findNext(-1)}
          aria-label={t('editor.find.previous')}
        >
          <IconArrowUp size={16} />
        </ActionIcon>
      </Tooltip>
      <Tooltip label={t('editor.find.next')} openDelay={400}>
        <ActionIcon
          variant="subtle"
          color="gray"
          size="md"
          disabled={!s.total}
          onClick={() => editor.commands.findNext(1)}
          aria-label={t('editor.find.next')}
        >
          <IconArrowDown size={16} />
        </ActionIcon>
      </Tooltip>
      {s.editable && (
        <>
          <TextInput
            size="xs"
            w={180}
            placeholder={t('editor.find.replace')}
            aria-label={t('editor.find.replace')}
            value={replacement}
            onChange={(e) => setReplacement(e.currentTarget.value)}
            onKeyDown={(e) => onKey(e, () => editor.commands.replaceCurrent(replacement))}
          />
          <Button
            size="compact-xs"
            variant="default"
            disabled={s.current < 0}
            onClick={() => editor.commands.replaceCurrent(replacement)}
          >
            {t('editor.find.replaceOne')}
          </Button>
          <Button
            size="compact-xs"
            variant="default"
            disabled={!s.total}
            onClick={() => editor.commands.replaceAll(replacement)}
          >
            {t('editor.find.replaceAll')}
          </Button>
        </>
      )}
      <Tooltip label={t('editor.find.close')} openDelay={400}>
        <ActionIcon
          variant="subtle"
          color="gray"
          size="md"
          onClick={close}
          aria-label={t('editor.find.close')}
          ml="auto"
        >
          <IconX size={16} />
        </ActionIcon>
      </Tooltip>
    </Group>
  );
}
