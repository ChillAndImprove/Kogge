// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Badge, Button, Card, Checkbox, Code, Group, Stack, Text, Title } from '@mantine/core';
import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatDateTime } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { CommitList, CopySha, PersonLabel, RefSelect } from './components';
import { DiffView } from './DiffView';
import { repoParams, repoPath, shortSha, srcPath, useRepo } from './hooks';

/** The history of a branch, tag or file. */
export function Commits() {
  const { t } = useTranslation();
  const repo = useRepo();
  const [search] = useSearchParams();
  const navigate = useNavigate();
  const gitRef = search.get('at') || repo.defaultBranch;
  const path = search.get('path') ?? '';
  const commits = useInfiniteQuery({
    queryKey: ['repository', repo.id, 'commits', gitRef, path],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/commits', {
          params: { path: repoParams(repo), query: { ref: gitRef, path, limit: 30, offset: pageParam } },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (last, pages) =>
      last.more ? pages.reduce((n, p) => n + p.commits.length, 0) : undefined,
    enabled: !repo.empty,
  });
  const list = commits.data?.pages.flatMap((p) => p.commits) ?? [];
  return (
    <Stack>
      <Group>
        <RefSelect
          repo={repo}
          value={gitRef}
          onChange={(ref) =>
            navigate(
              `${repoPath(repo, 'commits')}?at=${encodeURIComponent(ref)}${path ? `&path=${encodeURIComponent(path)}` : ''}`,
            )
          }
        />
        {path && (
          <Group gap={4}>
            <Code>{path}</Code>
            <Button
              size="compact-xs"
              variant="subtle"
              onClick={() => navigate(`${repoPath(repo, 'commits')}?at=${encodeURIComponent(gitRef)}`)}
            >
              ×
            </Button>
          </Group>
        )}
      </Group>
      <ErrorAlert error={commits.error} />
      <CommitList repo={repo} commits={list} />
      {commits.hasNextPage && (
        <Group justify="center">
          <Button
            variant="default"
            onClick={() => void commits.fetchNextPage()}
            loading={commits.isFetchingNextPage}
          >
            {t('code.loadMore')}
          </Button>
        </Group>
      )}
    </Stack>
  );
}

/** One commit with its changes. */
export function CommitPage() {
  const { t } = useTranslation();
  const repo = useRepo();
  const { sha = '' } = useParams();
  const [ignoreWhitespace, setIgnoreWhitespace] = useState(false);
  const commit = useQuery({
    queryKey: ['repository', repo.id, 'commit', sha, ignoreWhitespace],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/commits/{sha}', {
          params: { path: { ...repoParams(repo), sha }, query: { ignoreWhitespace } },
        }),
      ),
    placeholderData: (p) => p,
  });
  if (commit.error) return <ErrorAlert error={commit.error} />;
  if (!commit.data) return null;
  const c = commit.data;
  const [subject, ...body] = c.message.split('\n');
  return (
    <Stack>
      <Card withBorder>
        <Stack gap="xs">
          <Title order={4}>{subject}</Title>
          {body.join('\n').trim() && (
            <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>
              {body.join('\n').trim()}
            </Text>
          )}
          <Group justify="space-between">
            <Group gap="xs">
              <PersonLabel person={c.author} />
              <Text size="xs" c="dimmed">
                {formatDateTime(c.author.when)}
              </Text>
            </Group>
            <Group gap="xs">
              <Code>{shortSha(c.sha)}</Code>
              <CopySha sha={c.sha} />
              {c.parents.length > 0 && (
                <Text size="xs" c="dimmed">
                  {t('code.parents')}:{' '}
                  {c.parents.map((p) => (
                    <Anchor
                      key={p}
                      component={Link}
                      to={repoPath(repo, 'commits', p)}
                      size="xs"
                      ff="monospace"
                      mr={4}
                    >
                      {shortSha(p)}
                    </Anchor>
                  ))}
                </Text>
              )}
              <Button size="compact-xs" variant="default" component={Link} to={srcPath(repo, c.sha, '')}>
                {t('code.browseFiles')}
              </Button>
            </Group>
          </Group>
          {c.parents.length > 1 && (
            <Badge variant="light" w="fit-content">
              Merge
            </Badge>
          )}
        </Stack>
      </Card>
      <Checkbox
        size="xs"
        label={t('code.ignoreWhitespace')}
        checked={ignoreWhitespace}
        onChange={(e) => setIgnoreWhitespace(e.currentTarget.checked)}
      />
      {c.diff && <DiffView repo={repo} diff={c.diff} viewRef={c.sha} />}
    </Stack>
  );
}
