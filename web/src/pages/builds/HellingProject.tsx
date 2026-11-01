// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Card, Group, Stack, Table, Tabs, Text } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { ListPages } from '../../components/Pagination';
import { repoParams, useRepositoryPage, type Repository } from '../code/hooks';

/** Repositories per page. */
const pageSize = 25;
import { pipelinesPath, runKey, runPath } from './hooks';
import { ProjectVariables } from './Settings';
import { StateIcon } from './status';

/** The newest run of a repository's default branch. */
function LatestRun({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const runs = useQuery({
    queryKey: runKey(repo.id, 'latest', repo.defaultBranch),
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/runs', {
          params: { path: repoParams(repo), query: { branch: repo.defaultBranch, limit: 1 } },
        }),
      ),
  });
  const run = runs.data?.items?.[0];
  if (!run) {
    return (
      <Text size="sm" c="dimmed">
        {runs.isLoading ? '' : t('helling.noRuns')}
      </Text>
    );
  }
  return (
    <Group gap="xs" wrap="nowrap">
      <StateIcon state={run.state} size={16} />
      <Anchor component={Link} to={runPath(repo, run.number)} size="sm">
        #{run.number} · {run.name}
      </Anchor>
      <Text size="xs" c="dimmed">
        {formatRelative(run.created)}
      </Text>
    </Group>
  );
}

/** A code project in Helling: its repositories with the state of their default branch, and its variables. */
export function HellingProject() {
  const { t } = useTranslation();
  const { key = '' } = useParams();
  const project = useQuery({
    queryKey: ['code-project', key.toUpperCase()],
    queryFn: () => unwrap(api.GET('/api/v1/code/projects/{key}', { params: { path: { key } } })),
  });
  const [page, setPage] = useState(1);
  // A page at a time: every row asks for the latest run of its repository.
  const repos = useRepositoryPage({ project: key, limit: pageSize, offset: (page - 1) * pageSize });
  if (project.error) return <ErrorAlert error={project.error} />;
  const p = project.data;
  return (
    <>
      <PageHeader
        title={p?.name ?? key.toUpperCase()}
        subtitle={t('helling.projectSubtitle', { key: key.toUpperCase() })}
      />
      <Tabs defaultValue="repositories" keepMounted={false}>
        <Tabs.List mb="md">
          <Tabs.Tab value="repositories">{t('helling.repositories')}</Tabs.Tab>
          {p?.canAdmin && <Tabs.Tab value="variables">{t('builds.variables')}</Tabs.Tab>}
        </Tabs.List>
        <Tabs.Panel value="repositories">
          <Stack>
            <ErrorAlert error={repos.error} />
            <Card withBorder padding="xs">
              <Table verticalSpacing="sm" highlightOnHover>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t('helling.repository')}</Table.Th>
                    <Table.Th>{t('helling.latestRun')}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {(repos.data?.items ?? []).map((r) => (
                    <Table.Tr key={r.id}>
                      <Table.Td>
                        <Anchor component={Link} to={pipelinesPath(r)} fw={550}>
                          {r.name}
                        </Anchor>
                        <Text size="xs" c="dimmed">
                          {r.defaultBranch}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <LatestRun repo={r} />
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Card>
            <ListPages total={repos.data?.total ?? 0} size={pageSize} page={page} onChange={setPage} />
          </Stack>
        </Tabs.Panel>
        {p?.canAdmin && (
          <Tabs.Panel value="variables">
            <ProjectVariables projectKey={p.key} />
          </Tabs.Panel>
        )}
      </Tabs>
    </>
  );
}
