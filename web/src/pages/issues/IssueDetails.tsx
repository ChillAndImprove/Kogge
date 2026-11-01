// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Button,
  Card,
  Group,
  Progress,
  Stack,
  Text,
  Title,
  Tooltip,
  UnstyledButton,
} from '@mantine/core';
import { IconChevronDown, IconChevronUp, IconClockPlus } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { formatDateTime, formatRelative } from '../../app/format';
import { useSession } from '../../app/session';
import { DetailItem, DetailsList } from '../../components/InlineEdit';
import {
  F,
  FieldDisplay,
  InlineFieldEditor,
  canEditField,
  isDetailField,
  isEmptyValue,
  issueValue,
  layoutFields,
  useFieldContext,
  useFieldLayout,
  useFieldLabel,
  type LayoutField,
} from './fields';
import { useUpdateIssue } from './issueHooks';
import { formatDuration, useIssueConfig, type Issue } from './shared';

/** Fields that the details show even when they are empty. */
const always: string[] = [F.assignee, F.reporter, F.priority];

/** The details panel of an issue: its fields as the project's field layout shows them, edited in place. */
export function IssueDetails({ issue, onLogWork }: { issue: Issue; onLogWork: () => void }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const config = useIssueConfig();
  const layout = useFieldLayout(issue.project.key, issue.type.id);
  const ctx = useFieldContext(issue.project.key, issue.type.level, issue);
  const update = useUpdateIssue(issue);
  const label = useFieldLabel();
  const [more, setMore] = useState(false);
  const saving = update.isPending ? Object.keys(update.variables ?? {}) : [];

  const fields = layoutFields(layout.data, config.data?.fields).filter(
    ({ def, item }) => item.view && isDetailField(def),
  );
  // Sprints are no part of layouts: they belong to issues that can be planned.
  const sprintDef = config.data?.fields.find((f) => f.key === F.sprint);
  if (sprintDef && issue.type.level !== -1 && (issue.type.level === 0 || issue.sprint)) {
    const at = fields.findIndex((f) => f.def.key === F.dueDate);
    const entry: LayoutField = {
      def: sprintDef,
      item: { field: F.sprint, view: true, edit: true, create: false, required: false },
    };
    fields.splice(at < 0 ? fields.length : at + 1, 0, entry);
  }
  const editable = (f: LayoutField) => f.item.edit && canEditField(issue, f.def.key);
  const filled = fields.filter(
    (f) => always.includes(f.def.key) || !isEmptyValue(issueValue(issue, f.def.key)),
  );
  // Empty fields wait behind "more fields" — those the user could fill.
  const empty = fields.filter((f) => !filled.includes(f) && editable(f));

  const row = (f: LayoutField) => {
    const { def } = f;
    const value = issueValue(issue, def.key);
    const canAssignMe =
      def.key === F.assignee && issue.permissions.assign && issue.assignee?.id !== me.user.id;
    return (
      <DetailItem
        key={def.key}
        label={label(def)}
        editable={editable(f)}
        busy={saving.includes(def.key)}
        editor={(done) => (
          <InlineFieldEditor
            def={def}
            value={value}
            ctx={ctx}
            done={done}
            save={(v) => update.mutate({ [def.key]: v })}
          />
        )}
        footer={
          canAssignMe ? (
            <Anchor
              component="button"
              type="button"
              size="xs"
              className="kogge-details-action"
              onClick={() => update.mutate({ assignee: me.user.id })}
            >
              {t('issues.assignToMe')}
            </Anchor>
          ) : def.key === F.sprint && issue.closedSprints.length > 0 ? (
            <Text size="xs" c="dimmed" className="kogge-details-action">
              {t('agile.closedSprints')}: {issue.closedSprints.map((s) => s.name).join(', ')}
            </Text>
          ) : undefined
        }
      >
        <FieldDisplay
          def={def}
          value={value}
          ctx={ctx}
          empty={def.key === F.assignee ? t('issues.unassigned') : undefined}
        />
      </DetailItem>
    );
  };

  return (
    <Stack gap="md">
      <Card withBorder padding="md" radius="md">
        <Title order={5} mb="xs">
          {t('issues.details')}
        </Title>
        <DetailsList>{filled.map(row)}</DetailsList>
        {empty.length > 0 && (
          <>
            {more && <DetailsList>{empty.map(row)}</DetailsList>}
            <UnstyledButton
              className="kogge-details-more"
              onClick={() => setMore((m) => !m)}
              aria-expanded={more}
            >
              <Group gap={4}>
                {more ? <IconChevronUp size={14} /> : <IconChevronDown size={14} />}
                <Text size="xs" fw={550}>
                  {more ? t('issues.fewerFields') : t('issues.moreFields', { count: empty.length })}
                </Text>
              </Group>
            </UnstyledButton>
          </>
        )}
      </Card>
      <TimeTracking
        issue={issue}
        onLogWork={onLogWork}
        layoutFields={fields.length ? layout.data : undefined}
      />
      <Dates issue={issue} />
    </Stack>
  );
}

/** Logged time against the estimate, with the estimates edited in place. */
function TimeTracking({
  issue,
  onLogWork,
  layoutFields: layout,
}: {
  issue: Issue;
  onLogWork: () => void;
  layoutFields?: { field: string; view: boolean; edit: boolean }[];
}) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  const ctx = useFieldContext(issue.project.key, issue.type.level, issue);
  const update = useUpdateIssue(issue);
  const label = useFieldLabel();
  const units = ctx.units;
  const fmt = (s: number | null | undefined) => formatDuration(s ?? 0, units.hoursPerDay, units.daysPerWeek);
  const spent = issue.timeSpent ?? 0;
  const remaining = issue.remainingEstimate ?? 0;
  const original = issue.originalEstimate ?? 0;
  const total = Math.max(spent + remaining, original, 1);
  const over = original > 0 && spent + remaining > original;
  const estimates = [F.originalEstimate, F.remainingEstimate].flatMap((key) => {
    const def = config.data?.fields.find((f) => f.key === key);
    const item = layout?.find((it) => it.field === key);
    return def && (!layout || item?.view)
      ? [{ def, editable: (item?.edit ?? true) && issue.permissions.edit }]
      : [];
  });
  return (
    <Card withBorder padding="md" radius="md">
      <Group justify="space-between" mb="xs">
        <Title order={5}>{t('issues.timeTracking')}</Title>
        {issue.permissions.logWork && (
          <Button
            size="compact-xs"
            variant="light"
            leftSection={<IconClockPlus size={14} />}
            onClick={onLogWork}
          >
            {t('issues.logWork')}
          </Button>
        )}
      </Group>
      <Tooltip label={t('issues.timeSummary', { spent: fmt(spent), remaining: fmt(remaining) })} withinPortal>
        <Progress.Root size="md" radius="xl">
          <Progress.Section
            value={(spent / total) * 100}
            color={over ? 'orange' : 'blue'}
            aria-label={t('issues.loggedTime', { time: fmt(spent) })}
          />
          <Progress.Section
            value={(remaining / total) * 100}
            color="gray.3"
            aria-label={
              issue.remainingEstimate != null
                ? t('issues.remainingTime', { time: fmt(remaining) })
                : t('issues.noEstimate')
            }
          />
        </Progress.Root>
      </Tooltip>
      <Group justify="space-between" mt={6} mb="xs">
        <Text size="xs" c="dimmed">
          {t('issues.loggedTime', { time: fmt(spent) })}
        </Text>
        <Text size="xs" c="dimmed">
          {issue.remainingEstimate != null
            ? t('issues.remainingTime', { time: fmt(remaining) })
            : t('issues.noEstimate')}
        </Text>
      </Group>
      <DetailsList>
        {estimates.map(({ def, editable }) => {
          const value = issueValue(issue, def.key);
          return (
            <DetailItem
              key={def.key}
              label={label(def)}
              editable={editable}
              busy={update.isPending && def.key in (update.variables ?? {})}
              editor={(done) => (
                <InlineFieldEditor
                  def={def}
                  value={value}
                  ctx={ctx}
                  done={done}
                  save={(v) => update.mutate({ [def.key]: v })}
                />
              )}
            >
              <FieldDisplay def={def} value={value} ctx={ctx} />
            </DetailItem>
          );
        })}
      </DetailsList>
    </Card>
  );
}

function Dates({ issue }: { issue: Issue }) {
  const { t } = useTranslation();
  const rows: [string, string | undefined][] = [
    [t('issues.created'), issue.created],
    [t('issues.updated'), issue.updated],
    [t('issues.resolved'), issue.resolved ?? undefined],
  ];
  return (
    <Stack gap={2} px="xs">
      {rows
        .filter(([, v]) => v)
        .map(([name, v]) => (
          <Text key={name} size="xs" c="dimmed">
            {name}{' '}
            <Tooltip label={formatDateTime(v)} withinPortal>
              <span>{formatRelative(v)}</span>
            </Tooltip>
          </Text>
        ))}
    </Stack>
  );
}
