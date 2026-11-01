// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Badge,
  Button,
  Card,
  Code,
  Group,
  Menu,
  Modal,
  ScrollArea,
  Select,
  SimpleGrid,
  Stack,
  Text,
  Textarea,
  TextInput,
  ThemeIcon,
  Title,
  UnstyledButton,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconDots, IconPencil, IconPlus, IconTemplate, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatRelative } from '../../app/format';
import { useSession } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifyError, notifySuccess } from '../../components/notify';
import { PageHeader } from '../../components/PageHeader';
import { EditFileModal } from '../code/EditFile';
import type { Repository } from '../code/hooks';
import { PipelineCheck } from './PipelineCheck';

export type PipelineTemplate = Schemas['PipelineTemplate'];

export const pipelineFilePath = '.kogge/pipelines.yml';

/** A short sign of the kind of project of a built-in template — letters, no logos of others. */
const signs: Record<string, { text: string; color: string }> = {
  go: { text: 'Go', color: 'cyan' },
  node: { text: 'JS', color: 'green' },
  maven: { text: 'Mvn', color: 'orange' },
  gradle: { text: 'Gr', color: 'teal' },
  python: { text: 'Py', color: 'blue' },
  dotnet: { text: '.N', color: 'violet' },
  container: { text: 'Img', color: 'indigo' },
  minimal: { text: '1', color: 'gray' },
};

export function useTemplates(repo?: Pick<Repository, 'project' | 'slug'>) {
  return useQuery({
    queryKey: ['pipeline-templates', repo?.project.key, repo?.slug],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/builds/templates', {
          params: { query: repo ? { project: repo.project.key, repository: repo.slug } : {} },
        }),
      ),
  });
}

/** Name and description of a template: those of the organisation's templates, translated ones for built-in ones. */
export function useTemplateText() {
  const { t } = useTranslation();
  return (tpl: PipelineTemplate) =>
    tpl.builtin
      ? {
          name: t(`builds.templateNames.${tpl.id}`, { defaultValue: tpl.id }),
          description: t(`builds.templateDescriptions.${tpl.id}`, { defaultValue: '' }),
        }
      : { name: tpl.name ?? tpl.id, description: tpl.description ?? '' };
}

function TemplateSign({ tpl }: { tpl: PipelineTemplate }) {
  if (!tpl.builtin) {
    return (
      <ThemeIcon variant="light" color="orange" size={40} radius="md">
        <IconTemplate size={22} />
      </ThemeIcon>
    );
  }
  const s = signs[tpl.id] ?? signs.minimal;
  return (
    <ThemeIcon variant="light" color={s.color} size={40} radius="md">
      <Text fw={800} size={s.text.length > 2 ? 'xs' : 'sm'} ff="monospace">
        {s.text}
      </Text>
    </ThemeIcon>
  );
}

/** A template as a card to choose. */
function TemplateCard({ tpl, onChoose }: { tpl: PipelineTemplate; onChoose: () => void }) {
  const { t } = useTranslation();
  const text = useTemplateText()(tpl);
  return (
    <UnstyledButton onClick={onChoose} className="kogge-choice-card" aria-label={text.name}>
      <Group gap="sm" wrap="nowrap" align="flex-start">
        <TemplateSign tpl={tpl} />
        <Stack gap={4} style={{ minWidth: 0 }}>
          <Group gap={6}>
            <Text fw={600} size="sm">
              {text.name}
            </Text>
            {tpl.recommended && (
              <Badge size="xs" color="teal" variant="light">
                {t('builds.recommended')}
              </Badge>
            )}
            {!tpl.builtin && (
              <Badge size="xs" color="orange" variant="light">
                {t('builds.organisationTemplate')}
              </Badge>
            )}
          </Group>
          <Text size="xs" c="dimmed" lineClamp={2}>
            {text.description}
          </Text>
        </Stack>
      </Group>
    </UnstyledButton>
  );
}

/**
 * Sets up a repository's pipelines from a template: the template chosen opens in the editor, which checks the file
 * and commits it — to the branch, or to a new one for a pull request.
 */
export function SetUpPipelines({ repo, onClose }: { repo: Repository; onClose: () => void }) {
  const { t } = useTranslation();
  const templates = useTemplates(repo);
  const [chosen, setChosen] = useState<PipelineTemplate | null>(null);
  if (chosen) {
    return (
      <EditFileModal
        repo={repo}
        branch={repo.defaultBranch}
        path={pipelineFilePath}
        template={chosen.content}
        message="Add the pipeline file"
        onClose={onClose}
      />
    );
  }
  // Recommended templates first, then the organisation's, then the others.
  const list = [...(templates.data ?? [])].sort(
    (a, b) => Number(b.recommended) - Number(a.recommended) || Number(a.builtin) - Number(b.builtin),
  );
  return (
    <Modal opened onClose={onClose} title={t('builds.setUpPipelines')} size="xl">
      <Stack>
        <Text size="sm" c="dimmed">
          {t('builds.setUpPipelinesHelp', { path: pipelineFilePath })}
        </Text>
        <ErrorAlert error={templates.error} />
        <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="sm">
          {list.map((tpl) => (
            <TemplateCard key={tpl.id} tpl={tpl} onChoose={() => setChosen(tpl)} />
          ))}
        </SimpleGrid>
      </Stack>
    </Modal>
  );
}

/** Creates or changes a template of the organisation. */
function TemplateEditor({
  template,
  builtins,
  onClose,
}: {
  template: PipelineTemplate | null;
  builtins: PipelineTemplate[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const text = useTemplateText();
  const [name, setName] = useState(template?.name ?? '');
  const [description, setDescription] = useState(template?.description ?? '');
  const [content, setContent] = useState(template?.content ?? '');
  const save = useMutation({
    mutationFn: () =>
      template
        ? unwrap(
            api.PUT('/api/v1/builds/templates/{id}', {
              params: { path: { id: template.id } },
              body: { name, description, content },
            }),
          )
        : unwrap(api.POST('/api/v1/builds/templates', { body: { name, description, content } })),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['pipeline-templates'] });
      notifySuccess(t('common.saved'));
      onClose();
    },
  });
  return (
    <Modal
      opened
      onClose={onClose}
      size="xl"
      title={template ? t('builds.editTemplate', { name: template.name }) : t('builds.newTemplate')}
    >
      <Stack>
        <Group grow align="flex-start">
          <TextInput
            label={t('common.name')}
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
            required
            maxLength={100}
            data-autofocus
          />
          {!template && (
            <Select
              label={t('builds.startFrom')}
              placeholder={t('builds.emptyFile')}
              data={builtins.map((b) => ({ value: b.id, label: text(b).name }))}
              onChange={(id) => setContent(builtins.find((b) => b.id === id)?.content ?? '')}
              clearable
            />
          )}
        </Group>
        <TextInput
          label={t('common.description')}
          value={description}
          onChange={(e) => setDescription(e.currentTarget.value)}
          maxLength={1000}
        />
        <Textarea
          label={t('builds.pipelineFile')}
          value={content}
          onChange={(e) => setContent(e.currentTarget.value)}
          autosize
          minRows={14}
          maxRows={26}
          spellCheck={false}
          styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } }}
          required
        />
        <PipelineCheck content={content} />
        <ErrorAlert error={save.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={() => save.mutate()}
            loading={save.isPending}
            disabled={!name.trim() || !content.trim()}
          >
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** Helling's templates: the organisation's own, which Helling's administrators maintain, and the built-in ones. */
export function TemplatesPage() {
  const { t } = useTranslation();
  const { can } = useSession();
  const client = useQueryClient();
  const templates = useTemplates();
  const text = useTemplateText();
  const [editing, setEditing] = useState<PipelineTemplate | 'new' | null>(null);
  const [viewing, setViewing] = useState<PipelineTemplate | null>(null);
  const manage = can('agent.manage');
  const del = useMutation({
    mutationFn: (id: string) =>
      unwrap(api.DELETE('/api/v1/builds/templates/{id}', { params: { path: { id } } })),
    onSuccess: () => client.invalidateQueries({ queryKey: ['pipeline-templates'] }),
    onError: notifyError,
  });
  const all = templates.data ?? [];
  const org = all.filter((x) => !x.builtin);
  const builtins = all.filter((x) => x.builtin);
  return (
    <>
      <PageHeader
        title={t('builds.templates')}
        subtitle={t('builds.templatesHelp')}
        actions={
          manage && (
            <Button leftSection={<IconPlus size={16} />} onClick={() => setEditing('new')}>
              {t('builds.newTemplate')}
            </Button>
          )
        }
      />
      <ErrorAlert error={templates.error} />
      <Stack gap="xl">
        <Stack gap="sm">
          <Title order={4}>{t('builds.organisationTemplates')}</Title>
          {org.length === 0 ? (
            <Text c="dimmed" size="sm">
              {manage ? t('builds.noOrganisationTemplatesAdmin') : t('builds.noOrganisationTemplates')}
            </Text>
          ) : (
            <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} spacing="sm">
              {org.map((tpl) => (
                <Card key={tpl.id} withBorder radius="md" padding="md">
                  <Group justify="space-between" wrap="nowrap" align="flex-start">
                    <Group gap="sm" wrap="nowrap" align="flex-start" style={{ minWidth: 0 }}>
                      <TemplateSign tpl={tpl} />
                      <Stack gap={2} style={{ minWidth: 0 }}>
                        <Text fw={600} size="sm" truncate>
                          {tpl.name}
                        </Text>
                        <Text size="xs" c="dimmed" lineClamp={2}>
                          {tpl.description}
                        </Text>
                        {tpl.updated && (
                          <Text size="xs" c="dimmed">
                            {t('builds.changed', { when: formatRelative(tpl.updated) })}
                          </Text>
                        )}
                      </Stack>
                    </Group>
                    <Menu position="bottom-end">
                      <Menu.Target>
                        <ActionIcon
                          variant="subtle"
                          color="gray"
                          aria-label={t('builds.templateActions', { name: tpl.name })}
                        >
                          <IconDots size={16} />
                        </ActionIcon>
                      </Menu.Target>
                      <Menu.Dropdown>
                        <Menu.Item onClick={() => setViewing(tpl)}>{t('builds.showFile')}</Menu.Item>
                        {manage && (
                          <>
                            <Menu.Item leftSection={<IconPencil size={14} />} onClick={() => setEditing(tpl)}>
                              {t('common.edit')}
                            </Menu.Item>
                            <Menu.Item
                              color="red"
                              leftSection={<IconTrash size={14} />}
                              onClick={() =>
                                modals.openConfirmModal({
                                  title: t('builds.deleteTemplate'),
                                  children: (
                                    <Text size="sm">
                                      {t('builds.deleteTemplateText', { name: tpl.name })}
                                    </Text>
                                  ),
                                  labels: { confirm: t('common.delete'), cancel: t('common.cancel') },
                                  confirmProps: { color: 'red' },
                                  onConfirm: () => del.mutate(tpl.id),
                                })
                              }
                            >
                              {t('common.delete')}
                            </Menu.Item>
                          </>
                        )}
                      </Menu.Dropdown>
                    </Menu>
                  </Group>
                </Card>
              ))}
            </SimpleGrid>
          )}
        </Stack>
        <Stack gap="sm">
          <Title order={4}>{t('builds.builtinTemplates')}</Title>
          <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} spacing="sm">
            {builtins.map((tpl) => (
              <TemplateCard key={tpl.id} tpl={tpl} onChoose={() => setViewing(tpl)} />
            ))}
          </SimpleGrid>
        </Stack>
      </Stack>
      {editing && (
        <TemplateEditor
          template={editing === 'new' ? null : editing}
          builtins={builtins}
          onClose={() => setEditing(null)}
        />
      )}
      {viewing && (
        <Modal opened onClose={() => setViewing(null)} title={text(viewing).name} size="xl">
          <Stack>
            <Text size="sm" c="dimmed">
              {text(viewing).description}
            </Text>
            <ScrollArea.Autosize mah="60vh">
              <Code block>{viewing.content}</Code>
            </ScrollArea.Autosize>
          </Stack>
        </Modal>
      )}
    </>
  );
}
