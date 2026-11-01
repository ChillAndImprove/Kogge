// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Group, Paper, ScrollArea, Text, ThemeIcon, UnstyledButton } from '@mantine/core';
import {
  IconAlertTriangle,
  IconBlockquote,
  IconCalendar,
  IconChartDots3,
  IconCircleCheck,
  IconCode,
  IconColumns2,
  IconColumns3,
  IconH1,
  IconH2,
  IconH3,
  IconInfoCircle,
  IconLayoutList,
  IconList,
  IconListCheck,
  IconListNumbers,
  IconListTree,
  IconMath,
  IconMathFunction,
  IconNote,
  IconPhoto,
  IconPilcrow,
  IconPuzzle,
  IconSeparatorHorizontal,
  IconTable,
  IconTag,
  IconXboxX,
  type Icon,
} from '@tabler/icons-react';
import { Extension, type Editor, type Range } from '@tiptap/core';
import { PluginKey } from '@tiptap/pm/state';
import { ReactRenderer } from '@tiptap/react';
import Suggestion from '@tiptap/suggestion';
import type { TFunction } from 'i18next';
import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react';
import i18n from '../../i18n';

/**
 * What the menu asks the editor's toolbar to do (dialogs and the file picker live there); math and inlineMath ask
 * for the formula dialog, which edits the formula selected or inserts one (on its own line or in the text).
 */
export type SlashRequest = 'macro' | 'mermaid' | 'expand' | 'status' | 'image' | 'math' | 'inlineMath';

type SlashId =
  | 'text'
  | 'h1'
  | 'h2'
  | 'h3'
  | 'bulletList'
  | 'orderedList'
  | 'taskList'
  | 'quote'
  | 'codeBlock'
  | 'table'
  | 'divider'
  | 'columns2'
  | 'columns3'
  | 'info'
  | 'note'
  | 'warning'
  | 'success'
  | 'error'
  | 'expand'
  | 'date'
  | 'status'
  | 'toc'
  | 'diagram'
  | 'formula'
  | 'formulaBlock'
  | 'image'
  | 'macro';

interface SlashItem {
  id: SlashId;
  icon: Icon;
  /** Only in editors with wiki content (macros, panels, …). */
  wiki?: boolean;
  run: (editor: Editor, range: Range, request: (r: SlashRequest) => void) => void;
}

const chain = (editor: Editor, range: Range) => editor.chain().focus().deleteRange(range);

const items: SlashItem[] = [
  { id: 'text', icon: IconPilcrow, run: (e, r) => chain(e, r).setParagraph().run() },
  { id: 'h1', icon: IconH1, run: (e, r) => chain(e, r).setHeading({ level: 1 }).run() },
  { id: 'h2', icon: IconH2, run: (e, r) => chain(e, r).setHeading({ level: 2 }).run() },
  { id: 'h3', icon: IconH3, run: (e, r) => chain(e, r).setHeading({ level: 3 }).run() },
  { id: 'bulletList', icon: IconList, run: (e, r) => chain(e, r).toggleBulletList().run() },
  { id: 'orderedList', icon: IconListNumbers, run: (e, r) => chain(e, r).toggleOrderedList().run() },
  { id: 'taskList', icon: IconListCheck, run: (e, r) => chain(e, r).toggleTaskList().run() },
  { id: 'quote', icon: IconBlockquote, run: (e, r) => chain(e, r).toggleBlockquote().run() },
  { id: 'codeBlock', icon: IconCode, run: (e, r) => chain(e, r).setCodeBlock().run() },
  {
    id: 'table',
    icon: IconTable,
    run: (e, r) => chain(e, r).insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run(),
  },
  { id: 'divider', icon: IconSeparatorHorizontal, run: (e, r) => chain(e, r).setHorizontalRule().run() },
  {
    id: 'columns2',
    icon: IconColumns2,
    wiki: true,
    run: (e, r) => chain(e, r).insertColumns(2).run(),
  },
  {
    id: 'columns3',
    icon: IconColumns3,
    wiki: true,
    run: (e, r) => chain(e, r).insertColumns(3).run(),
  },
  {
    id: 'info',
    icon: IconInfoCircle,
    wiki: true,
    run: (e, r) => chain(e, r).wrapIn('panel', { kind: 'info' }).run(),
  },
  {
    id: 'note',
    icon: IconNote,
    wiki: true,
    run: (e, r) => chain(e, r).wrapIn('panel', { kind: 'note' }).run(),
  },
  {
    id: 'warning',
    icon: IconAlertTriangle,
    wiki: true,
    run: (e, r) => chain(e, r).wrapIn('panel', { kind: 'warning' }).run(),
  },
  {
    id: 'success',
    icon: IconCircleCheck,
    wiki: true,
    run: (e, r) => chain(e, r).wrapIn('panel', { kind: 'success' }).run(),
  },
  {
    id: 'error',
    icon: IconXboxX,
    wiki: true,
    run: (e, r) => chain(e, r).wrapIn('panel', { kind: 'error' }).run(),
  },
  {
    id: 'expand',
    icon: IconLayoutList,
    wiki: true,
    run: (e, r, request) => {
      chain(e, r).run();
      request('expand');
    },
  },
  {
    id: 'date',
    icon: IconCalendar,
    wiki: true,
    run: (e, r) =>
      chain(e, r)
        .insertContent({ type: 'date', attrs: { value: new Date().toISOString().slice(0, 10) } })
        .run(),
  },
  {
    id: 'status',
    icon: IconTag,
    wiki: true,
    run: (e, r, request) => {
      chain(e, r).run();
      request('status');
    },
  },
  {
    id: 'toc',
    icon: IconListTree,
    wiki: true,
    run: (e, r) =>
      chain(e, r)
        .insertContent({ type: 'macro', attrs: { name: 'toc', params: { maxLevel: 3 } } })
        .run(),
  },
  {
    id: 'diagram',
    icon: IconChartDots3,
    wiki: true,
    run: (e, r, request) => {
      chain(e, r).run();
      request('mermaid');
    },
  },
  // Formulas are no wiki content: descriptions and comments have them, too.
  {
    id: 'formula',
    icon: IconMathFunction,
    run: (e, r, request) => {
      chain(e, r).run();
      request('inlineMath');
    },
  },
  {
    id: 'formulaBlock',
    icon: IconMath,
    run: (e, r, request) => {
      chain(e, r).run();
      request('math');
    },
  },
  {
    id: 'image',
    icon: IconPhoto,
    run: (e, r, request) => {
      chain(e, r).run();
      request('image');
    },
  },
  {
    id: 'macro',
    icon: IconPuzzle,
    wiki: true,
    run: (e, r, request) => {
      chain(e, r).run();
      request('macro');
    },
  },
];

function matches(t: TFunction, item: SlashItem, query: string): boolean {
  if (!query) return true;
  const q = query.toLowerCase();
  const words = `${t(`editor.slash.${item.id}`)} ${t(`editor.slash.${item.id}Keywords`, { defaultValue: '' })} ${item.id}`;
  return words.toLowerCase().includes(q);
}

export interface SlashListHandle {
  onKeyDown: (event: KeyboardEvent) => boolean;
}

/** The list of the menu: arrow keys choose, Enter inserts, Escape closes. */
const SlashList = forwardRef<SlashListHandle, { items: SlashItem[]; command: (item: SlashItem) => void }>(
  function SlashList({ items: list, command }, ref) {
    const t = i18n.t.bind(i18n);
    const [selected, setSelected] = useState(0);
    const active = useRef<HTMLButtonElement | null>(null);
    useEffect(() => setSelected(0), [list]);
    useEffect(() => active.current?.scrollIntoView({ block: 'nearest' }), [selected]);
    useImperativeHandle(ref, () => ({
      onKeyDown: (event) => {
        if (event.key === 'ArrowUp') {
          setSelected((s) => (s + list.length - 1) % Math.max(list.length, 1));
          return true;
        }
        if (event.key === 'ArrowDown') {
          setSelected((s) => (s + 1) % Math.max(list.length, 1));
          return true;
        }
        if (event.key === 'Enter' || event.key === 'Tab') {
          const item = list[selected];
          if (item) command(item);
          return !!item;
        }
        return false;
      },
    }));
    if (list.length === 0) {
      return (
        <Paper shadow="md" withBorder p="xs" w={280}>
          <Text size="sm" c="dimmed">
            {t('editor.slash.none')}
          </Text>
        </Paper>
      );
    }
    return (
      <Paper shadow="md" withBorder p={4} w={280} className="kogge-slash-menu">
        <ScrollArea.Autosize mah={320} type="auto">
          <div role="listbox" aria-label={t('editor.slash.label')}>
            {list.map((item, i) => {
              const Icon = item.icon;
              return (
                <UnstyledButton
                  key={item.id}
                  ref={i === selected ? active : undefined}
                  display="block"
                  w="100%"
                  px="xs"
                  py={5}
                  role="option"
                  aria-selected={i === selected}
                  className="kogge-slash-item"
                  onMouseEnter={() => setSelected(i)}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    command(item);
                  }}
                >
                  <Group gap="sm" wrap="nowrap">
                    <ThemeIcon variant="light" color="gray" size="md" radius="sm">
                      <Icon size={16} />
                    </ThemeIcon>
                    <div>
                      <Text size="sm" lh={1.3}>
                        {t(`editor.slash.${item.id}`)}
                      </Text>
                      <Text size="xs" c="dimmed" lh={1.3}>
                        {t(`editor.slash.${item.id}Help`)}
                      </Text>
                    </div>
                  </Group>
                </UnstyledButton>
              );
            })}
          </div>
        </ScrollArea.Autosize>
      </Paper>
    );
  },
);

export interface SlashOptions {
  /** Offers the wiki's blocks (panels, macros, …). */
  wiki: boolean;
  /** Images can be uploaded. */
  images: boolean;
}

export interface SlashStorage {
  request: ((r: SlashRequest) => void) | null;
}

const slashKey = new PluginKey('koggeSlash');

const slashStorage = (editor: Editor) =>
  (editor.storage as unknown as Record<string, SlashStorage | undefined>).koggeSlash;

/** Lets the editor's toolbar answer the menu's requests; returns the function that stops it. */
export function onSlashRequest(editor: Editor, handle: (r: SlashRequest) => void): () => void {
  const storage = slashStorage(editor);
  if (!storage) return () => undefined;
  storage.request = handle;
  return () => {
    storage.request = null;
  };
}

/**
 * Asks the editor's toolbar for a dialog, as the menu does (also for typed shortcuts and nodes); false where no
 * toolbar answers (an editor without one).
 */
export function requestDialog(editor: Editor, r: SlashRequest): boolean {
  const request = slashStorage(editor)?.request;
  if (!request) return false;
  request(r);
  return true;
}

/** Typing "/" at the start of a line or after a space offers blocks to insert, filtered by what follows. */
export const SlashCommands = Extension.create<SlashOptions, SlashStorage>({
  name: 'koggeSlash',
  addOptions: () => ({ wiki: false, images: false }),
  addStorage: () => ({ request: null }),
  addProseMirrorPlugins() {
    const t = i18n.t.bind(i18n);
    const offered = items.filter(
      (i) => (this.options.wiki || !i.wiki) && (this.options.images || i.id !== 'image'),
    );
    const storage = this.storage;
    return [
      Suggestion<SlashItem, SlashItem>({
        editor: this.editor,
        pluginKey: slashKey,
        char: '/',
        // Not within code, where a slash is a slash.
        allow: ({ state, range }) => {
          const $from = state.doc.resolve(range.from);
          return !$from.parent.type.spec.code && !$from.marks().some((m) => m.type.name === 'code');
        },
        items: ({ query }) => offered.filter((i) => matches(t, i, query)),
        command: ({ editor, range, props }) => props.run(editor, range, (r) => storage.request?.(r)),
        render: () => {
          let renderer: ReactRenderer<SlashListHandle> | null = null;
          let element: HTMLDivElement | null = null;
          const place = (rect: DOMRect | null | undefined) => {
            if (!element || !rect) return;
            const below = rect.bottom + 330 < window.innerHeight;
            element.style.left = `${rect.left + window.scrollX}px`;
            element.style.top = below
              ? `${rect.bottom + window.scrollY + 4}px`
              : `${rect.top + window.scrollY - 4 - element.offsetHeight}px`;
          };
          return {
            onStart: (props) => {
              renderer = new ReactRenderer(SlashList, { props, editor: props.editor });
              element = document.createElement('div');
              element.style.position = 'absolute';
              element.style.zIndex = '1000';
              element.appendChild(renderer.element);
              document.body.appendChild(element);
              place(props.clientRect?.());
            },
            onUpdate: (props) => {
              renderer?.updateProps(props);
              place(props.clientRect?.());
            },
            onKeyDown: (props) => {
              if (props.event.key === 'Escape') {
                element?.remove();
                return true;
              }
              return renderer?.ref?.onKeyDown(props.event) ?? false;
            },
            onExit: () => {
              renderer?.destroy();
              element?.remove();
              renderer = null;
              element = null;
            },
          };
        },
      }),
    ];
  },
});
