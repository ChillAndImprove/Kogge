// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Anchor, Button, Group, Paper, Stack, TextInput, Tooltip } from '@mantine/core';
import { IconExternalLink, IconPencil, IconUnlink } from '@tabler/icons-react';
import { useEditorState, type Editor } from '@tiptap/react';
import { BubbleMenu } from '@tiptap/react/menus';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';

/**
 * A link address as documents keep it: web addresses (a missing scheme becomes https), e-mail addresses and paths of
 * Kogge; null for anything else (scripts and other schemes), as documents refuse them.
 */
export function normalizeHref(value: string): string | null {
  const v = value.trim();
  if (!v) return null;
  if (/^(https?:\/\/[^\s/]|mailto:[^\s]|\/(?!\/)|#)/i.test(v)) return v;
  if (/^[a-z][a-z0-9+.-]*:/i.test(v)) return null;
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v)) return `mailto:${v}`;
  if (/^[^\s/]+\.[^\s]+/.test(v)) return `https://${v}`;
  return null;
}

/** Sets or changes the link of the selection (or of the link the cursor is in). */
export function LinkForm({ editor, onDone }: { editor: Editor; onDone: () => void }) {
  const { t } = useTranslation();
  const current = (editor.getAttributes('link').href as string | undefined) ?? '';
  const [value, setValue] = useState(current);
  const [error, setError] = useState(false);
  const apply = () => {
    const href = normalizeHref(value);
    if (!href) {
      setError(true);
      return;
    }
    const chain = editor.chain().focus().extendMarkRange('link');
    if (editor.state.selection.empty && !editor.isActive('link')) {
      // Without a selection, the address becomes the linked text.
      chain
        .insertContent({ type: 'text', text: value.trim(), marks: [{ type: 'link', attrs: { href } }] })
        .run();
    } else {
      chain.setLink({ href }).run();
    }
    onDone();
  };
  return (
    <Stack gap="xs" w={320}>
      <TextInput
        size="xs"
        label={t('editor.linkAddress')}
        placeholder="https://"
        value={value}
        error={error ? t('editor.linkInvalid') : undefined}
        onChange={(e) => {
          setValue(e.currentTarget.value);
          setError(false);
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            apply();
          }
          if (e.key === 'Escape') onDone();
        }}
        data-autofocus
        autoFocus
      />
      <Group justify="flex-end" gap="xs">
        {current && (
          <Button
            size="compact-xs"
            variant="subtle"
            color="red"
            onClick={() => {
              editor.chain().focus().extendMarkRange('link').unsetLink().run();
              onDone();
            }}
          >
            {t('editor.linkRemove')}
          </Button>
        )}
        <Button size="compact-xs" onClick={apply}>
          {t('editor.linkApply')}
        </Button>
      </Group>
    </Stack>
  );
}

/** Next to a link the cursor is in: where it leads, and editing or removing it. */
export function LinkBubble({ editor }: { editor: Editor }) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const href = useEditorState({
    editor,
    selector: ({ editor: e }) => (e.getAttributes('link').href as string | undefined) ?? '',
  });
  useEffect(() => {
    const reset = () => setEditing(false);
    editor.on('selectionUpdate', reset);
    return () => {
      editor.off('selectionUpdate', reset);
    };
  }, [editor]);
  return (
    <BubbleMenu
      editor={editor}
      pluginKey="koggeLinkBubble"
      shouldShow={({ editor: e, state }) => e.isEditable && state.selection.empty && e.isActive('link')}
      options={{ placement: 'bottom-start', offset: 6 }}
    >
      <Paper shadow="md" withBorder p={6} className="kogge-link-bubble">
        {editing ? (
          <LinkForm editor={editor} onDone={() => setEditing(false)} />
        ) : (
          <Group gap={4} wrap="nowrap">
            <Anchor
              href={href}
              target="_blank"
              rel="noopener noreferrer"
              size="xs"
              maw={260}
              truncate
              title={href}
            >
              {href}
            </Anchor>
            <Tooltip label={t('editor.linkOpen')}>
              <ActionIcon
                size="sm"
                variant="subtle"
                color="gray"
                component="a"
                href={href}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={t('editor.linkOpen')}
              >
                <IconExternalLink size={14} />
              </ActionIcon>
            </Tooltip>
            <Tooltip label={t('editor.linkEdit')}>
              <ActionIcon
                size="sm"
                variant="subtle"
                color="gray"
                onClick={() => setEditing(true)}
                aria-label={t('editor.linkEdit')}
              >
                <IconPencil size={14} />
              </ActionIcon>
            </Tooltip>
            <Tooltip label={t('editor.linkRemove')}>
              <ActionIcon
                size="sm"
                variant="subtle"
                color="red"
                onClick={() => editor.chain().focus().extendMarkRange('link').unsetLink().run()}
                aria-label={t('editor.linkRemove')}
              >
                <IconUnlink size={14} />
              </ActionIcon>
            </Tooltip>
          </Group>
        )}
      </Paper>
    </BubbleMenu>
  );
}
