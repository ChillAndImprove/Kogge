// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Button,
  Card,
  Checkbox,
  Group,
  Modal,
  SegmentedControl,
  Stack,
  Switch,
  Table,
  Tabs,
  TagsInput,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconPencil, IconPlus, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatDateTime } from '../../app/format';
import { useSession } from '../../app/session';
import { Editor, type Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { Pagination } from '../../components/Pagination';
import { GroupPicker, PagePicker, UserPicker } from '../../components/Pickers';
import { notifyError, notifySuccess } from '../../components/notify';
import { pagePath, type Space } from './hooks';
import { useSpaceContext } from './SpaceLayout';
import { WebhooksEditor } from '../../components/Webhooks';

type Grant = Schemas['SpaceGrant'];
type Holder = Schemas['Holder'];

const permissions = [
  'space.view',
  'page.edit',
  'page.delete-own',
  'page.delete',
  'page.archive',
  'comment.add',
  'comment.delete',
  'attachment.add',
  'attachment.delete',
  'page.restrict',
  'space.export',
  'space.admin',
];

function Details({ space }: { space: Space }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const { can } = useSession();
  const [name, setName] = useState(space.name);
  const [description, setDescription] = useState(space.description);
  const [categories, setCategories] = useState<string[]>(space.categories);
  const [home, setHome] = useState<number | null>(space.homePageId ?? null);
  const save = useMutation({
    mutationFn: (patch?: Schemas['SpaceUpdate']) =>
      unwrap(
        api.PATCH('/api/v1/wiki/spaces/{key}', {
          params: { path: { key: space.key } },
          body: patch ?? { name, description, categories, homePageId: home ?? undefined },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['space'] });
      notifySuccess(t('common.saved'));
    },
  });
  const del = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE('/api/v1/wiki/spaces/{key}', { params: { path: { key: space.key } } })),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['space'] });
      navigate('/wiki');
    },
    onError: notifyError,
  });
  return (
    <Stack maw={720}>
      <TextInput label={t('common.name')} value={name} onChange={(e) => setName(e.currentTarget.value)} />
      <Textarea
        label={t('common.description')}
        value={description}
        onChange={(e) => setDescription(e.currentTarget.value)}
        autosize
      />
      <TagsInput label={t('wiki.categories')} value={categories} onChange={setCategories} />
      <PagePicker label={t('wiki.homePage')} value={home} onChange={setHome} />
      <ErrorAlert error={save.error} />
      <Group>
        <Button onClick={() => save.mutate(undefined)} loading={save.isPending}>
          {t('common.save')}
        </Button>
      </Group>
      <Card withBorder mt="xl" style={{ borderColor: 'var(--mantine-color-red-4)' }}>
        <Stack>
          <Switch
            label={t('wiki.archiveSpace')}
            description={t('wiki.archiveSpaceHelp')}
            checked={space.archived}
            onChange={(e) => save.mutate({ archived: e.currentTarget.checked })}
          />
          {(can('admin') || space.type === 'personal') && (
            <Group>
              <Button
                color="red"
                variant="light"
                onClick={() =>
                  modals.openConfirmModal({
                    title: t('wiki.deleteSpace'),
                    children: <Text size="sm">{t('wiki.deleteSpaceText', { name: space.name })}</Text>,
                    labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                    confirmProps: { color: 'red' },
                    onConfirm: () => del.mutate(),
                  })
                }
              >
                {t('wiki.deleteSpace')}
              </Button>
            </Group>
          )}
        </Stack>
      </Card>
    </Stack>
  );
}

const holderKey = (h: Holder) => `${h.type}:${h.id ?? 0}`;

function Permissions({ space }: { space: Space }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const grants = useQuery({
    queryKey: ['space', space.key, 'permissions'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/wiki/spaces/{key}/permissions', { params: { path: { key: space.key } } })),
  });
  const [draft, setDraft] = useState<Grant[] | null>(null);
  const list = draft ?? grants.data ?? [];
  const holders: Holder[] = [];
  for (const g of list)
    if (!holders.some((h) => holderKey(h) === holderKey(g.holder))) holders.push(g.holder);
  const has = (h: Holder, perm: string) =>
    list.some((g) => g.permission === perm && holderKey(g.holder) === holderKey(h));
  const toggle = (h: Holder, perm: string) =>
    setDraft(
      has(h, perm)
        ? list.filter((g) => !(g.permission === perm && holderKey(g.holder) === holderKey(h)))
        : [...list, { permission: perm, holder: h }],
    );
  const [type, setType] = useState<'user' | 'group' | 'logged-in' | 'anyone'>('group');
  const [id, setId] = useState<number | null>(null);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/wiki/spaces/{key}/permissions', {
          params: { path: { key: space.key } },
          body: list.map((g) => ({
            permission: g.permission,
            holder: { type: g.holder.type, id: g.holder.id },
          })),
        }),
      ),
    onSuccess: () => {
      setDraft(null);
      void client.invalidateQueries({ queryKey: ['space'] });
      notifySuccess(t('common.saved'));
    },
  });
  const label = (h: Holder) =>
    h.type === 'logged-in'
      ? t('sharing.logged-in')
      : h.type === 'anyone'
        ? t('sharing.anyone')
        : `${h.type === 'group' ? `${t('sharing.group')}: ` : ''}${h.name ?? `#${h.id}`}`;
  return (
    <Stack>
      <Text size="sm" c="dimmed">
        {t('wiki.permissionsHelp')}
      </Text>
      <Table.ScrollContainer minWidth={900}>
        <Table withColumnBorders>
          <Table.Thead>
            <Table.Tr>
              <Table.Th />
              {permissions.map((p) => (
                <Table.Th key={p} style={{ fontSize: 11, textAlign: 'center' }}>
                  {t(`wiki.perms.${p}`)}
                </Table.Th>
              ))}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {holders.map((h) => (
              <Table.Tr key={holderKey(h)}>
                <Table.Td>
                  <Text size="sm" fw={600}>
                    {label(h)}
                  </Text>
                </Table.Td>
                {permissions.map((p) => (
                  <Table.Td key={p} style={{ textAlign: 'center' }}>
                    <Checkbox
                      checked={has(h, p)}
                      onChange={() => toggle(h, p)}
                      disabled={p === 'space.admin' && (h.type === 'logged-in' || h.type === 'anyone')}
                      aria-label={`${label(h)} ${p}`}
                      style={{ display: 'inline-block' }}
                    />
                  </Table.Td>
                ))}
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      <Group align="flex-end">
        <SegmentedControl
          value={type}
          onChange={(v) => {
            setType(v as typeof type);
            setId(null);
          }}
          data={[
            { value: 'group', label: t('sharing.group') },
            { value: 'user', label: t('sharing.user') },
            { value: 'logged-in', label: t('sharing.logged-in') },
            { value: 'anyone', label: t('sharing.anyone') },
          ]}
        />
        {type === 'user' && <UserPicker w={220} value={id} onChange={setId} aria-label={t('sharing.user')} />}
        {type === 'group' && (
          <GroupPicker w={220} value={id} onChange={setId} aria-label={t('sharing.group')} />
        )}
        <Button
          variant="light"
          disabled={(type === 'user' || type === 'group') && !id}
          onClick={() => {
            const h: Holder = type === 'user' || type === 'group' ? { type, id: id! } : { type };
            if (!holders.some((x) => holderKey(x) === holderKey(h)))
              setDraft([...list, { permission: 'space.view', holder: h }]);
            setId(null);
          }}
        >
          {t('wiki.addHolder')}
        </Button>
      </Group>
      <ErrorAlert error={save.error} />
      <Group>
        <Button onClick={() => save.mutate()} disabled={!draft} loading={save.isPending}>
          {t('common.save')}
        </Button>
        {draft && (
          <Button variant="subtle" onClick={() => setDraft(null)}>
            {t('common.cancel')}
          </Button>
        )}
      </Group>
    </Stack>
  );
}

function TemplateForm({
  space,
  template,
  onClose,
}: {
  space: Space;
  template: Schemas['Template'] | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [name, setName] = useState(template?.name ?? '');
  const [description, setDescription] = useState(template?.description ?? '');
  const [labels, setLabels] = useState<string[]>(template?.labels ?? []);
  const [body, setBody] = useState<Doc | null>((template?.body as Doc) ?? null);
  const save = useMutation({
    mutationFn: () => {
      const b = {
        name,
        description,
        labels,
        kind: (template?.kind ?? 'page') as 'page' | 'blog',
        body: (body ?? { type: 'doc', content: [] }) as Schemas['Doc'],
      };
      return template
        ? unwrap(
            api.PUT('/api/v1/wiki/templates/{id}', {
              params: { path: { id: template.id }, query: { space: space.key } },
              body: b,
            }),
          )
        : unwrap(api.POST('/api/v1/wiki/templates', { params: { query: { space: space.key } }, body: b }));
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['space', space.key, 'templates'] });
      onClose();
    },
  });
  return (
    <Modal opened onClose={onClose} title={template ? template.name : t('wiki.newTemplate')} size="xl">
      <Stack>
        <TextInput
          label={t('common.name')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
        />
        <TextInput
          label={t('common.description')}
          value={description}
          onChange={(e) => setDescription(e.currentTarget.value)}
        />
        <TagsInput label={t('wiki.labels')} value={labels} onChange={setLabels} />
        <Text size="xs" c="dimmed">
          {t('wiki.templateVariablesHelp')}
        </Text>
        <Editor value={body} onChange={setBody} minHeight={260} macros />
        <ErrorAlert error={save.error} />
        <Group justify="flex-end">
          <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name.trim()}>
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function Templates({ space }: { space: Space }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const templates = useQuery({
    queryKey: ['space', space.key, 'templates'],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/templates', { params: { query: { space: space.key } } })),
  });
  const [editing, setEditing] = useState<Schemas['Template'] | 'new' | null>(null);
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/wiki/templates/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['space', space.key, 'templates'] }),
    onError: notifyError,
  });
  return (
    <Stack>
      <Group justify="space-between">
        <Text size="sm" c="dimmed">
          {t('wiki.templatesHelp')}
        </Text>
        <Button leftSection={<IconPlus size={16} />} onClick={() => setEditing('new')}>
          {t('wiki.newTemplate')}
        </Button>
      </Group>
      <Table>
        <Table.Tbody>
          {(templates.data ?? []).map((tpl) => (
            <Table.Tr key={tpl.id}>
              <Table.Td>
                <Text fw={600} size="sm">
                  {tpl.name}
                </Text>
                <Text size="xs" c="dimmed">
                  {tpl.description}
                </Text>
              </Table.Td>
              <Table.Td>{tpl.spaceKey ? space.name : t('wiki.globalTemplate')}</Table.Td>
              <Table.Td w={80}>
                {tpl.canEdit && (
                  <Group gap={2} wrap="nowrap">
                    <ActionIcon
                      variant="subtle"
                      onClick={() => setEditing(tpl)}
                      aria-label={t('common.edit')}
                    >
                      <IconPencil size={16} />
                    </ActionIcon>
                    <ActionIcon
                      variant="subtle"
                      color="red"
                      onClick={() => del.mutate(tpl.id)}
                      aria-label={t('common.delete')}
                    >
                      <IconTrash size={16} />
                    </ActionIcon>
                  </Group>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      {editing && (
        <TemplateForm
          space={space}
          template={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
        />
      )}
    </Stack>
  );
}

/** Trashed pages per page of the trash. */
const trashPageSize = 50;

/** The trash of a space, a page at a time, the latest deleted first; the page is kept in the address. */
function Trash({ space }: { space: Space }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [params, setParams] = useSearchParams();
  const page = Math.max(1, Number(params.get('page')) || 1);
  const setPage = (p: number) => {
    const next = new URLSearchParams(params);
    if (p > 1) next.set('page', String(p));
    else next.delete('page');
    setParams(next, { replace: true });
  };
  const trash = useQuery({
    queryKey: ['space', space.key, 'trash', page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/wiki/spaces/{key}/trash', {
          params: {
            path: { key: space.key },
            query: { limit: trashPageSize, offset: (page - 1) * trashPageSize },
          },
        }),
      ),
    placeholderData: (previous) => previous,
  });
  const total = trash.data?.total ?? 0;
  const pages = Math.ceil(total / trashPageSize);
  // Restoring or purging what was left on the last page empties it: the page before it is shown instead.
  const emptied = !!trash.data && !trash.isPlaceholderData && trash.data.items.length === 0 && page > 1;
  useEffect(() => {
    if (emptied) setPage(Math.max(1, pages));
    // setPage changes with the address; the page follows the trash only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [emptied, pages]);
  const restore = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.POST('/api/v1/wiki/pages/{id}/restore', { params: { path: { id } } })),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['space', space.key, 'trash'] });
      // The page is back in the tree.
      void client.invalidateQueries({ queryKey: ['page'] });
    },
    onError: notifyError,
  });
  const purge = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/wiki/pages/{id}/purge', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['space', space.key, 'trash'] }),
    onError: notifyError,
  });
  if (trash.error) return <ErrorAlert error={trash.error} />;
  if (!trash.data) return null;
  if (total === 0) return <Text c="dimmed">{t('wiki.trashEmpty')}</Text>;
  return (
    <Stack gap="xs">
      <Text size="sm" c="dimmed" aria-live="polite">
        {t('wiki.trashCount', { count: total })}
      </Text>
      <Table>
        <Table.Tbody>
          {trash.data.items.map((p) => (
            <Table.Tr key={p.id}>
              <Table.Td>
                <Anchor component={Link} to={pagePath(space.key, p.id, p.title)} size="sm">
                  {p.title || t('wiki.untitled')}
                </Anchor>
                {p.children > 0 && (
                  <Text size="xs" c="dimmed">
                    {t('wiki.trashChildren', { count: p.children })}
                  </Text>
                )}
              </Table.Td>
              <Table.Td>
                <Text size="xs" c="dimmed">
                  {formatDateTime(p.trashed)} · {p.trashedBy?.displayName ?? ''}
                </Text>
              </Table.Td>
              <Table.Td w={220}>
                <Group gap={4}>
                  <Button size="compact-xs" variant="light" onClick={() => restore.mutate(p.id)}>
                    {t('wiki.restore')}
                  </Button>
                  {space.permissions.includes('space.admin') && (
                    <Button
                      size="compact-xs"
                      variant="subtle"
                      color="red"
                      onClick={() =>
                        modals.openConfirmModal({
                          title: t('wiki.purge'),
                          children: <Text size="sm">{t('wiki.purgeText', { title: p.title })}</Text>,
                          labels: { confirm: t('wiki.purge'), cancel: t('common.cancel') },
                          confirmProps: { color: 'red' },
                          onConfirm: () => purge.mutate(p.id),
                        })
                      }
                    >
                      {t('wiki.purge')}
                    </Button>
                  )}
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      {pages > 1 && (
        <Group justify="center" mt="xs">
          <Pagination total={pages} value={Math.min(page, pages)} onChange={setPage} />
        </Group>
      )}
    </Stack>
  );
}

export function SpaceSettings() {
  const { t } = useTranslation();
  const space = useSpaceContext();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'details';
  return (
    <>
      <PageHeader title={t('wiki.spaceSettings')} subtitle={space.name} />
      <Tabs
        value={tab}
        onChange={(v) => setParams(v && v !== 'details' ? { tab: v } : {}, { replace: true })}
        keepMounted={false}
      >
        <Tabs.List mb="lg">
          <Tabs.Tab value="details">{t('wiki.details')}</Tabs.Tab>
          <Tabs.Tab value="permissions">{t('wiki.permissions')}</Tabs.Tab>
          <Tabs.Tab value="templates">{t('wiki.templates')}</Tabs.Tab>
          <Tabs.Tab value="trash">{t('wiki.trash')}</Tabs.Tab>
          {space.type !== 'personal' && <Tabs.Tab value="webhooks">{t('webhooks.title')}</Tabs.Tab>}
        </Tabs.List>
        <Tabs.Panel value="details">
          <Details key={space.version} space={space} />
        </Tabs.Panel>
        <Tabs.Panel value="permissions">
          <Permissions space={space} />
        </Tabs.Panel>
        <Tabs.Panel value="templates">
          <Templates space={space} />
        </Tabs.Panel>
        <Tabs.Panel value="trash">
          <Title order={4} mb="sm">
            {t('wiki.trash')}
          </Title>
          <Trash space={space} />
        </Tabs.Panel>
        {space.type !== 'personal' && (
          <Tabs.Panel value="webhooks">
            <WebhooksEditor scope="space" scopeKey={space.key} />
          </Tabs.Panel>
        )}
      </Tabs>
    </>
  );
}
