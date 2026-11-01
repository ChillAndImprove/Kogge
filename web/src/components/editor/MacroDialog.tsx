// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Group, Modal, NumberInput, Select, Stack, Text, Textarea, TextInput } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { MermaidDiagram } from '../MermaidDiagram';
import { PagePicker, RepositoryPicker, SpacePicker } from '../Pickers';
import { macroDef, macroDefs } from './macros';

/** The diagram as it will look, drawn once the text rests for a moment. */
function DiagramPreview({ code }: { code: string }) {
  const [shown] = useDebouncedValue(code, 400);
  return <MermaidDiagram code={shown} />;
}

export interface MacroValue {
  name: string;
  params: Record<string, unknown>;
}

/** Chooses a macro (when name is empty; preset chooses one to start with) and asks for its parameters. */
export function MacroDialog({
  value,
  preset,
  onSave,
  onClose,
}: {
  value: MacroValue | null;
  preset?: string;
  onSave: (v: MacroValue) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(value?.name ?? preset ?? '');
  const [params, setParams] = useState<Record<string, unknown>>(value?.params ?? {});
  const def = macroDef(name);
  const set = (k: string, v: unknown) => setParams((p) => ({ ...p, [k]: v }));
  const missing = def?.params.some(
    (p) => p.required && (params[p.key] === undefined || params[p.key] === '' || params[p.key] === null),
  );
  return (
    <Modal
      opened
      onClose={onClose}
      title={def ? t(`macros.names.${def.name}`) : t('macros.insert')}
      size="lg"
    >
      <Stack>
        {!value?.name && (
          <Select
            label={t('macros.macro')}
            data={macroDefs.map((m) => ({ value: m.name, label: t(`macros.names.${m.name}`) }))}
            value={name || null}
            onChange={(v) => {
              setName(v ?? '');
              setParams({});
            }}
            searchable
            data-autofocus
          />
        )}
        {def && (
          <Text size="sm" c="dimmed">
            {t(`macros.help.${def.name}`)}
          </Text>
        )}
        {def?.params.map((p) => {
          const label = t(`macros.params.${p.key}`);
          const v = params[p.key] ?? p.default;
          switch (p.type) {
            case 'number':
              return (
                <NumberInput
                  key={p.key}
                  label={label}
                  value={v === undefined ? '' : Number(v)}
                  onChange={(x) => set(p.key, x === '' ? undefined : Number(x))}
                  min={1}
                />
              );
            case 'select':
              return (
                <Select
                  key={p.key}
                  label={label}
                  data={(p.options ?? []).map((o) => ({
                    value: o,
                    label: t(`macros.options.${o}`, { defaultValue: o }),
                  }))}
                  value={v === undefined ? null : String(v)}
                  onChange={(x) => set(p.key, x ?? undefined)}
                />
              );
            case 'page':
              return (
                <PagePicker
                  key={p.key}
                  label={label}
                  value={v ? Number(v) : null}
                  onChange={(x) => set(p.key, x ?? undefined)}
                  required={p.required}
                />
              );
            case 'space':
              return (
                <SpacePicker
                  key={p.key}
                  label={label}
                  value={v ? String(v) : null}
                  onChange={(x) => set(p.key, x ?? undefined)}
                />
              );
            case 'repository':
              return (
                <RepositoryPicker
                  key={p.key}
                  label={label}
                  value={v ? String(v) : null}
                  onChange={(x) => set(p.key, x ?? undefined)}
                  required={p.required}
                />
              );
            case 'kql':
            case 'code':
              return (
                <Stack key={p.key} gap="xs">
                  <Textarea
                    label={label}
                    value={String(v ?? '')}
                    onChange={(e) => set(p.key, e.currentTarget.value)}
                    autosize
                    minRows={p.type === 'code' ? 6 : 2}
                    required={p.required}
                    styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
                  />
                  {name === 'mermaid' && String(v ?? '').trim() !== '' && <DiagramPreview code={String(v)} />}
                </Stack>
              );
            case 'issue':
              return (
                <TextInput
                  key={p.key}
                  label={label}
                  placeholder="SHOP-123"
                  value={String(v ?? '')}
                  onChange={(e) => set(p.key, e.currentTarget.value.toUpperCase().trim())}
                  required={p.required}
                />
              );
            default:
              return (
                <TextInput
                  key={p.key}
                  label={label}
                  value={String(v ?? '')}
                  onChange={(e) => set(p.key, e.currentTarget.value)}
                  required={p.required}
                />
              );
          }
        })}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            disabled={!def || missing}
            onClick={() => {
              const clean: Record<string, unknown> = {};
              for (const p of def!.params) {
                const v = params[p.key] ?? p.default;
                if (v !== undefined && v !== '') clean[p.key] = v;
              }
              onSave({ name, params: clean });
              onClose();
            }}
          >
            {value?.name ? t('common.save') : t('macros.insertButton')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
