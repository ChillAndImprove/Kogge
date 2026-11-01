// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Checkbox, Group, Loader, Modal, Stack, Textarea, TextInput } from '@mantine/core';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { rawUrl, repoParams, repoPath, srcPath, type Repository } from './hooks';
import { PipelineCheck } from '../builds/PipelineCheck';
import { pipelineFilePath } from '../builds/Templates';

const monospace = { input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } };

/** Edits a file in the browser and commits it, to the branch or to a new branch for a pull request. */
export function EditFileModal({
  repo,
  branch,
  path: initialPath,
  base,
  template,
  message: initialMessage = '',
  onClose,
}: {
  repo: Repository;
  branch: string;
  /** The file; empty to let the user name a new one. */
  path: string;
  /** The commit the file was read at (existing files). */
  base?: string;
  /** Content of a new file. */
  template?: string;
  message?: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const existing = template === undefined && !!initialPath;
  const current = useQuery({
    queryKey: ['repository', repo.id, 'raw', base ?? branch, initialPath],
    queryFn: async () => {
      const res = await fetch(rawUrl(repo, base ?? branch, initialPath));
      if (!res.ok) throw new Error(res.statusText);
      return res.text();
    },
    enabled: existing,
    staleTime: Infinity,
  });
  const [path, setPath] = useState(initialPath);
  const [content, setContent] = useState<string | null>(template ?? null);
  const [message, setMessage] = useState(initialMessage);
  const [toBranch, setToBranch] = useState(false);
  const [newBranch, setNewBranch] = useState(
    `edit/${(initialPath.split('/').pop() || 'file').replace(/[^A-Za-z0-9._-]/g, '-')}`,
  );
  const text = content ?? current.data ?? '';
  const commit = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/code/repos/{key}/{slug}/files', {
          params: { path: repoParams(repo) },
          body: { branch, path, content: text, message, base, newBranch: toBranch ? newBranch : undefined },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['repository', repo.id] });
      onClose();
      if (toBranch) {
        navigate(
          repoPath(repo, 'pull-requests', 'new') +
            `?source=${encodeURIComponent(newBranch)}&target=${encodeURIComponent(branch)}`,
        );
      } else {
        navigate(srcPath(repo, branch, path));
      }
    },
  });
  return (
    <Modal
      opened
      onClose={onClose}
      title={existing ? t('code.editFile', { path: initialPath }) : t('code.newFile')}
      size="xl"
    >
      <Stack>
        {!existing && (
          <TextInput
            label={t('code.filePath')}
            value={path}
            onChange={(e) => setPath(e.currentTarget.value)}
            required
          />
        )}
        {existing && current.isLoading ? (
          <Loader />
        ) : (
          <Textarea
            aria-label={t('code.fileContent')}
            value={text}
            onChange={(e) => setContent(e.currentTarget.value)}
            autosize
            minRows={14}
            maxRows={28}
            styles={monospace}
            spellCheck={false}
          />
        )}
        {path === pipelineFilePath && (
          <PipelineCheck content={text} repository={`${repo.project.key}/${repo.slug}`} gitRef={branch} />
        )}
        <TextInput
          label={t('code.commitMessage')}
          placeholder={`Update ${path}`}
          value={message}
          onChange={(e) => setMessage(e.currentTarget.value)}
        />
        <Checkbox
          label={t('code.commitToNewBranch')}
          checked={toBranch}
          onChange={(e) => setToBranch(e.currentTarget.checked)}
        />
        {toBranch && (
          <TextInput
            label={t('code.newBranchName')}
            value={newBranch}
            onChange={(e) => setNewBranch(e.currentTarget.value)}
          />
        )}
        <ErrorAlert error={commit.error ?? current.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={() => commit.mutate()}
            loading={commit.isPending}
            disabled={!path || (existing && current.data === undefined)}
          >
            {t('code.commitChanges')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
