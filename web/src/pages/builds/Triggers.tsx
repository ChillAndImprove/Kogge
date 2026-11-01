// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Button,
  Card,
  Code,
  CopyButton,
  Group,
  Modal,
  NumberInput,
  Stack,
  Table,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconCheck, IconCopy, IconPlus, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import type { components } from '../../api/schema';
import { formatDate, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { repoParams, type Repository } from '../code/hooks';
import { runKey } from './hooks';

type PipelineTrigger = components['schemas']['PipelineTrigger'];

/** The pipeline triggers of a repository: tokens with which other systems start its pipelines. */
export function PipelineTriggers({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const key = runKey(repo.id, 'triggers');
  const triggers = useQuery({
    queryKey: key,
    queryFn: () =>
      unwrap(api.GET('/api/v1/code/repos/{key}/{slug}/triggers', { params: { path: repoParams(repo) } })),
  });
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<PipelineTrigger | null>(null);
  const [description, setDescription] = useState('');
  const [days, setDays] = useState<number>(365);
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/code/repos/{key}/{slug}/triggers', {
          params: { path: repoParams(repo) },
          body: { description, expiresInDays: days },
        }),
      ),
    onSuccess: (tr) => {
      setCreating(false);
      setCreated(tr);
      setDescription('');
      void client.invalidateQueries({ queryKey: key });
    },
  });
  const revoke = useMutation({
    mutationFn: (id: number) =>
      unwrap(
        api.DELETE('/api/v1/code/repos/{key}/{slug}/triggers/{id}', {
          params: { path: { ...repoParams(repo), id } },
        }),
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: key }),
  });
  const confirmRevoke = (tr: PipelineTrigger) =>
    modals.openConfirmModal({
      title: t('builds.revokeTrigger'),
      children: <Text size="sm">{t('builds.revokeTriggerConfirm', { name: tr.description })}</Text>,
      labels: { confirm: t('builds.revokeTrigger'), cancel: t('common.cancel') },
      confirmProps: { color: 'red' },
      onConfirm: () => revoke.mutate(tr.id),
    });
  const example = created?.token
    ? [
        `curl -X POST ${window.location.origin}/api/v1/builds/trigger \\`,
        `  -H "Authorization: Bearer ${created.token}" \\`,
        `  -H "Content-Type: application/json" \\`,
        `  -d '{"ref": "${repo.defaultBranch}"}'`,
      ].join('\n')
    : '';
  return (
    <Stack gap="xs">
      <Group justify="space-between">
        <Text size="sm" c="dimmed" style={{ flex: 1 }}>
          {t('builds.triggersHelp')}
        </Text>
        <Button
          size="xs"
          variant="light"
          leftSection={<IconPlus size={14} />}
          onClick={() => setCreating(true)}
        >
          {t('builds.newTrigger')}
        </Button>
      </Group>
      {created?.token && (
        <Alert
          color="green"
          title={t('builds.triggerCreated')}
          withCloseButton
          onClose={() => setCreated(null)}
        >
          <Stack gap="xs">
            <Text size="sm">{t('builds.triggerCreatedHelp')}</Text>
            <Group gap={6} wrap="nowrap">
              <Code style={{ overflowWrap: 'anywhere' }}>{created.token}</Code>
              <CopyButton value={created.token}>
                {({ copied, copy }) => (
                  <Tooltip label={copied ? t('common.copied') : t('common.copy')}>
                    <ActionIcon variant="subtle" onClick={copy} aria-label={t('common.copy')}>
                      {copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
                    </ActionIcon>
                  </Tooltip>
                )}
              </CopyButton>
            </Group>
            <Code block>{example}</Code>
          </Stack>
        </Alert>
      )}
      <ErrorAlert error={triggers.error ?? revoke.error} />
      {(triggers.data?.length ?? 0) > 0 && (
        <Card withBorder padding="xs">
          <Table>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('builds.triggerDescription')}</Table.Th>
                <Table.Th>{t('builds.triggerToken')}</Table.Th>
                <Table.Th>{t('builds.triggerCreatedBy')}</Table.Th>
                <Table.Th>{t('builds.triggerLastUsed')}</Table.Th>
                <Table.Th w={50} />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(triggers.data ?? []).map((tr) => (
                <Table.Tr key={tr.id}>
                  <Table.Td>
                    <Text size="sm" fw={500}>
                      {tr.description}
                    </Text>
                    <Text size="xs" c="dimmed">
                      {tr.expires
                        ? t('builds.triggerExpires', { date: formatDate(tr.expires) })
                        : t('builds.triggerNoExpiry')}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Text ff="monospace" size="sm">
                      {tr.tokenHint}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm">{tr.createdBy?.displayName ?? '—'}</Text>
                    <Text size="xs" c="dimmed">
                      {formatDate(tr.created)}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm">
                      {tr.lastUsed ? formatRelative(tr.lastUsed) : t('builds.triggerNeverUsed')}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Tooltip label={t('builds.revokeTrigger')}>
                      <ActionIcon
                        variant="subtle"
                        color="red"
                        onClick={() => confirmRevoke(tr)}
                        aria-label={`${t('builds.revokeTrigger')}: ${tr.description}`}
                      >
                        <IconTrash size={16} />
                      </ActionIcon>
                    </Tooltip>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Card>
      )}
      <Modal opened={creating} onClose={() => setCreating(false)} title={t('builds.newTrigger')}>
        <Stack>
          <TextInput
            label={t('builds.triggerDescription')}
            description={t('builds.triggerDescriptionHelp')}
            required
            value={description}
            onChange={(e) => setDescription(e.currentTarget.value)}
          />
          <NumberInput
            label={t('builds.triggerDays')}
            description={t('builds.triggerDaysHelp')}
            min={0}
            max={3650}
            value={days}
            onChange={(v) => setDays(Number(v) || 0)}
          />
          <ErrorAlert error={create.error} />
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setCreating(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => create.mutate()} loading={create.isPending} disabled={!description.trim()}>
              {t('common.create')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
