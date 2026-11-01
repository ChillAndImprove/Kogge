// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Badge, Button, Group, Select, Stack, Table, Text, TextInput } from '@mantine/core';
import { IconTrash } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifySuccess } from '../../components/notify';
import { GroupPicker, UserPicker, UsersPicker } from '../../components/Pickers';
import type { BranchPermission, Grant } from './hooks';

type Perm = Grant['permission'];
type HolderType = Grant['holder']['type'];

/** Edits the grants of a code project or repository. */
export function GrantsEditor({
  grants,
  project,
  onSave,
}: {
  grants: Grant[];
  project: boolean;
  onSave: (g: Grant[]) => Promise<unknown>;
}) {
  const { t } = useTranslation();
  const [list, setList] = useState<Grant[]>(grants);
  const [perm, setPerm] = useState<Perm>('repo.read');
  const [type, setType] = useState<HolderType>('user');
  const [holderId, setHolderId] = useState<number | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const perms: Perm[] = project
    ? ['repo.read', 'repo.write', 'repo.admin', 'codeproject.admin']
    : ['repo.read', 'repo.write', 'repo.admin'];
  const readOnlyHolder = type === 'logged-in' || type === 'anyone';
  const add = () => {
    const g: Grant = {
      permission: readOnlyHolder ? 'repo.read' : perm,
      holder: { type, id: readOnlyHolder ? 0 : (holderId ?? 0) },
    };
    if (!readOnlyHolder && !holderId) return;
    setList([...list, g]);
    setHolderId(null);
  };
  const save = () => {
    setSaving(true);
    setError(null);
    onSave(list)
      .then(() => notifySuccess(t('common.saved')))
      .catch(setError)
      .finally(() => setSaving(false));
  };
  return (
    <Stack>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('code.holder')}</Table.Th>
            <Table.Th>{t('code.permission')}</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {list.map((g, i) => (
            <Table.Tr key={`${g.permission}-${g.holder.type}-${g.holder.id}-${i}`}>
              <Table.Td>
                <Group gap="xs">
                  <Badge variant="light" size="sm">
                    {t(`code.holders.${g.holder.type}`, { defaultValue: g.holder.type })}
                  </Badge>
                  <Text size="sm">{g.holder.name || (g.holder.id ? `#${g.holder.id}` : '')}</Text>
                </Group>
              </Table.Td>
              <Table.Td>{t(`code.perms.${g.permission}`, { defaultValue: g.permission })}</Table.Td>
              <Table.Td>
                <ActionIcon
                  variant="subtle"
                  color="red"
                  onClick={() => setList(list.filter((_, j) => j !== i))}
                  aria-label={t('common.remove')}
                >
                  <IconTrash size={14} />
                </ActionIcon>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <Group align="flex-end" wrap="wrap">
        <Select
          label={t('code.holder')}
          value={type}
          onChange={(v) => setType((v as HolderType) ?? 'user')}
          data={(['user', 'group', 'logged-in', 'anyone'] as const).map((h) => ({
            value: h,
            label: t(`code.holders.${h}`),
          }))}
          w={220}
        />
        {type === 'user' && (
          <UserPicker value={holderId} onChange={setHolderId} w={240} aria-label={t('code.holders.user')} />
        )}
        {type === 'group' && (
          <GroupPicker value={holderId} onChange={setHolderId} w={240} aria-label={t('code.holders.group')} />
        )}
        {!readOnlyHolder && (
          <Select
            label={t('code.permission')}
            value={perm}
            onChange={(v) => setPerm((v as Perm) ?? 'repo.read')}
            data={perms.map((p) => ({ value: p, label: t(`code.perms.${p}`) }))}
            w={200}
          />
        )}
        <Button variant="default" onClick={add} disabled={!readOnlyHolder && !holderId}>
          {t('code.addGrant')}
        </Button>
      </Group>
      <ErrorAlert error={error} />
      <Group justify="flex-end">
        <Button onClick={save} loading={saving}>
          {t('common.save')}
        </Button>
      </Group>
    </Stack>
  );
}

const kinds = ['restrict-push', 'no-delete', 'no-rewrite', 'pull-request-only'] as const;

/** Edits branch permissions; rules inherited from the project are shown, not edited. */
export function BranchRulesEditor({
  rules,
  onSave,
}: {
  rules: BranchPermission[];
  onSave: (r: BranchPermission[]) => Promise<unknown>;
}) {
  const { t } = useTranslation();
  const [list, setList] = useState<BranchPermission[]>(rules);
  const [pattern, setPattern] = useState('');
  const [kind, setKind] = useState<BranchPermission['kind']>('no-rewrite');
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const update = (i: number, r: BranchPermission) => setList(list.map((x, j) => (j === i ? r : x)));
  const save = () => {
    setSaving(true);
    setError(null);
    onSave(list.filter((r) => !r.inherited))
      .then(() => notifySuccess(t('common.saved')))
      .catch(setError)
      .finally(() => setSaving(false));
  };
  return (
    <Stack>
      <Text size="sm" c="dimmed">
        {t('code.branchRulesHelp')}
      </Text>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('code.pattern')}</Table.Th>
            <Table.Th>{t('code.kind')}</Table.Th>
            <Table.Th>{t('code.exemptions')}</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {list.map((r, i) => (
            <Table.Tr key={`${r.id ?? 'new'}-${i}`}>
              <Table.Td>
                <Group gap="xs">
                  <Text ff="monospace" size="sm">
                    {r.pattern}
                  </Text>
                  {r.inherited && (
                    <Badge size="xs" variant="outline">
                      {t('code.inherited')}
                    </Badge>
                  )}
                </Group>
              </Table.Td>
              <Table.Td>{t(`code.kinds.${r.kind}`, { defaultValue: r.kind })}</Table.Td>
              <Table.Td>
                {r.inherited ? (
                  <Text size="sm" c="dimmed">
                    {r.exemptUsers.length + r.exemptGroups.length || '—'}
                  </Text>
                ) : (
                  <UsersPicker
                    value={r.exemptUsers}
                    onChange={(ids) => update(i, { ...r, exemptUsers: ids })}
                    w={260}
                    size="xs"
                    aria-label={t('code.exemptions')}
                  />
                )}
              </Table.Td>
              <Table.Td>
                {!r.inherited && (
                  <ActionIcon
                    variant="subtle"
                    color="red"
                    onClick={() => setList(list.filter((_, j) => j !== i))}
                    aria-label={t('common.remove')}
                  >
                    <IconTrash size={14} />
                  </ActionIcon>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <Group align="flex-end">
        <TextInput
          label={t('code.pattern')}
          value={pattern}
          onChange={(e) => setPattern(e.currentTarget.value)}
          placeholder="main"
          w={220}
        />
        <Select
          label={t('code.kind')}
          value={kind}
          onChange={(v) => setKind((v as BranchPermission['kind']) ?? 'no-rewrite')}
          data={kinds.map((k) => ({ value: k, label: t(`code.kinds.${k}`) }))}
          w={280}
        />
        <Button
          variant="default"
          disabled={!pattern.trim()}
          onClick={() => {
            setList([
              ...list,
              { pattern: pattern.trim(), kind, exemptUsers: [], exemptGroups: [], inherited: false },
            ]);
            setPattern('');
          }}
        >
          {t('code.addRule')}
        </Button>
      </Group>
      <ErrorAlert error={error} />
      <Group justify="flex-end">
        <Button onClick={save} loading={saving}>
          {t('common.save')}
        </Button>
      </Group>
    </Stack>
  );
}
