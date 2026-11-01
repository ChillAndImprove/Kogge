// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Anchor, Button, Checkbox, Group, Loader, Select, Stack, Text, TextInput } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconLock } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { ApiError, api, unwrap, type Schemas } from '../../api/client';
import { Editor, isEmptyDoc, type Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import type { CreateIssueOptions } from './CreateIssue';
import {
  F,
  FieldInput,
  isEmptyValue,
  layoutFields,
  useFieldContext,
  useFieldLabel,
  useFieldLayout,
  useSecurityLevels,
  type LayoutField,
} from './fields';
import { TypeIcon, useIssueConfig, useProject } from './shared';

const lastProjectKey = 'kogge.lastProject';

function rememberedProject(): string | null {
  try {
    return localStorage.getItem(lastProjectKey);
  } catch {
    return null;
  }
}

/** Fields that the form places itself, or that new issues cannot have. */
const own: string[] = [
  F.summary,
  F.description,
  F.parent,
  F.security,
  F.reporter,
  F.remainingEstimate,
  F.sprint,
];

/**
 * Creates an issue with the fields the project's field layout asks for when creating issues of the chosen type:
 * required ones are marked, custom fields have their inputs, the security level starts at the
 * scheme's default.
 */
export function CreateIssueForm({ options, onClose }: { options: CreateIssueOptions; onClose: () => void }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const config = useIssueConfig();
  const label = useFieldLabel();
  const projects = useQuery({
    queryKey: ['projects'],
    queryFn: () => unwrap(api.GET('/api/v1/projects')),
  });
  const [projectKey, setProjectKey] = useState<string | null>(options.project ?? rememberedProject());
  const project = useProject(projectKey ?? undefined);
  const [typeId, setTypeId] = useState<string | null>(options.typeId ? String(options.typeId) : null);
  const [summary, setSummary] = useState(options.summary ?? '');
  const [description, setDescription] = useState<Doc | null>(
    (options.description as Doc | undefined) ?? null,
  );
  const [values, setValues] = useState<Record<string, unknown>>({});
  const [parent, setParent] = useState<string | null>(options.parent ?? null);
  const [security, setSecurity] = useState<string | null | undefined>(undefined);
  const [another, setAnother] = useState(false);
  const [formKey, setFormKey] = useState(0);
  const [tried, setTried] = useState(false);

  const types = (project.data?.issueTypes ?? []).filter((ty) => (options.parent ? ty.level === -1 : true));
  const type = types.find((ty) => String(ty.id) === typeId) ?? types.find((ty) => ty.level === 0) ?? types[0];
  const level = type?.level ?? 0;
  const layout = useFieldLayout(projectKey ?? undefined, type?.id);
  const ctx = useFieldContext(projectKey ?? undefined, level);
  const levels = useSecurityLevels(projectKey ?? undefined, !!project.data?.securitySchemeId && level !== -1);
  const permissions = project.data?.permissions ?? [];

  const create = layoutFields(layout.data, config.data?.fields).filter((f) => f.item.create);
  const item = (key: string) => create.find((f) => f.def.key === key);
  const fields = create.filter(
    (f) => !own.includes(f.def.key) && (f.def.key !== F.assignee || permissions.includes('issue.assign')),
  );
  const parentItem = item(F.parent);
  const showParent = level === -1 || (level === 0 && !!parentItem);
  const parentRequired = level === -1 || !!parentItem?.item.required;
  const choices = levels.data;
  const showSecurity = !!item(F.security) && !!choices?.canSet && choices.levels.length > 0 && level !== -1;
  // The scheme's default level is chosen at first, if the user may choose it.
  const defaultLevel =
    choices?.default && choices.levels.some((l) => l.id === choices.default!.id)
      ? String(choices.default.id)
      : null;
  const securityValue = security === undefined ? defaultLevel : security;

  const defaultPriority = config.data?.priorities.find((p) => p.isDefault)?.id ?? null;
  const value = (key: string) =>
    key in values ? values[key] : (options.fields?.[key] ?? (key === F.priority ? defaultPriority : null));
  const missing = (f: LayoutField) => f.item.required && isEmptyValue(value(f.def.key));
  const incomplete =
    !summary.trim() ||
    (parentRequired && !parent) ||
    fields.some(missing) ||
    (!!item(F.description)?.item.required && isEmptyDoc(description));

  const submit = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = { ...options.fields };
      for (const f of fields) {
        const v = value(f.def.key);
        if (!isEmptyValue(v)) body[f.def.key] = v;
      }
      if (showSecurity && securityValue) body[F.security] = Number(securityValue);
      return unwrap(
        api.POST('/api/v1/issues', {
          body: {
            project: projectKey!,
            typeId: type!.id,
            summary: summary.trim(),
            description:
              description && !isEmptyDoc(description) ? (description as Schemas['Doc']) : undefined,
            parent: parent ?? undefined,
            fields: body,
          },
        }),
      );
    },
    onSuccess: (issue) => {
      options.onCreated?.(issue.key);
      try {
        localStorage.setItem(lastProjectKey, issue.project.key);
      } catch {
        // storage unavailable — only a convenience
      }
      void client.invalidateQueries({ queryKey: ['issue', 'search'] });
      void client.invalidateQueries({ queryKey: ['board'] });
      void client.invalidateQueries({ queryKey: ['backlog'] });
      if (options.parent) void client.invalidateQueries({ queryKey: ['issue', options.parent] });
      notifications.show({
        color: 'teal',
        message: (
          <Text size="sm">
            {t('issues.created')}:{' '}
            <Anchor component={Link} to={`/browse/${issue.key}`} fw={600}>
              {issue.key}
            </Anchor>{' '}
            {issue.summary}
          </Text>
        ),
      });
      if (another) {
        // The next issue is likely alike: keep the fields, clear what describes this one.
        setSummary('');
        setDescription(null);
        setTried(false);
        setFormKey((k) => k + 1);
      } else {
        onClose();
      }
    },
  });
  // Errors of fields show at the fields.
  const fieldErrors = new Map<string, string>();
  if (submit.error instanceof ApiError) {
    for (const e of submit.error.errors) {
      const m = /^body\.(?:fields\.)?(.+)$/.exec(e.location ?? '');
      if (m) fieldErrors.set(m[1], e.message);
    }
  }
  const required = t('issues.required');
  const error = (key: string, isMissing: boolean) =>
    fieldErrors.get(key) ?? (tried && isMissing ? required : undefined);

  return (
    <Stack>
      <Group grow align="flex-start">
        <Select
          label={t('issues.project')}
          data={(projects.data ?? [])
            .filter((p) => !p.archived)
            .map((p) => ({ value: p.key, label: `${p.name} (${p.key})` }))}
          value={projectKey}
          onChange={(v) => {
            setProjectKey(v);
            setTypeId(null);
            setValues({});
            setSecurity(undefined);
          }}
          searchable
          required
          disabled={!!options.parent}
        />
        <Select
          label={t('issues.type')}
          data={types.map((ty) => ({ value: String(ty.id), label: ty.name }))}
          value={type ? String(type.id) : null}
          onChange={setTypeId}
          leftSection={<TypeIcon type={type} size={16} />}
          renderOption={({ option }) => (
            <Group gap="xs" wrap="nowrap">
              <TypeIcon type={types.find((ty) => String(ty.id) === option.value)} size={16} />
              <Text size="sm">{option.label}</Text>
            </Group>
          )}
          allowDeselect={false}
          required
        />
      </Group>
      <TextInput
        label={t('issues.summary')}
        value={summary}
        onChange={(e) => setSummary(e.currentTarget.value)}
        required
        data-autofocus
        maxLength={255}
        error={error(F.summary, !summary.trim())}
      />
      {(item(F.description) ?? !layout.data) && (
        <Stack gap={4}>
          <Text size="sm" fw={550}>
            {t('issues.description')}
            {item(F.description)?.item.required && (
              <Text span c="red">
                {' '}
                *
              </Text>
            )}
          </Text>
          <Editor key={formKey} value={description} onChange={setDescription} minHeight={140} />
        </Stack>
      )}
      {showParent && projectKey && (
        <FieldInput
          def={{
            key: F.parent,
            name: level === -1 ? t('issues.parent') : t('issues.epic'),
            type: 'issue',
            custom: false,
            editable: true,
            orderable: false,
            description: level === -1 ? undefined : t('issues.epicHelp'),
          }}
          value={parent}
          onChange={(v) => setParent((v as string | null) ?? null)}
          ctx={ctx}
          label={level === -1 ? t('issues.parent') : t('issues.epic')}
          required={parentRequired}
          error={error(F.parent, parentRequired && !parent)}
        />
      )}
      {layout.isLoading && type && <Loader size="sm" />}
      <div className="kogge-form-grid">
        {fields.map((f) => (
          <div key={`${f.def.key}-${formKey}`} data-wide={f.def.type === 'textarea' || undefined}>
            <FieldInput
              def={f.def}
              value={value(f.def.key)}
              onChange={(v) => setValues((cur) => ({ ...cur, [f.def.key]: v }))}
              ctx={ctx}
              label={label(f.def)}
              required={f.item.required}
              error={error(f.def.key, missing(f))}
            />
          </div>
        ))}
        {showSecurity && (
          <Select
            label={t('issues.security')}
            leftSection={<IconLock size={14} />}
            data={choices!.levels.map((l) => ({ value: String(l.id), label: l.name }))}
            value={securityValue}
            onChange={(v) => setSecurity(v)}
            clearable
            placeholder={t('issues.noSecurityLevel')}
            description={choices!.levels.find((l) => String(l.id) === securityValue)?.description}
            error={fieldErrors.get(F.security)}
          />
        )}
      </div>
      {!showSecurity && choices?.default && level !== -1 && (
        <Text size="xs" c="dimmed">
          <IconLock size={12} style={{ verticalAlign: 'middle' }} />{' '}
          {t('issues.defaultSecurity', { level: choices.default.name })}
        </Text>
      )}
      <ErrorAlert error={fieldErrors.size > 0 ? null : submit.error} />
      <Group justify="space-between">
        <Checkbox
          label={t('issues.createAnother')}
          checked={another}
          onChange={(e) => setAnother(e.currentTarget.checked)}
        />
        <Group gap="xs">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={() => {
              setTried(true);
              if (!incomplete) submit.mutate();
            }}
            loading={submit.isPending}
            disabled={!projectKey || !type}
          >
            {t('issues.create')}
          </Button>
        </Group>
      </Group>
    </Stack>
  );
}
