// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Avatar,
  Badge,
  Box,
  Button,
  Group,
  Modal,
  Radio,
  Select,
  Stack,
  Table,
  Tabs,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { DateTimePicker } from '@mantine/dates';
import { modals } from '@mantine/modals';
import { IconLock, IconPencil, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import dayjs from 'dayjs';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatDate, formatDateTime, formatRelative } from '../../app/format';
import { useSession } from '../../app/session';
import { DocView } from '../../components/editor/DocView';
import { Editor, isEmptyDoc, type Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError } from '../../components/notify';
import { UserBadge, formatDuration, useIssueConfig, type Issue } from './shared';

/** Comments, history and work log of an issue. */
export function IssueActivity({ issue }: { issue: Issue }) {
  const { t } = useTranslation();
  return (
    <Tabs defaultValue="comments" keepMounted={false}>
      <Tabs.List>
        <Tabs.Tab value="comments">{t('issues.comments')}</Tabs.Tab>
        <Tabs.Tab value="history">{t('issues.history')}</Tabs.Tab>
        <Tabs.Tab value="worklog">{t('issues.worklog')}</Tabs.Tab>
      </Tabs.List>
      <Tabs.Panel value="comments" pt="md">
        <Comments issue={issue} />
      </Tabs.Panel>
      <Tabs.Panel value="history" pt="md">
        <History issue={issue} />
      </Tabs.Panel>
      <Tabs.Panel value="worklog" pt="md">
        <Worklog issue={issue} />
      </Tabs.Panel>
    </Tabs>
  );
}

function initials(name: string) {
  return name
    .split(/\s+/)
    .map((p) => p[0])
    .join('')
    .slice(0, 2)
    .toUpperCase();
}

function Comments({ issue }: { issue: Issue }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const client = useQueryClient();
  const config = useIssueConfig();
  const comments = useQuery({
    queryKey: ['issue', issue.key, 'comments'],
    queryFn: () => unwrap(api.GET('/api/v1/issues/{key}/comments', { params: { path: { key: issue.key } } })),
  });
  const [draft, setDraft] = useState<Doc | null>(null);
  const [draftKey, setDraftKey] = useState(0);
  const [visibility, setVisibility] = useState<string | null>(null);
  const [editing, setEditing] = useState<number | null>(null);
  const [editDraft, setEditDraft] = useState<Doc | null>(null);
  const refresh = () => client.invalidateQueries({ queryKey: ['issue', issue.key] });
  const add = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/issues/{key}/comments', {
          params: { path: { key: issue.key } },
          body: {
            body: draft as Schemas['Doc'],
            ...(visibility ? { visibilityType: 'role' as const, visibilityId: Number(visibility) } : {}),
          },
        }),
      ),
    onSuccess: () => {
      setDraft(null);
      setDraftKey((k) => k + 1);
      void refresh();
    },
    onError: notifyError,
  });
  const save = useMutation({
    mutationFn: (id: number) =>
      unwrap(
        api.PUT('/api/v1/issues/{key}/comments/{id}', {
          params: { path: { key: issue.key, id } },
          body: { body: editDraft as Schemas['Doc'] },
        }),
      ),
    onSuccess: () => {
      setEditing(null);
      void refresh();
    },
    onError: notifyError,
  });
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/issues/{key}/comments/{id}', { params: { path: { key: issue.key, id } } })),
    onSuccess: refresh,
    onError: notifyError,
  });
  const list = comments.data ?? [];
  return (
    <Stack gap="lg">
      {comments.isSuccess && list.length === 0 && (
        <Text c="dimmed" size="sm">
          {t('issues.noComments')}
        </Text>
      )}
      {list.map((c) => (
        <Group key={c.id} align="flex-start" wrap="nowrap" gap="sm" className="kogge-comment">
          <Avatar size={32} radius="xl" color="initials" aria-hidden name={c.author?.displayName ?? '?'}>
            {initials(c.author?.displayName ?? '?')}
          </Avatar>
          <Box style={{ flex: 1, minWidth: 0 }}>
            <Group justify="space-between" wrap="nowrap" gap="xs">
              <Group gap="xs" wrap="nowrap" style={{ minWidth: 0 }}>
                <Text size="sm" fw={600} truncate>
                  {c.author?.displayName}
                </Text>
                <Tooltip label={formatDateTime(c.created)} withinPortal>
                  <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
                    {formatRelative(c.created)}
                    {c.edited ? ` · ${t('issues.edited')}` : ''}
                  </Text>
                </Tooltip>
                {c.visibilityName && (
                  <Badge
                    size="xs"
                    variant="light"
                    color="orange"
                    tt="none"
                    leftSection={<IconLock size={10} />}
                  >
                    {c.visibilityName}
                  </Badge>
                )}
              </Group>
              <Group gap={2} className="kogge-comment-actions" wrap="nowrap">
                {c.canEdit && (
                  <ActionIcon
                    size="sm"
                    variant="subtle"
                    color="gray"
                    onClick={() => {
                      setEditDraft(c.body as Doc);
                      setEditing(c.id);
                    }}
                    aria-label={t('common.edit')}
                  >
                    <IconPencil size={14} />
                  </ActionIcon>
                )}
                {c.canDelete && (
                  <ActionIcon
                    size="sm"
                    variant="subtle"
                    color="red"
                    aria-label={t('common.delete')}
                    onClick={() =>
                      modals.openConfirmModal({
                        title: t('common.delete'),
                        children: <Text size="sm">{t('issues.deleteComment')}</Text>,
                        labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                        confirmProps: { color: 'red' },
                        onConfirm: () => del.mutate(c.id),
                      })
                    }
                  >
                    <IconTrash size={14} />
                  </ActionIcon>
                )}
              </Group>
            </Group>
            {editing === c.id ? (
              <Stack gap="xs" mt={4}>
                <Editor
                  value={editDraft}
                  onChange={setEditDraft}
                  autofocus
                  onSubmit={() => save.mutate(c.id)}
                />
                <Group gap="xs">
                  <Button size="xs" onClick={() => save.mutate(c.id)} loading={save.isPending}>
                    {t('common.save')}
                  </Button>
                  <Button size="xs" variant="subtle" onClick={() => setEditing(null)}>
                    {t('common.cancel')}
                  </Button>
                </Group>
              </Stack>
            ) : (
              <Box mt={2}>
                <DocView doc={c.body as Doc} />
              </Box>
            )}
          </Box>
        </Group>
      ))}
      {issue.permissions.comment && (
        <Group align="flex-start" wrap="nowrap" gap="sm">
          <Avatar size={32} radius="xl" color="initials" aria-hidden name={me.user.displayName}>
            {initials(me.user.displayName)}
          </Avatar>
          <Stack gap="xs" style={{ flex: 1, minWidth: 0 }}>
            <Editor
              key={draftKey}
              value={draft}
              onChange={setDraft}
              placeholder={t('issues.addComment')}
              minHeight={80}
              onSubmit={() => !isEmptyDoc(draft) && add.mutate()}
            />
            <Group justify="space-between">
              <Select
                size="xs"
                w={220}
                leftSection={<IconLock size={12} />}
                placeholder={t('issues.visibleToAll')}
                clearable
                data={(config.data?.roles ?? []).map((r) => ({ value: String(r.id), label: r.name }))}
                value={visibility}
                onChange={setVisibility}
                aria-label={t('issues.commentVisibility')}
              />
              <Button
                size="xs"
                onClick={() => add.mutate()}
                loading={add.isPending}
                disabled={isEmptyDoc(draft)}
              >
                {t('issues.comment')}
              </Button>
            </Group>
          </Stack>
        </Group>
      )}
    </Stack>
  );
}

function History({ issue }: { issue: Issue }) {
  const { t } = useTranslation();
  const history = useQuery({
    queryKey: ['issue', issue.key, 'history', issue.version],
    queryFn: () => unwrap(api.GET('/api/v1/issues/{key}/history', { params: { path: { key: issue.key } } })),
  });
  if (history.isSuccess && history.data.length === 0) {
    return (
      <Text c="dimmed" size="sm">
        {t('issues.noHistory')}
      </Text>
    );
  }
  return (
    <Stack gap="md">
      {[...(history.data ?? [])].reverse().map((h) => (
        <Box key={h.id}>
          <Group gap="xs">
            <UserBadge user={h.actor} size="xs" />
            <Tooltip label={formatDateTime(h.at)} withinPortal>
              <Text size="xs" c="dimmed">
                {formatRelative(h.at)}
              </Text>
            </Tooltip>
          </Group>
          <Stack gap={2} pl={30} mt={4}>
            {h.items.map((it, i) => (
              <Text key={i} size="sm">
                <Text span fw={600}>
                  {it.name ||
                    (it.field === 'sprint'
                      ? t('agile.sprint')
                      : t(`issues.${it.field}`, { defaultValue: it.field }))}
                </Text>
                {': '}
                <Text span c="dimmed" td="line-through">
                  {it.fromString || '—'}
                </Text>
                {' → '}
                {it.toString || '—'}
              </Text>
            ))}
          </Stack>
        </Box>
      ))}
    </Stack>
  );
}

function Worklog({ issue }: { issue: Issue }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const config = useIssueConfig();
  const units = config.data?.timeTracking ?? { hoursPerDay: 8, daysPerWeek: 5 };
  const logs = useQuery({
    queryKey: ['issue', issue.key, 'worklogs', issue.version],
    queryFn: () => unwrap(api.GET('/api/v1/issues/{key}/worklogs', { params: { path: { key: issue.key } } })),
  });
  const del = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/issues/{key}/worklogs/{id}', { params: { path: { key: issue.key, id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['issue', issue.key] }),
    onError: notifyError,
  });
  if (logs.isSuccess && logs.data.length === 0) {
    return (
      <Text c="dimmed" size="sm">
        {t('issues.noWorklog')}
      </Text>
    );
  }
  return (
    <Table>
      <Table.Thead>
        <Table.Tr>
          <Table.Th>{t('issues.loggedBy')}</Table.Th>
          <Table.Th>{t('issues.started')}</Table.Th>
          <Table.Th>{t('issues.timeSpent')}</Table.Th>
          <Table.Th>{t('issues.comment')}</Table.Th>
          <Table.Th w={40} />
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {(logs.data ?? []).map((w) => (
          <Table.Tr key={w.id}>
            <Table.Td>
              <UserBadge user={w.author} size="xs" />
            </Table.Td>
            <Table.Td style={{ whiteSpace: 'nowrap' }}>{formatDate(w.started)}</Table.Td>
            <Table.Td style={{ whiteSpace: 'nowrap' }}>
              {formatDuration(w.seconds, units.hoursPerDay, units.daysPerWeek)}
            </Table.Td>
            <Table.Td>
              <Text size="sm">{w.comment}</Text>
            </Table.Td>
            <Table.Td>
              {w.canDelete && (
                <ActionIcon
                  size="sm"
                  variant="subtle"
                  color="red"
                  aria-label={t('common.delete')}
                  onClick={() =>
                    modals.openConfirmModal({
                      title: t('common.delete'),
                      children: <Text size="sm">{t('issues.deleteWorklog')}</Text>,
                      labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                      confirmProps: { color: 'red' },
                      onConfirm: () => del.mutate(w.id),
                    })
                  }
                >
                  <IconTrash size={14} />
                </ActionIcon>
              )}
            </Table.Td>
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  );
}

/** Logs time: how long, when, and how the remaining estimate changes. */
export function LogWorkModal({
  issue,
  opened,
  onClose,
}: {
  issue: Issue;
  opened: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [spent, setSpent] = useState('');
  const [started, setStarted] = useState<string | null>(null);
  const [comment, setComment] = useState('');
  const [adjust, setAdjust] = useState<'auto' | 'set'>('auto');
  const [remaining, setRemaining] = useState('');
  const close = () => {
    setSpent('');
    setStarted(null);
    setComment('');
    setAdjust('auto');
    setRemaining('');
    onClose();
  };
  const log = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/issues/{key}/worklogs', {
          params: { path: { key: issue.key } },
          body: {
            timeSpent: spent,
            comment: comment || undefined,
            started: started ? dayjs(started).toISOString() : undefined,
            remaining: adjust === 'set' ? remaining : undefined,
          },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['issue', issue.key] });
      close();
    },
  });
  return (
    <Modal opened={opened} onClose={close} title={t('issues.logWork')}>
      <Stack>
        <Group grow align="flex-start">
          <TextInput
            label={t('issues.timeSpentInput')}
            placeholder={t('issues.durationPlaceholder')}
            value={spent}
            onChange={(e) => setSpent(e.currentTarget.value)}
            data-autofocus
            required
          />
          <DateTimePicker
            label={t('issues.started')}
            placeholder={t('issues.now')}
            valueFormat="L LT"
            value={started}
            onChange={setStarted}
            clearable
            maxDate={dayjs().format('YYYY-MM-DD')}
          />
        </Group>
        <Radio.Group
          label={t('issues.remainingEstimate')}
          value={adjust}
          onChange={(v) => setAdjust(v as 'auto' | 'set')}
        >
          <Stack gap={6} mt={4}>
            <Radio value="auto" label={t('issues.reduceRemaining')} />
            <Group gap="xs" wrap="nowrap">
              <Radio value="set" label={t('issues.setRemaining')} />
              <TextInput
                size="xs"
                w={120}
                placeholder={t('issues.durationPlaceholder')}
                value={remaining}
                onChange={(e) => setRemaining(e.currentTarget.value)}
                disabled={adjust !== 'set'}
                aria-label={t('issues.setRemaining')}
              />
            </Group>
          </Stack>
        </Radio.Group>
        <TextInput
          label={t('issues.workDescription')}
          value={comment}
          onChange={(e) => setComment(e.currentTarget.value)}
          maxLength={4000}
        />
        <ErrorAlert error={log.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={close}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={() => log.mutate()}
            loading={log.isPending}
            disabled={!spent.trim() || (adjust === 'set' && !remaining.trim())}
          >
            {t('issues.logWork')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
