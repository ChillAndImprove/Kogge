// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Card,
  Code,
  CopyButton,
  Group,
  Modal,
  MultiSelect,
  PasswordInput,
  ScrollArea,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import {
  IconCheck,
  IconCopy,
  IconList,
  IconPencil,
  IconPlus,
  IconSend,
  IconTrash,
} from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Fragment, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../api/client';
import { formatRelative } from '../app/format';
import { ErrorAlert } from './ErrorAlert';
import { notifySuccess } from './notify';

type Webhook = Schemas['Webhook'];
type Delivery = Schemas['Delivery'];
export type WebhookScope = 'instance' | 'project' | 'space' | 'code-project' | 'repository';

const modulesOf: Record<WebhookScope, string[]> = {
  instance: ['issues', 'wiki', 'code', 'builds'],
  project: ['issues'],
  space: ['wiki'],
  'code-project': ['code', 'builds'],
  repository: ['code', 'builds'],
};

const stateColor: Record<string, string> = { succeeded: 'teal', failed: 'red', pending: 'yellow' };

/** The webhooks of a scope, for its administrators. */
export function WebhooksEditor({ scope, scopeKey }: { scope: WebhookScope; scopeKey?: string }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const key = ['webhooks', scope, scopeKey ?? ''];
  const hooks = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET('/api/v1/webhooks', { params: { query: { scope, key: scopeKey } } })),
  });
  const [editing, setEditing] = useState<Webhook | 'new' | null>(null);
  const [created, setCreated] = useState<Webhook | null>(null);
  const [showing, setShowing] = useState<Webhook | null>(null);
  const invalidate = () => client.invalidateQueries({ queryKey: key });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/api/v1/webhooks/{id}', { params: { path: { id } } })),
    onSuccess: invalidate,
  });
  const ping = useMutation({
    mutationFn: (id: number) => unwrap(api.POST('/api/v1/webhooks/{id}/ping', { params: { path: { id } } })),
    onSuccess: () => {
      notifySuccess(t('webhooks.pingSent'));
      setTimeout(() => void invalidate(), 1500);
    },
  });
  return (
    <Stack>
      <Group justify="space-between">
        <Text size="sm" c="dimmed" maw={640}>
          {t('webhooks.help')}
        </Text>
        <Button variant="light" leftSection={<IconPlus size={14} />} onClick={() => setEditing('new')}>
          {t('webhooks.add')}
        </Button>
      </Group>
      {created?.secret && (
        <Alert color="teal" title={t('webhooks.created')} withCloseButton onClose={() => setCreated(null)}>
          <Stack gap="xs">
            <Text size="sm">{t('webhooks.secretOnce')}</Text>
            <Group gap="xs">
              <Code>{created.secret}</Code>
              <CopyButton value={created.secret}>
                {({ copied, copy }) => (
                  <ActionIcon variant="subtle" onClick={copy} aria-label={t('common.copy')}>
                    {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
                  </ActionIcon>
                )}
              </CopyButton>
            </Group>
          </Stack>
        </Alert>
      )}
      <ErrorAlert error={hooks.error ?? remove.error ?? ping.error} />
      {hooks.data?.length === 0 ? (
        <Text c="dimmed">{t('webhooks.none')}</Text>
      ) : (
        <Card withBorder padding="xs">
          <Table>
            <Table.Tbody>
              {(hooks.data ?? []).map((h) => (
                <Table.Tr key={h.id}>
                  <Table.Td>
                    <Group gap="xs">
                      <Text fw={500}>{h.name}</Text>
                      {!h.active && (
                        <Badge size="xs" color="gray">
                          {t('webhooks.inactive')}
                        </Badge>
                      )}
                    </Group>
                    <Text size="xs" c="dimmed" ff="monospace">
                      {h.url}
                    </Text>
                    {h.owner && (
                      <Text size="xs" c="dimmed">
                        {t('webhooks.ownerLine', { name: h.owner.displayName })}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm">
                      {h.events.length === 0
                        ? t('webhooks.allEvents')
                        : t('webhooks.eventCount', { count: h.events.length })}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    {h.lastDelivery ? (
                      <Group gap={6}>
                        <Badge size="sm" variant="light" color={stateColor[h.lastDelivery.state]}>
                          {t(`webhooks.state.${h.lastDelivery.state}`)}
                        </Badge>
                        <Text size="xs" c="dimmed">
                          {formatRelative(h.lastDelivery.created)}
                        </Text>
                      </Group>
                    ) : (
                      <Text size="xs" c="dimmed">
                        {t('webhooks.noDeliveries')}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td w={150}>
                    <Group gap={2} justify="flex-end" wrap="nowrap">
                      <Tooltip label={t('webhooks.ping')}>
                        <ActionIcon
                          variant="subtle"
                          onClick={() => ping.mutate(h.id)}
                          aria-label={t('webhooks.ping')}
                        >
                          <IconSend size={16} />
                        </ActionIcon>
                      </Tooltip>
                      <Tooltip label={t('webhooks.deliveries')}>
                        <ActionIcon
                          variant="subtle"
                          onClick={() => setShowing(h)}
                          aria-label={t('webhooks.deliveries')}
                        >
                          <IconList size={16} />
                        </ActionIcon>
                      </Tooltip>
                      <ActionIcon
                        variant="subtle"
                        onClick={() => setEditing(h)}
                        aria-label={t('common.edit')}
                      >
                        <IconPencil size={16} />
                      </ActionIcon>
                      <ActionIcon
                        variant="subtle"
                        color="red"
                        onClick={() => remove.mutate(h.id)}
                        aria-label={t('common.delete')}
                      >
                        <IconTrash size={16} />
                      </ActionIcon>
                    </Group>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Card>
      )}
      {editing && (
        <WebhookModal
          scope={scope}
          scopeKey={scopeKey}
          hook={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(h) => {
            setEditing(null);
            if (h.secret) setCreated(h);
            void invalidate();
          }}
        />
      )}
      {showing && <DeliveriesModal hook={showing} onClose={() => setShowing(null)} />}
    </Stack>
  );
}

function WebhookModal({
  scope,
  scopeKey,
  hook,
  onClose,
  onSaved,
}: {
  scope: WebhookScope;
  scopeKey?: string;
  hook: Webhook | null;
  onClose: () => void;
  onSaved: (h: Webhook) => void;
}) {
  const { t } = useTranslation();
  const catalogue = useQuery({
    queryKey: ['webhooks', 'events'],
    queryFn: () => unwrap(api.GET('/api/v1/webhooks/events')),
  });
  const [name, setName] = useState(hook?.name ?? '');
  const [url, setUrl] = useState(hook?.url ?? 'https://');
  const [events, setEvents] = useState<string[]>(hook?.events ?? []);
  const [active, setActive] = useState(hook?.active ?? true);
  const [secret, setSecret] = useState('');
  const groups = modulesOf[scope].map((m) => ({
    group: t(`webhooks.module.${m}` as 'webhooks.module.issues'),
    items: (catalogue.data ?? []).filter((e) => e.module === m).map((e) => e.name),
  }));
  const save = useMutation({
    mutationFn: () => {
      const body = { name, url, events, active, secret: secret || undefined };
      return unwrap(
        hook
          ? api.PUT('/api/v1/webhooks/{id}', { params: { path: { id: hook.id } }, body })
          : api.POST('/api/v1/webhooks', { params: { query: { scope, key: scopeKey } }, body }),
      );
    },
    onSuccess: onSaved,
  });
  return (
    <Modal opened onClose={onClose} title={hook ? t('webhooks.edit') : t('webhooks.add')} size="lg">
      <Stack>
        <TextInput
          label={t('common.name')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
        />
        <TextInput
          label={t('webhooks.url')}
          description={hook && hook.url !== url.trim() ? t('webhooks.urlOwnerChange') : undefined}
          value={url}
          onChange={(e) => setUrl(e.currentTarget.value)}
          required
          ff="monospace"
        />
        <MultiSelect
          label={t('webhooks.events')}
          description={t('webhooks.eventsHelp')}
          data={groups}
          value={events}
          onChange={setEvents}
          searchable
          clearable
          placeholder={events.length === 0 ? t('webhooks.allEvents') : undefined}
        />
        <PasswordInput
          label={t('webhooks.secret')}
          description={hook ? t('webhooks.secretKeep') : t('webhooks.secretHelp')}
          value={secret}
          onChange={(e) => setSecret(e.currentTarget.value)}
        />
        <Switch
          label={t('webhooks.active')}
          checked={active}
          onChange={(e) => setActive(e.currentTarget.checked)}
        />
        <ErrorAlert error={save.error ?? catalogue.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name || !url}>
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function DeliveriesModal({ hook, onClose }: { hook: Webhook; onClose: () => void }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [open, setOpen] = useState<number | null>(null);
  const list = useQuery({
    queryKey: ['webhooks', 'deliveries', hook.id],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/webhooks/{id}/deliveries', {
          params: { path: { id: hook.id }, query: { limit: 50 } },
        }),
      ),
    refetchInterval: 5000,
  });
  const redeliver = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.POST('/api/v1/webhooks/deliveries/{id}/redeliver', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['webhooks'] }),
  });
  return (
    <Modal opened onClose={onClose} title={t('webhooks.deliveriesOf', { name: hook.name })} size="xl">
      <ErrorAlert error={list.error ?? redeliver.error} />
      {list.data?.length === 0 && <Text c="dimmed">{t('webhooks.noDeliveries')}</Text>}
      <ScrollArea.Autosize mah="70vh">
        <Table verticalSpacing={4}>
          <Table.Tbody>
            {(list.data ?? []).map((d: Delivery) => (
              <Fragment key={d.id}>
                <Table.Tr onClick={() => setOpen(open === d.id ? null : d.id)} style={{ cursor: 'pointer' }}>
                  <Table.Td miw={120}>
                    <Badge size="sm" variant="light" color={stateColor[d.state]}>
                      {t(`webhooks.state.${d.state}`)}
                    </Badge>
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm" ff="monospace">
                      {d.event}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm">
                      {d.responseStatus ?? '—'} · {t('webhooks.attempts', { count: d.attempts })}
                      {d.durationMs != null ? ` · ${d.durationMs} ms` : ''}
                    </Text>
                    {d.error && (
                      <Text size="xs" c="red">
                        {d.error}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td>
                    <Text size="xs" c="dimmed">
                      {formatRelative(d.created)}
                    </Text>
                  </Table.Td>
                  <Table.Td w={120}>
                    <Button
                      size="compact-xs"
                      variant="light"
                      onClick={(e) => {
                        e.stopPropagation();
                        redeliver.mutate(d.id);
                      }}
                    >
                      {t('webhooks.redeliver')}
                    </Button>
                  </Table.Td>
                </Table.Tr>
                {open === d.id && (
                  <Table.Tr>
                    <Table.Td colSpan={5}>
                      <Text size="xs" fw={600}>
                        {t('webhooks.payload')}
                      </Text>
                      {d.payloadHidden ? (
                        <Text size="sm" c="dimmed">
                          {t('webhooks.payloadHidden')}
                        </Text>
                      ) : (
                        <Code block>{JSON.stringify(d.payload, null, 2)}</Code>
                      )}
                      {d.responseExcerpt && (
                        <>
                          <Text size="xs" fw={600} mt="xs">
                            {t('webhooks.response')}
                          </Text>
                          <Code block>{d.responseExcerpt}</Code>
                        </>
                      )}
                    </Table.Td>
                  </Table.Tr>
                )}
              </Fragment>
            ))}
          </Table.Tbody>
        </Table>
      </ScrollArea.Autosize>
    </Modal>
  );
}
