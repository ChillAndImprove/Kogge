// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Modal } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { createContext, use, useCallback, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { CodeProjectPicker } from '../../components/Pickers';
import { CreateRepository } from './ProjectPage';

export interface CreateRepositoryOptions {
  /** The code project (key); without one the user chooses one. */
  project?: string;
}

const CreateRepositoryContext = createContext<(o?: CreateRepositoryOptions) => void>(() => {});

/** Opens the "new repository" dialog from anywhere in the app (Hafen's "New" menu, the quick search). */
export function useCreateRepository() {
  return use(CreateRepositoryContext);
}

/** Provides the app-wide "new repository" dialog: in the given code project, or in one the user chooses. */
export function CreateRepositoryProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const [options, setOptions] = useState<CreateRepositoryOptions | null>(null);
  const [project, setProject] = useState<string | null>(null);
  const key = project ?? options?.project?.toUpperCase() ?? null;
  // The given project or the one chosen, if the user may create repositories in it; otherwise they choose one.
  const current = useQuery({
    queryKey: ['code-project', key],
    queryFn: () => unwrap(api.GET('/api/v1/code/projects/{key}', { params: { path: { key: key! } } })),
    enabled: options !== null && !!key,
    retry: false,
  });
  const settled = !key || current.isSuccess || current.isError;
  const target = current.data?.canAdmin ? current.data : undefined;
  const close = useCallback(() => {
    setOptions(null);
    setProject(null);
  }, []);
  const open = useCallback((o?: CreateRepositoryOptions) => {
    setProject(null);
    setOptions(o ?? {});
  }, []);
  return (
    <CreateRepositoryContext value={open}>
      {children}
      {options && target && (!project || project === target.key) && (
        <CreateRepository projectKey={target.key} onClose={close} />
      )}
      {options && settled && !target && (
        <Modal opened onClose={close} title={t('hafen.newRepository')}>
          <CodeProjectPicker
            label={t('hafen.inProject')}
            placeholder={t('hafen.chooseProject')}
            admin
            value={project}
            onChange={setProject}
            nothingFoundMessage={t('hafen.noAdminProjects')}
          />
        </Modal>
      )}
    </CreateRepositoryContext>
  );
}
