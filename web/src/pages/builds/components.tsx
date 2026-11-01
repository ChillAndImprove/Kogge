// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Badge,
  Button,
  Group,
  Loader,
  Modal,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
} from '@mantine/core';
import { IconHandStop, IconPlayerPlay } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatDateTime, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { RefSelect } from '../code/components';
import { repoParams, shortSha, type Repository } from '../code/hooks';
import { elapsed, formatDuration, pipelinesPath, runKey, runPath, type RunSummary } from './hooks';
import { StateIcon } from './status';

export { CommitStatusIcon, StateBadge, StateIcon } from './status';

const currentTime = () => Date.now();

/** Updates every second while something runs. */
export function useNow(active: boolean) {
  const [now, setNow] = useState(currentTime);
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [active]);
  return now;
}

export function Duration({ started, finished }: { started?: string | null; finished?: string | null }) {
  const now = useNow(!!started && !finished);
  const s = elapsed(started, finished, now);
  return s === null ? null : <>{formatDuration(s)}</>;
}

export function TimeAgo({ at }: { at: string }) {
  return <span title={formatDateTime(at)}>{formatRelative(at)}</span>;
}

/** Runs in a table: number, state, pipeline, ref, commit, trigger, duration. */
export function RunTable({ runs, showRepo = false }: { runs: RunSummary[]; showRepo?: boolean }) {
  const { t } = useTranslation();
  if (runs.length === 0) {
    return (
      <Text c="dimmed" ta="center" py="lg">
        {t('builds.noRuns')}
      </Text>
    );
  }
  return (
    <Table highlightOnHover verticalSpacing="xs">
      <Table.Thead>
        <Table.Tr>
          <Table.Th w={110}>{t('builds.run')}</Table.Th>
          {showRepo && <Table.Th>{t('code.repository')}</Table.Th>}
          <Table.Th>{t('builds.pipeline')}</Table.Th>
          <Table.Th>{t('builds.commit')}</Table.Th>
          <Table.Th>{t('builds.trigger')}</Table.Th>
          <Table.Th w={130}>{t('builds.duration')}</Table.Th>
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {runs.map((r) => {
          const repo = r.repository ?? null;
          // Runs of a repository's own list come without it: the page lists them below the repository.
          const to = repo ? runPath(repo, r.number) : `runs/${r.number}`;
          return (
            <Table.Tr key={r.id}>
              <Table.Td>
                <Group gap={6} wrap="nowrap">
                  <StateIcon state={r.state} />
                  <Anchor component={Link} to={to} fw={600}>
                    #{r.number}
                  </Anchor>
                </Group>
              </Table.Td>
              {showRepo && (
                <Table.Td>
                  {repo && (
                    <Anchor component={Link} to={pipelinesPath(repo)} size="sm">
                      {repo.projectKey} / {repo.name}
                    </Anchor>
                  )}
                </Table.Td>
              )}
              <Table.Td>
                <Text size="sm" fw={500}>
                  {r.name}
                </Text>
                <Group gap={4}>
                  {r.branch && (
                    <Badge size="xs" variant="outline" color="gray" tt="none">
                      {r.branch}
                    </Badge>
                  )}
                  {r.tag && (
                    <Badge size="xs" variant="outline" color="grape" tt="none">
                      {r.tag}
                    </Badge>
                  )}
                  {r.pullRequest ? (
                    <Badge size="xs" variant="light" color="blue" tt="none">
                      PR #{r.pullRequest}
                    </Badge>
                  ) : null}
                </Group>
              </Table.Td>
              <Table.Td>
                <Text size="sm" lineClamp={1}>
                  {r.subject}
                </Text>
                <Text size="xs" c="dimmed" ff="monospace">
                  {shortSha(r.commit)}
                </Text>
              </Table.Td>
              <Table.Td>
                <Text size="sm">
                  {r.after
                    ? `${t('builds.after')} ${r.after.name} #${r.after.number}`
                    : t(`builds.event.${r.event}` as 'builds.event.push')}
                </Text>
                <Text size="xs" c="dimmed">
                  {r.triggeredBy?.displayName ?? ''}
                </Text>
              </Table.Td>
              <Table.Td>
                <Text size="sm">
                  <Duration started={r.started} finished={r.finished} />
                </Text>
                <Text size="xs" c="dimmed">
                  <TimeAgo at={r.created} />
                </Text>
              </Table.Td>
            </Table.Tr>
          );
        })}
      </Table.Tbody>
    </Table>
  );
}

/** Starts pipelines by hand: a ref, a pipeline and the values of its run form. */
export function RunPipelineModal({
  repo,
  opened,
  onClose,
}: {
  repo: Repository;
  opened: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [ref, setRef] = useState(repo.defaultBranch);
  const [pipeline, setPipeline] = useState<string | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const pipelines = useQuery({
    queryKey: runKey(repo.id, 'pipelines', ref),
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/pipelines', {
          params: { path: repoParams(repo), query: { ref } },
        }),
      ),
    enabled: opened,
    retry: false,
  });
  const manual = (pipelines.data ?? []).filter((p) => p.manual);
  const selected = manual.find((p) => p.id === pipeline) ?? (manual.length === 1 ? manual[0] : undefined);
  const start = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/code/repos/{key}/{slug}/runs', {
          params: { path: repoParams(repo) },
          body: { ref, pipeline: selected?.id, variables: values },
        }),
      ),
    onSuccess: (runs) => {
      void client.invalidateQueries({ queryKey: runKey(repo.id) });
      onClose();
      if (runs.length === 1) navigate(runPath(repo, runs[0].number));
    },
  });
  return (
    <Modal opened={opened} onClose={onClose} title={t('builds.runPipeline')}>
      <Stack>
        <RefSelect repo={repo} value={ref} onChange={setRef} label={t('builds.ref')} />
        {pipelines.isLoading && <Loader size="sm" />}
        {pipelines.data && manual.length === 0 && <Text c="dimmed">{t('builds.noManualPipelines')}</Text>}
        {manual.length > 1 && (
          <Select
            label={t('builds.pipeline')}
            data={manual.map((p) => ({ value: p.id, label: p.name }))}
            value={selected?.id ?? null}
            onChange={setPipeline}
          />
        )}
        {selected &&
          Object.entries(selected.variables).map(([name, v]) =>
            v.options?.length ? (
              <Select
                key={name}
                label={name}
                description={v.description}
                data={v.options}
                value={values[name] ?? v.default}
                onChange={(x) => setValues((s) => ({ ...s, [name]: x ?? '' }))}
              />
            ) : (
              <TextInput
                key={name}
                label={name}
                description={v.description}
                placeholder={v.default}
                value={values[name] ?? ''}
                onChange={(e) => {
                  const value = e.currentTarget.value;
                  setValues((s) => ({ ...s, [name]: value }));
                }}
              />
            ),
          )}
        <ErrorAlert error={start.error ?? pipelines.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            leftSection={<IconPlayerPlay size={14} />}
            onClick={() => start.mutate()}
            loading={start.isPending}
            disabled={!selected}
          >
            {t('builds.start')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

export function WaitingLabel({ waitingFor }: { waitingFor?: string }) {
  const { t } = useTranslation();
  if (!waitingFor) return null;
  return (
    <Group gap={4}>
      <IconHandStop size={14} />
      <Text size="xs">{t(`builds.waitingFor.${waitingFor}` as 'builds.waitingFor.manual')}</Text>
    </Group>
  );
}
