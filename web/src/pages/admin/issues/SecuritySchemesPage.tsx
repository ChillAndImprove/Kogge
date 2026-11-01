// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Button,
  Card,
  Group,
  Radio,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconPlus, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../../api/client';
import { ErrorAlert } from '../../../components/ErrorAlert';
import { PageHeader } from '../../../components/PageHeader';
import { notifyError, notifySuccess } from '../../../components/notify';
import { AddHolder, HolderBadge } from './Holders';

type Scheme = Schemas['SecurityScheme'];
type Level = Schemas['SecurityLevelInput'];

const memberTypes = ['role', 'group', 'user', 'reporter', 'assignee', 'project-lead'];

function useSecuritySchemes() {
  return useQuery({
    queryKey: ['issue-config', 'security-schemes'],
    queryFn: () => unwrap(api.GET('/api/v1/issue-config/security-schemes')),
  });
}

export function SecuritySchemesPage() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const schemes = useSecuritySchemes();
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/issue-config/security-schemes/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['issue-config'] }),
    onError: notifyError,
  });
  return (
    <>
      <PageHeader
        title={t('issueAdmin.securitySchemes')}
        subtitle={t('issueAdmin.securitySchemesHelp')}
        actions={
          <Button
            leftSection={<IconPlus size={16} />}
            component={Link}
            to="/issues/admin/security-schemes/new"
          >
            {t('common.create')}
          </Button>
        }
      />
      <ErrorAlert error={schemes.error} />
      {(schemes.data ?? []).length === 0 ? (
        <Text c="dimmed">{t('issueAdmin.noSecuritySchemes')}</Text>
      ) : (
        <Table>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.name')}</Table.Th>
              <Table.Th>{t('issueAdmin.levelsTitle')}</Table.Th>
              <Table.Th w={50} />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(schemes.data ?? []).map((s) => (
              <Table.Tr key={s.id}>
                <Table.Td>
                  <Anchor component={Link} to={`/issues/admin/security-schemes/${s.id}`} fw={600} size="sm">
                    {s.name}
                  </Anchor>
                  <Text size="xs" c="dimmed" lineClamp={1}>
                    {s.description}
                  </Text>
                </Table.Td>
                <Table.Td>{s.levels.map((l) => l.name).join(', ')}</Table.Td>
                <Table.Td>
                  <ActionIcon
                    variant="subtle"
                    color="red"
                    aria-label={t('common.delete')}
                    onClick={() =>
                      modals.openConfirmModal({
                        title: t('common.delete'),
                        children: <Text size="sm">{t('issueAdmin.deleteConfirm', { name: s.name })}</Text>,
                        labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                        confirmProps: { color: 'red' },
                        onConfirm: () => del.mutate(s.id),
                      })
                    }
                  >
                    <IconTrash size={16} />
                  </ActionIcon>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </>
  );
}

function SchemeEditor({ scheme }: { scheme: Scheme | null }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState(scheme?.name ?? '');
  const [description, setDescription] = useState(scheme?.description ?? '');
  // New levels get negative temporary ids so the default level can point at them before they are saved.
  const [levels, setLevels] = useState<(Level & { key: number })[]>(
    (scheme?.levels ?? []).map((l) => ({ ...l, key: l.id })),
  );
  const [defaultKey, setDefaultKey] = useState<number | null>(scheme?.defaultLevelId ?? null);
  const setLevel = (key: number, patch: Partial<Level>) =>
    setLevels((ls) => ls.map((l) => (l.key === key ? { ...l, ...patch } : l)));
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        description,
        defaultLevelId: defaultKey ?? undefined,
        levels: levels.map(({ key, name, description, members }) => ({
          id: key,
          name,
          description,
          members: members.map(({ holderType, holderId }) => ({ holderType, holderId })),
        })),
      };
      return scheme
        ? unwrap(
            api.PUT('/api/v1/issue-config/security-schemes/{id}', {
              params: { path: { id: scheme.id } },
              body,
            }),
          )
        : unwrap(api.POST('/api/v1/issue-config/security-schemes', { body }));
    },
    onSuccess: (r) => {
      void client.invalidateQueries({ queryKey: ['issue-config'] });
      notifySuccess(t('common.saved'));
      if (!scheme) navigate(`/issues/admin/security-schemes/${r.id}`, { replace: true });
    },
  });
  let temp = Math.min(0, ...levels.map((l) => l.key));
  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <Stack gap={4}>
          <Anchor component={Link} to="/issues/admin/security-schemes" size="sm">
            ← {t('issueAdmin.securitySchemes')}
          </Anchor>
          <Title order={2}>{name || t('issueAdmin.newScheme')}</Title>
        </Stack>
        <Button
          onClick={() => save.mutate()}
          loading={save.isPending}
          disabled={!name.trim() || levels.some((l) => !l.name.trim())}
        >
          {t('common.save')}
        </Button>
      </Group>
      <ErrorAlert error={save.error} />
      <Card withBorder>
        <Group grow align="flex-start">
          <TextInput
            label={t('common.name')}
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
            required
          />
          <Textarea
            label={t('common.description')}
            value={description}
            onChange={(e) => setDescription(e.currentTarget.value)}
            autosize
          />
        </Group>
      </Card>
      <Group justify="space-between">
        <Title order={4}>{t('issueAdmin.levelsTitle')}</Title>
        <Button
          size="xs"
          variant="light"
          leftSection={<IconPlus size={14} />}
          onClick={() =>
            setLevels((ls) => [...ls, { key: --temp, id: undefined, name: '', description: '', members: [] }])
          }
        >
          {t('issueAdmin.addLevel')}
        </Button>
      </Group>
      <Radio.Group
        value={defaultKey === null ? '' : String(defaultKey)}
        onChange={(v) => setDefaultKey(v ? Number(v) : null)}
      >
        <Stack>
          {levels.map((l) => (
            <Card key={l.key} withBorder>
              <Group align="flex-start" wrap="nowrap">
                <Stack style={{ flex: 1 }} gap="xs">
                  <Group grow align="flex-start">
                    <TextInput
                      size="xs"
                      label={t('common.name')}
                      value={l.name}
                      onChange={(e) => setLevel(l.key, { name: e.currentTarget.value })}
                      required
                    />
                    <TextInput
                      size="xs"
                      label={t('common.description')}
                      value={l.description ?? ''}
                      onChange={(e) => setLevel(l.key, { description: e.currentTarget.value })}
                    />
                  </Group>
                  <Group gap={6}>
                    <Text size="xs" c="dimmed">
                      {t('issueAdmin.levelMembers')}:
                    </Text>
                    {l.members.map((m, i) => (
                      <HolderBadge
                        key={`${m.holderType}-${m.holderId ?? 0}`}
                        holder={{ type: m.holderType, id: m.holderId, name: m.holderName }}
                        onRemove={() => setLevel(l.key, { members: l.members.filter((_, j) => j !== i) })}
                      />
                    ))}
                    <AddHolder
                      types={memberTypes}
                      onAdd={(h) =>
                        setLevel(l.key, {
                          members: [
                            ...l.members,
                            {
                              holderType: h.type as Level['members'][number]['holderType'],
                              holderId: h.id,
                              holderName: h.name,
                            },
                          ],
                        })
                      }
                    />
                  </Group>
                </Stack>
                <Stack gap="xs" align="flex-end">
                  <Radio value={String(l.key)} label={t('issueAdmin.defaultLevel')} size="xs" />
                  <ActionIcon
                    variant="subtle"
                    color="red"
                    onClick={() => setLevels((ls) => ls.filter((x) => x.key !== l.key))}
                    aria-label={t('common.delete')}
                  >
                    <IconTrash size={14} />
                  </ActionIcon>
                </Stack>
              </Group>
            </Card>
          ))}
        </Stack>
      </Radio.Group>
      {defaultKey !== null && (
        <Group>
          <Button size="xs" variant="subtle" onClick={() => setDefaultKey(null)}>
            {t('issueAdmin.noDefaultLevel')}
          </Button>
        </Group>
      )}
    </Stack>
  );
}

export function SecuritySchemeEditorPage() {
  const { id } = useParams();
  const schemes = useSecuritySchemes();
  if (id === 'new') return <SchemeEditor scheme={null} />;
  if (schemes.error) return <ErrorAlert error={schemes.error} />;
  const s = schemes.data?.find((x) => String(x.id) === id);
  if (!s) return null;
  return <SchemeEditor key={s.id} scheme={s} />;
}
