// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Anchor,
  Badge,
  Box,
  Button,
  Card,
  Checkbox,
  Grid,
  Group,
  Modal,
  MultiSelect,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core';
import { IconArrowRight, IconPlus, IconTrash } from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../../api/client';
import { ErrorAlert } from '../../../components/ErrorAlert';
import { GroupPicker } from '../../../components/Pickers';
import { notifySuccess } from '../../../components/notify';
import { StatusBadge, useIssueConfig } from '../../issues/shared';
import { useWorkflows } from './WorkflowsPage';

type Definition = Schemas['Definition'];
type Transition = Schemas['WorkflowTransition'];
type Rule = Schemas['Rule'];
type RuleKind = 'conditions' | 'validators' | 'postFunctions';

const ruleTypes: Record<RuleKind, string[]> = {
  conditions: ['permission', 'role', 'group', 'assignee-only', 'reporter-only', 'subtasks-done'],
  validators: ['field-required', 'comment-required', 'permission'],
  postFunctions: [
    'set-resolution',
    'clear-resolution',
    'assign-to-current-user',
    'assign-to-reporter',
    'assign-to-lead',
    'unassign',
    'set-field',
    'add-comment',
  ],
};

function RuleParams({ rule, onChange }: { rule: Rule; onChange: (r: Rule) => void }) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  const c = config.data;
  switch (rule.type) {
    case 'permission':
      return (
        <Select
          size="xs"
          data={(c?.permissions ?? []).map((p) => ({
            value: p,
            label: t(`issuePermissions.${p}`, { defaultValue: p }),
          }))}
          value={rule.permission ?? null}
          onChange={(v) => onChange({ ...rule, permission: v ?? undefined })}
          aria-label={t('issueAdmin.permission')}
          searchable
        />
      );
    case 'role':
      return (
        <Select
          size="xs"
          data={(c?.roles ?? []).map((r) => ({ value: String(r.id), label: r.name }))}
          value={rule.role ? String(rule.role) : null}
          onChange={(v) => onChange({ ...rule, role: v ? Number(v) : undefined })}
          aria-label={t('issueAdmin.role')}
        />
      );
    case 'group':
      return (
        <GroupPicker
          size="xs"
          value={rule.group ?? null}
          onChange={(v) => onChange({ ...rule, group: v ?? undefined })}
        />
      );
    case 'field-required':
    case 'set-field':
      return (
        <Group gap="xs" grow>
          <Select
            size="xs"
            data={(c?.fields ?? []).filter((f) => f.editable).map((f) => ({ value: f.key, label: f.name }))}
            value={rule.field ?? null}
            onChange={(v) => onChange({ ...rule, field: v ?? undefined })}
            aria-label={t('issueAdmin.field')}
            searchable
          />
          {rule.type === 'set-field' && (
            <TextInput
              size="xs"
              placeholder={t('issueAdmin.value')}
              value={
                rule.value == null
                  ? ''
                  : typeof rule.value === 'string'
                    ? rule.value
                    : JSON.stringify(rule.value)
              }
              onChange={(e) => {
                const raw = e.currentTarget.value;
                let value: unknown = raw;
                try {
                  if (/^[-\d[{]|^(true|false|null)$/.test(raw)) value = JSON.parse(raw);
                } catch {
                  value = raw;
                }
                onChange({ ...rule, value });
              }}
            />
          )}
        </Group>
      );
    case 'set-resolution':
      return (
        <Select
          size="xs"
          data={(c?.resolutions ?? []).map((r) => ({ value: String(r.id), label: r.name }))}
          value={rule.resolution ? String(rule.resolution) : null}
          onChange={(v) => onChange({ ...rule, resolution: v ? Number(v) : undefined })}
          aria-label={t('issues.resolution')}
        />
      );
    case 'add-comment':
      return (
        <Textarea
          size="xs"
          autosize
          value={rule.text ?? ''}
          onChange={(e) => onChange({ ...rule, text: e.currentTarget.value })}
        />
      );
    default:
      return null;
  }
}

function RulesEditor({
  kind,
  rules,
  onChange,
}: {
  kind: RuleKind;
  rules: Rule[];
  onChange: (r: Rule[]) => void;
}) {
  const { t } = useTranslation();
  return (
    <Stack gap="xs">
      <Group justify="space-between">
        <Text fw={600} size="sm">
          {t(`issueAdmin.ruleKinds.${kind}`)}
        </Text>
        <Select
          size="xs"
          w={240}
          placeholder={t('issueAdmin.addRule')}
          data={ruleTypes[kind].map((r) => ({ value: r, label: t(`issueAdmin.rules.${r}`) }))}
          value={null}
          onChange={(v) => v && onChange([...rules, { type: v }])}
        />
      </Group>
      {rules.length === 0 && (
        <Text size="xs" c="dimmed">
          {t('issueAdmin.noRules')}
        </Text>
      )}
      {rules.map((r, i) => (
        <Group key={i} gap="xs" wrap="nowrap" align="flex-start">
          <Badge variant="light" tt="none" mt={4} style={{ flexShrink: 0 }}>
            {t(`issueAdmin.rules.${r.type}`, { defaultValue: r.type })}
          </Badge>
          <Box style={{ flex: 1 }}>
            <RuleParams rule={r} onChange={(nr) => onChange(rules.map((x, j) => (j === i ? nr : x)))} />
          </Box>
          <ActionIcon
            variant="subtle"
            color="red"
            onClick={() => onChange(rules.filter((_, j) => j !== i))}
            aria-label={t('common.remove')}
          >
            <IconTrash size={14} />
          </ActionIcon>
        </Group>
      ))}
    </Stack>
  );
}

function TransitionRules({
  transition,
  onChange,
  onClose,
}: {
  transition: Transition;
  onChange: (t: Transition) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Modal opened onClose={onClose} title={transition.name} size="xl">
      <Stack>
        {(['conditions', 'validators', 'postFunctions'] as RuleKind[]).map((k) => (
          <Card key={k} withBorder>
            <RulesEditor
              kind={k}
              rules={transition[k] ?? []}
              onChange={(rules) => onChange({ ...transition, [k]: rules })}
            />
          </Card>
        ))}
        <Group justify="flex-end">
          <Button onClick={onClose}>{t('common.close')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

const emptyDefinition = (): Definition => ({
  statuses: [],
  initial: 0,
  transitions: [],
  options: { autoResolution: true },
});

function Editor({
  id,
  initial,
}: {
  id: number | null;
  initial: { name: string; description: string; definition: Definition; projects: string[] };
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const config = useIssueConfig();
  const [name, setName] = useState(initial.name);
  const [description, setDescription] = useState(initial.description);
  const [def, setDef] = useState<Definition>(initial.definition);
  const [rulesOf, setRulesOf] = useState<number | null>(null);
  const statuses = config.data?.statuses ?? [];
  const statusName = (sid: number) => statuses.find((s) => s.id === sid)?.name ?? `#${sid}`;
  const inWorkflow = statuses.filter((s) => def.statuses.includes(s.id));
  const statusOptions = inWorkflow.map((s) => ({ value: String(s.id), label: s.name }));
  const setTransition = (tid: number, patch: Partial<Transition>) =>
    setDef((d) => ({ ...d, transitions: d.transitions.map((x) => (x.id === tid ? { ...x, ...patch } : x)) }));
  const save = useMutation({
    mutationFn: () => {
      const body = { name, description, definition: def };
      return id
        ? unwrap(api.PUT('/api/v1/issue-config/workflows/{id}', { params: { path: { id } }, body }))
        : unwrap(api.POST('/api/v1/issue-config/workflows', { body }));
    },
    onSuccess: (r) => {
      void client.invalidateQueries({ queryKey: ['issue-config'] });
      notifySuccess(t('common.saved'));
      if (!id) navigate(`/issues/admin/workflows/${r.id}`, { replace: true });
    },
  });
  const addTransition = () => {
    const next = Math.max(0, ...def.transitions.map((x) => x.id)) + 1;
    const to = def.statuses[0] ?? 0;
    setDef((d) => ({ ...d, transitions: [...d.transitions, { id: next, name: '', from: 'any', to }] }));
  };
  const editing = def.transitions.find((x) => x.id === rulesOf);
  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <Stack gap={4}>
          <Anchor component={Link} to="/issues/admin/workflows" size="sm">
            ← {t('issueAdmin.workflows')}
          </Anchor>
          <Title order={2}>{name || t('issueAdmin.newWorkflow')}</Title>
        </Stack>
        <Button
          onClick={() => save.mutate()}
          loading={save.isPending}
          disabled={!name.trim() || def.statuses.length === 0}
        >
          {t('common.save')}
        </Button>
      </Group>
      {initial.projects.length > 0 && (
        <Alert color="blue" variant="light">
          {t('issueAdmin.workflowInUse', { projects: initial.projects.join(', ') })}
        </Alert>
      )}
      <ErrorAlert error={save.error} />
      <Grid gap="lg">
        <Grid.Col span={{ base: 12, xl: 4 }}>
          <Card withBorder>
            <Stack>
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
              <MultiSelect
                label={t('issueAdmin.statuses')}
                data={statuses.map((s) => ({ value: String(s.id), label: s.name }))}
                value={def.statuses.map(String)}
                onChange={(v) => {
                  const ids = v.map(Number);
                  setDef((d) => ({
                    ...d,
                    statuses: ids,
                    initial: ids.includes(d.initial) ? d.initial : (ids[0] ?? 0),
                  }));
                }}
                searchable
              />
              <Select
                label={t('issueAdmin.initialStatus')}
                data={statusOptions}
                value={def.initial ? String(def.initial) : null}
                onChange={(v) => v && setDef((d) => ({ ...d, initial: Number(v) }))}
                allowDeselect={false}
              />
              <Switch
                label={t('issueAdmin.autoResolution')}
                description={t('issueAdmin.autoResolutionHelp')}
                checked={def.options.autoResolution}
                onChange={(e) => {
                  const checked = e.currentTarget.checked;
                  setDef((d) => ({ ...d, options: { ...d.options, autoResolution: checked } }));
                }}
              />
            </Stack>
          </Card>
          <Card withBorder mt="lg">
            <Title order={5} mb="sm">
              {t('issueAdmin.overview')}
            </Title>
            <Stack gap="sm">
              {inWorkflow.map((s) => {
                const out = def.transitions.filter((x) =>
                  x.from === 'any' ? x.to !== s.id : x.from.includes(s.id),
                );
                return (
                  <Box key={s.id}>
                    <Group gap={6}>
                      <StatusBadge status={s} />
                      {def.initial === s.id && (
                        <Badge size="xs" variant="outline">
                          {t('issueAdmin.initial')}
                        </Badge>
                      )}
                    </Group>
                    {out.map((x) => (
                      <Group key={x.id} gap={4} pl="md" mt={2}>
                        <IconArrowRight size={12} />
                        <Text size="xs">
                          {x.name || '…'} → {statusName(x.to)}
                        </Text>
                      </Group>
                    ))}
                  </Box>
                );
              })}
            </Stack>
          </Card>
        </Grid.Col>
        <Grid.Col span={{ base: 12, xl: 8 }}>
          <Card withBorder>
            <Group justify="space-between" mb="sm">
              <Title order={5}>{t('issueAdmin.transitions')}</Title>
              <Button
                size="xs"
                variant="light"
                leftSection={<IconPlus size={14} />}
                onClick={addTransition}
                disabled={def.statuses.length === 0}
              >
                {t('issueAdmin.addTransition')}
              </Button>
            </Group>
            <Table>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('common.name')}</Table.Th>
                  <Table.Th>{t('issueAdmin.from')}</Table.Th>
                  <Table.Th>{t('issueAdmin.to')}</Table.Th>
                  <Table.Th>{t('issueAdmin.screen')}</Table.Th>
                  <Table.Th>{t('issueAdmin.rulesTitle')}</Table.Th>
                  <Table.Th w={40} />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {def.transitions.map((x) => {
                  const ruleCount =
                    (x.conditions?.length ?? 0) +
                    (x.validators?.length ?? 0) +
                    (x.postFunctions?.length ?? 0);
                  return (
                    <Table.Tr key={x.id}>
                      <Table.Td miw={190}>
                        <TextInput
                          size="xs"
                          value={x.name}
                          onChange={(e) => setTransition(x.id, { name: e.currentTarget.value })}
                          aria-label={t('common.name')}
                        />
                      </Table.Td>
                      <Table.Td miw={200}>
                        <Stack gap={4}>
                          <Checkbox
                            size="xs"
                            label={t('issueAdmin.fromAny')}
                            checked={x.from === 'any'}
                            onChange={(e) =>
                              setTransition(x.id, { from: e.currentTarget.checked ? 'any' : [] })
                            }
                          />
                          {x.from !== 'any' && (
                            <MultiSelect
                              size="xs"
                              data={statusOptions}
                              value={x.from.map(String)}
                              onChange={(v) => setTransition(x.id, { from: v.map(Number) })}
                              aria-label={t('issueAdmin.from')}
                            />
                          )}
                        </Stack>
                      </Table.Td>
                      <Table.Td miw={150}>
                        <Select
                          size="xs"
                          data={statusOptions}
                          value={String(x.to)}
                          onChange={(v) => v && setTransition(x.id, { to: Number(v) })}
                          allowDeselect={false}
                          aria-label={t('issueAdmin.to')}
                        />
                      </Table.Td>
                      <Table.Td>
                        <Checkbox
                          size="xs"
                          label={t('issues.resolution')}
                          checked={(x.screen ?? []).includes('resolution')}
                          onChange={(e) =>
                            setTransition(x.id, {
                              screen: e.currentTarget.checked
                                ? [...(x.screen ?? []), 'resolution']
                                : (x.screen ?? []).filter((f) => f !== 'resolution'),
                            })
                          }
                        />
                      </Table.Td>
                      <Table.Td>
                        <Button size="compact-xs" variant="subtle" onClick={() => setRulesOf(x.id)}>
                          {t('issueAdmin.nRules', { count: ruleCount })}
                        </Button>
                      </Table.Td>
                      <Table.Td>
                        <ActionIcon
                          variant="subtle"
                          color="red"
                          onClick={() =>
                            setDef((d) => ({ ...d, transitions: d.transitions.filter((y) => y.id !== x.id) }))
                          }
                          aria-label={t('common.delete')}
                        >
                          <IconTrash size={14} />
                        </ActionIcon>
                      </Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Card>
        </Grid.Col>
      </Grid>
      {editing && (
        <TransitionRules
          transition={editing}
          onChange={(nt) => setTransition(nt.id, nt)}
          onClose={() => setRulesOf(null)}
        />
      )}
    </Stack>
  );
}

export function WorkflowEditorPage() {
  const { id } = useParams();
  const workflows = useWorkflows();
  if (id === 'new')
    return (
      <Editor
        id={null}
        initial={{ name: '', description: '', definition: emptyDefinition(), projects: [] }}
      />
    );
  const w = workflows.data?.find((x) => String(x.id) === id);
  if (workflows.error) return <ErrorAlert error={workflows.error} />;
  if (!w) return null;
  return <Editor key={w.id} id={w.id} initial={w} />;
}
