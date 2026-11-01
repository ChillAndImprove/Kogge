// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Alert, Group, Loader, List, Stack, Text } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { IconAlertTriangle, IconCircleCheck, IconInfoCircle } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';

type Problem = Schemas['Problem'];

/** A mistake (or a doubtful part) of a pipeline file: the included file, line and column it is at. */
function ProblemItem({ p }: { p: Problem }) {
  const { t } = useTranslation();
  return (
    <List.Item>
      {p.file || p.line ? (
        <Text span ff="monospace" size="xs" fw={600}>
          {p.file ? `${t('builds.inFile', { file: p.file })} ` : ''}
          {p.line ? `${t('builds.lineColumn', { line: p.line, column: p.column })} ` : ''}
        </Text>
      ) : null}
      <Text span size="sm">
        {p.message}
      </Text>
    </List.Item>
  );
}

/**
 * Checks a pipeline file as it is typed: its mistakes with file, line and column, or the pipelines it defines. For a
 * repository (and a branch of it), the files it includes are read as for a run that the user starts.
 */
export function PipelineCheck({
  content,
  repository,
  gitRef,
}: {
  content: string;
  repository?: string;
  gitRef?: string;
}) {
  const { t } = useTranslation();
  const [text] = useDebouncedValue(content, 400);
  const check = useQuery({
    queryKey: ['pipeline-check', repository ?? '', gitRef ?? '', text],
    queryFn: () =>
      unwrap(api.POST('/api/v1/builds/check', { body: { content: text, repository, ref: gitRef } })),
    enabled: text.trim().length > 0,
    staleTime: Infinity,
    placeholderData: (p) => p,
  });
  const c = check.data;
  if (!c) {
    return check.isFetching ? (
      <Group gap="xs">
        <Loader size="xs" />
        <Text size="sm" c="dimmed">
          {t('builds.checking')}
        </Text>
      </Group>
    ) : null;
  }
  if (c.valid) {
    return (
      <Stack gap="xs">
        <Alert
          color="teal"
          variant="light"
          icon={<IconCircleCheck size={18} />}
          title={t('builds.fileValid')}
          p="sm"
        >
          <List size="sm" spacing={2}>
            {c.pipelines.map((p) => (
              <List.Item key={p.id}>
                <Text span fw={600} size="sm">
                  {p.name || p.id}
                </Text>{' '}
                <Text span size="sm" c="dimmed">
                  — {p.triggers.map((tr) => t(`builds.event.${tr}` as 'builds.event.push')).join(', ')} ·{' '}
                  {t('builds.jobCount', { count: p.jobs })}
                  {p.file ? ` · ${t('builds.definedIn', { file: p.file })}` : ''}
                </Text>
              </List.Item>
            ))}
          </List>
          {c.includes.length > 0 && (
            <Text size="xs" c="dimmed" mt={4}>
              {t('builds.includesRead', {
                files: c.includes
                  .map((i) => (i.repository ? `${i.repository}:${i.path}` : i.path))
                  .join(', '),
              })}
            </Text>
          )}
          {c.unresolved && (
            <Text size="xs" c="dimmed" mt={4}>
              {t('builds.includesUnread')}
            </Text>
          )}
        </Alert>
        {c.warnings.length > 0 && (
          <Alert
            color="yellow"
            variant="light"
            icon={<IconInfoCircle size={18} />}
            title={t('builds.fileWarnings')}
            p="sm"
          >
            <List size="sm" spacing={2}>
              {c.warnings.map((p, i) => (
                <ProblemItem key={i} p={p} />
              ))}
            </List>
          </Alert>
        )}
      </Stack>
    );
  }
  return (
    <Alert
      color="red"
      variant="light"
      icon={<IconAlertTriangle size={18} />}
      title={t('builds.fileInvalid')}
      p="sm"
    >
      <List size="sm" spacing={2}>
        {c.problems.map((p, i) => (
          <ProblemItem key={i} p={p} />
        ))}
      </List>
    </Alert>
  );
}
