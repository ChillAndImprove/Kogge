// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Button,
  Card,
  Group,
  Modal,
  SimpleGrid,
  Select,
  Stack,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core';
import { IconSearch } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { api, unwrap } from '../../api/client';
import { useSession } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { UserBadge } from './shared';

function CreateProject({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [name, setName] = useState('');
  const [key, setKey] = useState('');
  const [keyTouched, setKeyTouched] = useState(false);
  const [template, setTemplate] = useState('scrum');
  const [description, setDescription] = useState('');
  const suggested = name
    .toUpperCase()
    .replace(/[^A-Z0-9 ]/g, '')
    .split(/\s+/)
    .filter(Boolean)
    .map((w, _i, all) => (all.length > 1 ? w[0] : w.slice(0, 4)))
    .join('')
    .slice(0, 10);
  const effectiveKey = keyTouched ? key : suggested;
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/projects', {
          body: { key: effectiveKey, name, description, template: template as 'scrum' },
        }),
      ),
    onSuccess: (p) => {
      void client.invalidateQueries({ queryKey: ['projects'] });
      onClose();
      navigate(`/projects/${p.key}`);
    },
  });
  return (
    <Modal opened={opened} onClose={onClose} title={t('issues.createProject')} size="lg">
      <Stack>
        <TextInput
          label={t('common.name')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
          data-autofocus
        />
        <TextInput
          label={t('issues.key')}
          description={t('issues.keyHelp')}
          value={effectiveKey}
          onChange={(e) => {
            setKeyTouched(true);
            setKey(e.currentTarget.value.toUpperCase());
          }}
          required
        />
        <Select
          label={t('issues.template')}
          value={template}
          onChange={(v) => setTemplate(v ?? 'scrum')}
          data={['scrum', 'kanban', 'bugs', 'business'].map((v) => ({
            value: v,
            label: t(`issues.templates.${v}`),
          }))}
        />
        <Textarea
          label={t('common.description')}
          value={description}
          onChange={(e) => setDescription(e.currentTarget.value)}
        />
        <ErrorAlert error={create.error} />
        <Group justify="flex-end">
          <Button
            onClick={() => create.mutate()}
            loading={create.isPending}
            disabled={!name || !effectiveKey}
          >
            {t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

export function ProjectsPage() {
  const { t } = useTranslation();
  const { can } = useSession();
  const [creating, setCreating] = useState(false);
  const projects = useQuery({ queryKey: ['projects'], queryFn: () => unwrap(api.GET('/api/v1/projects')) });
  return (
    <>
      <PageHeader
        title={t('issues.projects')}
        actions={
          <>
            <Button
              component={Link}
              to="/issues/search"
              variant="default"
              leftSection={<IconSearch size={16} />}
            >
              {t('issues.search')}
            </Button>
            {can('project.create') && (
              <Button onClick={() => setCreating(true)}>{t('issues.createProject')}</Button>
            )}
          </>
        }
      />
      <ErrorAlert error={projects.error} />
      {projects.data?.length === 0 && <Text c="dimmed">{t('issues.noProjects')}</Text>}
      <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
        {(projects.data ?? []).map((p) => (
          <Card key={p.id} withBorder component={Link} to={`/projects/${p.key}`} padding="lg">
            <Group justify="space-between" align="flex-start">
              <div>
                <Title order={4}>{p.name}</Title>
                <Text size="sm" c="dimmed">
                  {p.key} · {t('issues.issueCount', { count: p.issues })}
                </Text>
              </div>
            </Group>
            {p.description && (
              <Text size="sm" mt="sm" lineClamp={2}>
                {p.description}
              </Text>
            )}
            {p.lead && (
              <Group mt="md" gap="xs">
                <Text size="xs" c="dimmed">
                  {t('issues.lead')}
                </Text>
                <UserBadge user={p.lead} size="xs" />
              </Group>
            )}
          </Card>
        ))}
      </SimpleGrid>
      <CreateProject opened={creating} onClose={() => setCreating(false)} />
    </>
  );
}
