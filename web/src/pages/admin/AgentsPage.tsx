// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Card,
  Code,
  Group,
  Modal,
  NumberInput,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Title,
  Tooltip,
} from '@mantine/core';
import { IconDownload, IconInfoCircle, IconPlus, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { formatBytes, formatDate, formatRelative } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import type { AgentToken } from '../builds/hooks';
import { resourcesText } from '../builds/JobCard';
import { VariablesEditor } from '../builds/Variables';

/** Build agents, their registration tokens and the variables of all pipelines. */
export function AgentsPage() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const agents = useQuery({
    queryKey: ['admin', 'agents'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/agents')),
    refetchInterval: 15_000,
  });
  const invalidate = () => client.invalidateQueries({ queryKey: ['admin', 'agents'] });
  const change = useMutation({
    mutationFn: (a: {
      id: number;
      enabled?: boolean;
      approve?: boolean;
      labels?: string[];
      protected?: boolean;
    }) =>
      unwrap(
        api.PATCH('/api/v1/admin/agents/{id}', {
          params: { path: { id: a.id } },
          body: { enabled: a.enabled, approve: a.approve, labels: a.labels, protected: a.protected },
        }),
      ),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/api/v1/admin/agents/{id}', { params: { path: { id } } })),
    onSuccess: invalidate,
  });
  return (
    <>
      <PageHeader title={t('agents.title')} subtitle={t('agents.subtitle')} />
      <Stack gap="xl">
        <Card withBorder padding="xs">
          <ErrorAlert error={agents.error ?? change.error ?? remove.error} />
          {agents.data?.length === 0 ? (
            <Text c="dimmed" ta="center" py="lg">
              {t('agents.none')}
            </Text>
          ) : (
            <Table>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('agents.name')}</Table.Th>
                  <Table.Th>{t('agents.labels')}</Table.Th>
                  <Table.Th>{t('agents.status')}</Table.Th>
                  <Table.Th>{t('agents.lastSeen')}</Table.Th>
                  <Table.Th>
                    <Group gap={4} wrap="nowrap">
                      {t('agents.protected')}
                      <Tooltip label={t('agents.protectedHelp')} multiline w={280}>
                        <IconInfoCircle size={14} aria-label={t('agents.protectedHelp')} />
                      </Tooltip>
                    </Group>
                  </Table.Th>
                  <Table.Th>{t('agents.enabled')}</Table.Th>
                  <Table.Th w={50} />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {(agents.data ?? []).map((a) => (
                  <Table.Tr key={a.id}>
                    <Table.Td>
                      <Text fw={500}>{a.name}</Text>
                      <Text size="xs" c="dimmed">
                        {a.os}/{a.arch} · {a.version}
                        {a.project ? ` · ${t('agents.onlyProject', { project: a.project })}` : ''}
                      </Text>
                      {!!(a.maxMilliCpus || a.maxMemory) && (
                        <Text size="xs" c="dimmed">
                          {t('agents.limits', {
                            limits: resourcesText(t, { milliCpus: a.maxMilliCpus, memory: a.maxMemory }),
                          })}
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <Group gap={4}>
                        {a.labels.map((l) => (
                          <Badge key={l} size="xs" variant="outline" color="gray" tt="none">
                            {l}
                          </Badge>
                        ))}
                      </Group>
                      {a.announcedLabels && (
                        <Group gap={6} mt={4} wrap="nowrap">
                          <Text size="xs" c="orange.8">
                            {t('agents.announces', { labels: a.announcedLabels.join(', ') })}
                          </Text>
                          <Button
                            size="compact-xs"
                            variant="light"
                            color="orange"
                            onClick={() => change.mutate({ id: a.id, labels: a.announcedLabels })}
                          >
                            {t('agents.acceptLabels')}
                          </Button>
                        </Group>
                      )}
                    </Table.Td>
                    <Table.Td>
                      {!a.approved ? (
                        <Group gap={6} wrap="nowrap">
                          <Badge color="orange" variant="light">
                            {t('agents.awaitingApproval')}
                          </Badge>
                          <Button
                            size="compact-xs"
                            onClick={() => change.mutate({ id: a.id, approve: true })}
                          >
                            {t('agents.approve')}
                          </Button>
                        </Group>
                      ) : !a.enabled ? (
                        <Badge color="gray">{t('agents.disabled')}</Badge>
                      ) : !a.online ? (
                        <Badge color="red" variant="light">
                          {t('agents.offline')}
                        </Badge>
                      ) : a.busy > 0 ? (
                        <Badge color="blue" variant="light">
                          {t('agents.busy', { count: a.busy })}
                        </Badge>
                      ) : (
                        <Badge color="green" variant="light">
                          {t('agents.idle')}
                        </Badge>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <Text size="sm">{a.lastSeen ? formatRelative(a.lastSeen) : '—'}</Text>
                      <Text size="xs" c="dimmed">
                        {a.lastIp}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Switch
                        checked={a.protected}
                        onChange={(e) => change.mutate({ id: a.id, protected: e.currentTarget.checked })}
                        aria-label={t('agents.protectedOf', { name: a.name })}
                      />
                    </Table.Td>
                    <Table.Td>
                      <Switch
                        checked={a.enabled}
                        onChange={(e) => change.mutate({ id: a.id, enabled: e.currentTarget.checked })}
                        aria-label={t('agents.enabled')}
                      />
                    </Table.Td>
                    <Table.Td>
                      <Tooltip label={t('agents.remove')}>
                        <ActionIcon
                          variant="subtle"
                          color="red"
                          onClick={() => remove.mutate(a.id)}
                          aria-label={t('agents.remove')}
                        >
                          <IconTrash size={16} />
                        </ActionIcon>
                      </Tooltip>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
        </Card>
        <Tokens />
        <Downloads />
        <Stack gap="xs">
          <Title order={4}>{t('agents.variables')}</Title>
          <Text size="sm" c="dimmed">
            {t('agents.variablesHelp')}
          </Text>
          <VariablesEditor scope={{ scope: 'instance' }} />
        </Stack>
      </Stack>
    </>
  );
}

function Tokens() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const tokens = useQuery({
    queryKey: ['admin', 'agent-tokens'],
    queryFn: () => unwrap(api.GET('/api/v1/admin/agent-tokens')),
  });
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<AgentToken | null>(null);
  const [description, setDescription] = useState('');
  const [project, setProject] = useState('');
  const [days, setDays] = useState<number>(7);
  const [maxUses, setMaxUses] = useState<number>(1);
  const [autoApprove, setAutoApprove] = useState(false);
  const invalidate = () => client.invalidateQueries({ queryKey: ['admin', 'agent-tokens'] });
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/admin/agent-tokens', {
          body: {
            description,
            project: project.trim().toUpperCase() || undefined,
            expiresInDays: days,
            maxUses,
            autoApprove,
          },
        }),
      ),
    onSuccess: (tok) => {
      setCreating(false);
      setCreated(tok);
      setDescription('');
      setProject('');
      void invalidate();
    },
  });
  const revoke = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE('/api/v1/admin/agent-tokens/{id}', { params: { path: { id } } })),
    onSuccess: invalidate,
  });
  const server = window.location.origin;
  return (
    <Stack gap="xs">
      <Group justify="space-between">
        <Title order={4}>{t('agents.tokens')}</Title>
        <Button
          size="xs"
          variant="light"
          leftSection={<IconPlus size={14} />}
          onClick={() => setCreating(true)}
        >
          {t('agents.newToken')}
        </Button>
      </Group>
      <Text size="sm" c="dimmed">
        {t('agents.tokensHelp')}
      </Text>
      {created?.token && (
        <Alert
          color="green"
          title={t('agents.tokenCreated')}
          withCloseButton
          onClose={() => setCreated(null)}
        >
          <Stack gap="xs">
            <Text size="sm">{t('agents.registerHelp')}</Text>
            <Code
              block
            >{`kogge-agent register -server ${server} -token ${created.token} -shell -container docker`}</Code>
          </Stack>
        </Alert>
      )}
      <ErrorAlert error={tokens.error ?? revoke.error} />
      {(tokens.data?.length ?? 0) > 0 && (
        <Card withBorder padding="xs">
          <Table>
            <Table.Tbody>
              {(tokens.data ?? []).map((tok) => (
                <Table.Tr key={tok.id}>
                  <Table.Td>
                    <Text ff="monospace" size="sm">
                      {tok.hint}…
                    </Text>
                  </Table.Td>
                  <Table.Td>{tok.description}</Table.Td>
                  <Table.Td>
                    {tok.project
                      ? t('agents.onlyProject', { project: tok.project })
                      : t('agents.allProjects')}
                  </Table.Td>
                  <Table.Td>
                    {tok.maxUses > 0
                      ? t('agents.usesOf', { uses: tok.uses, max: tok.maxUses })
                      : t('agents.uses', { count: tok.uses })}
                    {tok.autoApprove && (
                      <Text size="xs" c="dimmed">
                        {t('agents.autoApproves')}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td>
                    {tok.expires
                      ? t('agents.expires', { date: formatDate(tok.expires) })
                      : t('agents.noExpiry')}
                  </Table.Td>
                  <Table.Td w={50}>
                    <ActionIcon
                      variant="subtle"
                      color="red"
                      onClick={() => revoke.mutate(tok.id)}
                      aria-label={t('agents.revoke')}
                    >
                      <IconTrash size={16} />
                    </ActionIcon>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Card>
      )}
      <Modal opened={creating} onClose={() => setCreating(false)} title={t('agents.newToken')}>
        <Stack>
          <TextInput
            label={t('agents.tokenDescription')}
            value={description}
            onChange={(e) => setDescription(e.currentTarget.value)}
          />
          <TextInput
            label={t('agents.tokenProject')}
            description={t('agents.tokenProjectHelp')}
            value={project}
            onChange={(e) => setProject(e.currentTarget.value)}
          />
          <NumberInput
            label={t('agents.tokenDays')}
            description={t('agents.tokenDaysHelp')}
            min={0}
            max={3650}
            value={days}
            onChange={(v) => setDays(Number(v) || 0)}
          />
          <NumberInput
            label={t('agents.tokenMaxUses')}
            description={t('agents.tokenMaxUsesHelp')}
            min={0}
            max={10000}
            value={maxUses}
            onChange={(v) => setMaxUses(Number(v) || 0)}
          />
          <Switch
            label={t('agents.tokenAutoApprove')}
            description={t('agents.tokenAutoApproveHelp')}
            checked={autoApprove}
            onChange={(e) => setAutoApprove(e.currentTarget.checked)}
          />
          <ErrorAlert error={create.error} />
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setCreating(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => create.mutate()} loading={create.isPending}>
              {t('common.create')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}

/** The agent binaries the installation offers, with an install command for Linux hosts. */
function Downloads() {
  const { t } = useTranslation();
  const downloads = useQuery({
    queryKey: ['admin', 'agent-downloads'],
    queryFn: () => unwrap(api.GET('/api/v1/builds/agent-downloads')),
  });
  const server = window.location.origin;
  const linux = downloads.data?.find((d) => d.platform === 'linux-amd64');
  return (
    <Stack gap="xs">
      <Title order={4}>{t('agents.downloads')}</Title>
      <Text size="sm" c="dimmed">
        {t('agents.downloadsHelp')}
      </Text>
      <ErrorAlert error={downloads.error} />
      {downloads.data?.length === 0 ? (
        <Text size="sm" c="dimmed">
          {t('agents.noDownloads')}
        </Text>
      ) : (
        <Card withBorder padding="xs">
          <Table>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('agents.platform')}</Table.Th>
                <Table.Th>{t('agents.size')}</Table.Th>
                <Table.Th>{t('agents.checksum')}</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(downloads.data ?? []).map((d) => (
                <Table.Tr key={d.file}>
                  <Table.Td>{d.platform}</Table.Td>
                  <Table.Td>{formatBytes(d.size)}</Table.Td>
                  <Table.Td>
                    <Tooltip label={d.sha256}>
                      <Text ff="monospace" size="xs">
                        {d.sha256.slice(0, 16)}…
                      </Text>
                    </Tooltip>
                  </Table.Td>
                  <Table.Td w={140}>
                    <Button
                      component="a"
                      href={d.url}
                      download
                      size="xs"
                      variant="light"
                      leftSection={<IconDownload size={14} />}
                    >
                      {t('agents.download')}
                    </Button>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Card>
      )}
      {linux && (
        <>
          <Text size="sm">{t('agents.installHelp')}</Text>
          <Code block>
            {`curl -fLo kogge-agent ${server}${linux.url}\n` +
              `echo "${linux.sha256}  kogge-agent" | sha256sum -c && chmod +x kogge-agent`}
          </Code>
        </>
      )}
    </Stack>
  );
}
