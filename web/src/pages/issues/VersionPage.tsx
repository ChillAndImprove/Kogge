// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Badge,
  Button,
  Card,
  Group,
  Progress,
  SimpleGrid,
  Stack,
  Table,
  Text,
  Title,
  Tooltip,
} from '@mantine/core';
import { IconArrowLeft, IconFileText, IconRocket } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatDate, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { LinkedPageModal, type LinkedPageKind } from './LinkedPage';
import { useProjectContext } from './ProjectLayout';
import { kqlString } from './ProjectReleases';
import { StatusBadge, TypeIcon, UserBadge } from './shared';

/** A version: progress, issues, where they are deployed. */
export function VersionPage() {
  const { t } = useTranslation();
  const project = useProjectContext();
  const client = useQueryClient();
  const { versionId = '0' } = useParams();
  const version = project.versions.find((v) => v.id === Number(versionId));
  const admin = project.permissions.includes('project.admin');
  const development = project.permissions.includes('project.view-development');
  const [notes, setNotes] = useState<LinkedPageKind | null>(null);
  const query = version
    ? `project = ${project.key} AND fixVersion = ${kqlString(version.name)} ORDER BY rank`
    : '';
  const issues = useQuery({
    queryKey: ['issue', 'search', query, 'version'],
    queryFn: () => unwrap(api.GET('/api/v1/search', { params: { query: { q: query, limit: 500 } } })),
    enabled: !!version,
  });
  const deployments = useQuery({
    queryKey: ['run', 'version', project.key, versionId],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/projects/{key}/versions/{id}/deployments', {
          params: { path: { key: project.key, id: Number(versionId) } },
        }),
      ),
    enabled: !!version && development,
  });
  const release = useMutation({
    mutationFn: (released: boolean) =>
      unwrap(
        api.PUT('/api/v1/projects/{key}/versions/{id}', {
          params: { path: { key: project.key, id: version!.id } },
          body: {
            name: version!.name,
            description: version!.description,
            startDate: version!.startDate,
            releaseDate: version!.releaseDate,
            released,
            archived: version!.archived,
          },
        }),
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: ['project', project.key] }),
  });
  if (!version) return <ErrorAlert error={new Error(t('errors.notFound'))} />;
  const list = issues.data?.issues ?? [];
  const counts = { todo: 0, inprogress: 0, done: 0 };
  for (const i of list) counts[(i.status?.category ?? 'todo') as keyof typeof counts]++;
  const total = Math.max(list.length, 1);
  return (
    <Stack>
      <Anchor component={Link} to={`/projects/${project.key}/releases`} size="sm">
        <Group gap={4}>
          <IconArrowLeft size={14} />
          {t('issues.releases')}
        </Group>
      </Anchor>
      <Group justify="space-between" align="flex-start">
        <Stack gap={4}>
          <Group gap="sm">
            <Title order={2}>{version.name}</Title>
            {version.archived ? (
              <Badge color="gray">{t('issues.archived')}</Badge>
            ) : version.released ? (
              <Badge color="teal">{t('issues.released')}</Badge>
            ) : (
              <Badge color="blue" variant="light">
                {t('issues.unreleased')}
              </Badge>
            )}
          </Group>
          <Text size="sm" c="dimmed">
            {version.startDate ? `${t('issues.startDate')}: ${formatDate(version.startDate)} · ` : ''}
            {version.releaseDate ? `${t('issues.releaseDate')}: ${formatDate(version.releaseDate)}` : ''}
          </Text>
          {version.description && <Text size="sm">{version.description}</Text>}
        </Stack>
        <Group>
          <Button
            variant="default"
            leftSection={<IconFileText size={14} />}
            onClick={() =>
              setNotes({
                template: 'release-notes',
                title: t('linkedPage.releaseNotesTitle', { version: version.name, project: project.name }),
                variables: { version: version.name },
                query: `project = ${project.key} AND fixVersion = ${kqlString(version.name)} ORDER BY type, key`,
              })
            }
          >
            {t('linkedPage.createReleaseNotes')}
          </Button>
          {admin && !version.archived && (
            <Button
              onClick={() => release.mutate(!version.released)}
              loading={release.isPending}
              variant={version.released ? 'default' : 'filled'}
            >
              {version.released ? t('issues.unrelease') : t('issues.release')}
            </Button>
          )}
        </Group>
      </Group>
      <ErrorAlert error={issues.error ?? deployments.error ?? release.error} />
      <Card withBorder>
        <Group justify="space-between" mb="xs">
          <Text fw={500}>{t('releases.progress')}</Text>
          <Text size="sm" c="dimmed">
            {t('releases.doneOf', { done: counts.done, total: list.length })}
          </Text>
        </Group>
        <Progress.Root size="lg">
          <Tooltip label={`${t('releases.category.done')}: ${counts.done}`}>
            <Progress.Section value={(counts.done / total) * 100} color="teal" />
          </Tooltip>
          <Tooltip label={`${t('releases.category.inprogress')}: ${counts.inprogress}`}>
            <Progress.Section value={(counts.inprogress / total) * 100} color="blue" />
          </Tooltip>
          <Tooltip label={`${t('releases.category.todo')}: ${counts.todo}`}>
            <Progress.Section value={(counts.todo / total) * 100} color="gray.4" />
          </Tooltip>
        </Progress.Root>
      </Card>
      {(deployments.data ?? []).length > 0 && (
        <Stack gap="xs">
          <Title order={4}>{t('releases.deployments')}</Title>
          <SimpleGrid cols={{ base: 1, md: 2, lg: 3 }}>
            {(deployments.data ?? []).map((d) => {
              const all = d.deployed.length + d.pending.length;
              return (
                <Card withBorder key={`${d.repository.id}:${d.environment}`}>
                  <Group gap="xs" mb={4}>
                    <IconRocket size={16} />
                    <Text fw={600}>{d.environment}</Text>
                    <Text size="xs" c="dimmed">
                      {d.repository.projectKey}/{d.repository.slug}
                    </Text>
                  </Group>
                  <Text size="sm">
                    {t('releases.deployedOf', { deployed: d.deployed.length, total: all })}
                  </Text>
                  <Text size="xs" c="dimmed">
                    <Anchor
                      component={Link}
                      to={`/code/${d.repository.projectKey.toLowerCase()}/${d.repository.slug}/builds/${d.runNumber}`}
                      size="xs"
                    >
                      #{d.runNumber}
                    </Anchor>{' '}
                    · {formatRelative(d.finished)}
                  </Text>
                  {d.pending.length > 0 && (
                    <Text size="xs" mt={4} c="orange">
                      {t('releases.notYet')}: {d.pending.join(', ')}
                    </Text>
                  )}
                </Card>
              );
            })}
          </SimpleGrid>
        </Stack>
      )}
      <Card withBorder padding="xs">
        <Table>
          <Table.Tbody>
            {list.map((i) => (
              <Table.Tr key={i.key}>
                <Table.Td w={30}>
                  <TypeIcon type={i.type} size={16} />
                </Table.Td>
                <Table.Td w={110}>
                  <Anchor component={Link} to={`/browse/${i.key}`} size="sm">
                    {i.key}
                  </Anchor>
                </Table.Td>
                <Table.Td>
                  <Text size="sm">{i.summary}</Text>
                </Table.Td>
                <Table.Td w={150}>
                  <StatusBadge status={i.status} />
                </Table.Td>
                <Table.Td w={180}>{i.assignee ? <UserBadge user={i.assignee} size="xs" /> : '—'}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
        {issues.data && list.length === 0 && (
          <Text c="dimmed" ta="center" py="md">
            {t('releases.noIssues')}
          </Text>
        )}
      </Card>
      {notes && <LinkedPageModal projectKey={project.key} kind={notes} onClose={() => setNotes(null)} />}
    </Stack>
  );
}
