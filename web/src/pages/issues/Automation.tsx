// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Badge,
  Button,
  Card,
  Collapse,
  Group,
  Modal,
  MultiSelect,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  Textarea,
  TextInput,
} from '@mantine/core';
import {
  IconChevronDown,
  IconChevronRight,
  IconPencil,
  IconPlus,
  IconTrash,
  IconWand,
} from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { UserPicker } from '../../components/Pickers';

type Rule = Schemas['LinksRule'];
type Project = Schemas['ProjectView'];
type Status = Schemas['Status'];

const triggers = [
  'branch.created',
  'commit.pushed',
  'pull-request.opened',
  'pull-request.merged',
  'pull-request.declined',
  'build.failed',
  'deployment.succeeded',
] as const;
const actions = ['transition', 'comment', 'assign', 'fix-version'] as const;
type Trigger = (typeof triggers)[number];
type Action = (typeof actions)[number];

const resultColor: Record<string, string> = { done: 'teal', skipped: 'gray', failed: 'red' };

/** Automation rules of a project. */
export function Automation({ project }: { project: Project }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const key = ['project', project.key, 'automation'];
  const rules = useQuery({
    queryKey: key,
    queryFn: () =>
      unwrap(api.GET('/api/v1/projects/{key}/automation', { params: { path: { key: project.key } } })),
  });
  const statuses = useQuery({
    queryKey: ['project', project.key, 'statuses'],
    queryFn: () =>
      unwrap(api.GET('/api/v1/projects/{key}/statuses', { params: { path: { key: project.key } } })),
  });
  const [editing, setEditing] = useState<Rule | 'new' | null>(null);
  const invalidate = () => client.invalidateQueries({ queryKey: key });
  const save = useMutation({
    mutationFn: (r: Rule) =>
      unwrap(
        api.PUT('/api/v1/projects/{key}/automation/{id}', {
          params: { path: { key: project.key, id: r.id } },
          body: { ...r },
        }),
      ),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: (id: number) =>
      unwrap(
        api.DELETE('/api/v1/projects/{key}/automation/{id}', { params: { path: { key: project.key, id } } }),
      ),
    onSuccess: invalidate,
  });
  const examples = useMutation({
    mutationFn: async () => {
      const list = statuses.data ?? [];
      const progress = list.find((s) => s.category === 'inprogress');
      const done = list.find((s) => s.category === 'done');
      const made = [];
      for (const [name, trigger, status] of [
        [t('automation.exampleOpened'), 'pull-request.opened', progress],
        [t('automation.exampleMerged'), 'pull-request.merged', done],
      ] as const) {
        if (!status) continue;
        made.push(
          await unwrap(
            api.POST('/api/v1/projects/{key}/automation', {
              params: { path: { key: project.key } },
              body: {
                name,
                trigger,
                action: 'transition',
                params: { status: status.id },
                issueTypes: [],
                statuses: [],
                active: true,
              },
            }),
          ),
        );
      }
      return made;
    },
    onSuccess: invalidate,
  });
  const statusName = (id: unknown) =>
    (statuses.data ?? []).find((s) => s.id === Number(id))?.name ?? String(id);
  const describe = (r: Rule) => {
    switch (r.action) {
      case 'transition':
        return t('automation.describe.transition', { status: statusName(r.params.status) });
      case 'assign':
        return t(
          `automation.describe.assign.${String(r.params.to ?? 'user')}` as 'automation.describe.assign.user',
        );
      case 'fix-version': {
        const v = project.versions.find((x) => x.id === Number(r.params.version));
        return t('automation.describe.fixVersion', { version: v?.name ?? t('automation.nextVersion') });
      }
      default:
        return t('automation.describe.comment');
    }
  };
  return (
    <Stack>
      <Group justify="space-between">
        <Text size="sm" c="dimmed" maw={700}>
          {t('automation.help')}
        </Text>
        <Group gap="xs">
          {(rules.data ?? []).length === 0 && (
            <Button
              variant="default"
              leftSection={<IconWand size={14} />}
              onClick={() => examples.mutate()}
              loading={examples.isPending}
            >
              {t('automation.addExamples')}
            </Button>
          )}
          <Button variant="light" leftSection={<IconPlus size={14} />} onClick={() => setEditing('new')}>
            {t('automation.add')}
          </Button>
        </Group>
      </Group>
      <ErrorAlert error={rules.error ?? save.error ?? remove.error ?? examples.error} />
      {rules.data?.length === 0 && <Text c="dimmed">{t('automation.none')}</Text>}
      {(rules.data ?? []).map((r) => (
        <RuleCard
          key={r.id}
          rule={r}
          description={`${t(`automation.trigger.${r.trigger}`)} → ${describe(r)}`}
          onToggle={(active) => save.mutate({ ...r, active })}
          onEdit={() => setEditing(r)}
          onDelete={() => remove.mutate(r.id)}
        />
      ))}
      {editing && (
        <RuleModal
          project={project}
          statuses={statuses.data ?? []}
          rule={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void invalidate();
          }}
        />
      )}
    </Stack>
  );
}

function RuleCard({
  rule,
  description,
  onToggle,
  onEdit,
  onDelete,
}: {
  rule: Rule;
  description: string;
  onToggle: (active: boolean) => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const runs = rule.lastRuns ?? [];
  return (
    <Card withBorder padding="sm">
      <Group justify="space-between" wrap="nowrap">
        <Stack gap={2}>
          <Text fw={500}>{rule.name}</Text>
          <Text size="sm" c="dimmed">
            {description}
            {rule.environment ? ` (${rule.environment})` : ''}
          </Text>
          {rule.owner && (
            <Text size="xs" c="dimmed">
              {t('automation.ownerLine', { name: rule.owner.displayName })}
            </Text>
          )}
        </Stack>
        <Group gap="xs" wrap="nowrap">
          <Switch
            checked={rule.active}
            onChange={(e) => onToggle(e.currentTarget.checked)}
            aria-label={t('automation.active')}
          />
          <ActionIcon variant="subtle" onClick={onEdit} aria-label={t('common.edit')}>
            <IconPencil size={16} />
          </ActionIcon>
          <ActionIcon variant="subtle" color="red" onClick={onDelete} aria-label={t('common.delete')}>
            <IconTrash size={16} />
          </ActionIcon>
        </Group>
      </Group>
      {runs.length > 0 && (
        <>
          <Button
            variant="subtle"
            size="compact-xs"
            mt={4}
            leftSection={open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
            onClick={() => setOpen(!open)}
          >
            {t('automation.runs', { count: runs.length })}
          </Button>
          <Collapse expanded={open}>
            <Table verticalSpacing={2} mt={4}>
              <Table.Tbody>
                {runs.map((run, i) => (
                  <Table.Tr key={i}>
                    <Table.Td w={90}>
                      <Badge size="xs" variant="light" color={resultColor[run.result]}>
                        {t(`automation.result.${run.result}`)}
                      </Badge>
                    </Table.Td>
                    <Table.Td w={100}>
                      <Text size="xs" ff="monospace">
                        {run.issueKey}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Text size="xs">{run.message}</Text>
                    </Table.Td>
                    <Table.Td w={120}>
                      <Text size="xs" c="dimmed">
                        {formatRelative(run.at)}
                      </Text>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Collapse>
        </>
      )}
    </Card>
  );
}

function RuleModal({
  project,
  statuses,
  rule,
  onClose,
  onSaved,
}: {
  project: Project;
  statuses: Status[];
  rule: Rule | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(rule?.name ?? '');
  const [trigger, setTrigger] = useState<Trigger>((rule?.trigger as Trigger) ?? 'pull-request.merged');
  const [environment, setEnvironment] = useState(rule?.environment ?? '');
  const [issueTypes, setIssueTypes] = useState<string[]>((rule?.issueTypes ?? []).map(String));
  const [inStatuses, setInStatuses] = useState<string[]>((rule?.statuses ?? []).map(String));
  const [action, setAction] = useState<Action>((rule?.action as Action) ?? 'transition');
  const [params, setParams] = useState<Record<string, unknown>>(rule?.params ?? {});
  const set = (k: string, v: unknown) => setParams((p) => ({ ...p, [k]: v }));
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        trigger,
        environment: trigger === 'deployment.succeeded' ? environment : '',
        issueTypes: issueTypes.map(Number),
        statuses: inStatuses.map(Number),
        action,
        params,
        active: rule?.active ?? true,
      };
      return unwrap(
        rule
          ? api.PUT('/api/v1/projects/{key}/automation/{id}', {
              params: { path: { key: project.key, id: rule.id } },
              body,
            })
          : api.POST('/api/v1/projects/{key}/automation', { params: { path: { key: project.key } }, body }),
      );
    },
    onSuccess: onSaved,
  });
  const statusData = statuses.map((s) => ({ value: String(s.id), label: s.name }));
  return (
    <Modal opened onClose={onClose} title={rule ? t('automation.edit') : t('automation.add')} size="lg">
      <Stack>
        <TextInput
          label={t('common.name')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
        />
        <Select
          label={t('automation.when')}
          data={triggers.map((x) => ({ value: x, label: t(`automation.trigger.${x}`) }))}
          value={trigger}
          onChange={(v) => v && setTrigger(v as Trigger)}
          allowDeselect={false}
        />
        {trigger === 'deployment.succeeded' && (
          <TextInput
            label={t('automation.environment')}
            description={t('automation.environmentHelp')}
            value={environment}
            onChange={(e) => setEnvironment(e.currentTarget.value)}
          />
        )}
        <MultiSelect
          label={t('automation.onlyTypes')}
          placeholder={issueTypes.length === 0 ? t('automation.any') : undefined}
          data={project.issueTypes.map((it) => ({ value: String(it.id), label: it.name }))}
          value={issueTypes}
          onChange={setIssueTypes}
          clearable
        />
        <MultiSelect
          label={t('automation.onlyStatuses')}
          placeholder={inStatuses.length === 0 ? t('automation.any') : undefined}
          data={statusData}
          value={inStatuses}
          onChange={setInStatuses}
          clearable
        />
        <Select
          label={t('automation.then')}
          data={actions.map((x) => ({ value: x, label: t(`automation.action.${x}`) }))}
          value={action}
          onChange={(v) => {
            if (!v) return;
            setAction(v as Action);
            setParams(v === 'assign' ? { to: 'author' } : {});
          }}
          allowDeselect={false}
        />
        {action === 'transition' && (
          <Select
            label={t('automation.targetStatus')}
            data={statusData}
            value={params.status ? String(params.status) : null}
            onChange={(v) => set('status', v ? Number(v) : undefined)}
            required
          />
        )}
        {action === 'comment' && (
          <Textarea
            label={t('automation.commentText')}
            description={t('automation.commentHelp')}
            value={String(params.text ?? '')}
            onChange={(e) => set('text', e.currentTarget.value)}
            autosize
            minRows={2}
          />
        )}
        {action === 'assign' && (
          <>
            <Select
              label={t('automation.assignTo')}
              data={(['author', 'committer', 'user'] as const).map((x) => ({
                value: x,
                label: t(`automation.assignee.${x}`),
              }))}
              value={String(params.to ?? 'author')}
              onChange={(v) => set('to', v ?? 'author')}
              allowDeselect={false}
            />
            {params.to === 'user' && (
              <UserPicker
                label={t('automation.user')}
                value={params.user ? Number(params.user) : null}
                onChange={(v) => set('user', v ?? undefined)}
              />
            )}
          </>
        )}
        {action === 'fix-version' && (
          <Select
            label={t('automation.version')}
            data={[
              { value: '0', label: t('automation.nextVersion') },
              ...project.versions
                .filter((v) => !v.archived)
                .map((v) => ({ value: String(v.id), label: v.name })),
            ]}
            value={String(params.version ?? 0)}
            onChange={(v) => set('version', Number(v ?? 0))}
            allowDeselect={false}
          />
        )}
        <ErrorAlert error={save.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name}>
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
