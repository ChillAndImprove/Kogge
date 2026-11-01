// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Group, Loader, Menu, Modal, Select, Stack, Text } from '@mantine/core';
import { IconArrowRight, IconChevronDown } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { Editor, isEmptyDoc, type Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError } from '../../components/notify';
import { FieldInput, issueValue, useFieldContext, useFieldLabel } from './fields';
import { useIssue } from './issueHooks';
import { StatusBadge, useIssueConfig, type Issue } from './shared';

export type AvailableTransition = Schemas['AvailableTransition'];

const categoryColor = (c: string | undefined) =>
  c === 'done' ? 'teal' : c === 'inprogress' ? 'blue' : 'gray';

/** Whether a transition asks for something before it runs. */
export const needsScreen = (tr: AvailableTransition) => tr.screen.length > 0 || tr.commentRequired;

/** The status of an issue; for who may transition it, a menu of the transitions of its workflow. */
export function StatusMenu({ issue }: { issue: Issue }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [opened, setOpened] = useState(false);
  const [screen, setScreen] = useState<AvailableTransition | null>(null);
  const transitions = useQuery({
    queryKey: ['issue', issue.key, 'transitions', issue.version],
    queryFn: () =>
      unwrap(api.GET('/api/v1/issues/{key}/transitions', { params: { path: { key: issue.key } } })),
    enabled: opened && issue.permissions.transition,
  });
  const run = useMutation({
    mutationFn: (tr: AvailableTransition) =>
      unwrap(
        api.POST('/api/v1/issues/{key}/transitions', {
          params: { path: { key: issue.key } },
          body: { transitionId: tr.id },
        }),
      ),
    onSuccess: (data) => {
      client.setQueryData(['issue', issue.key], data);
      void client.invalidateQueries({ queryKey: ['issue', issue.key] });
      void client.invalidateQueries({ queryKey: ['board'] });
    },
    onError: notifyError,
  });
  if (!issue.permissions.transition) return <StatusBadge status={issue.status} />;
  const list = transitions.data ?? [];
  return (
    <>
      <Menu opened={opened} onChange={setOpened} position="bottom-start" shadow="md" width={280}>
        <Menu.Target>
          <Button
            size="xs"
            color={categoryColor(issue.status.category)}
            variant={issue.status.category === 'todo' ? 'light' : 'filled'}
            rightSection={
              run.isPending ? <Loader size={12} color="currentColor" /> : <IconChevronDown size={14} />
            }
            aria-label={t('issues.changeStatus', { status: issue.status.name })}
          >
            {issue.status.name}
          </Button>
        </Menu.Target>
        <Menu.Dropdown>
          <Menu.Label>{t('issues.transitions')}</Menu.Label>
          {transitions.isLoading && (
            <Group justify="center" p="xs">
              <Loader size="xs" />
            </Group>
          )}
          {transitions.isSuccess && list.length === 0 && (
            <Text size="sm" c="dimmed" px="sm" py={6}>
              {t('issues.noTransitions')}
            </Text>
          )}
          {list.map((tr) => (
            <Menu.Item
              key={tr.id}
              onClick={() => (needsScreen(tr) ? setScreen(tr) : run.mutate(tr))}
              rightSection={<StatusBadge status={tr.to} />}
            >
              <Group gap={6} wrap="nowrap">
                <IconArrowRight size={14} />
                <Text size="sm" truncate>
                  {tr.name}
                </Text>
              </Group>
            </Menu.Item>
          ))}
        </Menu.Dropdown>
      </Menu>
      <Modal opened={screen !== null} onClose={() => setScreen(null)} title={screen?.name} size="lg">
        {screen && (
          <TransitionForm
            issueKey={issue.key}
            transition={screen}
            onCancel={() => setScreen(null)}
            onDone={(data) => {
              client.setQueryData(['issue', issue.key], data);
              void client.invalidateQueries({ queryKey: ['issue', issue.key] });
              void client.invalidateQueries({ queryKey: ['board'] });
              setScreen(null);
            }}
          />
        )}
      </Modal>
    </>
  );
}

/**
 * Asks for the fields of a transition's screen — any field the workflow puts there, filled with the issue's values —
 * and for a comment, then runs the transition.
 */
export function TransitionForm({
  issueKey,
  transition,
  onDone,
  onCancel,
}: {
  issueKey: string;
  transition: AvailableTransition;
  onDone: (issue: Issue) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  const issue = useIssue(issueKey).data;
  const ctx = useFieldContext(issue?.project.key, issue?.type.level ?? 0, issue);
  const label = useFieldLabel();
  const [values, setValues] = useState<Record<string, unknown>>({});
  const [comment, setComment] = useState<Doc | null>(null);
  const run = useMutation({
    mutationFn: () => {
      const fields: Record<string, unknown> = {};
      for (const key of transition.screen) if (key in values) fields[key] = values[key];
      return unwrap(
        api.POST('/api/v1/issues/{key}/transitions', {
          params: { path: { key: issueKey } },
          body: {
            transitionId: transition.id,
            fields: Object.keys(fields).length ? (fields as Record<string, never>) : undefined,
            comment: comment && !isEmptyDoc(comment) ? (comment as Schemas['Doc']) : undefined,
          },
        }),
      );
    },
    onSuccess: onDone,
  });
  if (!issue) return <Loader size="sm" />;
  const value = (key: string) => (key in values ? values[key] : issueValue(issue, key));
  const set = (key: string) => (v: unknown) => setValues((cur) => ({ ...cur, [key]: v }));
  const missingComment = transition.commentRequired && (!comment || isEmptyDoc(comment));
  return (
    <Stack>
      <Group gap="xs">
        <StatusBadge status={issue.status} />
        <IconArrowRight size={14} />
        <StatusBadge status={transition.to} />
      </Group>
      {transition.screen.map((key) => {
        if (key === 'resolution') {
          return (
            <Select
              key={key}
              label={t('issues.resolution')}
              data={(config.data?.resolutions ?? []).map((r) => ({ value: String(r.id), label: r.name }))}
              value={values.resolution != null ? String(values.resolution) : null}
              onChange={(v) => set('resolution')(v ? Number(v) : null)}
              data-autofocus
            />
          );
        }
        const def = config.data?.fields.find((f) => f.key === key);
        if (!def) return null;
        return (
          <FieldInput
            key={key}
            def={def}
            value={value(key)}
            onChange={set(key)}
            ctx={ctx}
            label={label(def)}
          />
        );
      })}
      <Stack gap={4}>
        <Text size="sm" fw={550}>
          {t('issues.comment')}
          {transition.commentRequired && (
            <Text span c="red">
              {' '}
              *
            </Text>
          )}
        </Text>
        <Editor value={comment} onChange={setComment} placeholder={t('issues.addComment')} minHeight={90} />
      </Stack>
      <ErrorAlert error={run.error} />
      <Group justify="flex-end">
        <Button variant="default" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        <Button onClick={() => run.mutate()} loading={run.isPending} disabled={missingComment}>
          {transition.name}
        </Button>
      </Group>
    </Stack>
  );
}
