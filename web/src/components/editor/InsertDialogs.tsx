// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Badge, Button, ColorSwatch, Group, Modal, Stack, Text, TextInput } from '@mantine/core';
import { IconCheck } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

export const palette = [
  'gray',
  'red',
  'orange',
  'yellow',
  'green',
  'teal',
  'blue',
  'violet',
  'pink',
] as const;

/** Asks for the title of an expandable section. */
export function ExpandDialog({
  onInsert,
  onClose,
}: {
  onInsert: (title: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [title, setTitle] = useState('');
  const insert = () => {
    onInsert(title.trim());
    onClose();
  };
  return (
    <Modal opened onClose={onClose} title={t('editor.expand')} size="sm">
      <Stack>
        <TextInput
          label={t('editor.expandTitle')}
          value={title}
          onChange={(e) => setTitle(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') insert();
          }}
          data-autofocus
        />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={insert}>{t('macros.insertButton')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** Asks for the text and colour of a status lozenge, with how it will look. */
export function StatusDialog({
  onInsert,
  onClose,
}: {
  onInsert: (status: { text: string; color: string }) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState(t('editor.statusDefault'));
  const [color, setColor] = useState<string>('blue');
  const insert = () => {
    if (!text.trim()) return;
    onInsert({ text: text.trim(), color });
    onClose();
  };
  return (
    <Modal opened onClose={onClose} title={t('editor.status')} size="sm">
      <Stack>
        <TextInput
          label={t('editor.statusText')}
          value={text}
          maxLength={40}
          onChange={(e) => setText(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') insert();
          }}
          data-autofocus
        />
        <div>
          <Text size="sm" fw={500} mb={4}>
            {t('editor.color')}
          </Text>
          <Group gap={6} role="radiogroup" aria-label={t('editor.color')}>
            {palette.map((c) => (
              <ColorSwatch
                key={c}
                component="button"
                type="button"
                role="radio"
                aria-checked={c === color}
                aria-label={t(`colors.${c}`)}
                color={`var(--mantine-color-${c}-6)`}
                size={24}
                onClick={() => setColor(c)}
                style={{ color: '#fff', cursor: 'pointer' }}
              >
                {c === color && <IconCheck size={14} />}
              </ColorSwatch>
            ))}
          </Group>
        </div>
        <Group gap="xs">
          <Text size="sm" c="dimmed">
            {t('editor.statusPreview')}
          </Text>
          <Badge color={color} variant="light" radius="sm">
            {text.trim() || '…'}
          </Badge>
        </Group>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={insert} disabled={!text.trim()}>
            {t('macros.insertButton')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
