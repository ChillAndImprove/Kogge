// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Button,
  Code,
  CopyButton,
  Group,
  Modal,
  Select,
  Stack,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { IconBadge, IconCheck, IconCopy } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { repoParams, type Repository } from '../code/hooks';
import { pipelinesPath, runKey } from './hooks';

const all = '*';

/** A status badge of the repository's pipelines for READMEs and pages: preview, address and Markdown. */
export function BadgeButton({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const [opened, setOpened] = useState(false);
  const [pipeline, setPipeline] = useState(all);
  const [branch, setBranch] = useState('');
  const pipelines = useQuery({
    queryKey: runKey(repo.id, 'pipelines', repo.defaultBranch),
    queryFn: () =>
      unwrap(api.GET('/api/v1/code/repos/{key}/{slug}/pipelines', { params: { path: repoParams(repo) } })),
    enabled: opened,
    retry: false,
  });
  const origin = window.location.origin;
  const query = new URLSearchParams();
  if (branch.trim()) query.set('branch', branch.trim());
  if (pipeline !== all) query.set('pipeline', pipeline);
  const qs = query.toString();
  const image = `${origin}/api/v1/code/repos/${repo.project.key}/${repo.slug}/badge.svg${qs ? `?${qs}` : ''}`;
  const markdown = `[![${t('builds.badgeAlt')}](${image})](${origin}${pipelinesPath(repo)})`;
  const copy = (value: string) => (
    <CopyButton value={value}>
      {({ copied, copy: doCopy }) => (
        <Tooltip label={copied ? t('common.copied') : t('common.copy')}>
          <ActionIcon variant="subtle" onClick={doCopy} aria-label={t('common.copy')}>
            {copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
          </ActionIcon>
        </Tooltip>
      )}
    </CopyButton>
  );
  return (
    <>
      <Button variant="default" leftSection={<IconBadge size={18} />} onClick={() => setOpened(true)}>
        {t('builds.badge')}
      </Button>
      <Modal opened={opened} onClose={() => setOpened(false)} title={t('builds.badge')} size="lg">
        <Stack>
          <Text size="sm" c="dimmed">
            {t('builds.badgeHelp')}
          </Text>
          <Group grow align="flex-start">
            <Select
              label={t('builds.badgePipeline')}
              data={[
                { value: all, label: t('builds.badgeAllPipelines') },
                ...(pipelines.data ?? []).map((p) => ({ value: p.id, label: p.name || p.id })),
              ]}
              value={pipeline}
              onChange={(v) => setPipeline(v ?? all)}
              allowDeselect={false}
            />
            <TextInput
              label={t('builds.badgeBranch')}
              placeholder={repo.defaultBranch}
              value={branch}
              onChange={(e) => setBranch(e.currentTarget.value)}
            />
          </Group>
          <Group gap="sm">
            <Text size="sm" fw={500}>
              {t('builds.badgePreview')}
            </Text>
            <img src={image} alt={t('builds.badgeAlt')} height={20} />
          </Group>
          <Stack gap={4}>
            <Group justify="space-between">
              <Text size="sm" fw={500}>
                {t('builds.badgeMarkdown')}
              </Text>
              {copy(markdown)}
            </Group>
            <Code block style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
              {markdown}
            </Code>
          </Stack>
          <Stack gap={4}>
            <Group justify="space-between">
              <Text size="sm" fw={500}>
                {t('builds.badgeImage')}
              </Text>
              {copy(image)}
            </Group>
            <Code block style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
              {image}
            </Code>
          </Stack>
        </Stack>
      </Modal>
    </>
  );
}
