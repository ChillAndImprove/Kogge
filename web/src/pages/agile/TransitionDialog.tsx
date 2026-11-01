// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Modal, Select, Stack } from '@mantine/core';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { TransitionForm } from '../issues/IssueStatus';

export type AvailableTransition = Schemas['AvailableTransition'];

export interface PendingTransition {
  issueKey: string;
  candidates: AvailableTransition[];
  onDone: () => void;
}

export async function runTransition(
  key: string,
  t: AvailableTransition,
  fields?: Record<string, unknown>,
  comment?: string,
) {
  const doc = comment?.trim()
    ? ({
        type: 'doc',
        content: [{ type: 'paragraph', content: [{ type: 'text', text: comment.trim() }] }],
      } as Schemas['Doc'])
    : undefined;
  return unwrap(
    api.POST('/api/v1/issues/{key}/transitions', {
      params: { path: { key } },
      body: { transitionId: t.id, fields: fields as Record<string, never> | undefined, comment: doc },
    }),
  );
}

/** Lists the transitions of an issue that lead to one of the statuses. */
export async function transitionsTo(key: string, statuses: number[]) {
  const all = await unwrap(api.GET('/api/v1/issues/{key}/transitions', { params: { path: { key } } }));
  return all.filter((t) => statuses.includes(t.to.id));
}

/**
 * Asks which transition to run (when several lead to a column) and for the fields of its screen — the issue view's
 * transition form.
 */
export function TransitionDialog({
  pending,
  onClose,
}: {
  pending: PendingTransition | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [choice, setChoice] = useState<string | null>(null);
  if (!pending) return null;
  const selected =
    pending.candidates.length === 1
      ? pending.candidates[0]
      : pending.candidates.find((c) => String(c.id) === choice);
  return (
    <Modal
      opened
      onClose={onClose}
      size="lg"
      title={`${pending.issueKey}: ${selected?.name ?? t('agile.chooseTransition')}`}
    >
      <Stack>
        {pending.candidates.length > 1 && (
          <Select
            label={t('agile.transition')}
            data={pending.candidates.map((c) => ({ value: String(c.id), label: `${c.name} → ${c.to.name}` }))}
            value={choice}
            onChange={setChoice}
            data-autofocus
          />
        )}
        {selected && (
          <TransitionForm
            key={selected.id}
            issueKey={pending.issueKey}
            transition={selected}
            onCancel={onClose}
            onDone={() => {
              pending.onDone();
              onClose();
            }}
          />
        )}
      </Stack>
    </Modal>
  );
}
