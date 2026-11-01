// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Accordion,
  Alert,
  Anchor,
  Badge,
  Button,
  Card,
  Code,
  Group,
  Stack,
  Table,
  Tabs,
  Text,
  Textarea,
  Title,
} from '@mantine/core';
import {
  IconArrowLeft,
  IconDownload,
  IconFileImport,
  IconPlayerStop,
  IconRefresh,
} from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatBytes, formatDate } from '../../app/format';
import { useRecordVisit } from '../../app/visits';
import { ErrorAlert } from '../../components/ErrorAlert';
import { encodePath, repoPath, shortSha, srcPath, useRepo, type Repository } from '../code/hooks';
import { Duration, StateBadge, StateIcon, TimeAgo, WaitingLabel } from './components';
import { isGraph } from './graph';
import {
  pipelinesPath,
  runKey,
  runPath,
  useRun,
  type Deployment,
  type JobInfo,
  type RunDetail,
} from './hooks';
import { JobCard, resourcesText, skipText } from './JobCard';
import { JobGraph } from './JobGraph';
import { LogView } from './LogView';
import './builds.css';

type Included = Schemas['Included'];

/** Where an included file is shown: in its repository at the commit the run read it at. */
function includedPath(inc: Included, repo: Repository, commit: string) {
  if (!inc.repository) return srcPath(repo, inc.commit || commit, inc.path);
  const [projectKey, slug] = inc.repository.split('/');
  return (
    repoPath({ projectKey, slug }, 'src', encodePath(inc.path)) + (inc.commit ? `?at=${inc.commit}` : '')
  );
}

/** The name of an included file as errors and the run name it. */
function includedName(inc: Included) {
  return inc.repository ? `${inc.repository}:${inc.path}` : inc.path;
}

type RunLink = NonNullable<RunDetail['upstream']>;

/** A link to a run that a run followed or that followed it; runs of other repositories name their repository. */
function RunLinkAnchor({ link, from }: { link: RunLink; from: { project: { key: string }; slug: string } }) {
  const other = link.repository.projectKey !== from.project.key || link.repository.slug !== from.slug;
  return (
    <Anchor component={Link} to={runPath(link.repository, link.number)} size="sm">
      {other ? `${link.repository.name} › ` : ''}
      {link.name} #{link.number}
    </Anchor>
  );
}

/** The job shown first: a failed one, else a running or waiting one, else the first. */
function defaultJob(run: RunDetail): JobInfo | undefined {
  const jobs = run.stages.flatMap((s) => s.jobs);
  return (
    jobs.find((j) => j.state === 'failed') ??
    jobs.find((j) => j.state === 'running') ??
    jobs.find((j) => j.state === 'waiting') ??
    jobs[0]
  );
}

/** A pipeline run: its stages and jobs, the log of the selected job, artifacts, tests and deployments. */
export function RunPage() {
  const { t } = useTranslation();
  const repo = useRepo();
  const navigate = useNavigate();
  const client = useQueryClient();
  const { number = '0' } = useParams();
  const [search, setSearch] = useSearchParams();
  const run = useRun(repo, Number(number));
  useRecordVisit('run', run.data?.id);
  const refresh = () => client.invalidateQueries({ queryKey: runKey(repo.id) });
  const cancel = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/api/v1/builds/runs/{id}/cancel', { params: { path: { id: run.data!.id } } })),
    onSuccess: refresh,
  });
  const rerun = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/api/v1/builds/runs/{id}/rerun', { params: { path: { id: run.data!.id } } })),
    onSuccess: (r) => {
      void refresh();
      navigate(runPath(repo, r.number));
    },
  });
  if (run.error) return <ErrorAlert error={run.error} />;
  if (!run.data) return null;
  const r = run.data;
  const jobs = r.stages.flatMap((s) => s.jobs);
  const selectedID = Number(search.get('job')) || defaultJob(r)?.id;
  const selected = jobs.find((j) => j.id === selectedID);
  const active = r.state === 'queued' || r.state === 'running';

  return (
    <Stack>
      <Group justify="space-between" align="flex-start">
        <Stack gap={4}>
          <Anchor component={Link} to={pipelinesPath(repo)} size="sm">
            <Group gap={4}>
              <IconArrowLeft size={14} />
              {t('builds.runs')}
            </Group>
          </Anchor>
          <Group gap="sm">
            <Title order={3}>
              {r.name} #{r.number}
            </Title>
            <StateBadge state={r.state} />
          </Group>
          <Group gap="xs">
            {r.branch && (
              <Badge variant="outline" color="gray" tt="none">
                {r.branch}
              </Badge>
            )}
            {r.tag && (
              <Badge variant="outline" color="grape" tt="none">
                {r.tag}
              </Badge>
            )}
            {r.pullRequest ? (
              <Anchor component={Link} to={repoPath(repo, 'pull-requests', String(r.pullRequest))} size="sm">
                PR #{r.pullRequest}
              </Anchor>
            ) : null}
            <Anchor component={Link} to={repoPath(repo, 'commits', r.commit)} size="sm" ff="monospace">
              {shortSha(r.commit)}
            </Anchor>
            <Text size="sm">{r.subject}</Text>
          </Group>
          <Text size="sm" c="dimmed">
            {r.upstream ? (
              <>
                {t('builds.after')} <RunLinkAnchor link={r.upstream} from={repo} />
              </>
            ) : (
              t(`builds.event.${r.event}`)
            )}
            {r.triggeredBy ? ` · ${r.triggeredBy.displayName}` : ''}
            {r.trigger ? ` ${t('builds.viaTrigger', { name: r.trigger })}` : ''} · <TimeAgo at={r.created} />
            {r.started && (
              <>
                {' · '}
                <Duration started={r.started} finished={r.finished} />
              </>
            )}
          </Text>
        </Stack>
        {r.canRun && (
          <Group>
            {active && (
              <Button
                variant="default"
                color="red"
                leftSection={<IconPlayerStop size={14} />}
                onClick={() => cancel.mutate()}
                loading={cancel.isPending}
              >
                {t('builds.cancel')}
              </Button>
            )}
            {!active && r.state !== 'error' && (
              <Button
                variant="default"
                leftSection={<IconRefresh size={14} />}
                onClick={() => rerun.mutate()}
                loading={rerun.isPending}
              >
                {t('builds.rerun')}
              </Button>
            )}
          </Group>
        )}
      </Group>
      {r.downstream.length > 0 && (
        <Group gap="sm" role="list" aria-label={t('builds.followedBy')}>
          <Text size="sm" c="dimmed">
            {t('builds.followedBy')}
          </Text>
          {r.downstream.map((d) => (
            <Group
              key={`${d.repository.projectKey}/${d.repository.slug}/${d.number}`}
              gap={4}
              wrap="nowrap"
              role="listitem"
            >
              <StateIcon state={d.state} size={16} />
              <RunLinkAnchor link={d} from={repo} />
            </Group>
          ))}
        </Group>
      )}
      <ErrorAlert error={cancel.error ?? rerun.error} />
      {r.error && (
        <Alert color="red" title={t('builds.invalidFile')}>
          <Code block>{r.error.split('; ').join('\n')}</Code>
        </Alert>
      )}
      {Object.keys(r.variables).length > 0 && (
        <Group gap="xs">
          <Text size="sm" c="dimmed">
            {t('builds.runVariables')}:
          </Text>
          {Object.entries(r.variables).map(([k, v]) => (
            <Badge key={k} variant="light" color="gray" tt="none">
              {k}={v}
            </Badge>
          ))}
        </Group>
      )}
      {(r.file || r.includes.length > 0) && (
        <Group gap="xs">
          <IconFileImport size={16} aria-hidden="true" />
          {r.file && (
            <Text size="sm" c="dimmed">
              {t('builds.definedIn', { file: r.file })}
            </Text>
          )}
          {r.includes.length > 0 && (
            <>
              <Text size="sm" c="dimmed">
                {t('builds.includedFiles')}:
              </Text>
              <Group gap="xs" role="list" aria-label={t('builds.includedFiles')}>
                {r.includes.map((inc) => (
                  <span key={`${inc.repository ?? ''}:${inc.path}`} role="listitem">
                    <Anchor component={Link} to={includedPath(inc, repo, r.commit)} size="sm" ff="monospace">
                      {includedName(inc)}
                      {inc.repository && inc.commit
                        ? ` @ ${inc.ref ? `${inc.ref} ` : ''}${shortSha(inc.commit)}`
                        : ''}
                    </Anchor>
                  </span>
                ))}
              </Group>
            </>
          )}
        </Group>
      )}
      {r.stages.length > 0 && isGraph(r.stages) && (
        <JobGraph
          stages={r.stages}
          selected={selected?.id}
          canRun={r.canRun}
          onSelect={(id) => setSearch({ job: String(id) }, { replace: true })}
        />
      )}
      {r.stages.length > 0 && !isGraph(r.stages) && (
        <div className="kogge-stages">
          {r.stages.map((stage, i) => (
            <div className="kogge-stage" key={i}>
              <Text size="xs" fw={700} c="dimmed" tt="uppercase" mb={6}>
                {stage.name}
              </Text>
              <Stack gap={6}>
                {matrixGroups(stage.jobs).map((g) => {
                  const card = (j: JobInfo) => (
                    <JobCard
                      key={j.id}
                      job={j}
                      label={g.matrix ? matrixLabel(j) : undefined}
                      selected={j.id === selected?.id}
                      canRun={r.canRun}
                      onSelect={() => setSearch({ job: String(j.id) }, { replace: true })}
                    />
                  );
                  if (!g.matrix) return card(g.jobs[0]);
                  return (
                    <div
                      key={`matrix-${g.jobs[0].id}`}
                      className="kogge-matrix"
                      role="group"
                      aria-label={g.matrix}
                    >
                      <Group gap={6} wrap="nowrap" mb={4}>
                        <StateIcon state={groupState(g.jobs)} size={14} />
                        <Text size="xs" fw={600} truncate>
                          {g.matrix}
                        </Text>
                        <Text size="xs" c="dimmed" style={{ flexShrink: 0 }}>
                          {t('builds.jobCount', { count: g.jobs.length })}
                        </Text>
                      </Group>
                      <Stack gap={4}>{g.jobs.map(card)}</Stack>
                    </div>
                  );
                })}
              </Stack>
            </div>
          ))}
        </div>
      )}
      {selected && <JobPanel key={selected.id} job={selected} jobs={jobs} />}
    </Stack>
  );
}

/** Groups the jobs a matrix made (they follow each other in their stage); other jobs stand alone. */
function matrixGroups(jobs: JobInfo[]): { matrix?: string; jobs: JobInfo[] }[] {
  const out: { matrix?: string; jobs: JobInfo[] }[] = [];
  for (const j of jobs) {
    const last = out[out.length - 1];
    if (j.matrix && last?.matrix === j.matrix) last.jobs.push(j);
    else out.push({ matrix: j.matrix || undefined, jobs: [j] });
  }
  return out;
}

/** A matrix job by its values ("1.27, linux"), or by its own name when the pipeline file named it. */
function matrixLabel(j: JobInfo): string {
  const prefix = `${j.matrix} (`;
  return j.name.startsWith(prefix) && j.name.endsWith(')') ? j.name.slice(prefix.length, -1) : j.name;
}

/** The state of a matrix as a whole: what most needs attention first. */
function groupState(jobs: JobInfo[]): string {
  const states = jobs.map((j) => j.state);
  for (const s of ['failed', 'running', 'waiting', 'pending', 'created', 'cancelled'] as const) {
    if (states.includes(s)) return s;
  }
  return states.every((s) => s === 'skipped') ? 'skipped' : 'succeeded';
}

function JobPanel({ job, jobs }: { job: JobInfo; jobs: JobInfo[] }) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<string | null>('log');
  const tests = job.report;
  const needs = (job.needs ?? []).flatMap((id) => jobs.filter((j) => j.id === id).map((j) => j.name));
  const resources = job.resources ? resourcesText(t, job.resources) : '';
  return (
    <Card withBorder>
      <Group justify="space-between" mb="xs">
        <Group gap="xs">
          <StateIcon state={job.state} />
          <Title order={4}>{job.name}</Title>
          {job.attempt > 1 && (
            <Badge variant="light" color="gray">
              {t('builds.attempt', { n: job.attempt })}
            </Badge>
          )}
        </Group>
        <Text size="sm" c="dimmed">
          {job.agent && t('builds.onAgent', { agent: job.agent })}
          {job.exitCode != null && ` · ${t('builds.exitCode', { code: job.exitCode })}`}
        </Text>
      </Group>
      {(job.hasNeeds || resources) && (
        <Text size="sm" c="dimmed" mb="xs">
          {job.hasNeeds &&
            (needs.length > 0 ? t('builds.needsList', { jobs: needs.join(', ') }) : t('builds.needsNone'))}
          {job.hasNeeds && resources && ' · '}
          {resources && t('builds.resourcesOf', { resources })}
        </Text>
      )}
      {job.skip && (
        <Alert color="gray" mb="sm" py="xs">
          {skipText(t, job.skip)}
        </Alert>
      )}
      {job.failure && (
        <Alert color={job.state === 'cancelled' ? 'gray' : 'red'} mb="sm" py="xs">
          {job.failure}
        </Alert>
      )}
      <Tabs value={tab} onChange={setTab}>
        <Tabs.List mb="sm">
          <Tabs.Tab value="log">{t('builds.log')}</Tabs.Tab>
          {tests && (
            <Tabs.Tab value="tests">
              {t('builds.tests')} (
              {tests.failed + tests.errors > 0
                ? `${tests.failed + tests.errors}/${tests.total}`
                : tests.total}
              )
            </Tabs.Tab>
          )}
          {job.artifacts.length > 0 && (
            <Tabs.Tab value="artifacts">
              {t('builds.artifacts')} ({job.artifacts.length})
            </Tabs.Tab>
          )}
          {job.deployment && <Tabs.Tab value="deployment">{t('builds.deployment')}</Tabs.Tab>}
        </Tabs.List>
        <Tabs.Panel value="log">
          {job.state === 'created' || job.state === 'skipped' || (job.state === 'waiting' && !job.started) ? (
            <Text c="dimmed">
              {job.state === 'waiting' ? (
                <WaitingLabel waitingFor={job.waitingFor} />
              ) : (
                t('builds.notStarted')
              )}
            </Text>
          ) : job.state === 'pending' && !job.started ? (
            job.resources?.noAgent ? (
              <Alert color="yellow" py="xs">
                {t('builds.noAgentResources', { resources })}
              </Alert>
            ) : (
              <Text c="dimmed">
                {resources ? t('builds.pendingResources', { resources }) : t('builds.pendingHelp')}
              </Text>
            )
          ) : (
            <LogView
              key={`${job.id}-${job.attempt}`}
              jobId={job.id}
              attempt={job.attempt}
              running={job.state === 'running'}
            />
          )}
        </Tabs.Panel>
        {tests && (
          <Tabs.Panel value="tests">
            <Group mb="sm">
              <Badge color="green" variant="light">
                {t('builds.testsPassed', {
                  count: tests.total - tests.failed - tests.errors - tests.skipped,
                })}
              </Badge>
              {tests.failed + tests.errors > 0 && (
                <Badge color="red" variant="light">
                  {t('builds.testsFailed', { count: tests.failed + tests.errors })}
                </Badge>
              )}
              {tests.skipped > 0 && (
                <Badge color="gray" variant="light">
                  {t('builds.testsSkipped', { count: tests.skipped })}
                </Badge>
              )}
              <Text size="sm" c="dimmed">
                {(tests.durationMs / 1000).toFixed(1)} s
              </Text>
            </Group>
            {tests.failures.length > 0 && (
              <Accordion variant="separated">
                {tests.failures.map((f, i) => (
                  <Accordion.Item key={i} value={String(i)}>
                    <Accordion.Control>
                      <Text size="sm" fw={500}>
                        {f.class ? `${f.class}.` : ''}
                        {f.name}
                      </Text>
                      <Text size="xs" c="red">
                        {f.message}
                      </Text>
                    </Accordion.Control>
                    <Accordion.Panel>
                      {f.output ? <Code block>{f.output}</Code> : <Text c="dimmed">{f.suite}</Text>}
                    </Accordion.Panel>
                  </Accordion.Item>
                ))}
              </Accordion>
            )}
          </Tabs.Panel>
        )}
        {job.artifacts.length > 0 && (
          <Tabs.Panel value="artifacts">
            <Table>
              <Table.Tbody>
                {job.artifacts.map((a) => (
                  <Table.Tr key={a.id}>
                    <Table.Td>
                      <Anchor href={`/api/v1/builds/artifacts/${a.id}`}>
                        <Group gap={4}>
                          <IconDownload size={14} />
                          {a.name}.zip
                        </Group>
                      </Anchor>
                    </Table.Td>
                    <Table.Td>{formatBytes(a.size)}</Table.Td>
                    <Table.Td>
                      <Text size="sm" c="dimmed">
                        {a.expires ? t('builds.expires', { date: formatDate(a.expires) }) : ''}
                      </Text>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Tabs.Panel>
        )}
        {job.deployment && (
          <Tabs.Panel value="deployment">
            <DeploymentPanel deployment={job.deployment} />
          </Tabs.Panel>
        )}
      </Tabs>
    </Card>
  );
}

/** A deployment with its approvals; approvers decide here. */
export function DeploymentPanel({ deployment }: { deployment: Deployment }) {
  const { t } = useTranslation();
  const repo = useRepo();
  const client = useQueryClient();
  const [comment, setComment] = useState('');
  const decide = useMutation({
    mutationFn: (approved: boolean) =>
      unwrap(
        api.POST('/api/v1/builds/deployments/{id}/approval', {
          params: { path: { id: deployment.id } },
          body: { approved, comment },
        }),
      ),
    onSuccess: () => {
      setComment('');
      void client.invalidateQueries({ queryKey: runKey(repo.id) });
    },
  });
  const approved = deployment.approvals.filter((a) => a.approved).length;
  return (
    <Stack>
      <Group gap="xs">
        <Text fw={500}>{t('builds.deployTo', { environment: deployment.environment })}</Text>
        <StateBadge state={deployment.state} />
        {deployment.required > 0 && (
          <Text size="sm" c="dimmed">
            {t('builds.approvalsOf', { count: approved, required: deployment.required })}
          </Text>
        )}
      </Group>
      {deployment.approvals.map((a) => (
        <Group key={a.user.id} gap="xs">
          <StateIcon state={a.approved ? 'succeeded' : 'failed'} size={16} />
          <Text size="sm">
            {a.user.displayName} {a.approved ? t('builds.approved') : t('builds.rejected')}
          </Text>
          {a.comment && (
            <Text size="sm" c="dimmed">
              „{a.comment}“
            </Text>
          )}
        </Group>
      ))}
      {deployment.canApprove && (
        <Stack gap="xs">
          <Textarea
            placeholder={t('builds.approvalComment')}
            value={comment}
            onChange={(e) => setComment(e.currentTarget.value)}
            autosize
          />
          <Group>
            <Button color="green" onClick={() => decide.mutate(true)} loading={decide.isPending}>
              {t('builds.approve')}
            </Button>
            <Button
              color="red"
              variant="light"
              onClick={() => decide.mutate(false)}
              loading={decide.isPending}
            >
              {t('builds.reject')}
            </Button>
          </Group>
        </Stack>
      )}
      <ErrorAlert error={decide.error} />
    </Stack>
  );
}
