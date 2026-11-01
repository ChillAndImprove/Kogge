// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Alert, Anchor, Card, Code, Group, List, Popover, Stack, Text } from '@mantine/core';
import { IconCircleCheck, IconClock } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { encodePath, repoParams, repoPath, type PullRequest, type Repository } from './hooks';

type OwnedFiles = Schemas['OwnedFiles'];

/** The owners of a line of CODEOWNERS, as words: people, groups, and what names nobody. */
function ownerNames(t: TFunction, o: Pick<OwnedFiles, 'users' | 'groups' | 'unknown'>): string {
  const names = [
    ...o.users.map((u) => u.displayName),
    ...o.groups.map((g) => t('code.owners.group', { name: g.name })),
  ];
  if (o.unknown.length > 0) names.push(t('code.owners.unknown', { names: o.unknown.join(', ') }));
  return names.join(', ');
}

/** The changed files of a pull request by their code owners, with who approved them. */
export function CodeOwnersCard({ repo, pr }: { repo: Repository; pr: PullRequest }) {
  const { t } = useTranslation();
  if (!pr.codeOwners || pr.codeOwners.length === 0) return null;
  return (
    <Card withBorder>
      <Text fw={600} size="sm" mb="xs">
        {t('code.owners.title')}
      </Text>
      <Stack gap={8} role="list" aria-label={t('code.owners.title')}>
        {pr.codeOwners.map((o) => {
          const approved = o.approvedBy.length > 0;
          return (
            <Group key={o.line} gap={6} wrap="nowrap" align="flex-start" role="listitem">
              {approved ? (
                <IconCircleCheck
                  size={16}
                  color="var(--mantine-color-teal-6)"
                  style={{ flexShrink: 0, marginTop: 1 }}
                  aria-hidden
                />
              ) : (
                <IconClock
                  size={16}
                  color="var(--mantine-color-dimmed)"
                  style={{ flexShrink: 0, marginTop: 1 }}
                  aria-hidden
                />
              )}
              <div style={{ minWidth: 0 }}>
                <Text size="xs" fw={500} style={{ overflowWrap: 'anywhere' }}>
                  {ownerNames(t, o)}
                </Text>
                <Text size="xs" c="dimmed">
                  <OwnedFileList files={o} />
                  {' · '}
                  {approved
                    ? t('code.owners.approvedBy', {
                        names: o.approvedBy.map((u) => u.displayName).join(', '),
                      })
                    : t('code.owners.waiting')}
                </Text>
              </div>
            </Group>
          );
        })}
      </Stack>
      {pr.codeOwnersFile && (
        <Text size="xs" c="dimmed" mt="sm">
          {t('code.owners.source')}:{' '}
          <Anchor
            component={Link}
            to={`${repoPath(repo, 'src', encodePath(pr.codeOwnersFile))}?at=${encodeURIComponent(pr.target)}`}
            size="xs"
            underline="always"
          >
            {pr.codeOwnersFile}
          </Anchor>{' '}
          ({pr.target})
        </Text>
      )}
    </Card>
  );
}

/** The number of files, which opens their list. */
function OwnedFileList({ files }: { files: OwnedFiles }) {
  const { t } = useTranslation();
  return (
    <Popover width={320} position="bottom-start" shadow="md" withArrow>
      <Popover.Target>
        <Anchor component="button" type="button" size="xs">
          {t('code.owners.files', { count: files.fileCount })}
        </Anchor>
      </Popover.Target>
      <Popover.Dropdown>
        <Text size="xs" c="dimmed" mb={4}>
          <Code>{files.pattern}</Code>
        </Text>
        <List size="xs" spacing={2}>
          {files.files.map((f) => (
            <List.Item key={f} style={{ overflowWrap: 'anywhere' }}>
              <Text span size="xs" ff="monospace">
                {f}
              </Text>
            </List.Item>
          ))}
        </List>
        {files.fileCount > files.files.length && (
          <Text size="xs" c="dimmed" mt={4}>
            {t('code.owners.more', { count: files.fileCount - files.files.length })}
          </Text>
        )}
      </Popover.Dropdown>
    </Popover>
  );
}

/** The owners of a file, in its header (nothing if it has none). */
export function FileOwners({ repo, gitRef, path }: { repo: Repository; gitRef: string; path: string }) {
  const { t } = useTranslation();
  const owners = useQuery({
    queryKey: ['repository', repo.id, 'code-owners', gitRef, path],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/code-owners', {
          params: { path: repoParams(repo), query: { ref: gitRef, path } },
        }),
      ),
    staleTime: 60_000,
  });
  const o = owners.data?.owners;
  if (!o) return null;
  return (
    <Text size="xs" c="dimmed">
      {t('code.owners.fileOwners', { names: ownerNames(t, o) })}
    </Text>
  );
}

/** The CODEOWNERS file of the default branch as Kogge reads it: where it is and what does not work. */
export function CodeOwnersSummary({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const file = useQuery({
    queryKey: ['repository', repo.id, 'code-owners', repo.defaultBranch],
    queryFn: () =>
      unwrap(api.GET('/api/v1/code/repos/{key}/{slug}/code-owners', { params: { path: repoParams(repo) } })),
  });
  if (file.error) return <ErrorAlert error={file.error} />;
  const f = file.data;
  if (!f) return null;
  if (!f.path) {
    return (
      <Text size="xs" c="dimmed">
        {t('code.owners.none')}
      </Text>
    );
  }
  return (
    <Stack gap={6}>
      <Text size="xs" c="dimmed">
        <Anchor
          component={Link}
          to={`${repoPath(repo, 'src', encodePath(f.path))}?at=${encodeURIComponent(f.ref)}`}
          size="xs"
        >
          {t('code.owners.file', { path: f.path, branch: f.ref, count: f.rules.length })}
        </Anchor>
      </Text>
      {f.problems.length > 0 && (
        <Alert
          color="orange"
          variant="light"
          p="xs"
          title={t('code.owners.problems', { count: f.problems.length })}
        >
          <List size="xs" spacing={2}>
            {f.problems.map((p, i) => (
              <List.Item key={i}>
                {p.line ? t('code.owners.line', { line: p.line }) : t('code.owners.whole')}:{' '}
                {t(`code.owners.problem.${p.code}`, { ...p.values, defaultValue: p.message })}
              </List.Item>
            ))}
          </List>
        </Alert>
      )}
    </Stack>
  );
}
