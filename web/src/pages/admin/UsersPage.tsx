// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Badge,
  Button,
  Card,
  Group,
  Menu,
  Modal,
  PasswordInput,
  SegmentedControl,
  Stack,
  Table,
  Text,
  TextInput,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { modals } from '@mantine/modals';
import { IconDots, IconDownload, IconKey, IconLink, IconSearch, IconUserX } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Pagination } from '../../components/Pagination';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatDate, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError, notifySuccess } from '../../components/notify';
import { PageHeader } from '../../components/PageHeader';

const pageSize = 25;
type User = Schemas['User'];

export function UsersPage() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [query, setQuery] = useState('');
  const [debounced] = useDebouncedValue(query, 250);
  const [status, setStatus] = useState('');
  const [page, setPage] = useState(1);
  const users = useQuery({
    queryKey: ['admin', 'users', debounced, status, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/admin/users', {
          params: {
            query: {
              query: debounced,
              status: status as 'active' | 'disabled' | '',
              limit: pageSize,
              offset: (page - 1) * pageSize,
            },
          },
        }),
      ),
  });
  const invalidate = () => client.invalidateQueries({ queryKey: ['admin', 'users'] });

  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ username: '', displayName: '', email: '', password: '' });
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/admin/users', {
          body: { ...form, email: form.email.trim() || undefined, password: form.password || undefined },
        }),
      ),
    onSuccess: () => {
      setCreating(false);
      void invalidate();
    },
  });

  const update = useMutation({
    mutationFn: (v: { id: number; status: 'active' | 'disabled' }) =>
      unwrap(
        api.PATCH('/api/v1/admin/users/{id}', { params: { path: { id: v.id } }, body: { status: v.status } }),
      ),
    onSuccess: invalidate,
    onError: notifyError,
  });
  const endSessions = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/admin/users/{id}/sessions', { params: { path: { id } } })),
    onSuccess: () => notifySuccess(t('common.saved')),
    onError: notifyError,
  });
  const resetTwoFactor = (u: User) =>
    modals.openConfirmModal({
      title: t('admin.resetTwoFactor'),
      children: <Text size="sm">{t('admin.resetTwoFactorConfirm', { name: u.displayName })}</Text>,
      labels: { confirm: t('admin.resetTwoFactor'), cancel: t('common.cancel') },
      confirmProps: { color: 'red' },
      onConfirm: () =>
        void unwrap(api.DELETE('/api/v1/admin/users/{id}/two-factor', { params: { path: { id: u.id } } }))
          .then(() => {
            notifySuccess(t('admin.twoFactorReset'));
            void invalidate();
          })
          .catch(notifyError),
    });
  const [accessUser, setAccessUser] = useState<User | null>(null);
  const [anonUser, setAnonUser] = useState<User | null>(null);
  const [linkUser, setLinkUser] = useState<User | null>(null);
  const unlink = useMutation({
    mutationFn: (u: User) =>
      unwrap(api.DELETE('/api/v1/admin/users/{id}/sso-link', { params: { path: { id: u.id } } })),
    onSuccess: () => {
      notifySuccess(t('admin.ssoLinkRemoved'));
      void invalidate();
    },
    onError: notifyError,
  });
  const exportUser = async (u: User) => {
    try {
      const data = await unwrap(
        api.GET('/api/v1/admin/users/{id}/export', { params: { path: { id: u.id } } }),
      );
      const url = URL.createObjectURL(
        new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }),
      );
      const link = document.createElement('a');
      link.href = url;
      link.download = `kogge-user-${u.username}.json`;
      link.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      notifyError(e);
    }
  };
  const [pwUser, setPwUser] = useState<User | null>(null);
  const [password, setPassword] = useState('');
  const setPw = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/admin/users/{id}/password', {
          params: { path: { id: pwUser!.id } },
          body: { password },
        }),
      ),
    onSuccess: () => {
      setPwUser(null);
      notifySuccess(t('common.saved'));
    },
  });

  return (
    <>
      <PageHeader
        title={t('admin.users')}
        subtitle={users.data ? t('common.total', { count: users.data.total }) : undefined}
        actions={
          <Button
            onClick={() => {
              setForm({ username: '', displayName: '', email: '', password: '' });
              create.reset();
              setCreating(true);
            }}
          >
            {t('admin.newUser')}
          </Button>
        }
      />
      <DirectorySync />
      <Group mb="md">
        <TextInput
          leftSection={<IconSearch size={16} />}
          placeholder={t('common.search')}
          aria-label={t('common.search')}
          value={query}
          onChange={(e) => {
            setQuery(e.currentTarget.value);
            setPage(1);
          }}
          w={320}
        />
        <SegmentedControl
          value={status}
          onChange={(v) => {
            setStatus(v);
            setPage(1);
          }}
          data={[
            { value: '', label: t('common.all') },
            { value: 'active', label: t('admin.active') },
            { value: 'disabled', label: t('admin.disabled') },
          ]}
        />
      </Group>
      <ErrorAlert error={users.error} />
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('admin.displayName')}</Table.Th>
            <Table.Th>{t('admin.email')}</Table.Th>
            <Table.Th>{t('admin.status')}</Table.Th>
            <Table.Th>{t('admin.lastLogin')}</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(users.data?.items ?? []).map((u) => (
            <Table.Tr key={u.id}>
              <Table.Td>
                <Text fw={550}>{u.displayName}</Text>
                <Text size="xs" c="dimmed">
                  {u.username} ·{' '}
                  {u.external
                    ? t('admin.external')
                    : u.kind === 'placeholder'
                      ? t('admin.placeholder')
                      : t('admin.local')}
                  {u.twoFactor ? ` · ${t('admin.twoFactor')}` : ''}
                </Text>
                {u.ssoLink && (
                  <Badge size="xs" variant="light" color="blue" mt={4} tt="none">
                    {t('admin.ssoLinkWaiting', { username: u.ssoLink })}
                  </Badge>
                )}
              </Table.Td>
              <Table.Td>{u.email}</Table.Td>
              <Table.Td>
                <Badge color={u.status === 'active' ? 'teal' : 'gray'} variant="light">
                  {u.status === 'active'
                    ? t('admin.active')
                    : u.status === 'anonymised'
                      ? t('admin.statusAnonymised')
                      : t('admin.disabled')}
                </Badge>
              </Table.Td>
              <Table.Td>{u.lastLoginAt ? formatRelative(u.lastLoginAt) : t('common.never')}</Table.Td>
              <Table.Td>
                <Menu position="bottom-end">
                  <Menu.Target>
                    <Button variant="subtle" size="xs" px={6} aria-label={t('common.actions')}>
                      <IconDots size={16} />
                    </Button>
                  </Menu.Target>
                  <Menu.Dropdown>
                    {u.status === 'active' ? (
                      <Menu.Item color="red" onClick={() => update.mutate({ id: u.id, status: 'disabled' })}>
                        {t('admin.disable')}
                      </Menu.Item>
                    ) : (
                      <Menu.Item onClick={() => update.mutate({ id: u.id, status: 'active' })}>
                        {t('admin.enable')}
                      </Menu.Item>
                    )}
                    {!u.external && (
                      <Menu.Item
                        onClick={() => {
                          setPassword('');
                          setPw.reset();
                          setPwUser(u);
                        }}
                      >
                        {t('admin.setPassword')}
                      </Menu.Item>
                    )}
                    {u.twoFactor && (
                      <Menu.Item onClick={() => resetTwoFactor(u)}>{t('admin.resetTwoFactor')}</Menu.Item>
                    )}
                    <Menu.Item onClick={() => endSessions.mutate(u.id)}>{t('admin.endSessions')}</Menu.Item>
                    <Menu.Item leftSection={<IconKey size={14} />} onClick={() => setAccessUser(u)}>
                      {t('admin.access')}
                    </Menu.Item>
                    {linkable(u) && (
                      <Menu.Item leftSection={<IconLink size={14} />} onClick={() => setLinkUser(u)}>
                        {t('admin.ssoLink')}
                      </Menu.Item>
                    )}
                    {u.ssoLink && (
                      <Menu.Item onClick={() => unlink.mutate(u)}>{t('admin.ssoLinkRemove')}</Menu.Item>
                    )}
                    <Menu.Divider />
                    <Menu.Item leftSection={<IconDownload size={14} />} onClick={() => void exportUser(u)}>
                      {t('admin.exportData')}
                    </Menu.Item>
                    {u.status !== 'anonymised' && (
                      <Menu.Item
                        color="red"
                        leftSection={<IconUserX size={14} />}
                        onClick={() => setAnonUser(u)}
                      >
                        {t('admin.anonymise')}
                      </Menu.Item>
                    )}
                  </Menu.Dropdown>
                </Menu>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      {(users.data?.total ?? 0) > pageSize && (
        <Pagination
          mt="md"
          total={Math.ceil((users.data?.total ?? 0) / pageSize)}
          value={page}
          onChange={setPage}
        />
      )}

      <Modal opened={creating} onClose={() => setCreating(false)} title={t('admin.newUser')}>
        <Stack>
          <TextInput
            label={t('admin.username')}
            value={form.username}
            onChange={(e) => setForm({ ...form, username: e.currentTarget.value.toLowerCase() })}
            required
          />
          <TextInput
            label={t('admin.displayName')}
            value={form.displayName}
            onChange={(e) => setForm({ ...form, displayName: e.currentTarget.value })}
            required
          />
          <TextInput
            label={t('admin.email')}
            value={form.email}
            onChange={(e) => setForm({ ...form, email: e.currentTarget.value })}
          />
          <PasswordInput
            label={t('admin.passwordOptional')}
            value={form.password}
            onChange={(e) => setForm({ ...form, password: e.currentTarget.value })}
          />
          <ErrorAlert error={create.error} />
          <Group justify="flex-end">
            <Button onClick={() => create.mutate()} loading={create.isPending}>
              {t('common.create')}
            </Button>
          </Group>
        </Stack>
      </Modal>

      {accessUser && <UserAccess user={accessUser} onClose={() => setAccessUser(null)} />}
      {linkUser && (
        <SSOLink
          user={linkUser}
          onClose={() => setLinkUser(null)}
          onDone={() => {
            setLinkUser(null);
            void invalidate();
          }}
        />
      )}
      {anonUser && (
        <Anonymise
          user={anonUser}
          onClose={() => setAnonUser(null)}
          onDone={() => {
            setAnonUser(null);
            void invalidate();
          }}
        />
      )}

      <Modal
        opened={pwUser !== null}
        onClose={() => setPwUser(null)}
        title={`${t('admin.setPassword')}: ${pwUser?.displayName ?? ''}`}
      >
        <Stack>
          <PasswordInput
            label={t('profile.newPassword')}
            value={password}
            onChange={(e) => setPassword(e.currentTarget.value)}
          />
          <ErrorAlert error={setPw.error} />
          <Group justify="flex-end">
            <Button onClick={() => setPw.mutate()} loading={setPw.isPending} disabled={password.length < 12}>
              {t('common.save')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}

/**
 * Whether single sign-on may take an account over: people and placeholders (of migrations) without an identity of
 * the provider and without a local password.
 */
function linkable(u: User): boolean {
  return (
    !u.external &&
    !u.hasPassword &&
    (u.kind === 'person' || u.kind === 'placeholder') &&
    u.status !== 'anonymised'
  );
}

/**
 * Links an account to a user of the identity provider: at their first sign-in (or when the directory sync finds
 * them) they take the account over, with everything it holds. For people whose e-mail address the provider does
 * not confirm; with a confirmed one, the account is taken over without a link.
 */
function SSOLink({ user, onClose, onDone }: { user: User; onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const [username, setUsername] = useState(user.ssoLink ?? user.username);
  const link = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/admin/users/{id}/sso-link', {
          params: { path: { id: user.id } },
          body: { username: username.trim() },
        }),
      ),
    onSuccess: () => {
      notifySuccess(t('admin.ssoLinkSaved', { name: user.displayName, username: username.trim() }));
      onDone();
    },
  });
  return (
    <Modal opened onClose={onClose} title={t('admin.ssoLinkTitle', { name: user.displayName })}>
      <Stack>
        <Text size="sm">{t('admin.ssoLinkHelp')}</Text>
        <TextInput
          label={t('admin.ssoLinkUsername')}
          value={username}
          onChange={(e) => setUsername(e.currentTarget.value)}
          autoComplete="off"
          data-autofocus
        />
        <ErrorAlert error={link.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button disabled={username.trim() === ''} loading={link.isPending} onClick={() => link.mutate()}>
            {t('admin.ssoLinkSave')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** Anonymising a person (right to erasure): irreversible, so the user name is typed to confirm. */
function Anonymise({ user, onClose, onDone }: { user: User; onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const [confirm, setConfirm] = useState('');
  const anonymise = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/admin/users/{id}/anonymise', {
          params: { path: { id: user.id } },
          body: { confirm },
        }),
      ),
    onSuccess: () => {
      notifySuccess(t('admin.anonymised', { name: user.displayName }));
      onDone();
    },
  });
  return (
    <Modal opened onClose={onClose} title={t('admin.anonymiseTitle', { name: user.displayName })}>
      <Stack>
        <Text size="sm">{t('admin.anonymiseText')}</Text>
        <TextInput
          label={t('admin.anonymiseConfirm', { username: user.username })}
          value={confirm}
          onChange={(e) => setConfirm(e.currentTarget.value)}
          autoComplete="off"
          data-autofocus
        />
        <ErrorAlert error={anonymise.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            color="red"
            disabled={confirm.trim() !== user.username}
            loading={anonymise.isPending}
            onClick={() => anonymise.mutate()}
          >
            {t('admin.anonymiseTitle', { name: user.displayName })}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/**
 * What a user signs in with besides the password: sessions, access tokens and SSH keys. Administrators revoke
 * them one by one or all at once, e.g. when an account is compromised.
 */
function UserAccess({ user, onClose }: { user: User; onClose: () => void }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const key = ['admin', 'users', user.id, 'credentials'];
  const creds = useQuery({
    queryKey: key,
    queryFn: () =>
      unwrap(api.GET('/api/v1/admin/users/{id}/credentials', { params: { path: { id: user.id } } })),
  });
  const refresh = () => client.invalidateQueries({ queryKey: key });
  const revokeToken = useMutation({
    mutationFn: (tokenId: number) =>
      unwrap(
        api.DELETE('/api/v1/admin/users/{id}/tokens/{tokenId}', {
          params: { path: { id: user.id, tokenId } },
        }),
      ),
    onSuccess: refresh,
    onError: notifyError,
  });
  const deleteKey = useMutation({
    mutationFn: (keyId: number) =>
      unwrap(
        api.DELETE('/api/v1/admin/users/{id}/ssh-keys/{keyId}', { params: { path: { id: user.id, keyId } } }),
      ),
    onSuccess: refresh,
    onError: notifyError,
  });
  const revokeAll = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE('/api/v1/admin/users/{id}/credentials', { params: { path: { id: user.id } } })),
    onSuccess: () => {
      notifySuccess(t('admin.revokeAllDone', { name: user.displayName }));
      void refresh();
    },
    onError: notifyError,
  });
  const c = creds.data;
  return (
    <Modal opened onClose={onClose} title={t('admin.accessTitle', { name: user.displayName })} size="lg">
      <Stack>
        <Text size="sm" c="dimmed">
          {t('admin.accessHelp')}
        </Text>
        <ErrorAlert error={creds.error} />
        {c && (
          <>
            <Text fw={550}>{t('admin.activeSessions', { count: c.sessions })}</Text>
            <Text fw={550}>{t('profile.tokens')}</Text>
            {c.tokens.length === 0 ? (
              <Text size="sm" c="dimmed">
                {t('profile.noTokens')}
              </Text>
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
                  {c.tokens.map((tok) => (
                    <Table.Tr key={tok.id}>
                      <Table.Td>
                        <Text size="sm">{tok.name}</Text>
                        <Text size="xs" c="dimmed" ff="monospace">
                          {tok.hint}
                        </Text>
                      </Table.Td>
                      <Table.Td>{tok.scopes.join(', ')}</Table.Td>
                      <Table.Td>
                        {tok.lastUsedAt ? formatRelative(tok.lastUsedAt) : t('common.never')}
                      </Table.Td>
                      <Table.Td>{tok.expiresAt ? formatDate(tok.expiresAt) : t('common.never')}</Table.Td>
                      <Table.Td>
                        <Button
                          size="xs"
                          variant="subtle"
                          color="red"
                          loading={revokeToken.isPending && revokeToken.variables === tok.id}
                          onClick={() => revokeToken.mutate(tok.id)}
                        >
                          {t('profile.revoke')}
                        </Button>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
            <Text fw={550}>{t('code.sshKeys')}</Text>
            {c.sshKeys.length === 0 ? (
              <Text size="sm" c="dimmed">
                {t('code.noSshKeys')}
              </Text>
            ) : (
              <Table>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t('profile.tokenName')}</Table.Th>
                    <Table.Th>{t('code.fingerprint')}</Table.Th>
                    <Table.Th>{t('profile.lastUsed')}</Table.Th>
                    <Table.Th />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {c.sshKeys.map((k) => (
                    <Table.Tr key={k.id}>
                      <Table.Td>
                        <Text size="sm">{k.name}</Text>
                        <Text size="xs" c="dimmed">
                          {k.type}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <Text size="xs" ff="monospace">
                          {k.fingerprint}
                        </Text>
                      </Table.Td>
                      <Table.Td>{k.lastUsed ? formatRelative(k.lastUsed) : t('common.never')}</Table.Td>
                      <Table.Td>
                        <Button
                          size="xs"
                          variant="subtle"
                          color="red"
                          loading={deleteKey.isPending && deleteKey.variables === k.id}
                          onClick={() => deleteKey.mutate(k.id)}
                        >
                          {t('common.delete')}
                        </Button>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
            <Group justify="flex-end">
              <Button
                color="red"
                variant="light"
                loading={revokeAll.isPending}
                onClick={() =>
                  modals.openConfirmModal({
                    title: t('admin.revokeAll'),
                    children: (
                      <Text size="sm">{t('admin.revokeAllConfirm', { name: user.displayName })}</Text>
                    ),
                    labels: { confirm: t('admin.revokeAll'), cancel: t('common.cancel') },
                    confirmProps: { color: 'red' },
                    onConfirm: () => revokeAll.mutate(),
                  })
                }
              >
                {t('admin.revokeAll')}
              </Button>
            </Group>
          </>
        )}
      </Stack>
    </Modal>
  );
}

/** The directory sync: users and groups from the identity provider, offboarding included. */
function DirectorySync() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const state = useQuery({
    queryKey: ['admin', 'directory-sync'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/directory-sync')),
  });
  const run = useMutation({
    mutationFn: (force: boolean) =>
      unwrap(api.POST('/api/v1/admin/directory-sync', { params: { query: { force } } })),
    onSuccess: (r) => {
      notifySuccess(
        t('directorySync.done', { created: r.created, disabled: r.disabled, enabled: r.enabled }),
      );
    },
    onSettled: () => {
      void client.invalidateQueries({ queryKey: ['admin', 'directory-sync'] });
      void client.invalidateQueries({ queryKey: ['admin', 'users'] });
    },
  });
  if (!state.data) return null;
  if (!state.data.configured) {
    return (
      <Text size="sm" c="dimmed" mb="md">
        {t('directorySync.notConfigured')}
      </Text>
    );
  }
  const last = state.data.last;
  return (
    <Card withBorder mb="md" padding="sm">
      <Group justify="space-between" align="flex-start">
        <Stack gap={2}>
          <Text fw={500}>{t('directorySync.title')}</Text>
          {last ? (
            <Text size="sm" c={last.error ? 'red' : 'dimmed'}>
              {last.error
                ? `${formatRelative(last.finished)}: ${last.error}`
                : t('directorySync.last', {
                    when: formatRelative(last.finished),
                    users: last.users,
                    groups: last.groups,
                  })}
            </Text>
          ) : (
            <Text size="sm" c="dimmed">
              {t('directorySync.never')}
            </Text>
          )}
        </Stack>
        <Group gap="xs">
          {last?.error && (
            <Button
              size="xs"
              variant="subtle"
              color="red"
              loading={run.isPending}
              onClick={() => {
                if (window.confirm(t('directorySync.forceConfirm'))) run.mutate(true);
              }}
            >
              {t('directorySync.force')}
            </Button>
          )}
          <Button size="xs" variant="light" loading={run.isPending} onClick={() => run.mutate(false)}>
            {t('directorySync.now')}
          </Button>
        </Group>
      </Group>
      <ErrorAlert error={run.error} />
    </Card>
  );
}
