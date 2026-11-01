// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Loader, Modal } from '@mantine/core';
import { createContext, lazy, Suspense, use, useCallback, useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

const CreateIssueForm = lazy(() => import('./CreateIssueForm').then((m) => ({ default: m.CreateIssueForm })));

export interface CreateIssueOptions {
  project?: string;
  typeId?: number;
  parent?: string;
  /** Extra field values, e.g. a sprint or a version when created from a board. */
  fields?: Record<string, unknown>;
  /** Prefilled summary and description, e.g. from text selected on a wiki page. */
  summary?: string;
  description?: Record<string, unknown>;
  /** Called with the new issue's key. */
  onCreated?: (key: string) => void;
}

const CreateIssueContext = createContext<(o?: CreateIssueOptions) => void>(() => {});

/** Opens the "create issue" dialog from anywhere in the app. */
export function useCreateIssue() {
  return use(CreateIssueContext);
}

/** Provides the app-wide "create issue" dialog. */
export function CreateIssueProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const [options, setOptions] = useState<CreateIssueOptions | null>(null);
  const [instance, setInstance] = useState(0);
  const open = useCallback((o?: CreateIssueOptions) => {
    setInstance((i) => i + 1);
    setOptions(o ?? {});
  }, []);
  const value = useMemo(() => open, [open]);
  return (
    <CreateIssueContext value={value}>
      {children}
      <Modal
        opened={options !== null}
        onClose={() => setOptions(null)}
        title={t('issues.createIssue')}
        size="xl"
      >
        {options && (
          <Suspense fallback={<Loader size="sm" />}>
            <CreateIssueForm key={instance} options={options} onClose={() => setOptions(null)} />
          </Suspense>
        )}
      </Modal>
    </CreateIssueContext>
  );
}
