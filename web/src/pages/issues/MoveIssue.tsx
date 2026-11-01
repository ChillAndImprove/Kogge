// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Alert, Badge, Button, Group, Loader, Modal, Select, Stack, Table, Text } from '@mantine/core';
import { IconArrowRight } from '@tabler/icons-react';
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifySuccess } from '../../components/notify';
import { useFieldLabel } from './fields';
import { useIssueConfig } from './shared';

export type MovePlan = Schemas['MovePlan'];
export type MoveInput = Schemas['MoveInput'];

/** The choices made for a move: the types issues become and the statuses they take, by what the target lacks. */
export interface MoveChoices {
  types: Record<number, number>;
  statuses: Record<string, number>;
}

export const noChoices: MoveChoices = { types: {}, statuses: {} };

const statusKey = (type: number, from: number) => `${type}:${from}`;

/** The choices made by the user, for asking the server for a plan (it proposes the others). */
export function chosenInput(project: string, choices: MoveChoices): MoveInput {
  return {
    project,
    types: Object.entries(choices.types).map(([from, to]) => ({ from: Number(from), to })),
    statuses: Object.entries(choices.statuses).map(([k, to]) => {
      const [type, from] = k.split(':').map(Number);
      return { type, from, to };
    }),
  };
}

/** What the move is to do: the user's choices, and what the plan proposes for the rest. */
export function moveInput(project: string, plan: MovePlan | undefined, choices: MoveChoices): MoveInput {
  const types: Record<number, number> = {};
  const statuses: Record<string, number> = {};
  for (const q of plan?.types ?? []) {
    const to = choices.types[q.from.id] ?? q.chosen;
    if (to) types[q.from.id] = to;
  }
  for (const q of plan?.statuses ?? []) {
    const k = statusKey(q.type.id, q.from.id);
    const to = choices.statuses[k] ?? q.chosen;
    if (to) statuses[k] = to;
  }
  return chosenInput(project, { types, statuses });
}

/** Whether every issue of the plan can move with the choices. */
export function planReady(plan: MovePlan | undefined): boolean {
  return (
    !!plan &&
    plan.types.every((q) => !!q.chosen) &&
    plan.statuses.every((q) => !!q.chosen) &&
    plan.issues.some((i) => i.movable)
  );
}

/** The plan of a move: the types and statuses to choose, and what becomes of each issue. */
export function MovePlanView({
  plan,
  choices,
  onChoices,
}: {
  plan: MovePlan;
  choices: MoveChoices;
  onChoices: (c: MoveChoices) => void;
}) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  const label = useFieldLabel();
  const fieldName = (key: string, name?: string) => {
    const def = config.data?.fields.find((f) => f.key === key);
    return name || (def ? label(def) : t(`issues.${key}`, { defaultValue: key }));
  };
  return (
    <Stack gap="sm">
      {plan.types.length > 0 && (
        <Stack gap={6}>
          <Text size="sm" fw={600}>
            {t('move.types', { project: plan.project.key })}
          </Text>
          {plan.types.map((q) => (
            <Group key={q.from.id} gap="xs" wrap="nowrap">
              <Text size="sm" w={160} truncate>
                {q.from.name} ({q.issues.length})
              </Text>
              <IconArrowRight size={14} aria-hidden />
              <Select
                size="xs"
                aria-label={t('move.typeFor', { type: q.from.name })}
                data={q.choices.map((c) => ({ value: String(c.id), label: c.name }))}
                value={q.chosen ? String(choices.types[q.from.id] ?? q.chosen) : null}
                onChange={(v) =>
                  v && onChoices({ ...choices, types: { ...choices.types, [q.from.id]: Number(v) } })
                }
                allowDeselect={false}
                error={q.choices.length === 0 ? t('move.noType') : undefined}
              />
            </Group>
          ))}
        </Stack>
      )}
      {plan.statuses.length > 0 && (
        <Stack gap={6}>
          <Text size="sm" fw={600}>
            {t('move.statuses', { project: plan.project.key })}
          </Text>
          {plan.statuses.map((q) => {
            const k = statusKey(q.type.id, q.from.id);
            return (
              <Group key={k} gap="xs" wrap="nowrap">
                <Text size="sm" w={160} truncate>
                  {q.from.name} · {q.type.name} ({q.issues.length})
                </Text>
                <IconArrowRight size={14} aria-hidden />
                <Select
                  size="xs"
                  aria-label={t('move.statusFor', { status: q.from.name, type: q.type.name })}
                  data={q.choices.map((c) => ({ value: String(c.id), label: c.name }))}
                  value={q.chosen ? String(choices.statuses[k] ?? q.chosen) : null}
                  onChange={(v) =>
                    v && onChoices({ ...choices, statuses: { ...choices.statuses, [k]: Number(v) } })
                  }
                  allowDeselect={false}
                />
              </Group>
            );
          })}
        </Stack>
      )}
      <Table withTableBorder verticalSpacing={4} data-testid="move-plan">
        <Table.Tbody>
          {plan.issues.map((i) => (
            <Table.Tr key={i.key}>
              <Table.Td w={110} style={{ whiteSpace: 'nowrap' }}>
                <Text size="sm" fw={600} span>
                  {i.key}
                </Text>
              </Table.Td>
              <Table.Td>
                <Text size="sm" lineClamp={1}>
                  {i.summary}
                </Text>
                {i.withParent && (
                  <Text size="xs" c="dimmed">
                    {t('move.withParent', { key: i.withParent })}
                  </Text>
                )}
                {!i.movable && !i.withParent && (
                  <Text size="xs" c="red">
                    {i.reason}
                  </Text>
                )}
                {i.changes.length > 0 && (
                  <Group gap={4} mt={2}>
                    {i.changes.map((c, n) => (
                      <Badge
                        key={n}
                        size="xs"
                        variant="light"
                        color={c.removed ? 'orange' : 'gray'}
                        tt="none"
                      >
                        {fieldName(c.field, c.name)}: {c.from}
                        {c.removed ? ` – ${t('move.removed')}` : ` → ${c.to}`}
                      </Badge>
                    ))}
                  </Group>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

/** Chooses a project to move issues to: the projects the user may create issues in, but the issues' own. */
export function TargetProjectSelect({
  value,
  onChange,
  exclude,
}: {
  value: string | null;
  onChange: (key: string | null) => void;
  exclude?: string;
}) {
  const { t } = useTranslation();
  const projects = useQuery({ queryKey: ['projects'], queryFn: () => unwrap(api.GET('/api/v1/projects')) });
  return (
    <Select
      label={t('move.target')}
      searchable
      data={(projects.data ?? [])
        .filter((p) => p.key !== exclude)
        .map((p) => ({ value: p.key, label: `${p.name} (${p.key})` }))}
      value={value}
      onChange={onChange}
      nothingFoundMessage={t('common.none')}
      data-autofocus
    />
  );
}

/** Moves an issue (with its sub-tasks) to another project, after showing what the move does. */
export function MoveIssueModal({
  issueKey,
  projectKey,
  onClose,
}: {
  issueKey: string;
  projectKey: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const navigate = useNavigate();
  const [target, setTarget] = useState<string | null>(null);
  const [choices, setChoices] = useState<MoveChoices>(noChoices);
  const plan = useQuery({
    queryKey: ['move-plan', issueKey, target, choices],
    queryFn: () =>
      unwrap(
        api.POST('/api/v1/issues/move-plan', {
          body: { issues: [issueKey], ...chosenInput(target!, choices) },
        }),
      ),
    enabled: !!target,
    placeholderData: keepPreviousData,
    retry: false,
  });
  const move = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/issues/{key}/move', {
          params: { path: { key: issueKey } },
          body: moveInput(target!, plan.data, choices),
        }),
      ),
    onSuccess: (moved) => {
      void client.invalidateQueries({ queryKey: ['issue'] });
      notifySuccess(t('move.done', { from: issueKey, to: moved.key }));
      onClose();
      navigate(`/browse/${moved.key}`, { replace: true });
    },
  });
  const current = plan.data && plan.data.project.key === target ? plan.data : undefined;
  return (
    <Modal opened onClose={onClose} size="xl" title={t('move.title', { key: issueKey })}>
      <Stack>
        <TargetProjectSelect
          value={target}
          onChange={(v) => {
            setTarget(v);
            setChoices(noChoices);
          }}
          exclude={projectKey}
        />
        {target && plan.isPending && <Loader size="sm" />}
        <ErrorAlert error={plan.error} />
        {current && <MovePlanView plan={current} choices={choices} onChoices={setChoices} />}
        {current && (
          <Alert color="blue" variant="light">
            {t('move.keeps')}
          </Alert>
        )}
        <ErrorAlert error={move.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={() => move.mutate()}
            loading={move.isPending}
            disabled={!planReady(current) || plan.isFetching}
          >
            {t('move.submit')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
