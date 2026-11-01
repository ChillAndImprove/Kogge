// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Modal, Stack } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { createContext, use, useCallback, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { SpacePicker } from '../../components/Pickers';
import { CreatePageDialog } from './CreatePageDialog';
import { usePage } from './hooks';

export interface CreatePageOptions {
  kind: 'page' | 'blog';
  /** The space to create it in (key); without one, or without the right to write there, the user chooses one. */
  space?: string;
  /** The page on screen: a new page goes below it when it is a current page of the space. */
  pageId?: number;
}

const CreatePageContext = createContext<(o: CreatePageOptions) => void>(() => {});

/** Opens the "create page" dialog from anywhere in the app (Logbuch's create button, the quick search). */
export function useCreatePage() {
  return use(CreatePageContext);
}

/** Provides the app-wide "create page" dialog: in the given space, or in one the user chooses. */
export function CreatePageProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const [options, setOptions] = useState<CreatePageOptions | null>(null);
  const [chosen, setChosen] = useState<string | null>(null);
  const key = chosen ?? options?.space?.toUpperCase() ?? null;
  // The given space or the one chosen, if the user may write in it; otherwise they choose one.
  const current = useQuery({
    queryKey: ['space', key],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/spaces/{key}', { params: { path: { key: key! } } })),
    enabled: options !== null && !!key,
    retry: false,
  });
  const settled = !key || current.isSuccess || current.isError;
  const space =
    current.data && current.data.permissions.includes('page.edit') && !current.data.archived
      ? current.data
      : undefined;
  const onScreen = usePage(options?.pageId);
  const parentId =
    onScreen.data &&
    onScreen.data.kind === 'page' &&
    onScreen.data.status === 'current' &&
    onScreen.data.space.key === space?.key
      ? onScreen.data.id
      : (space?.homePageId ?? 0);
  const close = useCallback(() => {
    setOptions(null);
    setChosen(null);
  }, []);
  const open = useCallback((o: CreatePageOptions) => {
    setChosen(null);
    setOptions(o);
  }, []);
  return (
    <CreatePageContext value={open}>
      {children}
      {options && space && (!chosen || chosen === space.key) && !onScreen.isLoading && (
        <CreatePageDialog space={space} kind={options.kind} parentId={parentId} onClose={close} />
      )}
      {options && settled && !space && (
        <Modal
          opened
          onClose={close}
          title={options.kind === 'blog' ? t('wiki.createBlogPost') : t('wiki.createPage')}
        >
          <Stack>
            <SpacePicker
              label={t('logbuch.inSpace')}
              placeholder={t('logbuch.chooseSpace')}
              permission="page.edit"
              value={chosen}
              onChange={setChosen}
              nothingFoundMessage={t('logbuch.noWritableSpace')}
            />
          </Stack>
        </Modal>
      )}
    </CreatePageContext>
  );
}
