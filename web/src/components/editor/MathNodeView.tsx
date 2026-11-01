// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { NodeViewWrapper, type ReactNodeViewProps } from '@tiptap/react';
import { useTranslation } from 'react-i18next';
import { latexOf } from '../math/latex';
import { MathFormula } from '../MathFormula';
import { requestDialog } from './SlashMenu';

/**
 * A formula in the editor, typeset as readers see it. A click selects it; a double click, or Enter while it is
 * selected, edits it in the toolbar's formula dialog. An empty formula says that it is one, so that it can still be
 * found and chosen.
 */
export function MathNodeView({ node, editor, getPos, selected }: ReactNodeViewProps) {
  const { t } = useTranslation();
  const display = node.type.name === 'math';
  const latex = latexOf(node.attrs);
  const edit = () => {
    const pos = getPos();
    if (!editor.isEditable || typeof pos !== 'number') return;
    editor.commands.setNodeSelection(pos);
    requestDialog(editor, display ? 'math' : 'inlineMath');
  };
  const Empty = display ? 'div' : 'span';
  return (
    <NodeViewWrapper
      as={display ? 'div' : 'span'}
      className="kogge-math-edit"
      data-selected={selected || undefined}
      onDoubleClick={edit}
    >
      {latex.trim() ? (
        <MathFormula latex={latex} display={display} />
      ) : (
        <Empty className="kogge-math-empty">{t('editor.formulaEmpty')}</Empty>
      )}
    </NodeViewWrapper>
  );
}
