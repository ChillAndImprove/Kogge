// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Group, Text } from '@mantine/core';
import { IconPencil, IconPuzzle, IconTrash } from '@tabler/icons-react';
import { NodeViewContent, NodeViewWrapper, type ReactNodeViewProps } from '@tiptap/react';
import { useContext, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ErrorBoundary } from '../ErrorBoundary';
import type { DocNode } from './DocView';
import { MacroDialog } from './MacroDialog';
import { macroDef, paramSummary } from './macros';
import { MacroRender } from './preview';

function Failed() {
  const { t } = useTranslation();
  return (
    <Text size="sm" c="dimmed">
      {t('macros.failed')}
    </Text>
  );
}

/**
 * How a macro looks in the editor: its name and parameters, and below them what readers see (where the editor
 * renders macros) or its body. A click selects the macro, a double click edits its parameters; its output is
 * not interactive while editing.
 */
export function MacroNodeView({
  node,
  updateAttributes,
  deleteNode,
  editor,
  getPos,
  selected,
}: ReactNodeViewProps) {
  const { t } = useTranslation();
  const render = useContext(MacroRender);
  const [editing, setEditing] = useState(false);
  const name = String(node.attrs.name ?? '');
  const params = (node.attrs.params ?? {}) as Record<string, unknown>;
  const def = macroDef(name);
  const json = useMemo(() => node.toJSON() as DocNode, [node]);
  const editable = editor.isEditable && !!def && def.params.length > 0;
  const select = () => {
    const pos = getPos();
    if (editor.isEditable && typeof pos === 'number') editor.commands.setNodeSelection(pos);
  };
  return (
    <NodeViewWrapper className="kogge-macro" data-macro={name} data-selected={selected || undefined}>
      <Group
        justify="space-between"
        gap="xs"
        wrap="nowrap"
        contentEditable={false}
        className="kogge-macro-head"
        onClick={select}
      >
        <Group gap={6} wrap="nowrap" style={{ minWidth: 0 }}>
          <IconPuzzle size={14} />
          <Text size="xs" fw={700}>
            {t(`macros.names.${name}`, { defaultValue: name })}
          </Text>
          <Text size="xs" c="dimmed" truncate>
            {paramSummary(params)}
          </Text>
        </Group>
        {editor.isEditable && (
          <Group gap={2} wrap="nowrap">
            {editable && (
              <ActionIcon
                size="xs"
                variant="subtle"
                onClick={() => setEditing(true)}
                aria-label={t('common.edit')}
              >
                <IconPencil size={12} />
              </ActionIcon>
            )}
            <ActionIcon
              size="xs"
              variant="subtle"
              color="red"
              onClick={() => deleteNode()}
              aria-label={t('common.remove')}
            >
              <IconTrash size={12} />
            </ActionIcon>
          </Group>
        )}
      </Group>
      {def?.body ? (
        <NodeViewContent className="kogge-macro-body" />
      ) : (
        render && (
          <div
            className="kogge-macro-preview"
            contentEditable={false}
            onClick={select}
            onDoubleClick={() => editable && setEditing(true)}
          >
            {/* Links and buttons of the output would leave the editor or take its focus. */}
            <div inert>
              <ErrorBoundary resetKey={json} fallback={<Failed />}>
                {render(json, false)}
              </ErrorBoundary>
            </div>
          </div>
        )
      )}
      {editing && (
        <MacroDialog
          value={{ name, params }}
          onSave={(v) => updateAttributes({ params: v.params })}
          onClose={() => setEditing(false)}
        />
      )}
    </NodeViewWrapper>
  );
}

/** An inline macro in the editor: its output where the editor renders macros, else its name. */
export function InlineMacroNodeView({ node }: ReactNodeViewProps) {
  const render = useContext(MacroRender);
  const json = useMemo(() => node.toJSON() as DocNode, [node]);
  const name = String(node.attrs.name ?? '');
  return (
    <NodeViewWrapper as="span" className="kogge-inline-macro" data-inline-macro={name}>
      {render ? (
        <span inert>
          <ErrorBoundary resetKey={json} fallback={name}>
            {render(json, true)}
          </ErrorBoundary>
        </span>
      ) : (
        name
      )}
    </NodeViewWrapper>
  );
}
