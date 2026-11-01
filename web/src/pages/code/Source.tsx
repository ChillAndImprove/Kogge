// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Box,
  Breadcrumbs,
  Button,
  Card,
  Code,
  Group,
  Image,
  SegmentedControl,
  Stack,
  Table,
  Text,
  Title,
} from '@mantine/core';
import {
  IconBook,
  IconDownload,
  IconFile,
  IconFolder,
  IconGitCommit,
  IconLink,
  IconPackage,
  IconPencil,
} from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useLocation, useNavigate, useParams, useSearchParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatBytes, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { DocView } from '../../components/editor/DocView';
import { FileOwners } from './CodeOwners';
import { CodeLines, PersonLabel, RefSelect } from './components';
import { EditFileModal } from './EditFile';
import {
  rawUrl,
  repoParams,
  repoPath,
  shortSha,
  srcPath,
  useBranches,
  useRepo,
  type Repository,
} from './hooks';

function EmptyRepository({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  return (
    <Card withBorder>
      <Title order={4} mb="xs">
        {t('code.emptyTitle')}
      </Title>
      <Text size="sm" mb="xs">
        {t('code.emptyText')}
      </Text>
      <Code block>{`git remote add origin ${repo.cloneHttp}\ngit push -u origin ${repo.defaultBranch}`}</Code>
    </Card>
  );
}

function PathCrumbs({ repo, gitRef, path }: { repo: Repository; gitRef: string; path: string }) {
  const parts = path.split('/').filter(Boolean);
  return (
    <Breadcrumbs separator="/">
      <Anchor component={Link} to={srcPath(repo, gitRef, '')} fw={600}>
        {repo.slug}
      </Anchor>
      {parts.map((p, i) =>
        i === parts.length - 1 ? (
          <Text key={i} fw={600}>
            {p}
          </Text>
        ) : (
          <Anchor key={i} component={Link} to={srcPath(repo, gitRef, parts.slice(0, i + 1).join('/'))}>
            {p}
          </Anchor>
        ),
      )}
    </Breadcrumbs>
  );
}

function Readme({ repo, gitRef, path }: { repo: Repository; gitRef: string; path: string }) {
  const { t } = useTranslation();
  const file = useQuery({
    queryKey: ['repository', repo.id, 'file', gitRef, path],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/file', {
          params: { path: repoParams(repo), query: { ref: gitRef, path } },
        }),
      ),
  });
  if (!file.data) return null;
  return (
    <Card withBorder padding={0}>
      <Group gap={6} px="md" py="xs" className="kogge-readme-head">
        <IconBook size={16} aria-hidden />
        <Text fw={600} size="sm">
          {t('code.readme')}
        </Text>
      </Group>
      <Box px="xl" py="lg">
        {file.data.markdown ? (
          <DocView doc={file.data.markdown} />
        ) : (
          <Box ff="monospace" style={{ whiteSpace: 'pre-wrap' }} fz="sm">
            {file.data.lines
              .map((l) => new DOMParser().parseFromString(l, 'text/html').body.textContent)
              .join('\n')}
          </Box>
        )}
      </Box>
    </Card>
  );
}

function Blame({ repo, gitRef, path }: { repo: Repository; gitRef: string; path: string }) {
  const blame = useQuery({
    queryKey: ['repository', repo.id, 'blame', gitRef, path],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/blame', {
          params: { path: repoParams(repo), query: { ref: gitRef, path } },
        }),
      ),
  });
  if (blame.error) return <ErrorAlert error={blame.error} />;
  if (!blame.data) return null;
  const start = new Map(blame.data.ranges.map((r) => [r.from, r]));
  return (
    <Box style={{ overflowX: 'auto' }}>
      <table className="kogge-code">
        <tbody>
          {blame.data.lines.map((html, i) => {
            const r = start.get(i + 1);
            return (
              <tr key={i}>
                <td
                  style={{
                    width: 260,
                    borderTop: r ? '1px solid var(--mantine-color-default-border)' : undefined,
                    verticalAlign: 'top',
                  }}
                >
                  {r && (
                    <Group gap={6} wrap="nowrap">
                      <Anchor
                        component={Link}
                        to={repoPath(repo, 'commits', r.commit.sha)}
                        size="xs"
                        ff="monospace"
                      >
                        {shortSha(r.commit.sha)}
                      </Anchor>
                      <Text size="xs" truncate title={r.commit.subject}>
                        {r.commit.author.user?.displayName ?? r.commit.author.name} ·{' '}
                        {formatRelative(r.commit.author.when)}
                      </Text>
                    </Group>
                  )}
                </td>
                <td className="ln">{i + 1}</td>
                <td className="src" dangerouslySetInnerHTML={{ __html: html || ' ' }} />
              </tr>
            );
          })}
        </tbody>
      </table>
    </Box>
  );
}

function FileView({ repo, gitRef, path }: { repo: Repository; gitRef: string; path: string }) {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const [mode, setMode] = useState<'view' | 'code' | 'blame'>('view');
  const [editing, setEditing] = useState(false);
  const branches = useBranches(repo, gitRef, 20);
  const onBranch = (branches.data?.items ?? []).some((b) => b.name === gitRef);
  const writable = (repo.permission === 'write' || repo.permission === 'admin') && !repo.archived;
  const file = useQuery({
    queryKey: ['repository', repo.id, 'file', gitRef, path],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/file', {
          params: { path: repoParams(repo), query: { ref: gitRef, path } },
        }),
      ),
  });
  const selected = /^#L(\d+)$/.exec(location.hash)?.[1];
  useEffect(() => {
    if (selected && file.data) document.getElementById(`L${selected}`)?.scrollIntoView({ block: 'center' });
  }, [selected, file.data]);
  if (file.error) return <ErrorAlert error={file.error} />;
  if (!file.data) return null;
  const f = file.data;
  const markdown = !!f.markdown;
  const show = mode === 'view' && !markdown ? 'code' : mode;
  return (
    <Card withBorder padding={0}>
      <Group
        justify="space-between"
        px="sm"
        py={6}
        style={{ borderBottom: '1px solid var(--mantine-color-default-border)' }}
      >
        <Group gap="xs">
          <Text size="xs" c="dimmed">
            {formatBytes(f.size)}
            {f.language ? ` · ${f.language}` : ''}
            {` · ${t('code.lineCount', { count: f.lines.length })}`}
          </Text>
          <FileOwners repo={repo} gitRef={gitRef} path={path} />
        </Group>
        <Group gap="xs">
          {!f.binary && !f.image && !f.lfs && (
            <SegmentedControl
              size="xs"
              value={show}
              onChange={(v) => setMode(v as typeof mode)}
              data={[
                ...(markdown ? [{ value: 'view', label: t('code.readme') }] : []),
                { value: 'code', label: t('code.source') },
                { value: 'blame', label: t('code.blame') },
              ]}
            />
          )}
          {writable && onBranch && !f.binary && !f.image && !f.lfs && !f.truncated && (
            <Button
              size="compact-xs"
              variant="default"
              leftSection={<IconPencil size={12} />}
              onClick={() => setEditing(true)}
            >
              {t('common.edit')}
            </Button>
          )}
          <Button
            size="compact-xs"
            variant="default"
            component={Link}
            to={
              repoPath(repo, 'commits') + `?at=${encodeURIComponent(gitRef)}&path=${encodeURIComponent(path)}`
            }
            leftSection={<IconGitCommit size={12} />}
          >
            {t('code.history')}
          </Button>
          <Button
            size="compact-xs"
            variant="default"
            component="a"
            href={rawUrl(repo, gitRef, path)}
            target="_blank"
            rel="noreferrer"
            leftSection={<IconLink size={12} />}
          >
            {t('code.raw')}
          </Button>
          <Button
            size="compact-xs"
            variant="default"
            component="a"
            href={rawUrl(repo, gitRef, path, true)}
            leftSection={<IconDownload size={12} />}
          >
            {t('code.download')}
          </Button>
        </Group>
      </Group>
      {f.truncated && (
        <Text size="xs" c="orange" px="sm" pt="xs">
          {t('code.truncated')}
        </Text>
      )}
      {f.image ? (
        <Box p="md">
          <Image src={rawUrl(repo, gitRef, path)} alt={path} fit="contain" mah={600} w="auto" />
        </Box>
      ) : f.lfs ? (
        <Text p="md" c="dimmed">
          {t('code.lfsFile', { size: formatBytes(f.lfs.size) })}
        </Text>
      ) : f.binary ? (
        <Text p="md" c="dimmed">
          {t('code.binaryFile')}
        </Text>
      ) : show === 'view' ? (
        <Box p="md">
          <DocView doc={f.markdown} />
        </Box>
      ) : show === 'blame' ? (
        <Blame repo={repo} gitRef={gitRef} path={path} />
      ) : (
        <CodeLines
          lines={f.lines}
          selected={selected ? Number(selected) : undefined}
          onSelect={(n) => navigate({ hash: `L${n}` }, { replace: true })}
        />
      )}
      {editing && (
        <EditFileModal
          repo={repo}
          branch={gitRef}
          path={path}
          base={f.commit}
          onClose={() => setEditing(false)}
        />
      )}
    </Card>
  );
}

/** Files and directories of a repository at a branch, tag or commit. */
export function Source() {
  const { t } = useTranslation();
  const repo = useRepo();
  const params = useParams();
  const [search] = useSearchParams();
  const navigate = useNavigate();
  const gitRef = search.get('at') || repo.defaultBranch;
  const path = (params['*'] ?? '')
    .split('/')
    .filter(Boolean)
    .map((p) => decodeURIComponent(p))
    .join('/');
  const tree = useQuery({
    queryKey: ['repository', repo.id, 'tree', gitRef, path],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/tree', {
          params: { path: repoParams(repo), query: { ref: gitRef, path } },
        }),
      ),
    retry: false,
    enabled: !repo.empty,
  });
  if (repo.empty) return <EmptyRepository repo={repo} />;
  const isFile = tree.data?.kind === 'file';
  return (
    <Stack>
      <Group justify="space-between">
        <Group>
          <RefSelect repo={repo} value={gitRef} onChange={(ref) => navigate(srcPath(repo, ref, path))} />
          <PathCrumbs repo={repo} gitRef={gitRef} path={path} />
        </Group>
        {!isFile && path === '' && (
          <Group gap="xs">
            {(['zip', 'tar.gz'] as const).map((format) => (
              <Button
                key={format}
                size="xs"
                variant="default"
                component="a"
                href={`/api/v1/code/repos/${repo.project.key}/${repo.slug}/archive?${new URLSearchParams({ ref: gitRef, format })}`}
                leftSection={<IconPackage size={14} />}
              >
                {t('code.downloadArchive', { format })}
              </Button>
            ))}
          </Group>
        )}
      </Group>
      {isFile ? (
        <FileView repo={repo} gitRef={gitRef} path={path} />
      ) : tree.error ? (
        <ErrorAlert error={tree.error} />
      ) : (
        tree.data && (
          <>
            <Card withBorder padding={0}>
              {tree.data.latest && (
                <Group
                  justify="space-between"
                  px="sm"
                  py={8}
                  style={{ borderBottom: '1px solid var(--mantine-color-default-border)' }}
                  wrap="nowrap"
                >
                  <Group gap="xs" wrap="nowrap" style={{ minWidth: 0 }}>
                    <PersonLabel person={tree.data.latest.author} />
                    <Anchor
                      component={Link}
                      to={repoPath(repo, 'commits', tree.data.latest.sha)}
                      size="sm"
                      truncate
                    >
                      {tree.data.latest.subject}
                    </Anchor>
                  </Group>
                  <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
                    {shortSha(tree.data.latest.sha)} · {formatRelative(tree.data.latest.committer.when)}
                  </Text>
                </Group>
              )}
              <Table verticalSpacing={6} highlightOnHover>
                <Table.Tbody>
                  {path !== '' && (
                    <Table.Tr>
                      <Table.Td colSpan={2}>
                        <Anchor
                          component={Link}
                          to={srcPath(repo, gitRef, path.split('/').slice(0, -1).join('/'))}
                          size="sm"
                        >
                          ..
                        </Anchor>
                      </Table.Td>
                    </Table.Tr>
                  )}
                  {tree.data.entries.map((e) => (
                    <Table.Tr key={e.path}>
                      <Table.Td>
                        <Group gap="xs" wrap="nowrap">
                          {e.type === 'dir' ? (
                            <IconFolder size={16} color="var(--mantine-color-blue-6)" />
                          ) : (
                            <IconFile size={16} color="var(--mantine-color-dimmed)" />
                          )}
                          {e.type === 'submodule' ? (
                            <Text size="sm">
                              {e.name}{' '}
                              <Text span size="xs" c="dimmed">
                                ({t('code.submodule')})
                              </Text>
                            </Text>
                          ) : (
                            <Anchor component={Link} to={srcPath(repo, gitRef, e.path)} size="sm">
                              {e.name}
                            </Anchor>
                          )}
                        </Group>
                      </Table.Td>
                      <Table.Td>
                        <Text size="xs" c="dimmed" ta="right">
                          {e.type === 'file' ? formatBytes(e.size ?? 0) : ''}
                        </Text>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Card>
            {tree.data.readme && <Readme repo={repo} gitRef={gitRef} path={tree.data.readme} />}
          </>
        )
      )}
    </Stack>
  );
}
