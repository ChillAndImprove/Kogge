// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Group, Modal, Stack, Text, TextInput, Textarea } from '@mantine/core';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { ShareEditor, type Share } from '../../components/ShareEditor';
import { notifySuccess } from '../../components/notify';

export type Filter = Schemas['Filter'];

/** Creates a filter (from a query) or changes its name, description, query and sharing. */
export function FilterForm({
  filter,
  query = '',
  columns = [],
  onClose,
}: {
  filter: Filter | null;
  query?: string;
  /** The columns of a new filter. */
  columns?: string[];
  onClose: (saved?: Filter) => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [name, setName] = useState(filter?.name ?? '');
  const [description, setDescription] = useState(filter?.description ?? '');
  const [q, setQ] = useState(filter?.query ?? query);
  const [shares, setShares] = useState<Share[]>(filter?.shares ?? []);
  const save = useMutation({
    mutationFn: () => {
      const body = { name: name.trim(), description, query: q, columns: filter?.columns ?? columns, shares };
      return filter
        ? unwrap(api.PUT('/api/v1/filters/{id}', { params: { path: { id: filter.id } }, body }))
        : unwrap(api.POST('/api/v1/filters', { body }));
    },
    onSuccess: (f) => {
      void client.invalidateQueries({ queryKey: ['filters'] });
      void client.invalidateQueries({ queryKey: ['filter-subscriptions'] });
      notifySuccess(t('filters.saved'));
      onClose(f);
    },
  });
  return (
    <Modal
      opened
      onClose={() => onClose()}
      title={filter ? t('filters.edit') : t('issues.saveFilter')}
      size="lg"
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) save.mutate();
        }}
      >
        <Stack>
          <TextInput
            label={t('filters.name')}
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
            maxLength={200}
            required
            data-autofocus
          />
          <Textarea
            label={t('common.description')}
            value={description}
            onChange={(e) => setDescription(e.currentTarget.value)}
            maxLength={2000}
            autosize
            minRows={2}
          />
          <Textarea
            label={t('filters.query')}
            description={t('filters.queryHelp')}
            value={q}
            onChange={(e) => setQ(e.currentTarget.value)}
            autosize
            minRows={2}
            maxRows={8}
            styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
          />
          <Stack gap={4}>
            <Text size="sm" fw={500}>
              {t('sharing.title')}
            </Text>
            <ShareEditor value={shares} onChange={setShares} />
          </Stack>
          <ErrorAlert error={save.error} />
          <Group justify="flex-end">
            <Button variant="default" onClick={() => onClose()}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={save.isPending} disabled={!name.trim()}>
              {t('common.save')}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
