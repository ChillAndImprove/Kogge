// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Alert,
  Badge,
  Button,
  Checkbox,
  Code,
  CopyButton,
  Group,
  Modal,
  NumberInput,
  PasswordInput,
  Select,
  Stack,
  Table,
  Tabs,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatDateTime, formatRelative } from '../../app/format';
import { meQueryKey, useSession } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError, notifySuccess } from '../../components/notify';
import { PageHeader } from '../../components/PageHeader';
import { TwoFactorSection } from './TwoFactor';

const timeZones = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.(
  'timeZone',
) ?? ['Europe/Berlin', 'UTC'];

function SettingsTab() {
  const { t } = useTranslation();
  const { me } = useSession();
  const client = useQueryClient();
  const [displayName, setDisplayName] = useState(me.user.displayName);
  const [locale, setLocale] = useState(me.user.locale || '');
  const [timeZone, setTimeZone] = useState(me.user.timeZone || '');
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/api/v1/me', {
          body: {
            ...(me.user.external ? {} : { displayName }),
            locale: locale as 'de' | 'en' | '',
            timeZone,
          },
        }),
      ),
    onSuccess: (data) => {
      client.setQueryData(meQueryKey, data);
      notifySuccess(t('common.saved'));
    },
  });
  return (
    <Stack maw={480}>
      {me.user.external && <Alert color="petrol">{t('profile.managedByProvider')}</Alert>}
      <TextInput
        label={t('profile.displayName')}
        value={displayName}
        disabled={me.user.external}
        onChange={(e) => setDisplayName(e.currentTarget.value)}
      />
      <TextInput label={t('admin.email')} value={me.user.email ?? ''} disabled />
      <Select
        label={t('profile.locale')}
        value={locale}
        onChange={(v) => setLocale(v ?? '')}
        data={[
          { value: 'de', label: 'Deutsch' },
          { value: 'en', label: 'English' },
        ]}
      />
      <Select
        label={t('profile.timeZone')}
        value={timeZone}
        onChange={(v) => setTimeZone(v ?? '')}
        data={timeZones}
        searchable
      />
      <ErrorAlert error={save.error} />
      <Group>
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          {t('common.save')}
        </Button>
      </Group>
    </Stack>
  );
}

function PasswordTab() {
  const { t } = useTranslation();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const change = useMutation({
    mutationFn: () => unwrap(api.PUT('/api/v1/me/password', { body: { current, new: next } })),
    onSuccess: () => {
      setCurrent('');
      setNext('');
      notifySuccess(t('profile.passwordChanged'));
    },
  });
  return (
    <Stack maw={420}>
      <PasswordInput
        label={t('profile.currentPassword')}
        value={current}
        onChange={(e) => setCurrent(e.currentTarget.value)}
      />
      <PasswordInput
        label={t('profile.newPassword')}
        value={next}
        onChange={(e) => setNext(e.currentTarget.value)}
      />
      <ErrorAlert error={change.error} />
      <Group>
        <Button
          onClick={() => change.mutate()}
          loading={change.isPending}
          disabled={!current || next.length < 12}
        >
          {t('profile.changePassword')}
        </Button>
      </Group>
    </Stack>
  );
}

function TokensTab() {
  const { t } = useTranslation();
  const { me } = useSession();
  const client = useQueryClient();
  const tokens = useQuery({ queryKey: ['tokens'], queryFn: () => unwrap(api.GET('/api/v1/me/tokens')) });
  const maxDays = me?.tokenMaxDays ?? 0;
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [scopes, setScopes] = useState<string[]>(['read']);
  const [days, setDays] = useState<number | string>(maxDays > 0 ? Math.min(90, maxDays) : 90);
  const [created, setCreated] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/me/tokens', {
          body: { name, scopes: scopes as ('read' | 'write' | 'admin')[], expiresInDays: Number(days) || 0 },
        }),
      ),
    onSuccess: (data) => {
      setCreated(data.token);
      void client.invalidateQueries({ queryKey: ['tokens'] });
    },
  });
  const revoke = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/api/v1/me/tokens/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['tokens'] }),
    onError: notifyError,
  });
  return (
    <Stack>
      <Group justify="space-between">
        <Text c="dimmed" size="sm">
          {t('profile.tokensHelp')}
        </Text>
        <Button
          onClick={() => {
            setName('');
            setCreated(null);
            create.reset();
            setOpen(true);
          }}
        >
          {t('profile.newToken')}
        </Button>
      </Group>
      {(tokens.data ?? []).length === 0 ? (
        <Text c="dimmed">{t('profile.noTokens')}</Text>
      ) : (
        <Table>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('profile.tokenName')}</Table.Th>
              <Table.Th>{t('profile.scopes')}</Table.Th>
              <Table.Th>{t('profile.lastUsed')}</Table.Th>
              <Table.Th>{t('profile.expires')}</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(tokens.data ?? []).map((tok) => (
              <Table.Tr key={tok.id}>
                <Table.Td>
                  <Text fw={550}>{tok.name}</Text>
                  <Code>{tok.hint}</Code>
                </Table.Td>
                <Table.Td>
                  <Group gap={4}>
                    {(tok.scopes ?? []).map((s) => (
                      <Badge key={s} variant="light">
                        {s}
                      </Badge>
                    ))}
                  </Group>
                </Table.Td>
                <Table.Td>{tok.lastUsedAt ? formatRelative(tok.lastUsedAt) : t('common.never')}</Table.Td>
                <Table.Td>{tok.expiresAt ? formatDateTime(tok.expiresAt) : t('common.never')}</Table.Td>
                <Table.Td>
                  <Button
                    size="xs"
                    variant="subtle"
                    color="red"
                    onClick={() =>
                      modals.openConfirmModal({
                        title: t('profile.revoke'),
                        children: <Text size="sm">{t('profile.revokeConfirm', { name: tok.name })}</Text>,
                        labels: { confirm: t('profile.revoke'), cancel: t('common.cancel') },
                        confirmProps: { color: 'red' },
                        onConfirm: () => revoke.mutate(tok.id),
                      })
                    }
                  >
                    {t('profile.revoke')}
                  </Button>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      <Modal opened={open} onClose={() => setOpen(false)} title={t('profile.newToken')}>
        {created ? (
          <Stack>
            <Alert color="amber">{t('profile.tokenCreated')}</Alert>
            <Code block className="kogge-token">
              {created}
            </Code>
            <CopyButton value={created}>
              {({ copied, copy }) => (
                <Button onClick={copy} variant={copied ? 'light' : 'filled'}>
                  {copied ? t('common.copied') : t('common.copy')}
                </Button>
              )}
            </CopyButton>
          </Stack>
        ) : (
          <Stack>
            <TextInput
              label={t('profile.tokenName')}
              value={name}
              onChange={(e) => setName(e.currentTarget.value)}
              required
            />
            <Checkbox.Group label={t('profile.scopes')} value={scopes} onChange={setScopes}>
              <Group mt={4}>
                <Checkbox value="read" label={t('profile.scopeRead')} />
                <Checkbox value="write" label={t('profile.scopeWrite')} />
                <Checkbox value="admin" label={t('profile.scopeAdmin')} />
              </Group>
            </Checkbox.Group>
            <NumberInput
              label={
                maxDays > 0 ? t('profile.expiresInDaysMax', { max: maxDays }) : t('profile.expiresInDays')
              }
              min={maxDays > 0 ? 1 : 0}
              max={maxDays > 0 ? maxDays : 3650}
              value={days}
              onChange={setDays}
            />
            <ErrorAlert error={create.error} />
            <Group justify="flex-end">
              <Button
                onClick={() => create.mutate()}
                loading={create.isPending}
                disabled={!name || scopes.length === 0}
              >
                {t('common.create')}
              </Button>
            </Group>
          </Stack>
        )}
      </Modal>
    </Stack>
  );
}

function SessionsTab() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const sessions = useQuery({
    queryKey: ['sessions'],
    queryFn: () => unwrap(api.GET('/api/v1/me/sessions')),
  });
  const end = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/me/sessions/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['sessions'] }),
    onError: notifyError,
  });
  const endOthers = useMutation({
    mutationFn: () => unwrap(api.DELETE('/api/v1/me/sessions')),
    onSuccess: (r) => {
      notifySuccess(t('profile.endedOtherSessions', { count: r.ended }));
      void client.invalidateQueries({ queryKey: ['sessions'] });
    },
    onError: notifyError,
  });
  const others = (sessions.data ?? []).filter((s) => !s.current).length;
  return (
    <Stack>
      <Group justify="flex-end">
        <Button
          size="xs"
          variant="light"
          color="red"
          disabled={others === 0}
          loading={endOthers.isPending}
          onClick={() => endOthers.mutate()}
        >
          {t('profile.endOtherSessions')}
        </Button>
      </Group>
      <SessionsTable sessions={sessions.data ?? []} onEnd={(id) => end.mutate(id)} />
    </Stack>
  );
}

function SessionsTable({ sessions, onEnd }: { sessions: Schemas['Session'][]; onEnd: (id: string) => void }) {
  const { t } = useTranslation();
  return (
    <Table>
      <Table.Thead>
        <Table.Tr>
          <Table.Th>{t('profile.signedInWith')}</Table.Th>
          <Table.Th>{t('admin.address')}</Table.Th>
          <Table.Th>{t('profile.lastSeen')}</Table.Th>
          <Table.Th />
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {sessions.map((s) => (
          <Table.Tr key={s.id}>
            <Table.Td>
              <Text size="sm">{s.authMethod}</Text>
              <Text size="xs" c="dimmed" lineClamp={1} maw={420}>
                {s.userAgent}
              </Text>
            </Table.Td>
            <Table.Td>{s.ip}</Table.Td>
            <Table.Td>{formatRelative(s.lastSeenAt)}</Table.Td>
            <Table.Td>
              {s.current ? (
                <Badge variant="light">{t('profile.current')}</Badge>
              ) : (
                <Button size="xs" variant="subtle" color="red" onClick={() => onEnd(s.id)}>
                  {t('profile.endSession')}
                </Button>
              )}
            </Table.Td>
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  );
}

function SSHKeysTab() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const keys = useQuery({ queryKey: ['ssh-keys'], queryFn: () => unwrap(api.GET('/api/v1/me/ssh-keys')) });
  const [name, setName] = useState('');
  const [key, setKey] = useState('');
  const add = useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/me/ssh-keys', { body: { name, key } })),
    onSuccess: () => {
      setName('');
      setKey('');
      void client.invalidateQueries({ queryKey: ['ssh-keys'] });
    },
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/api/v1/me/ssh-keys/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['ssh-keys'] }),
    onError: notifyError,
  });
  return (
    <Stack>
      <Text c="dimmed" size="sm">
        {t('code.sshKeysHelp')}
      </Text>
      {(keys.data ?? []).length === 0 ? (
        <Text c="dimmed">{t('code.noSshKeys')}</Text>
      ) : (
        <Table>
          <Table.Tbody>
            {(keys.data ?? []).map((k) => (
              <Table.Tr key={k.id}>
                <Table.Td>
                  <Text fw={550}>{k.name}</Text>
                  <Code fz="xs">
                    {k.type} {k.fingerprint}
                  </Code>
                </Table.Td>
                <Table.Td>{k.lastUsed ? formatRelative(k.lastUsed) : t('common.never')}</Table.Td>
                <Table.Td>
                  <Button
                    size="xs"
                    variant="subtle"
                    color="red"
                    onClick={() =>
                      modals.openConfirmModal({
                        title: t('common.remove'),
                        children: <Text size="sm">{t('code.removeKeyConfirm', { name: k.name })}</Text>,
                        labels: { confirm: t('common.remove'), cancel: t('common.cancel') },
                        confirmProps: { color: 'red' },
                        onConfirm: () => remove.mutate(k.id),
                      })
                    }
                  >
                    {t('common.remove')}
                  </Button>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      <Stack gap="xs" maw={640}>
        <TextInput label={t('common.name')} value={name} onChange={(e) => setName(e.currentTarget.value)} />
        <Textarea
          label={t('code.publicKey')}
          value={key}
          onChange={(e) => setKey(e.currentTarget.value)}
          autosize
          minRows={2}
          placeholder="ssh-ed25519 AAAA…"
        />
        <ErrorAlert error={add.error} />
        <Group justify="flex-end">
          <Button onClick={() => add.mutate()} loading={add.isPending} disabled={!key.trim()}>
            {t('code.addSshKey')}
          </Button>
        </Group>
      </Stack>
    </Stack>
  );
}

/** E-mail delivery of notifications per category. */
function NotificationsTab() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const prefs = useQuery({
    queryKey: ['me', 'notification-preferences'],
    queryFn: () => unwrap(api.GET('/api/v1/me/notification-preferences')),
  });
  const save = useMutation({
    mutationFn: (p: { category: Category; mode: Mode }) =>
      unwrap(api.PUT('/api/v1/me/notification-preferences', { body: [p] })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['me', 'notification-preferences'] }),
  });
  const d = prefs.data;
  return (
    <Stack maw={640}>
      <ErrorAlert error={prefs.error ?? save.error} />
      {d && !d.mail && <Alert color="gray">{t('profile.mailNotConfigured')}</Alert>}
      {d && d.mail && !d.email && <Alert color="yellow">{t('profile.mailNoAddress')}</Alert>}
      {d && d.mail && d.email && (
        <Text size="sm" c="dimmed">
          {t('profile.mailTo', { email: d.email })}
        </Text>
      )}
      {d?.preferences.map((p) => (
        <Select
          key={p.category}
          label={t(`profile.mailCategory.${p.category}`)}
          value={p.mode}
          onChange={(v) => v && save.mutate({ category: p.category, mode: v as Mode })}
          data={(['immediately', 'hourly', 'daily', 'off'] as const).map((m) => ({
            value: m,
            label: t(`profile.mailMode.${m}`),
          }))}
          disabled={!d.mail}
          allowDeselect={false}
        />
      ))}
      <Text size="xs" c="dimmed">
        {t('profile.mailHelp')}
      </Text>
    </Stack>
  );
}

type Category = 'issues' | 'wiki' | 'code' | 'builds' | 'system';
type Mode = 'immediately' | 'hourly' | 'daily' | 'off';

export function ProfilePage() {
  const { t } = useTranslation();
  const { me } = useSession();
  const [search, setSearch] = useSearchParams();
  const tab = search.get('tab') ?? 'settings';
  return (
    <>
      <PageHeader title={t('profile.title')} subtitle={`${me.user.displayName} · ${me.user.username}`} />
      <Tabs
        value={tab}
        onChange={(v) => setSearch({ tab: v ?? 'settings' }, { replace: true })}
        keepMounted={false}
      >
        <Tabs.List mb="md">
          <Tabs.Tab value="settings">{t('profile.settings')}</Tabs.Tab>
          <Tabs.Tab value="notifications">{t('notifications.title')}</Tabs.Tab>
          {me.user.hasPassword && <Tabs.Tab value="password">{t('profile.security')}</Tabs.Tab>}
          <Tabs.Tab value="tokens">{t('profile.tokens')}</Tabs.Tab>
          <Tabs.Tab value="ssh-keys">{t('code.sshKeys')}</Tabs.Tab>
          <Tabs.Tab value="sessions">{t('profile.sessions')}</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="settings">
          <SettingsTab />
        </Tabs.Panel>
        <Tabs.Panel value="notifications">
          <NotificationsTab />
        </Tabs.Panel>
        <Tabs.Panel value="password">
          <Stack gap="xl">
            <PasswordTab />
            <TwoFactorSection />
          </Stack>
        </Tabs.Panel>
        <Tabs.Panel value="tokens">
          <TokensTab />
        </Tabs.Panel>
        <Tabs.Panel value="ssh-keys">
          <SSHKeysTab />
        </Tabs.Panel>
        <Tabs.Panel value="sessions">
          <SessionsTab />
        </Tabs.Panel>
      </Tabs>
    </>
  );
}
