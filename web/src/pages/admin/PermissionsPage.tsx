// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Badge, Button, Card, Group, Modal, Select, Stack, Text, Title } from '@mantine/core';
import { IconX } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { meQueryKey } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError } from '../../components/notify';
import { PageHeader } from '../../components/PageHeader';
import { GroupPicker, UserPicker } from '../../components/Pickers';

type Grant = Schemas['Grant'];
type PrincipalType = Grant['principalType'];

export function PermissionsPage() {
  const { t, i18n } = useTranslation();
  const client = useQueryClient();
  const names = i18n.getResource(i18n.language, 'translation', 'permissionNames') as
    Record<string, [string, string]> | undefined;
  const perms = useQuery({
    queryKey: ['admin', 'global-permissions'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/global-permissions')),
  });
  const refresh = () => {
    void client.invalidateQueries({ queryKey: ['admin', 'global-permissions'] });
    void client.invalidateQueries({ queryKey: meQueryKey });
  };
  const [granting, setGranting] = useState<string | null>(null);
  const [ptype, setPtype] = useState<PrincipalType>('group');
  const [principal, setPrincipal] = useState<number | null>(null);
  const grant = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/admin/global-permissions', {
          body: { permission: granting!, principalType: ptype, principalId: principal ?? 0 },
        }),
      ),
    onSuccess: () => {
      setGranting(null);
      refresh();
    },
  });
  const revoke = useMutation({
    mutationFn: (g: Grant) =>
      unwrap(
        api.DELETE('/api/v1/admin/global-permissions', {
          params: {
            query: {
              permission: g.permission,
              principalType: g.principalType,
              principalId: g.principalId ?? 0,
            },
          },
        }),
      ),
    onSuccess: refresh,
    onError: notifyError,
  });
  const label = (g: Grant) => {
    switch (g.principalType) {
      case 'logged-in':
        return t('admin.principalLoggedIn');
      case 'anyone':
        return t('admin.principalAnyone');
      default:
        return g.principalName || `#${g.principalId}`;
    }
  };
  return (
    <>
      <PageHeader title={t('admin.permissions')} />
      <ErrorAlert error={perms.error} />
      <Stack>
        {(perms.data ?? []).map((p) => (
          <Card withBorder key={p.key}>
            <Group justify="space-between" align="flex-start">
              <div>
                <Title order={5}>{names?.[p.key.replace('.', '_')]?.[0] ?? p.name}</Title>
                <Text size="sm" c="dimmed">
                  {names?.[p.key.replace('.', '_')]?.[1] ?? p.description}
                </Text>
              </div>
              <Button
                size="xs"
                variant="light"
                onClick={() => {
                  setPtype('group');
                  setPrincipal(null);
                  grant.reset();
                  setGranting(p.key);
                }}
              >
                {t('admin.grant')}
              </Button>
            </Group>
            <Group gap="xs" mt="sm">
              {(p.grants ?? []).length === 0 && (
                <Text size="sm" c="dimmed">
                  {t('common.none')}
                </Text>
              )}
              {(p.grants ?? []).map((g) => (
                <Badge
                  key={`${g.principalType}:${g.principalId}`}
                  tt="none"
                  variant="light"
                  color={
                    g.principalType === 'user' ? 'petrol' : g.principalType === 'group' ? 'grape' : 'amber'
                  }
                  rightSection={
                    <ActionIcon
                      size="xs"
                      variant="transparent"
                      onClick={() => revoke.mutate(g)}
                      aria-label={t('common.remove')}
                    >
                      <IconX size={12} />
                    </ActionIcon>
                  }
                >
                  {label(g)}
                </Badge>
              ))}
            </Group>
          </Card>
        ))}
      </Stack>
      <Modal opened={granting !== null} onClose={() => setGranting(null)} title={t('admin.grant')}>
        <Stack>
          <Select
            label={t('admin.grantTo')}
            value={ptype}
            onChange={(v) => {
              setPtype((v ?? 'group') as PrincipalType);
              setPrincipal(null);
            }}
            data={[
              { value: 'group', label: t('admin.principalGroup') },
              { value: 'user', label: t('admin.principalUser') },
              { value: 'logged-in', label: t('admin.principalLoggedIn') },
              { value: 'anyone', label: t('admin.principalAnyone') },
            ]}
          />
          {ptype === 'user' && (
            <UserPicker label={t('admin.principalUser')} value={principal} onChange={setPrincipal} />
          )}
          {ptype === 'group' && (
            <GroupPicker label={t('admin.principalGroup')} value={principal} onChange={setPrincipal} />
          )}
          <ErrorAlert error={grant.error} />
          <Group justify="flex-end">
            <Button
              onClick={() => grant.mutate()}
              loading={grant.isPending}
              disabled={(ptype === 'user' || ptype === 'group') && principal === null}
            >
              {t('admin.grant')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}
