// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Badge,
  Breadcrumbs,
  Button,
  Card,
  Group,
  Modal,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
} from '@mantine/core';
import { IconFile, IconFolder } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import type { Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PagePicker } from '../../components/Pickers';
import { usePage, type Space } from './hooks';

type Template = Schemas['Template'];

const variablePattern = /\{\{\s*([\p{L}\p{N}_-]+)\s*\}\}/gu;

/** The variables ({{name}}) of a template, in order of appearance. */
export function templateVariables(doc: unknown): string[] {
  const out: string[] = [];
  const walk = (v: unknown) => {
    if (Array.isArray(v)) v.forEach(walk);
    else if (v && typeof v === 'object') {
      const o = v as { text?: unknown; content?: unknown };
      if (typeof o.text === 'string')
        for (const m of o.text.matchAll(variablePattern)) if (!out.includes(m[1])) out.push(m[1]);
      walk(o.content);
    }
  };
  walk(doc);
  return out;
}

/** Replaces the variables of a template document. Empty values keep the placeholder text out. */
export function fillTemplate(doc: unknown, values: Record<string, string>): Doc {
  const copy = JSON.parse(JSON.stringify(doc)) as Doc;
  const walk = (v: unknown) => {
    if (Array.isArray(v)) v.forEach(walk);
    else if (v && typeof v === 'object') {
      const o = v as { text?: string; content?: unknown };
      if (typeof o.text === 'string')
        o.text = o.text.replace(variablePattern, (_m, name: string) => values[name] ?? '');
      walk(o.content);
    }
  };
  walk(copy);
  // Text nodes that became empty are not allowed in documents.
  const prune = (n: Doc): Doc => ({
    ...n,
    content: n.content?.filter((c) => c.type !== 'text' || (c.text ?? '') !== '').map(prune),
  });
  return prune(copy);
}

/** Where a new page goes: the path of its parent, changeable to any page of the space. */
function Location({
  space,
  parent,
  onChange,
}: {
  space: Space;
  parent: number;
  onChange: (id: number) => void;
}) {
  const { t } = useTranslation();
  const [choosing, setChoosing] = useState(false);
  const page = usePage(parent || undefined);
  // The home page stands for the space itself.
  const path = page.data
    ? [...page.data.ancestors, page.data].filter((p) => p.id !== space.homePageId).map((p) => p.title)
    : [];
  return (
    <Stack gap={6}>
      <Group gap="xs" wrap="nowrap">
        <IconFolder size={16} style={{ flexShrink: 0, opacity: 0.6 }} aria-hidden />
        <Text size="sm" c="dimmed" style={{ flexShrink: 0 }}>
          {t('wiki.location')}
        </Text>
        <Breadcrumbs
          separator="›"
          separatorMargin={6}
          style={{ minWidth: 0, flexWrap: 'wrap' }}
          data-testid="page-location"
        >
          <Text size="sm" fw={600}>
            {space.name}
          </Text>
          {parent === 0 ? (
            <Text size="sm" c="dimmed">
              {t('wiki.topLevel')}
            </Text>
          ) : (
            path.map((title, i) => (
              <Text key={i} size="sm" fw={i === path.length - 1 ? 600 : 400}>
                {title}
              </Text>
            ))
          )}
        </Breadcrumbs>
        <Button
          variant="subtle"
          size="compact-sm"
          ml="auto"
          onClick={() => setChoosing((c) => !c)}
          aria-expanded={choosing}
        >
          {t('wiki.changeLocation')}
        </Button>
      </Group>
      {choosing && (
        <PagePicker
          space={space.key}
          kind="page"
          label={t('wiki.parentPage')}
          description={t('wiki.parentHelp')}
          value={parent || null}
          onChange={(id) => onChange(id ?? 0)}
          data-autofocus
        />
      )}
    </Stack>
  );
}

/** Starts a page or blog post, optionally from a template, below a parent page that people can change. */
export function CreatePageDialog({
  space,
  kind,
  parentId,
  onClose,
}: {
  space: Space;
  kind: 'page' | 'blog';
  parentId: number;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const templates = useQuery({
    queryKey: ['space', space.key, 'templates'],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/templates', { params: { query: { space: space.key } } })),
  });
  const [chosen, setChosen] = useState<Template | null>(null);
  const [parent, setParent] = useState(parentId);
  const [values, setValues] = useState<Record<string, string>>({});
  const create = useMutation({
    mutationFn: (template: Template | null) =>
      unwrap(
        api.POST('/api/v1/wiki/spaces/{key}/pages', {
          params: { path: { key: space.key } },
          body: {
            title: '',
            parentId: kind === 'page' ? parent : 0,
            kind,
            templateId: template && templateVariables(template.body).length === 0 ? template.id : undefined,
            body:
              template && templateVariables(template.body).length > 0
                ? (fillTemplate(template.body, values) as Schemas['Doc'])
                : undefined,
          },
        }),
      ),
    onSuccess: (p) => {
      void client.invalidateQueries({ queryKey: ['page'] });
      onClose();
      navigate(`/wiki/spaces/${space.key}/pages/${p.id}/edit`);
    },
  });
  const variables = chosen ? templateVariables(chosen.body) : [];
  const list = (templates.data ?? []).filter((tpl) => tpl.kind === kind);
  return (
    <Modal
      opened
      onClose={onClose}
      title={kind === 'blog' ? t('wiki.createBlogPost') : t('wiki.createPage')}
      size="xl"
    >
      {chosen && variables.length > 0 ? (
        <Stack>
          <Text fw={600}>{chosen.name}</Text>
          {variables.map((v) => (
            <TextInput
              key={v}
              label={v}
              value={values[v] ?? ''}
              onChange={(e) => setValues((o) => ({ ...o, [v]: e.currentTarget.value }))}
            />
          ))}
          <ErrorAlert error={create.error} />
          <Group justify="space-between">
            <Button variant="subtle" onClick={() => setChosen(null)}>
              {t('wiki.back')}
            </Button>
            <Button onClick={() => create.mutate(chosen)} loading={create.isPending}>
              {t('common.create')}
            </Button>
          </Group>
        </Stack>
      ) : (
        <Stack>
          {kind === 'page' && <Location space={space} parent={parent} onChange={setParent} />}
          <SimpleGrid cols={{ base: 1, sm: 2, md: 3 }}>
            <Card
              component="button"
              type="button"
              withBorder
              padding="md"
              className="kogge-choice-card"
              onClick={() => create.mutate(null)}
              disabled={create.isPending}
            >
              <Group gap="xs">
                <IconFile size={18} />
                <Text fw={600}>{t('wiki.blankPage')}</Text>
              </Group>
              <Text size="xs" c="dimmed" mt={4}>
                {t('wiki.blankPageHelp')}
              </Text>
            </Card>
            {list.map((tpl) => (
              <Card
                key={tpl.id}
                component="button"
                type="button"
                withBorder
                padding="md"
                className="kogge-choice-card"
                disabled={create.isPending}
                onClick={() => {
                  if (templateVariables(tpl.body).length > 0) setChosen(tpl);
                  else create.mutate(tpl);
                }}
              >
                <Group gap="xs" justify="space-between" wrap="nowrap">
                  <Text fw={600} truncate>
                    {tpl.name}
                  </Text>
                  {tpl.spaceKey && (
                    <Badge size="xs" variant="light">
                      {tpl.spaceKey}
                    </Badge>
                  )}
                </Group>
                <Text size="xs" c="dimmed" mt={4} lineClamp={2}>
                  {tpl.description}
                </Text>
              </Card>
            ))}
          </SimpleGrid>
          <ErrorAlert error={create.error} />
        </Stack>
      )}
    </Modal>
  );
}
