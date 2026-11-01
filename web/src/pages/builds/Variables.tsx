// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Badge,
  Button,
  Checkbox,
  Group,
  Modal,
  PasswordInput,
  Stack,
  Table,
  Text,
  Textarea,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { IconLock, IconPencil, IconPlus, IconShieldLock, IconTrash } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import type { Variable } from './hooks';

/** Which variables: the instance, a code project, a repository or an environment. */
export type VariableScope =
  | { scope: 'instance' }
  | { scope: 'project'; project: string }
  | { scope: 'repository'; project: string; repository: string }
  | { scope: 'environment'; project: string; repository: string; environment: string };

function query(s: VariableScope) {
  return {
    scope: s.scope,
    project: 'project' in s ? s.project : undefined,
    repository: 'repository' in s ? s.repository : undefined,
    environment: 'environment' in s ? s.environment : undefined,
  };
}

/** Lists and edits the variables and secrets of a scope (for those who manage it). */
export function VariablesEditor({ scope }: { scope: VariableScope }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const key = ['build-variables', query(scope)];
  const vars = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET('/api/v1/builds/variables', { params: { query: query(scope) } })),
  });
  const [editing, setEditing] = useState<Variable | 'new' | null>(null);
  const remove = useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.DELETE('/api/v1/builds/variables/{name}', { params: { path: { name }, query: query(scope) } }),
      ),
    onSuccess: () => client.invalidateQueries({ queryKey: key }),
  });
  return (
    <Stack gap="xs">
      <ErrorAlert error={vars.error ?? remove.error} />
      {(vars.data?.length ?? 0) === 0 ? (
        <Text c="dimmed" size="sm">
          {t('builds.noVariables')}
        </Text>
      ) : (
        <Table>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('builds.variableName')}</Table.Th>
              <Table.Th>{t('builds.variableValue')}</Table.Th>
              <Table.Th w={90} />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(vars.data ?? []).map((v) => (
              <Table.Tr key={v.name}>
                <Table.Td>
                  <Group gap={6}>
                    <Text ff="monospace" size="sm">
                      {v.name}
                    </Text>
                    {v.protected && (
                      <Tooltip label={t('builds.protectedHelp')}>
                        <Badge
                          size="xs"
                          variant="light"
                          color="orange"
                          leftSection={<IconShieldLock size={10} />}
                        >
                          {t('builds.protected')}
                        </Badge>
                      </Tooltip>
                    )}
                  </Group>
                </Table.Td>
                <Table.Td>
                  {v.secret ? (
                    <Badge size="sm" variant="light" color="grape" leftSection={<IconLock size={10} />}>
                      {t('builds.secret')}
                    </Badge>
                  ) : (
                    <Text ff="monospace" size="sm" lineClamp={2}>
                      {v.value}
                    </Text>
                  )}
                </Table.Td>
                <Table.Td>
                  <Group gap={4} justify="flex-end">
                    <ActionIcon variant="subtle" aria-label={t('common.edit')} onClick={() => setEditing(v)}>
                      <IconPencil size={14} />
                    </ActionIcon>
                    <ActionIcon
                      variant="subtle"
                      color="red"
                      aria-label={t('common.delete')}
                      onClick={() => remove.mutate(v.name)}
                    >
                      <IconTrash size={14} />
                    </ActionIcon>
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      <Group>
        <Button
          variant="light"
          size="xs"
          leftSection={<IconPlus size={14} />}
          onClick={() => setEditing('new')}
        >
          {t('builds.addVariable')}
        </Button>
      </Group>
      {editing && (
        <VariableModal
          scope={scope}
          variable={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void client.invalidateQueries({ queryKey: key });
          }}
        />
      )}
    </Stack>
  );
}

function VariableModal({
  scope,
  variable,
  onClose,
  onSaved,
}: {
  scope: VariableScope;
  variable: Variable | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(variable?.name ?? '');
  const [value, setValue] = useState(variable?.secret ? '' : (variable?.value ?? ''));
  const [secret, setSecret] = useState(variable?.secret ?? false);
  const [prot, setProt] = useState(variable?.protected ?? false);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/api/v1/builds/variables/{name}', {
          params: { path: { name }, query: query(scope) },
          body: { value, secret, protected: prot },
        }),
      ),
    onSuccess: onSaved,
  });
  return (
    <Modal opened onClose={onClose} title={variable ? t('builds.editVariable') : t('builds.addVariable')}>
      <Stack>
        <TextInput
          label={t('builds.variableName')}
          value={name}
          onChange={(e) => setName(e.currentTarget.value.toUpperCase().replace(/[^A-Z0-9_]/g, '_'))}
          disabled={!!variable}
          ff="monospace"
          required
          description={t('builds.variableNameHelp')}
        />
        {secret ? (
          <PasswordInput
            label={t('builds.variableValue')}
            value={value}
            onChange={(e) => setValue(e.currentTarget.value)}
            description={variable?.secret ? t('builds.secretKeepHelp') : t('builds.secretHelp')}
          />
        ) : (
          <Textarea
            label={t('builds.variableValue')}
            value={value}
            onChange={(e) => setValue(e.currentTarget.value)}
            autosize
            minRows={1}
            ff="monospace"
          />
        )}
        <Checkbox
          label={t('builds.secret')}
          description={t('builds.secretHelp')}
          checked={secret}
          onChange={(e) => setSecret(e.currentTarget.checked)}
        />
        {scope.scope !== 'environment' && (
          <Checkbox
            label={t('builds.protected')}
            description={t('builds.protectedHelp')}
            checked={prot}
            onChange={(e) => setProt(e.currentTarget.checked)}
          />
        )}
        <ErrorAlert error={save.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!name}>
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
