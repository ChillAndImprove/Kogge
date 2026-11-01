// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Checkbox, Group, Modal, Stack, Text, TextInput } from '@mantine/core';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { SpacePicker } from '../../components/Pickers';
import type { Doc } from '../../components/editor/Editor';
import { fillTemplate } from '../wiki/CreatePageDialog';

/** What the page is about: the release notes of a version or the retrospective of a sprint. */
export interface LinkedPageKind {
  /** Label of the template to start from (the built-in ones have release-notes and retrospective). */
  template: 'release-notes' | 'retrospective';
  title: string;
  /** Values of the template's variables. */
  variables: Record<string, string>;
  /** KQL of the issues listed on the page. */
  query: string;
}

/** Creates a page in the project's wiki space from a template, with the issues, and opens it in the editor. */
export function LinkedPageModal({
  projectKey,
  kind,
  onClose,
}: {
  projectKey: string;
  kind: LinkedPageKind;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const linked = useQuery({
    queryKey: ['project', projectKey, 'space'],
    queryFn: () => unwrap(api.GET('/api/v1/projects/{key}/space', { params: { path: { key: projectKey } } })),
  });
  const [space, setSpace] = useState<string | null>(null);
  const [title, setTitle] = useState(kind.title);
  const [remember, setRemember] = useState(true);
  const target = space ?? linked.data?.space?.key ?? null;
  const create = useMutation({
    mutationFn: async () => {
      if (!target) throw new Error(t('linkedPage.chooseSpace'));
      if (remember && !linked.data?.space) {
        // Only project administrators can link a space; for others the page is still created.
        await unwrap(
          api.PUT('/api/v1/projects/{key}/space', {
            params: { path: { key: projectKey } },
            body: { space: target },
          }),
        ).then(
          () => client.invalidateQueries({ queryKey: ['project', projectKey, 'space'] }),
          () => undefined,
        );
      }
      const templates = await unwrap(
        api.GET('/api/v1/wiki/templates', { params: { query: { space: target } } }),
      );
      const template = templates.find((tp) => tp.labels.includes(kind.template));
      const base: Doc = template?.body
        ? fillTemplate(template.body, kind.variables)
        : { type: 'doc', content: [] };
      const body: Doc = {
        ...base,
        content: [
          ...(base.content ?? []),
          { type: 'heading', attrs: { level: 2 }, content: [{ type: 'text', text: t('linkedPage.issues') }] },
          { type: 'macro', attrs: { name: 'issue-list', params: { query: kind.query, max: 200 } } },
        ],
      };
      return {
        space: target,
        page: await unwrap(
          api.POST('/api/v1/wiki/spaces/{key}/pages', {
            params: { path: { key: target } },
            body: { title, kind: 'page', parentId: 0, body: body as Schemas['Doc'] },
          }),
        ),
      };
    },
    onSuccess: ({ space: key, page }) => {
      onClose();
      navigate(`/wiki/spaces/${key}/pages/${page.id}/edit`);
    },
  });
  return (
    <Modal opened onClose={onClose} title={t(`linkedPage.title.${kind.template}`)}>
      <Stack>
        <TextInput
          label={t('linkedPage.pageTitle')}
          value={title}
          onChange={(e) => setTitle(e.currentTarget.value)}
          required
        />
        <SpacePicker
          label={t('linkedPage.space')}
          value={target}
          onChange={setSpace}
          permission="page.edit"
          description={linked.data?.space ? t('linkedPage.linkedSpace') : t('linkedPage.noLinkedSpace')}
        />
        {!linked.data?.space && target && (
          <Checkbox
            label={t('linkedPage.remember')}
            checked={remember}
            onChange={(e) => setRemember(e.currentTarget.checked)}
          />
        )}
        <Text size="xs" c="dimmed">
          {t('linkedPage.help')}
        </Text>
        <ErrorAlert error={create.error ?? linked.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={() => create.mutate()}
            loading={create.isPending}
            disabled={!target || !title.trim()}
          >
            {t('linkedPage.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
