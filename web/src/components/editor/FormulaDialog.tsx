// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Group, Modal, SegmentedControl, Stack, Text, Textarea } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { latexTooLong } from '../math/latex';
import { MathFormula } from '../MathFormula';

export interface FormulaValue {
  latex: string;
  /** On a line of its own, else within the text. */
  display: boolean;
}

/**
 * Writes a formula in LaTeX beside how it will look (typeset once the typing rests for a moment, with KaTeX's
 * reason where it cannot be); inserting asks, too, whether it stands in the text or on a line of its own.
 * Ctrl/Cmd+Enter saves. value is the LaTeX of the formula edited, null to insert one.
 */
export function FormulaDialog({
  value,
  display: initialDisplay,
  onSave,
  onClose,
}: {
  value: string | null;
  display: boolean;
  onSave: (v: FormulaValue) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [latex, setLatex] = useState(value ?? '');
  const [display, setDisplay] = useState(initialDisplay);
  const [shown] = useDebouncedValue(latex, 200);
  const [error, setError] = useState<string | null>(null);
  const source = latex.trim();
  const tooLong = latexTooLong(source);
  const save = () => {
    if (!source || tooLong) return;
    onSave({ latex: source, display });
    onClose();
  };
  return (
    <Modal opened onClose={onClose} title={t('editor.formula')} size="lg">
      <Stack>
        {value === null && (
          <SegmentedControl
            value={display ? 'display' : 'inline'}
            onChange={(v) => setDisplay(v === 'display')}
            aria-label={t('editor.formula')}
            data={[
              { value: 'inline', label: t('editor.formulaInline') },
              { value: 'display', label: t('editor.formulaDisplay') },
            ]}
          />
        )}
        <Textarea
          label={t('editor.formulaLatex')}
          value={latex}
          onChange={(e) => setLatex(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
              e.preventDefault();
              save();
            }
          }}
          error={tooLong ? t('editor.formulaTooLong') : undefined}
          autosize
          minRows={3}
          maxRows={14}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          data-autofocus
          styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
        />
        <div>
          <Text size="sm" fw={500} mb={4}>
            {t('editor.formulaPreview')}
          </Text>
          <div className="kogge-formula-preview kogge-doc" data-display={display || undefined}>
            {shown.trim() ? (
              <MathFormula latex={shown.trim()} display={display} onResult={setError} />
            ) : (
              <Text size="sm" c="dimmed">
                {t('editor.formulaEmpty')}
              </Text>
            )}
          </div>
          {error && shown.trim() && (
            <Text size="xs" c="red" mt={4} role="status">
              {t('editor.formulaError', { message: error })}
            </Text>
          )}
        </div>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={save} disabled={!source || tooLong}>
            {value !== null ? t('common.save') : t('macros.insertButton')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
