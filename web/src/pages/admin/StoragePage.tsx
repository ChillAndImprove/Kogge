// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Alert,
  Anchor,
  Badge,
  Button,
  Card,
  Group,
  Modal,
  Progress,
  Radio,
  SimpleGrid,
  Stack,
  Table,
  Text,
  Title,
} from '@mantine/core';
import { IconAlertOctagon } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatBytes, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifySuccess } from '../../components/notify';
import { PageHeader } from '../../components/PageHeader';
import { Pagination } from '../../components/Pagination';
import { SizeInput } from '../../components/SizeInput';
import { repoPath } from '../code/hooks';

type RepoStorage = Schemas['RepoStorage'];

const pageSize = 50;

/** The space Kogge takes: the data directory and the largest repositories, with their quotas. */
export function StoragePage() {
  const { t } = useTranslation();
  const [page, setPage] = useState(1);
  const [editing, setEditing] = useState<RepoStorage | null>(null);
  const overview = useQuery({
    queryKey: ['admin', 'overview'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/overview')),
  });
  const repos = useQuery({
    queryKey: ['admin', 'repository-sizes', page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/admin/repositories', {
          params: { query: { limit: pageSize, offset: (page - 1) * pageSize } },
        }),
      ),
  });
  const o = overview.data;
  const used =
    o && o.dataTotalBytes > 0
      ? Math.round(((o.dataTotalBytes - o.dataFreeBytes) / o.dataTotalBytes) * 100)
      : 0;
  const low = !!o && o.dataTotalBytes > 0 && o.dataFreeBytes < o.dataReserveBytes;
  return (
    <>
      <PageHeader title={t('admin.storagePage.title')} subtitle={t('admin.storagePage.subtitle')} />
      <Stack gap="xl">
        <ErrorAlert error={overview.error} />
        {o && (
          <Card withBorder padding="md" component="section" aria-labelledby="storage-data">
            <Stack gap="sm">
              <Group justify="space-between">
                <Text fw={600} id="storage-data">
                  {t('admin.storagePage.dataDirectory')}
                </Text>
                <Anchor component={Link} to="/admin/settings" size="sm">
                  {t('admin.storagePage.limits')}
                </Anchor>
              </Group>
              {low && (
                <Alert color="red" variant="light" icon={<IconAlertOctagon size={20} />}>
                  {t('admin.storagePage.low')}
                </Alert>
              )}
              <SimpleGrid cols={{ base: 1, sm: 3 }}>
                <Figure
                  label={t('admin.storagePage.free')}
                  value={o.dataTotalBytes > 0 ? formatBytes(o.dataFreeBytes) : '—'}
                />
                <Figure
                  label={t('admin.storagePage.size')}
                  value={o.dataTotalBytes > 0 ? formatBytes(o.dataTotalBytes) : '—'}
                />
                <Figure label={t('admin.storagePage.minFree')} value={formatBytes(o.dataReserveBytes)} />
              </SimpleGrid>
              {o.dataTotalBytes > 0 && (
                <Progress
                  value={used}
                  color={low || used > 90 ? 'red' : used > 80 ? 'orange' : 'teal'}
                  aria-label={t('admin.overview.dataUsed', { used })}
                />
              )}
            </Stack>
          </Card>
        )}
        <Stack gap="xs">
          <Title order={4} id="storage-repositories">
            {t('admin.storagePage.repositories')}
          </Title>
          <Text size="sm" c="dimmed">
            {t('admin.storagePage.measured')}
          </Text>
          <ErrorAlert error={repos.error} />
          {repos.data &&
            (repos.data.items.length === 0 ? (
              <Text c="dimmed">{t('admin.storagePage.noRepositories')}</Text>
            ) : (
              <Table.ScrollContainer minWidth={720}>
                <Table aria-labelledby="storage-repositories" verticalSpacing="xs">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>{t('admin.storagePage.repository')}</Table.Th>
                      <Table.Th ta="right">{t('admin.storagePage.git')}</Table.Th>
                      <Table.Th ta="right">{t('admin.storagePage.lfs')}</Table.Th>
                      <Table.Th ta="right">{t('admin.storagePage.together')}</Table.Th>
                      <Table.Th>{t('admin.storagePage.quota')}</Table.Th>
                      <Table.Th>{t('admin.storagePage.pushed')}</Table.Th>
                      <Table.Th />
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {repos.data.items.map((r) => (
                      <RepoRow key={r.id} r={r} onEdit={() => setEditing(r)} />
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            ))}
          {repos.data && repos.data.total > pageSize && (
            <Pagination total={Math.ceil(repos.data.total / pageSize)} value={page} onChange={setPage} />
          )}
        </Stack>
      </Stack>
      {editing && <QuotaModal repo={editing} onClose={() => setEditing(null)} />}
    </>
  );
}

function Figure({ label, value }: { label: string; value: string }) {
  return (
    <Stack gap={0}>
      <Text size="xs" c="dimmed">
        {label}
      </Text>
      <Text fw={600}>{value}</Text>
    </Stack>
  );
}

function RepoRow({ r, onEdit }: { r: RepoStorage; onEdit: () => void }) {
  const { t } = useTranslation();
  const total = r.size + r.lfsSize;
  const share = r.quota > 0 ? Math.min(100, Math.round((total / r.quota) * 100)) : 0;
  return (
    <Table.Tr>
      <Table.Td>
        <Anchor component={Link} to={repoPath(r)} size="sm" fw={500}>
          {r.project.key}/{r.slug}
        </Anchor>
        <Text size="xs" c="dimmed">
          {r.name}
        </Text>
      </Table.Td>
      <Table.Td ta="right">{formatBytes(r.size)}</Table.Td>
      <Table.Td ta="right">{formatBytes(r.lfsSize)}</Table.Td>
      <Table.Td ta="right" fw={600}>
        {formatBytes(total)}
      </Table.Td>
      <Table.Td>
        <Group gap={6} wrap="nowrap">
          <Text size="sm" c={r.quota > 0 && total > r.quota ? 'red' : undefined}>
            {r.quota > 0 ? `${formatBytes(r.quota)} (${share} %)` : t('admin.storagePage.noQuota')}
          </Text>
          {r.quotaMb !== undefined && (
            <Badge size="xs" variant="light">
              {t('admin.storagePage.ownQuota')}
            </Badge>
          )}
        </Group>
      </Table.Td>
      <Table.Td>
        <Text size="sm" c="dimmed">
          {r.pushed ? formatRelative(r.pushed) : '—'}
        </Text>
      </Table.Td>
      <Table.Td>
        <Button size="compact-xs" variant="light" onClick={onEdit}>
          {t('admin.storagePage.setQuota')}
        </Button>
      </Table.Td>
    </Table.Tr>
  );
}

/** Gives a repository a quota of its own, or the one of the settings again. */
function QuotaModal({ repo, onClose }: { repo: RepoStorage; onClose: () => void }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const settings = useQuery({
    queryKey: ['admin', 'settings'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/settings')),
  });
  const [mode, setMode] = useState<'settings' | 'none' | 'own'>(
    repo.quotaMb === undefined ? 'settings' : repo.quotaMb === 0 ? 'none' : 'own',
  );
  const [own, setOwn] = useState(repo.quotaMb || 10240);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/admin/repositories/{id}/quota', {
          params: { path: { id: repo.id } },
          body: mode === 'settings' ? {} : { quotaMb: mode === 'none' ? 0 : own },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['admin', 'repository-sizes'] });
      notifySuccess(t('admin.storagePage.quotaSaved'));
      onClose();
    },
  });
  const ofSettings = settings.data?.repositoryQuotaMb
    ? formatBytes(settings.data.repositoryQuotaMb * 1024 * 1024)
    : t('admin.storagePage.noQuota');
  return (
    <Modal
      opened
      onClose={onClose}
      title={t('admin.storagePage.quotaTitle', { name: `${repo.project.key}/${repo.slug}` })}
    >
      <Stack>
        <Radio.Group value={mode} onChange={(v) => setMode(v as typeof mode)}>
          <Stack gap="xs">
            <Radio value="settings" label={t('admin.storagePage.quotaOfSettings', { quota: ofSettings })} />
            <Radio value="none" label={t('admin.storagePage.quotaNone')} />
            <Radio value="own" label={t('admin.storagePage.quotaOwn')} />
          </Stack>
        </Radio.Group>
        {mode === 'own' && <SizeInput label={t('admin.storagePage.quota')} value={own} onChange={setOwn} />}
        <ErrorAlert error={save.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => save.mutate()} loading={save.isPending}>
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
