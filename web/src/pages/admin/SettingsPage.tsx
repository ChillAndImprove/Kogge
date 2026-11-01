// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Button,
  Group,
  NumberInput,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { meQueryKey } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifySuccess } from '../../components/notify';
import { PageHeader } from '../../components/PageHeader';
import { SizeInput } from '../../components/SizeInput';

type Instance = Schemas['Instance'];

const timeZones = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.(
  'timeZone',
) ?? ['Europe/Berlin'];

export function SettingsPage() {
  const settings = useQuery({
    queryKey: ['admin', 'settings'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/settings')),
  });
  if (!settings.data) return <ErrorAlert error={settings.error} />;
  return <SettingsForm initial={settings.data} />;
}

/** The limits: what one user may store, so that nobody fills the disk (sizes in MiB, shown in MiB or GiB). */
function Limits({
  v,
  set,
}: {
  v: Instance;
  set: <K extends keyof Instance>(k: K, val: Instance[K]) => void;
}) {
  const { t } = useTranslation();
  return (
    <Stack gap="sm" mt="md" component="section" aria-labelledby="settings-limits">
      <Title order={3} id="settings-limits">
        {t('admin.limits.title')}
      </Title>
      <Text size="sm" c="dimmed">
        {t('admin.limits.help')}
      </Text>
      <Title order={5}>{t('admin.limits.git')}</Title>
      <SimpleGrid cols={{ base: 1, sm: 2 }}>
        <SizeInput
          label={t('admin.limits.maxPush')}
          description={t('admin.limits.maxPushHelp')}
          value={v.maxPushMb}
          onChange={(x) => set('maxPushMb', x)}
        />
        <SizeInput
          label={t('admin.limits.repositoryQuota')}
          description={t('admin.limits.repositoryQuotaHelp')}
          value={v.repositoryQuotaMb}
          onChange={(x) => set('repositoryQuotaMb', x)}
          min={0}
          offLabel={t('admin.limits.noQuota')}
          onValue={10240}
        />
        <SizeInput
          label={t('admin.limits.maxGitFile')}
          description={t('admin.limits.maxGitFileHelp')}
          value={v.maxGitFileMb}
          onChange={(x) => set('maxGitFileMb', x)}
          min={0}
          offLabel={t('admin.limits.noLimit')}
          onValue={100}
        />
        <SizeInput
          label={t('admin.limits.maxLfsObject')}
          value={v.maxLfsObjectMb ?? 5120}
          onChange={(x) => set('maxLfsObjectMb', x)}
          max={102400}
        />
      </SimpleGrid>
      <Title order={5}>{t('admin.limits.attachments')}</Title>
      <SimpleGrid cols={{ base: 1, sm: 2 }}>
        <SizeInput
          label={t('admin.limits.maxAttachment')}
          description={t('admin.limits.maxAttachmentHelp')}
          value={v.maxAttachmentMb}
          onChange={(x) => set('maxAttachmentMb', x)}
          max={10240}
        />
      </SimpleGrid>
      <Title order={5}>{t('admin.limits.builds')}</Title>
      <SimpleGrid cols={{ base: 1, sm: 2 }}>
        <SizeInput
          label={t('admin.limits.maxArtifact')}
          value={v.maxArtifactMb}
          onChange={(x) => set('maxArtifactMb', x)}
        />
        <SizeInput
          label={t('admin.limits.maxRunArtifacts')}
          description={t('admin.limits.maxRunArtifactsHelp')}
          value={v.maxRunArtifactsMb}
          onChange={(x) => set('maxRunArtifactsMb', x)}
          min={0}
          offLabel={t('admin.limits.noLimit')}
          onValue={10240}
        />
        <SizeInput
          label={t('admin.limits.maxCache')}
          value={v.maxCacheMb}
          onChange={(x) => set('maxCacheMb', x)}
        />
        <SizeInput
          label={t('admin.limits.maxJobLog')}
          description={t('admin.limits.maxJobLogHelp')}
          value={v.maxJobLogMb}
          onChange={(x) => set('maxJobLogMb', x)}
          max={10240}
        />
      </SimpleGrid>
      <Title order={5}>{t('admin.limits.space')}</Title>
      <Text size="sm" c="dimmed">
        {t('admin.limits.spaceHelp')}
      </Text>
      <SimpleGrid cols={{ base: 1, sm: 2 }}>
        <SizeInput
          label={t('admin.limits.minFreeSpace')}
          value={v.minFreeSpaceMb}
          onChange={(x) => set('minFreeSpaceMb', x)}
          min={0}
          offLabel={t('admin.limits.noMinimum')}
          onValue={2048}
        />
        <NumberInput
          label={t('admin.limits.minFreeSpacePercent')}
          min={0}
          max={50}
          suffix=" %"
          allowDecimal={false}
          value={v.minFreeSpacePercent}
          onChange={(x) => set('minFreeSpacePercent', Math.min(Math.max(Number(x) || 0, 0), 50))}
        />
      </SimpleGrid>
    </Stack>
  );
}

function SettingsForm({ initial }: { initial: Instance }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [v, setV] = useState<Instance>(initial);
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT('/api/v1/admin/settings', { body: v })),
    onSuccess: (data) => {
      client.setQueryData(['admin', 'settings'], data);
      void client.invalidateQueries({ queryKey: meQueryKey });
      notifySuccess(t('common.saved'));
    },
  });
  const set = <K extends keyof Instance>(k: K, val: Instance[K]) => setV({ ...v, [k]: val });
  return (
    <>
      <PageHeader title={t('admin.settings')} />
      <Stack maw={560}>
        <TextInput
          label={t('admin.instanceName')}
          value={v.name}
          onChange={(e) => set('name', e.currentTarget.value)}
        />
        <Select
          label={t('admin.defaultLocale')}
          value={v.defaultLocale}
          onChange={(x) => set('defaultLocale', (x ?? 'de') as Instance['defaultLocale'])}
          data={[
            { value: 'de', label: 'Deutsch' },
            { value: 'en', label: 'English' },
          ]}
        />
        <Select
          label={t('admin.defaultTimeZone')}
          value={v.defaultTimeZone}
          searchable
          onChange={(x) => set('defaultTimeZone', x ?? 'Europe/Berlin')}
          data={timeZones}
        />
        <Select
          label={t('admin.contentLanguage')}
          value={v.contentLanguage}
          onChange={(x) => set('contentLanguage', (x ?? 'german') as Instance['contentLanguage'])}
          data={[
            { value: 'german', label: 'Deutsch' },
            { value: 'english', label: 'English' },
            { value: 'simple', label: 'Simple' },
          ]}
        />
        <Switch
          label={t('admin.allowAnonymous')}
          checked={v.allowAnonymous}
          onChange={(e) => set('allowAnonymous', e.currentTarget.checked)}
        />
        <Switch
          label={t('admin.allowExternalImages')}
          checked={v.allowExternalImages}
          onChange={(e) => set('allowExternalImages', e.currentTarget.checked)}
        />
        <Switch
          label={t('admin.requireTwoFactor')}
          description={t('admin.requireTwoFactorHelp')}
          checked={!!v.requireTwoFactor}
          onChange={(e) => set('requireTwoFactor', e.currentTarget.checked)}
        />
        <Group grow>
          <NumberInput
            label={t('admin.hoursPerDay')}
            min={1}
            max={24}
            value={v.hoursPerDay}
            onChange={(x) => set('hoursPerDay', Number(x) || 8)}
          />
          <NumberInput
            label={t('admin.daysPerWeek')}
            min={1}
            max={7}
            value={v.daysPerWeek}
            onChange={(x) => set('daysPerWeek', Number(x) || 5)}
          />
        </Group>
        <NumberInput
          label={t('admin.auditRetentionDays')}
          min={30}
          value={v.auditRetentionDays}
          onChange={(x) => set('auditRetentionDays', Number(x) || 730)}
        />
        <NumberInput
          label={t('admin.trashRetentionDays')}
          min={0}
          value={v.trashRetentionDays}
          onChange={(x) => set('trashRetentionDays', Number(x) || 0)}
        />
        <Group grow>
          <NumberInput
            label={t('admin.buildLogRetentionDays')}
            min={0}
            value={v.buildLogRetentionDays}
            onChange={(x) => set('buildLogRetentionDays', Number(x) || 0)}
          />
          <NumberInput
            label={t('admin.maxJobTimeoutMinutes')}
            min={1}
            max={1440}
            value={v.maxJobTimeoutMinutes}
            onChange={(x) => set('maxJobTimeoutMinutes', Number(x) || 1440)}
          />
        </Group>
        <NumberInput
          label={t('admin.maxTokenDays')}
          description={t('admin.maxTokenDaysHelp')}
          min={0}
          max={3650}
          value={v.maxTokenDays}
          onChange={(x) => set('maxTokenDays', Number(x) || 0)}
        />
        <Textarea
          label={t('admin.webhookTargets')}
          value={v.webhookTargets}
          onChange={(e) => set('webhookTargets', e.currentTarget.value)}
          autosize
          minRows={2}
          ff="monospace"
        />
        <Textarea
          label={t('admin.announcement')}
          value={v.announcement}
          onChange={(e) => set('announcement', e.currentTarget.value)}
        />
        <Limits v={v} set={set} />
        <ErrorAlert error={save.error} />
        <Group>
          <Button onClick={() => save.mutate()} loading={save.isPending}>
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </>
  );
}
