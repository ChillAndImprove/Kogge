// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Alert,
  Anchor,
  Badge,
  Button,
  Card,
  Checkbox,
  Group,
  Skeleton,
  Stack,
  Text,
  TextInput,
} from '@mantine/core';
import { IconFile, IconSearch } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useState, type FormEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { encodePath, repoParams, repoPath, useRepo, type Repository } from './hooks';

/** The search box of a repository: it searches at the branch or tag being viewed. */
export function RepoSearchBox({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [q, setQ] = useState('');
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (q.trim().length < 2) return;
    const next = new URLSearchParams({ q: q.trim() });
    const at = params.get('at');
    if (at) next.set('at', at);
    navigate(`${repoPath(repo, 'search')}?${next}`);
    setQ(''); // the search page has its own field with the query
  };
  return (
    <form onSubmit={submit} role="search" aria-label={t('code.search.placeholder')}>
      <TextInput
        value={q}
        onChange={(e) => setQ(e.currentTarget.value)}
        placeholder={t('code.search.placeholder')}
        aria-label={t('code.search.placeholder')}
        leftSection={<IconSearch size={16} />}
        w={{ base: '100%', xs: 260 }}
      />
    </form>
  );
}

/** Marks what matched in a line (as text: nothing is interpreted as markup). */
function highlight(text: string, query: string, regex: boolean, caseSensitive: boolean): ReactNode[] {
  let re: RegExp;
  try {
    const source = regex ? query : query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    re = new RegExp(source, caseSensitive ? 'g' : 'gi');
  } catch {
    return [text];
  }
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(re)) {
    if (m.index === undefined || m[0] === '') continue;
    if (m.index > last) out.push(text.slice(last, m.index));
    out.push(<mark key={m.index}>{m[0]}</mark>);
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

interface SearchParams {
  q: string;
  regex: boolean;
  caseSensitive: boolean;
  path: string;
}

/** The lines of a repository's files that contain a text or match an expression. */
export function CodeSearch() {
  const { t } = useTranslation();
  const repo = useRepo();
  const [params, setParams] = useSearchParams();
  const q = params.get('q') ?? '';
  const at = params.get('at') ?? '';
  const regex = params.get('regex') === '1';
  const caseSensitive = params.get('case') === '1';
  const path = params.get('path') ?? '';
  const search = useQuery({
    queryKey: ['code-search', repo.id, q, at, regex, caseSensitive, path],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/search', {
          params: {
            path: repoParams(repo),
            query: { q, ref: at || undefined, regex, case: caseSensitive, path: path || undefined },
          },
        }),
      ),
    enabled: q.trim().length >= 2,
    retry: false,
  });
  const submit = (draft: SearchParams) => {
    const next = new URLSearchParams({ q: draft.q.trim() });
    if (at) next.set('at', at);
    if (draft.regex) next.set('regex', '1');
    if (draft.caseSensitive) next.set('case', '1');
    if (draft.path.trim()) next.set('path', draft.path.trim());
    setParams(next);
  };
  const ref = search.data?.ref ?? (at || repo.defaultBranch);
  const fileLink = (file: string, line?: number) =>
    `${repoPath(repo, 'src', encodePath(file))}${at ? `?at=${encodeURIComponent(at)}` : ''}${line ? `#L${line}` : ''}`;
  return (
    <Stack>
      {/* Keyed by the query, so that a search from the box in the header shows here too. */}
      <SearchForm
        key={params.toString()}
        initial={{ q, regex, caseSensitive, path }}
        refName={ref}
        autoFocus={!q}
        onSubmit={submit}
      />
      <ErrorAlert error={search.error} />
      {search.isFetching && !search.data && <Skeleton h={120} />}
      {search.data && (
        <>
          <Text size="sm" c="dimmed" role="status">
            {search.data.matches === 0
              ? t('code.search.none')
              : t('code.search.summary', {
                  matches: t('code.search.matches', { count: search.data.matches }),
                  files: t('code.search.inFiles', { count: search.data.files.length }),
                })}
          </Text>
          {!search.data.complete && (
            <Alert color="orange" variant="light">
              {t('code.search.incomplete')}
            </Alert>
          )}
          {search.data.files.map((f) => (
            <Card key={f.path} withBorder padding={0}>
              <Group gap="xs" px="sm" py={6} className="kogge-search-file">
                <IconFile size={14} aria-hidden />
                <Anchor component={Link} to={fileLink(f.path)} size="sm" fw={600} ff="monospace">
                  {f.path}
                </Anchor>
                <Text size="xs" c="dimmed">
                  {f.more
                    ? t('code.search.linesMore', { count: f.lines.length })
                    : t('code.search.lines', { count: f.lines.length })}
                </Text>
              </Group>
              <div className="kogge-search-lines">
                {f.lines.map((l) => (
                  <div key={l.line} className="kogge-search-line">
                    <Anchor
                      component={Link}
                      to={fileLink(f.path, l.line)}
                      className="kogge-search-number"
                      aria-label={t('code.search.lineOf', { line: l.line, path: f.path })}
                    >
                      {l.line}
                    </Anchor>
                    <code>{highlight(l.text, q, regex, caseSensitive)}</code>
                  </div>
                ))}
              </div>
            </Card>
          ))}
        </>
      )}
    </Stack>
  );
}

function SearchForm({
  initial,
  refName,
  autoFocus,
  onSubmit,
}: {
  initial: SearchParams;
  refName: string;
  autoFocus: boolean;
  onSubmit: (p: SearchParams) => void;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(initial);
  return (
    <form
      role="search"
      aria-label={t('code.search.query')}
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        onSubmit(draft);
      }}
    >
      <Stack gap="xs">
        <Group align="flex-end" gap="sm" wrap="wrap">
          <TextInput
            label={t('code.search.query')}
            value={draft.q}
            onChange={(e) => setDraft({ ...draft, q: e.currentTarget.value })}
            leftSection={<IconSearch size={16} />}
            style={{ flex: 1, minWidth: 260 }}
            autoFocus={autoFocus}
          />
          <TextInput
            label={t('code.search.path')}
            placeholder="src/"
            value={draft.path}
            onChange={(e) => setDraft({ ...draft, path: e.currentTarget.value })}
            w={220}
          />
          <Button type="submit" disabled={draft.q.trim().length < 2}>
            {t('code.search.submit')}
          </Button>
        </Group>
        <Group gap="lg">
          <Checkbox
            label={t('code.search.regex')}
            checked={draft.regex}
            onChange={(e) => setDraft({ ...draft, regex: e.currentTarget.checked })}
          />
          <Checkbox
            label={t('code.search.caseSensitive')}
            checked={draft.caseSensitive}
            onChange={(e) => setDraft({ ...draft, caseSensitive: e.currentTarget.checked })}
          />
          <Text size="sm" c="dimmed">
            {t('code.search.at')}{' '}
            <Badge variant="light" tt="none">
              {refName}
            </Badge>
          </Text>
        </Group>
      </Stack>
    </form>
  );
}
