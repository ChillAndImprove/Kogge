// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Group, Modal, Radio, Select, Stack, Text, TextInput, Textarea } from '@mantine/core';
import { DateTimePicker } from '@mantine/dates';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifySuccess } from '../../components/notify';
import type { Sprint } from './hooks';

const twoWeeks = 14 * 24 * 3600 * 1000;

function toDate(v: string | null): string | undefined {
  if (!v) return undefined;
  const d = new Date(v.replace(' ', 'T'));
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}

function fromIso(v: string | undefined): string | null {
  if (!v) return null;
  const d = new Date(v);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:00`;
}

/** Starts a sprint or changes its name, goal and dates. */
export function SprintModal({
  sprint,
  mode,
  issueCount,
  onClose,
}: {
  sprint: Sprint;
  mode: 'start' | 'edit';
  issueCount?: number;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const now = new Date();
  const [name, setName] = useState(sprint.name);
  const [goal, setGoal] = useState(sprint.goal);
  const [start, setStart] = useState<string | null>(
    fromIso(sprint.start ?? (mode === 'start' ? now.toISOString() : undefined)),
  );
  const [end, setEnd] = useState<string | null>(
    fromIso(sprint.end ?? (mode === 'start' ? new Date(now.getTime() + twoWeeks).toISOString() : undefined)),
  );
  const save = useMutation({
    mutationFn: () => {
      const body = { name, goal, start: toDate(start), end: toDate(end) };
      return mode === 'start'
        ? unwrap(api.POST('/api/v1/sprints/{id}/start', { params: { path: { id: sprint.id } }, body }))
        : unwrap(api.PUT('/api/v1/sprints/{id}', { params: { path: { id: sprint.id } }, body }));
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['board'] });
      if (mode === 'start') notifySuccess(t('agile.sprintStarted', { name }));
      onClose();
    },
  });
  return (
    <Modal opened onClose={onClose} title={mode === 'start' ? t('agile.startSprint') : t('agile.editSprint')}>
      <Stack>
        {mode === 'start' && issueCount !== undefined && (
          <Text size="sm">{t('agile.startSprintText', { count: issueCount })}</Text>
        )}
        <TextInput
          label={t('common.name')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
          data-autofocus
        />
        <Group grow>
          <DateTimePicker
            label={t('agile.start')}
            value={start}
            onChange={setStart}
            valueFormat="DD.MM.YYYY HH:mm"
            clearable={mode === 'edit'}
          />
          <DateTimePicker
            label={t('agile.end')}
            value={end}
            onChange={setEnd}
            valueFormat="DD.MM.YYYY HH:mm"
            clearable={mode === 'edit'}
          />
        </Group>
        <Textarea
          label={t('agile.goal')}
          value={goal}
          onChange={(e) => setGoal(e.currentTarget.value)}
          autosize
          minRows={2}
        />
        <ErrorAlert error={save.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name.trim()}>
            {mode === 'start' ? t('agile.startSprint') : t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** Completes a sprint: unfinished issues go to the backlog, a future sprint or a new sprint. */
export function CompleteSprintModal({
  sprint,
  done,
  open,
  futureSprints,
  onClose,
}: {
  sprint: Sprint;
  done: number;
  open: number;
  futureSprints: Sprint[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [moveTo, setMoveTo] = useState<'backlog' | 'sprint' | 'new'>(futureSprints.length ? 'sprint' : 'new');
  const [target, setTarget] = useState<string | null>(futureSprints[0] ? String(futureSprints[0].id) : null);
  const complete = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/sprints/{id}/complete', {
          params: { path: { id: sprint.id } },
          body: { moveTo, sprintId: moveTo === 'sprint' && target ? Number(target) : undefined },
        }),
      ),
    onSuccess: (r) => {
      void client.invalidateQueries({ queryKey: ['board'] });
      notifySuccess(
        t('agile.sprintCompleted', { name: r.sprint.name, completed: r.completed, moved: r.moved }),
      );
      onClose();
    },
  });
  return (
    <Modal opened onClose={onClose} title={t('agile.completeSprintTitle', { name: sprint.name })}>
      <Stack>
        <Text size="sm">{t('agile.completeSprintText', { done, open })}</Text>
        {open > 0 && (
          <Radio.Group
            label={t('agile.moveUnfinished')}
            value={moveTo}
            onChange={(v) => setMoveTo(v as typeof moveTo)}
          >
            <Stack gap="xs" mt="xs">
              {futureSprints.length > 0 && <Radio value="sprint" label={t('agile.toSprint')} />}
              {moveTo === 'sprint' && futureSprints.length > 0 && (
                <Select
                  data={futureSprints.map((s) => ({ value: String(s.id), label: s.name }))}
                  value={target}
                  onChange={setTarget}
                  allowDeselect={false}
                  ml="xl"
                  aria-label={t('agile.toSprint')}
                />
              )}
              <Radio value="new" label={t('agile.toNewSprint')} />
              <Radio value="backlog" label={t('agile.toBacklog')} />
            </Stack>
          </Radio.Group>
        )}
        <ErrorAlert error={complete.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => complete.mutate()} loading={complete.isPending}>
            {t('agile.completeSprint')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
