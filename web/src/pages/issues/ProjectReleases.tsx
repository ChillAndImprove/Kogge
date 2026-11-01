// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Badge,
  Button,
  Group,
  Menu,
  Modal,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { DateInput } from '@mantine/dates';
import { IconDots, IconPlus } from '@tabler/icons-react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { formatDate } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { notifyError } from '../../components/notify';
import { useProjectContext } from './ProjectLayout';
import { LinkedPageModal, type LinkedPageKind } from './LinkedPage';

type Version = Schemas['Version'];

/** KQL string literal. */
export function kqlString(s: string) {
  return `"${s.replace(/["\\]/g, '\\$&')}"`;
}

function VersionForm({
  projectKey,
  version,
  onClose,
}: {
  projectKey: string;
  version: Version | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [name, setName] = useState(version?.name ?? '');
  const [description, setDescription] = useState(version?.description ?? '');
  const [startDate, setStartDate] = useState<string | null>(version?.startDate ?? null);
  const [releaseDate, setReleaseDate] = useState<string | null>(version?.releaseDate ?? null);
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        description,
        startDate: startDate ?? undefined,
        releaseDate: releaseDate ?? undefined,
        released: version?.released,
        archived: version?.archived,
      };
      return version
        ? unwrap(
            api.PUT('/api/v1/projects/{key}/versions/{id}', {
              params: { path: { key: projectKey, id: version.id } },
              body,
            }),
          )
        : unwrap(
            api.POST('/api/v1/projects/{key}/versions', { params: { path: { key: projectKey } }, body }),
          );
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['project', projectKey] });
      onClose();
    },
  });
  return (
    <Stack>
      <TextInput
        label={t('common.name')}
        value={name}
        onChange={(e) => setName(e.currentTarget.value)}
        required
        data-autofocus
      />
      <Group grow>
        <DateInput
          label={t('issues.startDate')}
          value={startDate}
          onChange={setStartDate}
          clearable
          valueFormat="DD.MM.YYYY"
        />
        <DateInput
          label={t('issues.releaseDate')}
          value={releaseDate}
          onChange={setReleaseDate}
          clearable
          valueFormat="DD.MM.YYYY"
        />
      </Group>
      <Textarea
        label={t('common.description')}
        value={description}
        onChange={(e) => setDescription(e.currentTarget.value)}
        autosize
        minRows={2}
      />
      <ErrorAlert error={save.error} />
      <Group justify="flex-end">
        <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name.trim()}>
          {t('common.save')}
        </Button>
      </Group>
    </Stack>
  );
}

function DeleteVersion({
  projectKey,
  version,
  others,
  onClose,
}: {
  projectKey: string;
  version: Version;
  others: Version[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [moveTo, setMoveTo] = useState<string | null>(null);
  const del = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/api/v1/projects/{key}/versions/{id}', {
          params: {
            path: { key: projectKey, id: version.id },
            query: moveTo ? { moveTo: Number(moveTo) } : {},
          },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['project', projectKey] });
      onClose();
    },
  });
  return (
    <Stack>
      <Text size="sm">{t('issues.deleteVersionText', { name: version.name })}</Text>
      <Select
        label={t('issues.moveIssuesTo')}
        placeholder={t('issues.removeFromIssues')}
        data={others.map((v) => ({ value: String(v.id), label: v.name }))}
        value={moveTo}
        onChange={setMoveTo}
        clearable
      />
      <ErrorAlert error={del.error} />
      <Group justify="flex-end">
        <Button color="red" onClick={() => del.mutate()} loading={del.isPending}>
          {t('common.delete')}
        </Button>
      </Group>
    </Stack>
  );
}

export function ProjectReleases() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const project = useProjectContext();
  const admin = project.permissions.includes('project.admin');
  const [editing, setEditing] = useState<Version | 'new' | null>(null);
  const [deleting, setDeleting] = useState<Version | null>(null);
  const [pageFor, setPageFor] = useState<LinkedPageKind | null>(null);
  const update = useMutation({
    mutationFn: (v: Version & { patch: Partial<Version> }) =>
      unwrap(
        api.PUT('/api/v1/projects/{key}/versions/{id}', {
          params: { path: { key: project.key, id: v.id } },
          body: {
            name: v.name,
            description: v.description,
            startDate: v.startDate,
            releaseDate: v.releaseDate,
            released: v.released,
            archived: v.archived,
            ...v.patch,
          },
        }),
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: ['project', project.key] }),
    onError: notifyError,
  });
  const versions = [...project.versions].sort(
    (a, b) => Number(a.archived) - Number(b.archived) || a.position - b.position,
  );
  return (
    <>
      <PageHeader
        title={t('issues.releases')}
        actions={
          admin && (
            <Button leftSection={<IconPlus size={16} />} onClick={() => setEditing('new')}>
              {t('issues.newVersion')}
            </Button>
          )
        }
      />
      {versions.length === 0 ? (
        <Text c="dimmed">{t('issues.noVersions')}</Text>
      ) : (
        <Table>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.name')}</Table.Th>
              <Table.Th>{t('issues.status')}</Table.Th>
              <Table.Th>{t('issues.startDate')}</Table.Th>
              <Table.Th>{t('issues.releaseDate')}</Table.Th>
              <Table.Th>{t('common.description')}</Table.Th>
              {admin && <Table.Th w={40} />}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {versions.map((v) => {
              const overdue =
                !v.released && v.releaseDate && v.releaseDate < new Date().toISOString().slice(0, 10);
              return (
                <Table.Tr key={v.id} style={{ opacity: v.archived ? 0.6 : 1 }}>
                  <Table.Td>
                    <Anchor component={Link} to={`/projects/${project.key}/releases/${v.id}`} fw={600}>
                      {v.name}
                    </Anchor>
                  </Table.Td>
                  <Table.Td>
                    {v.archived ? (
                      <Badge color="gray">{t('issues.archived')}</Badge>
                    ) : v.released ? (
                      <Badge color="teal">{t('issues.released')}</Badge>
                    ) : (
                      <Badge color={overdue ? 'red' : 'blue'} variant="light">
                        {overdue ? t('issues.overdue') : t('issues.unreleased')}
                      </Badge>
                    )}
                  </Table.Td>
                  <Table.Td>{v.startDate ? formatDate(v.startDate) : '—'}</Table.Td>
                  <Table.Td>{v.releaseDate ? formatDate(v.releaseDate) : '—'}</Table.Td>
                  <Table.Td>
                    <Text size="sm" lineClamp={2}>
                      {v.description}
                    </Text>
                  </Table.Td>
                  {admin && (
                    <Table.Td>
                      <Menu position="bottom-end">
                        <Menu.Target>
                          <ActionIcon variant="subtle" aria-label={t('issues.more')}>
                            <IconDots size={16} />
                          </ActionIcon>
                        </Menu.Target>
                        <Menu.Dropdown>
                          <Menu.Item onClick={() => setEditing(v)}>{t('common.edit')}</Menu.Item>
                          <Menu.Item
                            onClick={() =>
                              setPageFor({
                                template: 'release-notes',
                                title: t('linkedPage.releaseNotesTitle', {
                                  version: v.name,
                                  project: project.name,
                                }),
                                variables: { version: v.name },
                                query: `project = ${project.key} AND fixVersion = "${v.name.replace(/"/g, '\\"')}" ORDER BY type, key`,
                              })
                            }
                          >
                            {t('linkedPage.createReleaseNotes')}
                          </Menu.Item>
                          <Menu.Item
                            onClick={() => update.mutate({ ...v, patch: { released: !v.released } })}
                          >
                            {v.released ? t('issues.unrelease') : t('issues.release')}
                          </Menu.Item>
                          <Menu.Item
                            onClick={() => update.mutate({ ...v, patch: { archived: !v.archived } })}
                          >
                            {v.archived ? t('issues.unarchive') : t('issues.archive')}
                          </Menu.Item>
                          <Menu.Divider />
                          <Menu.Item color="red" onClick={() => setDeleting(v)}>
                            {t('common.delete')}
                          </Menu.Item>
                        </Menu.Dropdown>
                      </Menu>
                    </Table.Td>
                  )}
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      )}
      <Modal
        opened={editing !== null}
        onClose={() => setEditing(null)}
        title={editing === 'new' ? t('issues.newVersion') : t('common.edit')}
      >
        {editing !== null && (
          <VersionForm
            projectKey={project.key}
            version={editing === 'new' ? null : editing}
            onClose={() => setEditing(null)}
          />
        )}
      </Modal>
      {pageFor && (
        <LinkedPageModal projectKey={project.key} kind={pageFor} onClose={() => setPageFor(null)} />
      )}
      <Modal opened={deleting !== null} onClose={() => setDeleting(null)} title={t('common.delete')}>
        {deleting && (
          <DeleteVersion
            projectKey={project.key}
            version={deleting}
            others={project.versions.filter((v) => v.id !== deleting.id && !v.archived)}
            onClose={() => setDeleting(null)}
          />
        )}
      </Modal>
    </>
  );
}
