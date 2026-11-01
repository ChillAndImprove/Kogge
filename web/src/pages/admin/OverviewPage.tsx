// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Alert,
  Anchor,
  Card,
  Group,
  Progress,
  SimpleGrid,
  Skeleton,
  Stack,
  Text,
  ThemeIcon,
  Title,
} from '@mantine/core';
import {
  IconAlertTriangle,
  IconCircleCheck,
  IconDatabase,
  IconInfoCircle,
  IconShieldLock,
  IconUsers,
  IconAlertOctagon,
  IconArchive,
} from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useId, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatBytes, formatDateTime, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { products, type ProductId } from '../../products';

type Overview = Schemas['Overview'];
type Attention = Overview['attention'][number];

/** Where an administrator acts on what needs attention. */
const attentionLinks: Record<Attention['key'], string> = {
  'storage-low': '/admin/storage',
  'disk-low': '/admin/storage',
  'audit-forwarding-failing': '/admin/audit',
  'backup-failed': '/admin/system',
  'jobs-failed': '/admin/system',
  'no-backup': '/admin/system',
  'backup-old': '/admin/system',
  'admins-without-two-factor': '/admin/users',
  'single-admin': '/admin/permissions',
  'mail-off': '/admin/system',
  'agents-awaiting-approval': '/builds/agents',
  'accounts-locked': '/admin/users',
};

const severityColor = { critical: 'red', warning: 'orange', info: 'blue' } as const;
const severityIcon = {
  critical: IconAlertOctagon,
  warning: IconAlertTriangle,
  info: IconInfoCircle,
} as const;

/** The administration's start page: the installation at a glance. */
export function OverviewPage() {
  const { t } = useTranslation();
  const overview = useQuery({
    queryKey: ['admin', 'overview'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/overview')),
    refetchInterval: 60_000,
  });
  const o = overview.data;
  return (
    <>
      <PageHeader
        title={t('admin.overview.title')}
        subtitle={
          o
            ? t('admin.overview.subtitle', { version: o.version, since: formatDateTime(o.startedAt) })
            : undefined
        }
      />
      <ErrorAlert error={overview.error} />
      {!o ? (
        overview.isLoading && <Skeleton h={320} />
      ) : (
        <Stack gap="xl">
          <AttentionList items={o.attention} />
          <SimpleGrid cols={{ base: 1, sm: 2, xl: 4 }}>
            <People o={o} />
            <Security o={o} />
            <Storage o={o} />
            <Backup o={o} />
          </SimpleGrid>
          <Stack gap="xs">
            <Title order={4}>{t('admin.overview.products')}</Title>
            <SimpleGrid cols={{ base: 1, sm: 2, xl: 4 }}>
              <ProductCard
                id="kontor"
                rows={[
                  [t('admin.overview.issueProjects'), o.figures.issueProjects],
                  [t('admin.overview.issues'), o.figures.issues],
                  [t('admin.overview.issuesCreated30d'), o.figures.issuesCreated30d],
                  [t('admin.overview.activeSprints'), o.figures.activeSprints],
                ]}
              />
              <ProductCard
                id="logbuch"
                rows={[
                  [t('admin.overview.spaces'), o.figures.spaces],
                  [t('admin.overview.pages'), o.figures.pages],
                  [t('admin.overview.pageEdits30d'), o.figures.pageEdits30d],
                ]}
              />
              <ProductCard
                id="hafen"
                rows={[
                  [t('admin.overview.codeProjects'), o.figures.codeProjects],
                  [t('admin.overview.repositories'), o.figures.repositories],
                  [t('admin.overview.repositoryBytes'), formatBytes(o.figures.repositoryBytes ?? 0)],
                  [t('admin.overview.openPullRequests'), o.figures.openPullRequests],
                ]}
              />
              <ProductCard
                id="helling"
                rows={[
                  [t('admin.overview.runs7d'), o.figures.runs7d],
                  [t('admin.overview.failedRuns7d'), o.figures.failedRuns7d],
                  [t('admin.overview.agentsOnline'), o.figures.agentsOnline],
                  [t('admin.overview.jobsWaiting'), o.figures.jobsWaiting],
                ]}
              />
            </SimpleGrid>
          </Stack>
        </Stack>
      )}
    </>
  );
}

function AttentionList({ items }: { items: Attention[] }) {
  const { t } = useTranslation();
  if (items.length === 0) {
    return (
      <Alert
        color="teal"
        variant="light"
        icon={<IconCircleCheck size={20} />}
        title={t('admin.overview.allWell')}
      >
        {t('admin.overview.allWellText')}
      </Alert>
    );
  }
  return (
    <Stack gap="xs">
      <Title order={4} id="overview-attention">
        {t('admin.overview.attention')}
      </Title>
      <Stack gap="xs" role="list" aria-labelledby="overview-attention">
        {items.map((a) => {
          const Icon = severityIcon[a.severity];
          return (
            <Alert
              key={a.key}
              role="listitem"
              color={severityColor[a.severity]}
              variant="light"
              icon={<Icon size={20} />}
              title={t(`admin.overview.items.${a.key}.title` as 'admin.overview.title', { count: a.count })}
            >
              <Group justify="space-between" gap="xs" wrap="wrap">
                <Text size="sm">
                  {t(`admin.overview.items.${a.key}.text` as 'admin.overview.title', {
                    count: a.count,
                    since: a.since ? formatRelative(a.since) : '',
                  })}
                </Text>
                <Anchor component={Link} to={attentionLinks[a.key]} size="sm" fw={600}>
                  {t(`admin.overview.items.${a.key}.action` as 'admin.overview.title')}
                </Anchor>
              </Group>
            </Alert>
          );
        })}
      </Stack>
    </Stack>
  );
}

function StatCard({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  const id = useId();
  return (
    <Card withBorder padding="md" component="section" aria-labelledby={id}>
      <Group gap="xs" mb="sm">
        <ThemeIcon variant="light" size="md" radius="md">
          {icon}
        </ThemeIcon>
        <Text fw={600} id={id}>
          {title}
        </Text>
      </Group>
      <Stack gap={6}>{children}</Stack>
    </Card>
  );
}

function Row({ label, value }: { label: string; value: ReactNode }) {
  return (
    <Group justify="space-between" gap="xs" wrap="nowrap" align="flex-start">
      <Text size="sm" c="dimmed" style={{ minWidth: 0 }}>
        {label}
      </Text>
      <Text size="sm" fw={600} ta="right" style={{ whiteSpace: 'nowrap' }}>
        {value}
      </Text>
    </Group>
  );
}

function People({ o }: { o: Overview }) {
  const { t } = useTranslation();
  const p = o.people;
  return (
    <StatCard icon={<IconUsers size={18} />} title={t('admin.overview.people')}>
      <Row label={t('admin.overview.active')} value={p.active} />
      <Row label={t('admin.overview.activeLast30d')} value={p.activeLast30d} />
      <Row label={t('admin.overview.administrators')} value={p.administrators} />
      <Row label={t('admin.overview.sessions')} value={p.sessions} />
      <Row label={t('admin.overview.disabled')} value={p.disabled} />
    </StatCard>
  );
}

function Security({ o }: { o: Overview }) {
  const { t } = useTranslation();
  const p = o.people;
  const share = p.local > 0 ? Math.round((p.twoFactor / p.local) * 100) : 0;
  return (
    <StatCard icon={<IconShieldLock size={18} />} title={t('admin.overview.security')}>
      <Row
        label={t('admin.overview.signIn')}
        value={o.sso ? t('admin.overview.sso') : t('admin.overview.localOnly')}
      />
      <Row label={t('admin.overview.twoFactor')} value={`${p.twoFactor} / ${p.local}`} />
      <Row
        label={t('admin.overview.twoFactorRequired')}
        value={p.twoFactorRequired ? t('admin.overview.yes') : t('admin.overview.no')}
      />
      {p.local > 0 && (
        <Progress
          value={share}
          color={share === 100 ? 'teal' : 'orange'}
          size="sm"
          aria-label={t('admin.overview.twoFactorShare', { share })}
        />
      )}
      <Row label={t('admin.overview.failedSignIns')} value={p.failedSignIns24h} />
      <Row label={t('admin.overview.locked')} value={p.locked} />
    </StatCard>
  );
}

function Storage({ o }: { o: Overview }) {
  const { t } = useTranslation();
  const used =
    o.dataTotalBytes > 0 ? Math.round(((o.dataTotalBytes - o.dataFreeBytes) / o.dataTotalBytes) * 100) : 0;
  // Below the free space the settings keep, Kogge refuses pushes and uploads.
  const low = o.dataTotalBytes > 0 && o.dataFreeBytes < o.dataReserveBytes;
  return (
    <StatCard icon={<IconDatabase size={18} />} title={t('admin.overview.storage')}>
      <Row
        label={t('admin.overview.dataFree')}
        value={
          o.dataTotalBytes > 0 ? `${formatBytes(o.dataFreeBytes)} / ${formatBytes(o.dataTotalBytes)}` : '—'
        }
      />
      {o.dataTotalBytes > 0 && (
        <Progress
          value={used}
          color={low || used > 90 ? 'red' : used > 80 ? 'orange' : 'teal'}
          size="sm"
          aria-label={t('admin.overview.dataUsed', { used })}
        />
      )}
      {o.dataReserveBytes > 0 && (
        <Row
          label={t('admin.overview.minFree')}
          value={
            <Text span size="sm" fw={600} c={low ? 'red' : undefined}>
              {formatBytes(o.dataReserveBytes)}
            </Text>
          }
        />
      )}
      <Row label={t('admin.overview.database')} value={formatBytes(o.databaseBytes)} />
      <Row label={t('admin.overview.repositoryBytes')} value={formatBytes(o.figures.repositoryBytes ?? 0)} />
      <Row label={t('admin.overview.jobsWaitingBackground')} value={o.jobs.waiting} />
    </StatCard>
  );
}

function Backup({ o }: { o: Overview }) {
  const { t } = useTranslation();
  return (
    <StatCard icon={<IconArchive size={18} />} title={t('admin.overview.backup')}>
      <Row
        label={t('admin.overview.lastBackup')}
        value={o.lastBackup ? formatRelative(o.lastBackup) : t('admin.overview.never')}
      />
      {o.lastBackupBytes ? (
        <Row label={t('admin.overview.backupSize')} value={formatBytes(o.lastBackupBytes)} />
      ) : null}
      <Row
        label={t('admin.overview.mail')}
        value={o.mail ? t('admin.overview.on') : t('admin.overview.off')}
      />
      <Text size="xs" c="dimmed">
        {t('admin.overview.backupHelp')}
      </Text>
    </StatCard>
  );
}

function ProductCard({ id, rows }: { id: ProductId; rows: [string, ReactNode][] }) {
  const p = products[id];
  return (
    <Card withBorder padding="md">
      <Group gap="xs" mb="sm">
        <p.Logo size={24} />
        <Anchor component={Link} to={p.home} fw={600} c="var(--mantine-color-text)">
          {p.name}
        </Anchor>
      </Group>
      <Stack gap={6}>
        {rows.map(([label, value]) => (
          <Row key={label} label={label} value={value ?? 0} />
        ))}
      </Stack>
    </Card>
  );
}
