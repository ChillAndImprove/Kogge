// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, ColorSwatch, Divider, Group, Menu, Text, Tooltip } from '@mantine/core';
import {
  IconColumnInsertLeft,
  IconColumnInsertRight,
  IconColumnRemove,
  IconLayoutNavbar,
  IconLayoutSidebar,
  IconPalette,
  IconRowInsertBottom,
  IconRowInsertTop,
  IconRowRemove,
  IconTableMinus,
  IconTableOff,
  IconTablePlus,
} from '@tabler/icons-react';
import { useEditorState, type Editor } from '@tiptap/react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { palette as backgrounds } from './InsertDialogs';

function Tool({
  label,
  onClick,
  disabled,
  children,
}: {
  label: string;
  onClick: () => void;
  disabled?: boolean;
  children: ReactNode;
}) {
  return (
    <Tooltip label={label} openDelay={400}>
      <ActionIcon
        variant="subtle"
        color="gray"
        size="md"
        onMouseDown={(e) => e.preventDefault()}
        onClick={onClick}
        disabled={disabled}
        aria-label={label}
      >
        {children}
      </ActionIcon>
    </Tooltip>
  );
}

/** The tools of the table the cursor is in: rows, columns, cells and their colours. */
export function TableTools({ editor }: { editor: Editor }) {
  const { t } = useTranslation();
  const can = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      merge: e.can().mergeCells(),
      split: e.can().splitCell(),
      deleteRow: e.can().deleteRow(),
      deleteColumn: e.can().deleteColumn(),
    }),
  });
  const c = () => editor.chain().focus();
  return (
    <Group
      gap={2}
      px={6}
      py={2}
      className="kogge-editor-tabletools"
      role="toolbar"
      aria-label={t('editor.tableTools')}
    >
      <Text size="xs" c="dimmed" mr={4}>
        {t('editor.table')}
      </Text>
      <Tool label={t('editor.rowAbove')} onClick={() => c().addRowBefore().run()}>
        <IconRowInsertTop size={16} />
      </Tool>
      <Tool label={t('editor.rowBelow')} onClick={() => c().addRowAfter().run()}>
        <IconRowInsertBottom size={16} />
      </Tool>
      <Tool label={t('editor.rowDelete')} onClick={() => c().deleteRow().run()} disabled={!can.deleteRow}>
        <IconRowRemove size={16} />
      </Tool>
      <Divider orientation="vertical" />
      <Tool label={t('editor.columnLeft')} onClick={() => c().addColumnBefore().run()}>
        <IconColumnInsertLeft size={16} />
      </Tool>
      <Tool label={t('editor.columnRight')} onClick={() => c().addColumnAfter().run()}>
        <IconColumnInsertRight size={16} />
      </Tool>
      <Tool
        label={t('editor.columnDelete')}
        onClick={() => c().deleteColumn().run()}
        disabled={!can.deleteColumn}
      >
        <IconColumnRemove size={16} />
      </Tool>
      <Divider orientation="vertical" />
      <Tool label={t('editor.mergeCells')} onClick={() => c().mergeCells().run()} disabled={!can.merge}>
        <IconTableMinus size={16} />
      </Tool>
      <Tool label={t('editor.splitCell')} onClick={() => c().splitCell().run()} disabled={!can.split}>
        <IconTablePlus size={16} />
      </Tool>
      <Tool label={t('editor.headerRow')} onClick={() => c().toggleHeaderRow().run()}>
        <IconLayoutNavbar size={16} />
      </Tool>
      <Tool label={t('editor.headerColumn')} onClick={() => c().toggleHeaderColumn().run()}>
        <IconLayoutSidebar size={16} />
      </Tool>
      <Menu position="bottom-start" withinPortal>
        <Menu.Target>
          <ActionIcon
            variant="subtle"
            color="gray"
            size="md"
            aria-label={t('editor.cellColor')}
            onMouseDown={(e) => e.preventDefault()}
          >
            <IconPalette size={16} />
          </ActionIcon>
        </Menu.Target>
        <Menu.Dropdown>
          <Group gap={4} p={4} maw={180}>
            {backgrounds.map((color) => (
              <ColorSwatch
                key={color}
                component="button"
                type="button"
                color={`var(--mantine-color-${color}-light)`}
                size={20}
                aria-label={t(`colors.${color}`)}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => c().setCellAttribute('background', color).run()}
              />
            ))}
          </Group>
          <Menu.Item onClick={() => c().setCellAttribute('background', null).run()}>
            {t('editor.cellColorNone')}
          </Menu.Item>
        </Menu.Dropdown>
      </Menu>
      <Divider orientation="vertical" />
      <Tool label={t('editor.tableDelete')} onClick={() => c().deleteTable().run()}>
        <IconTableOff size={16} />
      </Tool>
    </Group>
  );
}
