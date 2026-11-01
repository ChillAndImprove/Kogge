// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Divider, Group, Text, Tooltip } from '@mantine/core';
import {
  IconColumns2,
  IconColumns3,
  IconColumnsOff,
  IconLayoutBoardSplit,
  IconLayoutColumns,
  IconLayoutSidebar,
  IconLayoutSidebarRight,
  IconTrash,
} from '@tabler/icons-react';
import { useEditorState, type Editor } from '@tiptap/react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { columnLayouts, columnsAt, type ColumnLayout } from './columns';

function Tool({
  label,
  onClick,
  active,
  disabled,
  children,
}: {
  label: string;
  onClick: () => void;
  active?: boolean;
  disabled?: boolean;
  children: ReactNode;
}) {
  return (
    <Tooltip label={label} openDelay={400}>
      <ActionIcon
        variant={active ? 'light' : 'subtle'}
        color={active ? undefined : 'gray'}
        size="md"
        onMouseDown={(e) => e.preventDefault()}
        onClick={onClick}
        disabled={disabled}
        aria-label={label}
        aria-pressed={active}
      >
        {children}
      </ActionIcon>
    </Tooltip>
  );
}

const layoutIcons = [
  ['equal', IconLayoutColumns, 'editor.layoutEqual'],
  ['left', IconLayoutSidebar, 'editor.layoutLeft'],
  ['right', IconLayoutSidebarRight, 'editor.layoutRight'],
  ['sides', IconLayoutBoardSplit, 'editor.layoutSides'],
] as const satisfies readonly (readonly [ColumnLayout, unknown, string])[];

/** The tools of the layout the cursor is in: two or three columns, their widths, and removing the layout. */
export function ColumnsTools({ editor }: { editor: Editor }) {
  const { t } = useTranslation();
  const s = useEditorState({
    editor,
    selector: ({ editor: e }) => {
      const at = columnsAt(e.state.selection.$from);
      return { count: at?.node.childCount ?? 0, layout: String(at?.node.attrs.layout ?? 'equal') };
    },
  });
  const c = () => editor.chain().focus();
  return (
    <Group
      gap={2}
      px={6}
      py={2}
      className="kogge-editor-tabletools"
      role="toolbar"
      aria-label={t('editor.columnsTools')}
    >
      <Text size="xs" c="dimmed" mr={4}>
        {t('editor.columnsTools')}
      </Text>
      <Tool label={t('editor.twoColumns')} active={s.count === 2} onClick={() => c().setColumnCount(2).run()}>
        <IconColumns2 size={16} />
      </Tool>
      <Tool
        label={t('editor.threeColumns')}
        active={s.count === 3}
        onClick={() => c().setColumnCount(3).run()}
      >
        <IconColumns3 size={16} />
      </Tool>
      <Divider orientation="vertical" />
      {layoutIcons.map(([layout, Icon, label]) => (
        <Tool
          key={layout}
          label={t(label)}
          active={s.layout === layout}
          disabled={!columnLayouts[s.count]?.includes(layout)}
          onClick={() => c().setColumnsLayout(layout).run()}
        >
          <Icon size={16} />
        </Tool>
      ))}
      <Divider orientation="vertical" />
      <Tool label={t('editor.columnsUnwrap')} onClick={() => c().unsetColumns().run()}>
        <IconColumnsOff size={16} />
      </Tool>
      <Tool label={t('editor.columnsDelete')} onClick={() => c().deleteColumns().run()}>
        <IconTrash size={16} />
      </Tool>
    </Group>
  );
}
