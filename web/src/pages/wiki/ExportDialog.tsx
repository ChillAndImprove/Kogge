// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Badge,
  Button,
  Group,
  Modal,
  Paper,
  Progress,
  Radio,
  Select,
  Stack,
  Text,
  Tooltip,
} from '@mantine/core';
import { IconDownload, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatBytes, formatDateTime } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';

type Export = Schemas['WikiExport'];
type Format = Export['format'];

/** The exports of a space by the person signed in; live messages ("wiki-export") refresh them, and while one is
 * in progress they are asked for every few seconds, too. */
export function useExports(spaceKey: string) {
  return useQuery({
    queryKey: ['wiki-export', spaceKey],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/exports', { params: { query: { space: spaceKey } } })),
    refetchInterval: (q) =>
      q.state.data?.some((e) => e.state === 'queued' || e.state === 'running') ? 3000 : false,
  });
}

function ExportRow({ e, onDelete }: { e: Export; onDelete: () => void }) {
  const { t } = useTranslation();
  const what = e.page ? t('wiki.export.tree', { title: e.page.title }) : t('wiki.export.whole');
  return (
    <Paper withBorder p="xs" radius="sm" data-export={e.id} data-state={e.state}>
      <Group justify="space-between" wrap="nowrap" align="flex-start">
        <Stack gap={4} style={{ flex: 1, minWidth: 0 }}>
          <Group gap="xs" wrap="nowrap">
            <Badge size="sm" variant="light">
              {t(`wiki.export.format.${e.format}`)}
            </Badge>
            <Text size="sm" truncate>
              {what}
            </Text>
          </Group>
          {(e.state === 'queued' || e.state === 'running') && (
            <>
              <Progress
                value={e.pagesTotal ? (100 * e.pagesDone) / e.pagesTotal : 0}
                animated
                size="sm"
                aria-label={t('wiki.export.progress')}
              />
              <Text size="xs" c="dimmed">
                {e.state === 'queued' || !e.pagesTotal
                  ? t('wiki.export.queued')
                  : t('wiki.export.running', { done: e.pagesDone, total: e.pagesTotal })}
              </Text>
            </>
          )}
          {e.state === 'done' && (
            <Text size="xs" c="dimmed">
              {t('wiki.export.ready', {
                pages: e.pagesTotal,
                size: formatBytes(e.size),
                when: formatDateTime(e.expires),
              })}
            </Text>
          )}
          {e.state === 'failed' && (
            <Text size="xs" c="red">
              {e.error}
            </Text>
          )}
        </Stack>
        <Group gap={4} wrap="nowrap">
          {e.state === 'done' && e.url && (
            <Button
              size="xs"
              variant="light"
              component="a"
              href={e.url}
              download={e.filename}
              leftSection={<IconDownload size={14} />}
            >
              {t('wiki.export.download')}
            </Button>
          )}
          <Tooltip label={t('wiki.export.delete')}>
            <ActionIcon variant="subtle" color="gray" onClick={onDelete} aria-label={t('wiki.export.delete')}>
              <IconTrash size={16} />
            </ActionIcon>
          </Tooltip>
        </Group>
      </Group>
    </Paper>
  );
}

/** Exports a space, or a page with the pages below it: as an HTML site or Markdown files (ZIP archives with the
 * attachments) or as one PDF. The export runs in the background; it is offered for download here when it is
 * done, and kept for three days. */
export function ExportDialog({
  space,
  page,
  onClose,
}: {
  space: { key: string; name: string };
  page?: { id: number; title: string };
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [format, setFormat] = useState<Format>('html');
  const [paper, setPaper] = useState<'a4' | 'letter'>('a4');
  const exports = useExports(space.key);
  const refresh = () => client.invalidateQueries({ queryKey: ['wiki-export', space.key] });
  const start = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/wiki/spaces/{key}/exports', {
          params: { path: { key: space.key } },
          body: { format, pageId: page?.id, paper: format === 'pdf' ? paper : undefined },
        }),
      ),
    onSuccess: () => void refresh(),
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/api/v1/wiki/exports/{id}', { params: { path: { id } } })),
    onSuccess: () => void refresh(),
  });
  const list = (exports.data ?? []).filter((e) => !page || e.page?.id === page.id);
  return (
    <Modal
      opened
      onClose={onClose}
      size="lg"
      title={
        page
          ? t('wiki.export.titlePage', { title: page.title })
          : t('wiki.export.titleSpace', { name: space.name })
      }
    >
      <Stack>
        <Text size="sm" c="dimmed">
          {t('wiki.export.help')}
        </Text>
        <Radio.Group
          value={format}
          onChange={(v) => setFormat(v as Format)}
          label={t('wiki.export.formatLabel')}
        >
          <Stack gap="xs" mt={4}>
            {(['html', 'markdown', 'pdf'] as const).map((f) => (
              <Radio
                key={f}
                value={f}
                label={t(`wiki.export.format.${f}`)}
                description={t(`wiki.export.formatHelp.${f}`)}
              />
            ))}
          </Stack>
        </Radio.Group>
        {format === 'pdf' && (
          <Select
            label={t('wiki.export.paper')}
            data={[
              { value: 'a4', label: 'A4' },
              { value: 'letter', label: 'Letter' },
            ]}
            value={paper}
            onChange={(v) => setPaper(v === 'letter' ? 'letter' : 'a4')}
            allowDeselect={false}
            w={160}
          />
        )}
        <ErrorAlert error={start.error ?? remove.error} />
        <Group justify="flex-end">
          <Button onClick={() => start.mutate()} loading={start.isPending}>
            {t('wiki.export.start')}
          </Button>
        </Group>
        <Text fw={600} size="sm">
          {t('wiki.export.recent')}
        </Text>
        {list.length === 0 ? (
          <Text size="sm" c="dimmed">
            {t('wiki.export.none')}
          </Text>
        ) : (
          <Stack gap="xs" aria-label={t('wiki.export.recent')}>
            {list.map((e) => (
              <ExportRow key={e.id} e={e} onDelete={() => remove.mutate(e.id)} />
            ))}
          </Stack>
        )}
      </Stack>
    </Modal>
  );
}
