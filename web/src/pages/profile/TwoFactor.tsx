// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Alert,
  Badge,
  Button,
  Card,
  Code,
  CopyButton,
  Group,
  Image,
  Modal,
  PasswordInput,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { IconDownload, IconShieldCheck, IconShieldOff } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { meQueryKey } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifySuccess } from '../../components/notify';

const statusKey = ['me', 'two-factor'];

/** Two-factor authentication of the signed-in user: an authenticator app, recovery codes. */
export function TwoFactorSection() {
  const { t } = useTranslation();
  const status = useQuery({ queryKey: statusKey, queryFn: () => unwrap(api.GET('/api/v1/me/two-factor')) });
  const [dialog, setDialog] = useState<'setup' | 'disable' | 'codes' | null>(null);
  const s = status.data;
  if (status.error) return <ErrorAlert error={status.error} />;
  if (!s?.available) return null;
  return (
    <Card withBorder padding="lg" maw={640}>
      <Stack gap="sm">
        <Group justify="space-between">
          <Group gap="xs">
            {s.enabled ? (
              <IconShieldCheck size={20} color="var(--mantine-color-teal-6)" aria-hidden />
            ) : (
              <IconShieldOff size={20} color="var(--mantine-color-dimmed)" aria-hidden />
            )}
            <Title order={4}>{t('profile.twoFactor.title')}</Title>
          </Group>
          <Badge color={s.enabled ? 'teal' : 'gray'} variant="light">
            {s.enabled ? t('profile.twoFactor.on') : t('profile.twoFactor.off')}
          </Badge>
        </Group>
        <Text size="sm" c="dimmed">
          {t('profile.twoFactor.help')}
        </Text>
        {s.enabled ? (
          <>
            <Text size="sm">{t('profile.twoFactor.codesLeft', { count: s.recoveryCodes })}</Text>
            {s.recoveryCodes <= 3 && (
              <Alert color="orange" variant="light">
                {t('profile.twoFactor.fewCodes')}
              </Alert>
            )}
            <Group>
              <Button variant="default" onClick={() => setDialog('codes')}>
                {t('profile.twoFactor.newCodes')}
              </Button>
              <Button variant="subtle" color="red" onClick={() => setDialog('disable')}>
                {t('profile.twoFactor.turnOff')}
              </Button>
            </Group>
          </>
        ) : (
          <Group>
            <Button onClick={() => setDialog('setup')}>{t('profile.twoFactor.setUp')}</Button>
          </Group>
        )}
      </Stack>
      {dialog === 'setup' && <SetupDialog onClose={() => setDialog(null)} />}
      {dialog === 'disable' && <DisableDialog onClose={() => setDialog(null)} />}
      {dialog === 'codes' && <NewCodesDialog onClose={() => setDialog(null)} />}
    </Card>
  );
}

function useRefresh() {
  const client = useQueryClient();
  return () => {
    void client.invalidateQueries({ queryKey: statusKey });
    void client.invalidateQueries({ queryKey: meQueryKey });
  };
}

/** Setting up in a dialog of the profile. */
function SetupDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const [done, setDone] = useState(false);
  return (
    <Modal
      opened
      onClose={onClose}
      title={done ? t('profile.twoFactor.title') : t('profile.twoFactor.setUp')}
      size="lg"
      closeOnClickOutside={!done}
    >
      <TwoFactorSetup onEnabled={() => setDone(true)} onDone={onClose} onCancel={onClose} />
    </Modal>
  );
}

/**
 * Setting up: scan the QR code, confirm a code with the password, keep the recovery codes. Who is signed in hears
 * of the new second factor once the codes are kept (onDone): a session that may only set one up then opens.
 */
export function TwoFactorSetup({
  onEnabled,
  onDone,
  onCancel,
}: {
  onEnabled?: () => void;
  onDone: () => void;
  onCancel?: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const setup = useQuery({
    queryKey: ['me', 'two-factor', 'setup'],
    queryFn: () => unwrap(api.POST('/api/v1/me/two-factor')),
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  });
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const enable = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/api/v1/me/two-factor/enable', { body: { code: code.trim(), password } })),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: statusKey });
      onEnabled?.();
    },
  });
  if (enable.data) {
    return (
      <RecoveryCodes
        codes={enable.data.codes}
        onDone={() => {
          void client.invalidateQueries({ queryKey: meQueryKey });
          onDone();
        }}
      />
    );
  }
  return (
    <Stack>
      <ErrorAlert error={setup.error} />
      {setup.data && (
        <>
          <Text size="sm">{t('profile.twoFactor.scan')}</Text>
          <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="lg">
            <Image
              src={`data:image/svg+xml;charset=utf-8,${encodeURIComponent(setup.data.qrCode)}`}
              alt={t('profile.twoFactor.qrAlt')}
              w={200}
              h={200}
              fit="contain"
            />
            <Stack gap={6}>
              <Text size="sm" c="dimmed">
                {t('profile.twoFactor.manual')}
              </Text>
              <Code block style={{ overflowWrap: 'anywhere', whiteSpace: 'pre-wrap' }}>
                {setup.data.secret}
              </Code>
              <Group>
                <CopyButton value={setup.data.secret.replace(/ /g, '')}>
                  {({ copied, copy }) => (
                    <Button size="compact-sm" variant="default" onClick={copy}>
                      {copied ? t('common.copied') : t('common.copy')}
                    </Button>
                  )}
                </CopyButton>
              </Group>
            </Stack>
          </SimpleGrid>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              enable.mutate();
            }}
          >
            <Stack>
              <TextInput
                label={t('profile.twoFactor.code')}
                description={t('profile.twoFactor.codeHelp')}
                value={code}
                onChange={(e) => setCode(e.currentTarget.value)}
                autoComplete="one-time-code"
                inputMode="numeric"
                maxLength={8}
                required
              />
              <PasswordInput
                label={t('profile.currentPassword')}
                value={password}
                onChange={(e) => setPassword(e.currentTarget.value)}
                autoComplete="current-password"
                required
              />
              <ErrorAlert error={enable.error} />
              <Group justify="flex-end">
                {onCancel && (
                  <Button variant="default" onClick={onCancel}>
                    {t('common.cancel')}
                  </Button>
                )}
                <Button type="submit" loading={enable.isPending}>
                  {t('profile.twoFactor.turnOn')}
                </Button>
              </Group>
            </Stack>
          </form>
        </>
      )}
    </Stack>
  );
}

/** The recovery codes, shown once: to copy or download. */
function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const { t } = useTranslation();
  const text = codes.join('\n');
  const download = () => {
    const url = URL.createObjectURL(new Blob([`${text}\n`], { type: 'text/plain' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = 'kogge-recovery-codes.txt';
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <Stack>
      <Alert color="teal" variant="light" title={t('profile.twoFactor.codesTitle')}>
        {t('profile.twoFactor.codesHelp')}
      </Alert>
      <Code block>{text}</Code>
      <Group justify="space-between">
        <Group gap="xs">
          <CopyButton value={text}>
            {({ copied, copy }) => (
              <Button variant="default" onClick={copy}>
                {copied ? t('common.copied') : t('common.copy')}
              </Button>
            )}
          </CopyButton>
          <Button variant="default" leftSection={<IconDownload size={16} />} onClick={download}>
            {t('profile.twoFactor.download')}
          </Button>
        </Group>
        <Button onClick={onDone}>{t('profile.twoFactor.saved')}</Button>
      </Group>
    </Stack>
  );
}

function DisableDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const refresh = useRefresh();
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const disable = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/api/v1/me/two-factor/disable', { body: { code: code.trim(), password } })),
    onSuccess: () => {
      refresh();
      notifySuccess(t('profile.twoFactor.turnedOff'));
      onClose();
    },
  });
  return (
    <Modal opened onClose={onClose} title={t('profile.twoFactor.turnOff')}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          disable.mutate();
        }}
      >
        <Stack>
          <Text size="sm">{t('profile.twoFactor.turnOffHelp')}</Text>
          <TextInput
            label={t('profile.twoFactor.code')}
            description={t('profile.twoFactor.codeOrRecovery')}
            value={code}
            onChange={(e) => setCode(e.currentTarget.value)}
            autoComplete="one-time-code"
            maxLength={32}
            required
          />
          <PasswordInput
            label={t('profile.currentPassword')}
            value={password}
            onChange={(e) => setPassword(e.currentTarget.value)}
            autoComplete="current-password"
            required
          />
          <ErrorAlert error={disable.error} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" color="red" loading={disable.isPending}>
              {t('profile.twoFactor.turnOff')}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function NewCodesDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const refresh = useRefresh();
  const [password, setPassword] = useState('');
  const renew = useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/me/two-factor/recovery-codes', { body: { password } })),
    onSuccess: refresh,
  });
  return (
    <Modal
      opened
      onClose={onClose}
      title={t('profile.twoFactor.newCodes')}
      size={renew.data ? 'lg' : 'md'}
      closeOnClickOutside={!renew.data}
    >
      {renew.data ? (
        <RecoveryCodes codes={renew.data.codes} onDone={onClose} />
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            renew.mutate();
          }}
        >
          <Stack>
            <Text size="sm">{t('profile.twoFactor.newCodesHelp')}</Text>
            <PasswordInput
              label={t('profile.currentPassword')}
              value={password}
              onChange={(e) => setPassword(e.currentTarget.value)}
              autoComplete="current-password"
              required
            />
            <ErrorAlert error={renew.error} />
            <Group justify="flex-end">
              <Button variant="default" onClick={onClose}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" loading={renew.isPending}>
                {t('profile.twoFactor.newCodes')}
              </Button>
            </Group>
          </Stack>
        </form>
      )}
    </Modal>
  );
}
