// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Anchor,
  Badge,
  Button,
  Card,
  Code,
  CopyButton,
  Group,
  List,
  Stack,
  Table,
  Text,
  ThemeIcon,
  Title,
  Tooltip,
} from '@mantine/core';
import { IconCheck, IconCircleCheck, IconCircleX, IconCopy, IconPlugConnected } from '@tabler/icons-react';
import { useMutation, useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatDateTime } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';

/** Single sign-on: Kogge's configuration, what to register at the identity provider, and a check of both. */
export function SSOPage() {
  const { t } = useTranslation();
  const sso = useQuery({ queryKey: ['admin', 'sso'], queryFn: () => unwrap(api.GET('/api/v1/admin/sso')) });
  const sync = useQuery({
    queryKey: ['admin', 'directory-sync'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/directory-sync')),
    enabled: !!sso.data?.sync,
  });
  const check = useMutation({ mutationFn: () => unwrap(api.POST('/api/v1/admin/sso/check')) });
  const s = sso.data;
  return (
    <>
      <PageHeader title={t('admin.ssoPage.title')} subtitle={t('admin.ssoPage.subtitle')} />
      <ErrorAlert error={sso.error} />
      {s && !s.configured && (
        <Alert color="blue" variant="light" title={t('admin.ssoPage.notConfigured')} maw={760}>
          <Text size="sm">{t('admin.ssoPage.notConfiguredText')}</Text>
          <Code block mt="xs">
            {
              'KOGGE_OIDC_ISSUER=https://sso.example.com/realms/kogge\nKOGGE_OIDC_CLIENT_ID=kogge-web\nKOGGE_OIDC_CLIENT_SECRET_FILE=/run/secrets/oidc_client_secret'
            }
          </Code>
        </Alert>
      )}
      {s?.configured && (
        <Stack gap="lg" maw={960}>
          <Card withBorder>
            <Title order={4} mb="sm">
              {t('admin.ssoPage.configuration')}
            </Title>
            <Table>
              <Table.Tbody>
                <Row label={t('admin.ssoPage.providerName')} value={s.providerName} />
                <Row label={t('admin.ssoPage.issuer')} value={<Code>{s.issuer}</Code>} />
                {s.internalUrl && (
                  <Row label={t('admin.ssoPage.internalUrl')} value={<Code>{s.internalUrl}</Code>} />
                )}
                <Row label={t('admin.ssoPage.client')} value={<Code>{s.clientId}</Code>} />
                <Row label={t('admin.ssoPage.scopes')} value={s.scopes.join(' ')} />
                <Row
                  label={t('admin.ssoPage.groupsClaim')}
                  value={s.groupsClaim ? <Code>{s.groupsClaim}</Code> : '—'}
                />
                <Row
                  label={t('admin.ssoPage.localLogin')}
                  value={
                    <Badge variant="light" color={s.localLogin ? 'orange' : 'teal'} tt="none">
                      {s.localLogin ? t('admin.ssoPage.localAllowed') : t('admin.ssoPage.localOff')}
                    </Badge>
                  }
                />
              </Table.Tbody>
            </Table>
          </Card>

          <Card withBorder>
            <Title order={4}>{t('admin.ssoPage.register')}</Title>
            <Text size="sm" c="dimmed" mb="sm">
              {t('admin.ssoPage.registerHelp')}
            </Text>
            <Table>
              <Table.Tbody>
                <Row label={t('admin.ssoPage.redirectUri')} value={<Copyable value={s.redirectUri} />} />
                <Row
                  label={t('admin.ssoPage.backchannel')}
                  value={<Copyable value={s.backchannelLogoutUri} />}
                />
                <Row
                  label={t('admin.ssoPage.postLogout')}
                  value={<Copyable value={s.postLogoutRedirect} />}
                />
              </Table.Tbody>
            </Table>
          </Card>

          <Card withBorder>
            <Group justify="space-between" mb="sm">
              <Title order={4}>{t('admin.ssoPage.check')}</Title>
              <Button
                leftSection={<IconPlugConnected size={16} />}
                onClick={() => check.mutate()}
                loading={check.isPending}
                variant="light"
              >
                {t('admin.ssoPage.runCheck')}
              </Button>
            </Group>
            <ErrorAlert error={check.error} />
            {check.data ? (
              <Stack gap={8} role="list" aria-label={t('admin.ssoPage.check')}>
                {check.data.map((c) => (
                  <Group key={c.key} gap="sm" wrap="nowrap" align="flex-start" role="listitem">
                    <ThemeIcon variant="transparent" color={c.ok ? 'teal' : 'red'} size="sm">
                      {c.ok ? (
                        <IconCircleCheck size={18} aria-hidden />
                      ) : (
                        <IconCircleX size={18} aria-hidden />
                      )}
                    </ThemeIcon>
                    <div style={{ minWidth: 0 }}>
                      <Text size="sm" fw={600}>
                        {t(`admin.ssoPage.checks.${c.key}` as 'admin.ssoPage.title')}
                        <Text span size="sm" c={c.ok ? 'teal' : 'red'} fw={500}>
                          {' · '}
                          {c.ok ? t('admin.ssoPage.ok') : t('admin.ssoPage.failed')}
                        </Text>
                      </Text>
                      <Text size="xs" c="dimmed" style={{ overflowWrap: 'anywhere' }}>
                        {c.detail}
                      </Text>
                    </div>
                  </Group>
                ))}
              </Stack>
            ) : (
              <Text size="sm" c="dimmed">
                {t('admin.ssoPage.checkHelp')}
              </Text>
            )}
          </Card>

          <Card withBorder>
            <Title order={4} mb="xs">
              {t('admin.ssoPage.sync')}
            </Title>
            {!s.sync ? (
              <Text size="sm" c="dimmed">
                {t('admin.ssoPage.syncOff')}
              </Text>
            ) : (
              <Stack gap="xs">
                <Text size="sm">
                  {t('admin.ssoPage.syncAccount')} <Code>{s.syncClientId}</Code> ·{' '}
                  <Code>{s.syncAdminUrl}</Code>
                </Text>
                {sync.data?.last ? (
                  <List size="sm" spacing={2}>
                    <List.Item>
                      {t('admin.ssoPage.syncLast', { at: formatDateTime(sync.data.last.finished) })}
                      {sync.data.last.error && (
                        <Text span c="red" size="sm">
                          {' '}
                          — {sync.data.last.error}
                        </Text>
                      )}
                    </List.Item>
                    <List.Item>
                      {t('admin.ssoPage.syncCounts', {
                        users: sync.data.last.users,
                        created: sync.data.last.created,
                        disabled: sync.data.last.disabled,
                        groups: sync.data.last.groups,
                      })}
                    </List.Item>
                  </List>
                ) : (
                  <Text size="sm" c="dimmed">
                    {t('admin.ssoPage.syncNever')}
                  </Text>
                )}
                <Anchor component={Link} to="/admin/users" size="sm">
                  {t('admin.ssoPage.syncRun')}
                </Anchor>
              </Stack>
            )}
          </Card>
        </Stack>
      )}
    </>
  );
}

function Row({ label, value }: { label: string; value: ReactNode }) {
  return (
    <Table.Tr>
      <Table.Th w={260} fw={500} c="dimmed" style={{ verticalAlign: 'top' }}>
        {label}
      </Table.Th>
      <Table.Td style={{ overflowWrap: 'anywhere' }}>{value || '—'}</Table.Td>
    </Table.Tr>
  );
}

function Copyable({ value }: { value: string }) {
  const { t } = useTranslation();
  return (
    <Group gap={6} wrap="nowrap">
      <Code style={{ overflowWrap: 'anywhere' }}>{value}</Code>
      <CopyButton value={value}>
        {({ copied, copy }) => (
          <Tooltip label={copied ? t('common.copied') : t('common.copy')}>
            <ActionIcon
              variant="subtle"
              size="sm"
              onClick={copy}
              aria-label={`${t('common.copy')}: ${value}`}
            >
              {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
            </ActionIcon>
          </Tooltip>
        )}
      </CopyButton>
    </Group>
  );
}
