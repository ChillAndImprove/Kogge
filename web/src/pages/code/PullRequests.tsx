// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Card, Checkbox, Group, SegmentedControl, Stack, Tabs, Text, TextInput } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { IconArrowLeft, IconSearch } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { MarkdownEditor } from '../../components/MarkdownEditor';
import { ListPages } from '../../components/Pagination';
import { UsersPicker } from '../../components/Pickers';
import { PullRequestTable } from './CodeHome';
import { CommitList, RefSelect } from './components';
import { DiffView } from './DiffView';
import { repoParams, repoPath, useRepo } from './hooks';

/** Pull requests per page of a repository's list. */
const pageSize = 25;

/** The pull requests of a repository, a page at a time, most recently updated first. */
export function PullRequests() {
  const { t } = useTranslation();
  const repo = useRepo();
  const navigate = useNavigate();
  const [state, setState] = useState<'open' | 'merged' | 'declined' | 'all'>('open');
  const [query, setQuery] = useState('');
  const [debounced] = useDebouncedValue(query.trim(), 250);
  const [page, setPage] = useState(1);
  const prs = useQuery({
    queryKey: ['pull-request', repo.id, 'list', state, debounced, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/pull-requests', {
          params: {
            path: repoParams(repo),
            query: { state, query: debounced, limit: pageSize, offset: (page - 1) * pageSize },
          },
        }),
      ),
    placeholderData: (p) => p,
  });
  return (
    <Stack>
      <Group justify="space-between">
        <Group>
          <SegmentedControl
            value={state}
            onChange={(v) => {
              setState(v as typeof state);
              setPage(1);
            }}
            data={(['open', 'merged', 'declined', 'all'] as const).map((s) => ({
              value: s,
              label: t(`code.pr.${s}`),
            }))}
          />
          <TextInput
            leftSection={<IconSearch size={14} />}
            value={query}
            onChange={(e) => {
              setQuery(e.currentTarget.value);
              setPage(1);
            }}
            placeholder={t('common.search')}
            w={240}
            aria-label={t('common.search')}
          />
          <Text size="sm" c="dimmed" aria-live="polite">
            {!!prs.data?.total && t('code.pr.count', { count: prs.data.total })}
          </Text>
        </Group>
        {!repo.empty && !repo.archived && (
          <Button onClick={() => navigate(repoPath(repo, 'pull-requests', 'new'))}>{t('code.pr.new')}</Button>
        )}
      </Group>
      <ErrorAlert error={prs.error} />
      <Card withBorder padding="xs">
        <PullRequestTable prs={prs.data?.items ?? []} />
      </Card>
      <ListPages total={prs.data?.total ?? 0} size={pageSize} page={page} onChange={setPage} />
    </Stack>
  );
}

/** Opens a pull request: choose the branches, see the changes, describe them. */
export function NewPullRequest() {
  const { t } = useTranslation();
  const repo = useRepo();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [search] = useSearchParams();
  const [source, setSource] = useState(search.get('source') ?? '');
  const [target, setTarget] = useState(search.get('target') ?? repo.defaultBranch);
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [reviewers, setReviewers] = useState<number[]>([]);
  const [draft, setDraft] = useState(false);
  const [tab, setTab] = useState<string | null>('diff');
  const compare = useQuery({
    queryKey: ['repository', repo.id, 'compare', target, source],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/compare', {
          params: { path: repoParams(repo), query: { from: target, to: source } },
        }),
      ),
    enabled: !!source && !!target && source !== target,
    retry: false,
  });
  const suggestedTitle =
    compare.data?.commits.length === 1
      ? compare.data.commits[0].subject
      : source.replace(/^[a-z]+\//, '').replace(/[-_]/g, ' ');
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/code/repos/{key}/{slug}/pull-requests', {
          params: { path: repoParams(repo) },
          body: {
            title: title || suggestedTitle,
            descriptionMarkdown: description,
            source,
            target,
            reviewers,
            draft,
          },
        }),
      ),
    onSuccess: (pr) => {
      void client.invalidateQueries({ queryKey: ['pull-request'] });
      navigate(repoPath(repo, 'pull-requests', String(pr.number)));
    },
  });
  return (
    <Stack>
      <Group>
        <Button
          variant="subtle"
          leftSection={<IconArrowLeft size={14} />}
          onClick={() => navigate(repoPath(repo, 'pull-requests'))}
        >
          {t('code.pullRequests')}
        </Button>
      </Group>
      <Card withBorder>
        <Stack>
          <Group align="flex-end">
            <RefSelect
              repo={repo}
              value={source || '…'}
              onChange={setSource}
              tags={false}
              label={t('code.pr.source')}
            />
            <Text pb={6}>→</Text>
            <RefSelect
              repo={repo}
              value={target}
              onChange={setTarget}
              tags={false}
              label={t('code.pr.target')}
            />
          </Group>
          <TextInput
            label={t('code.pr.title')}
            value={title}
            placeholder={suggestedTitle}
            onChange={(e) => setTitle(e.currentTarget.value)}
          />
          <MarkdownEditor
            label={t('code.pr.description')}
            value={description}
            onChange={setDescription}
            minRows={4}
          />
          <UsersPicker label={t('code.pr.reviewers')} value={reviewers} onChange={setReviewers} />
          <Checkbox
            label={t('code.pr.draft')}
            description={t('code.pr.draftHelp')}
            checked={draft}
            onChange={(e) => setDraft(e.currentTarget.checked)}
          />
          <ErrorAlert error={create.error ?? compare.error} />
          <Group justify="flex-end">
            <Button
              onClick={() => create.mutate()}
              loading={create.isPending}
              disabled={!source || source === target || (compare.data?.commits.length ?? 0) === 0}
            >
              {t('code.pr.create')}
            </Button>
          </Group>
        </Stack>
      </Card>
      {compare.data && (
        <Tabs value={tab} onChange={setTab}>
          <Tabs.List mb="md">
            <Tabs.Tab value="diff">{t('code.pr.diff')}</Tabs.Tab>
            <Tabs.Tab value="commits">
              {t('code.pr.commits')} ({compare.data.commits.length}
              {compare.data.more ? '+' : ''})
            </Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="diff">
            {compare.data.diff && <DiffView repo={repo} diff={compare.data.diff} viewRef={source} />}
          </Tabs.Panel>
          <Tabs.Panel value="commits">
            <CommitList repo={repo} commits={compare.data.commits} />
            {compare.data.more && (
              <Text size="sm" c="dimmed" mt="xs">
                {t('code.newestCommitsOnly', { shown: compare.data.commits.length })}
              </Text>
            )}
          </Tabs.Panel>
        </Tabs>
      )}
    </Stack>
  );
}
