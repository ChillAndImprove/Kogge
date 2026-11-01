// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Badge,
  Box,
  Code,
  CopyButton,
  Group,
  Loader,
  NavLink,
  ScrollArea,
  Stack,
  Table,
  Text,
  TextInput,
  Title,
  Tooltip,
  UnstyledButton,
} from '@mantine/core';
import { IconCheck, IconChevronDown, IconChevronRight, IconCopy, IconSearch } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { ErrorAlert } from '../../components/ErrorAlert';

/** The parts of an OpenAPI 3.1 document the reference shows. */
interface Schema {
  $ref?: string;
  type?: string | string[];
  description?: string;
  enum?: unknown[];
  items?: Schema;
  properties?: Record<string, Schema>;
  required?: string[];
  additionalProperties?: Schema | boolean;
  format?: string;
  default?: unknown;
  minimum?: number;
  maximum?: number;
  maxLength?: number;
}
interface Parameter {
  name: string;
  in: string;
  required?: boolean;
  description?: string;
  schema?: Schema;
}
interface Operation {
  operationId: string;
  summary?: string;
  description?: string;
  tags?: string[];
  parameters?: Parameter[];
  requestBody?: { content?: Record<string, { schema?: Schema }>; required?: boolean };
  responses?: Record<string, { description?: string; content?: Record<string, { schema?: Schema }> }>;
}
interface Document {
  info: { title: string; version: string; description?: string };
  paths: Record<string, Record<string, Operation>>;
  components?: { schemas?: Record<string, Schema> };
}
interface Entry {
  method: string;
  path: string;
  op: Operation;
}

const methods = ['get', 'post', 'put', 'patch', 'delete'];
const methodColor: Record<string, string> = {
  get: 'teal',
  post: 'blue',
  put: 'orange',
  patch: 'violet',
  delete: 'red',
};

/**
 * The reference of the REST API, rendered from its OpenAPI document: operations by topic with their
 * parameters, bodies and answers, and an example call. It needs nothing from outside, like everything in Kogge.
 */
export function ApiDocsPage() {
  const { t } = useTranslation();
  const doc = useQuery({
    queryKey: ['openapi'],
    queryFn: async () => {
      const res = await fetch('/api/v1/openapi.json');
      if (!res.ok) throw new Error(res.statusText);
      return (await res.json()) as Document;
    },
    staleTime: Infinity,
  });
  const [search, setSearch] = useState('');
  const [tag, setTag] = useState<string | null>(null);
  const entries = useMemo(() => {
    const out: Entry[] = [];
    for (const [path, item] of Object.entries(doc.data?.paths ?? {})) {
      for (const method of methods) {
        if (item[method]) out.push({ method, path, op: item[method] });
      }
    }
    return out;
  }, [doc.data]);
  const tags = useMemo(() => {
    const counts = new Map<string, number>();
    for (const e of entries) for (const tg of e.op.tags ?? ['—']) counts.set(tg, (counts.get(tg) ?? 0) + 1);
    return [...counts].sort((a, b) => a[0].localeCompare(b[0]));
  }, [entries]);
  const q = search.trim().toLowerCase();
  const shown = entries.filter(
    (e) =>
      (!tag || (e.op.tags ?? ['—']).includes(tag)) &&
      (!q ||
        e.path.toLowerCase().includes(q) ||
        e.op.operationId.includes(q) ||
        (e.op.summary ?? '').toLowerCase().includes(q)),
  );
  const schemas = doc.data?.components?.schemas ?? {};
  return (
    <Group align="flex-start" wrap="nowrap" gap="xl">
      <Box w={240} visibleFrom="md" style={{ flexShrink: 0, position: 'sticky', top: 64 }}>
        <ScrollArea.Autosize mah="calc(100vh - 96px)">
          <NavLink label={t('apiDocs.all')} active={tag === null} onClick={() => setTag(null)} />
          {tags.map(([name, count]) => (
            <NavLink
              key={name}
              label={name}
              active={tag === name}
              onClick={() => setTag(name)}
              rightSection={
                <Text size="xs" c="dimmed">
                  {count}
                </Text>
              }
            />
          ))}
        </ScrollArea.Autosize>
      </Box>
      <Stack style={{ flex: 1, minWidth: 0 }} gap="md">
        <div>
          <Title order={2}>{t('apiDocs.title')}</Title>
          <Text c="dimmed" size="sm" mt={4}>
            {t('apiDocs.intro')}{' '}
            <Anchor href="/api/v1/openapi.json" target="_blank" rel="noreferrer">
              openapi.json
            </Anchor>
            {doc.data ? ` · ${t('apiDocs.version', { version: doc.data.info.version })}` : ''}
          </Text>
        </div>
        <TextInput
          leftSection={<IconSearch size={16} />}
          placeholder={t('apiDocs.search')}
          aria-label={t('apiDocs.search')}
          value={search}
          onChange={(e) => setSearch(e.currentTarget.value)}
          maw={480}
        />
        <ErrorAlert error={doc.error} />
        {doc.isLoading && <Loader size="sm" />}
        <Stack gap={0} className="kogge-api-list">
          {shown.map((e) => (
            <OperationRow key={e.method + e.path} entry={e} schemas={schemas} />
          ))}
        </Stack>
        {doc.data && shown.length === 0 && (
          <Text c="dimmed" size="sm">
            {t('apiDocs.none')}
          </Text>
        )}
      </Stack>
    </Group>
  );
}

function OperationRow({ entry, schemas }: { entry: Entry; schemas: Record<string, Schema> }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const { method, path, op } = entry;
  const body = op.requestBody?.content?.['application/json']?.schema;
  const params = op.parameters ?? [];
  return (
    <Box style={{ borderBottom: '1px solid var(--mantine-color-default-border)' }}>
      <UnstyledButton onClick={() => setOpen(!open)} py={8} w="100%" aria-expanded={open} id={op.operationId}>
        <Group gap="sm" wrap="nowrap">
          {open ? <IconChevronDown size={14} /> : <IconChevronRight size={14} />}
          <Badge w={76} variant="light" color={methodColor[method]} radius="sm" style={{ flexShrink: 0 }}>
            {method.toUpperCase()}
          </Badge>
          <Text ff="monospace" size="sm" style={{ whiteSpace: 'nowrap' }}>
            {path}
          </Text>
          <Text size="sm" c="dimmed" truncate>
            {op.summary}
          </Text>
        </Group>
      </UnstyledButton>
      {open && (
        <Stack gap="sm" pl={30} pb="md">
          <Group gap="xs">
            <Text size="xs" c="dimmed">
              {t('apiDocs.operation')}
            </Text>
            <Code>{op.operationId}</Code>
          </Group>
          {op.description && <Text size="sm">{op.description}</Text>}
          {params.length > 0 && (
            <Section title={t('apiDocs.parameters')}>
              <Table withRowBorders={false} verticalSpacing={4}>
                <Table.Tbody>
                  {params.map((p) => (
                    <Table.Tr key={p.in + p.name}>
                      <Table.Td w={200}>
                        <Text ff="monospace" size="sm">
                          {p.name}
                          {p.required && <RequiredMark />}
                        </Text>
                        <Text size="xs" c="dimmed">
                          {p.in}
                        </Text>
                      </Table.Td>
                      <Table.Td w={160}>
                        <TypeLabel schema={p.schema} schemas={schemas} />
                      </Table.Td>
                      <Table.Td>
                        <Text size="sm">{p.description ?? p.schema?.description}</Text>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Section>
          )}
          {body && (
            <Section title={t('apiDocs.body')}>
              <SchemaView schema={body} schemas={schemas} depth={0} />
            </Section>
          )}
          <Section title={t('apiDocs.responses')}>
            <Stack gap={4}>
              {Object.entries(op.responses ?? {}).map(([status, res]) => {
                const schema = Object.values(res.content ?? {})[0]?.schema;
                return (
                  <Group key={status} gap="sm" align="flex-start" wrap="nowrap">
                    <Badge
                      variant="outline"
                      color={status.startsWith('2') ? 'teal' : 'gray'}
                      w={72}
                      radius="sm"
                    >
                      {status === 'default' ? t('apiDocs.error') : status}
                    </Badge>
                    <Text size="sm">{res.description}</Text>
                    {schema && <TypeLabel schema={schema} schemas={schemas} />}
                  </Group>
                );
              })}
            </Stack>
          </Section>
          <Section title={t('apiDocs.example')}>
            <Example method={method} path={path} body={body} schemas={schemas} />
          </Section>
        </Stack>
      )}
    </Box>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <Text size="xs" fw={600} tt="uppercase" c="dimmed" mb={4}>
        {title}
      </Text>
      {children}
    </div>
  );
}

function RequiredMark() {
  const { t } = useTranslation();
  return (
    <Tooltip label={t('apiDocs.required')}>
      <Text span c="red" ml={2}>
        *
      </Text>
    </Tooltip>
  );
}

/** Follows a reference into the components. */
function resolve(
  schema: Schema | undefined,
  schemas: Record<string, Schema>,
): [Schema | undefined, string | null] {
  if (schema?.$ref) {
    const name = schema.$ref.split('/').pop() ?? '';
    return [schemas[name], name];
  }
  return [schema, null];
}

/** A short type: string, integer, Issue, string[], "a" | "b". */
function typeOf(schema: Schema | undefined, schemas: Record<string, Schema>): string {
  const [s, name] = resolve(schema, schemas);
  if (!s) return name ?? 'any';
  if (s.enum) return s.enum.map((v) => JSON.stringify(v)).join(' | ');
  const types = (Array.isArray(s.type) ? s.type : [s.type]).filter(Boolean) as string[];
  const main = types.find((tp) => tp !== 'null');
  const nullable = types.includes('null') ? ' | null' : '';
  if (main === 'array') return `${typeOf(s.items, schemas)}[]${nullable}`;
  if (main === 'object' && name) return name + nullable;
  return (
    (main === 'object' ? 'object' : (main ?? name ?? 'any')) + (s.format ? ` (${s.format})` : '') + nullable
  );
}

function TypeLabel({ schema, schemas }: { schema?: Schema; schemas: Record<string, Schema> }) {
  return (
    <Text ff="monospace" size="xs" c="var(--mantine-primary-color-filled)">
      {typeOf(schema, schemas)}
    </Text>
  );
}

/** The properties of an object, nested ones on request. */
function SchemaView({
  schema,
  schemas,
  depth,
}: {
  schema: Schema;
  schemas: Record<string, Schema>;
  depth: number;
}) {
  const [s] = resolve(schema, schemas);
  const target =
    s?.type === 'array' || (Array.isArray(s?.type) && s.type.includes('array'))
      ? resolve(s?.items, schemas)[0]
      : s;
  const props = Object.entries(target?.properties ?? {}).filter(([n]) => n !== '$schema');
  if (props.length === 0) return <TypeLabel schema={schema} schemas={schemas} />;
  return (
    <Stack
      gap={2}
      pl={depth ? 'md' : 0}
      style={depth ? { borderLeft: '1px solid var(--mantine-color-default-border)' } : undefined}
    >
      {props.map(([name, prop]) => (
        <Property
          key={name}
          name={name}
          prop={prop}
          required={target?.required?.includes(name) ?? false}
          schemas={schemas}
          depth={depth}
        />
      ))}
    </Stack>
  );
}

function Property({
  name,
  prop,
  required,
  schemas,
  depth,
}: {
  name: string;
  prop: Schema;
  required: boolean;
  schemas: Record<string, Schema>;
  depth: number;
}) {
  const [open, setOpen] = useState(false);
  const [resolved] = resolve(prop, schemas);
  const item = resolve(resolved?.items, schemas)[0];
  const nested = Object.keys(resolved?.properties ?? item?.properties ?? {}).length > 0 && depth < 4;
  return (
    <div>
      <Group gap="xs" wrap="nowrap" align="baseline">
        {nested ? (
          <UnstyledButton onClick={() => setOpen(!open)} aria-expanded={open}>
            <Group gap={2} wrap="nowrap">
              {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
              <Text ff="monospace" size="sm">
                {name}
              </Text>
            </Group>
          </UnstyledButton>
        ) : (
          <Text ff="monospace" size="sm" pl={14}>
            {name}
          </Text>
        )}
        {required && <RequiredMark />}
        <TypeLabel schema={prop} schemas={schemas} />
        <Text size="xs" c="dimmed" lineClamp={2}>
          {prop.description ?? resolved?.description}
        </Text>
      </Group>
      {open && nested && <SchemaView schema={prop} schemas={schemas} depth={depth + 1} />}
    </div>
  );
}

/** A value of the right shape for an example body: required properties, first enum values, placeholders. */
function exampleOf(schema: Schema | undefined, schemas: Record<string, Schema>, depth = 0): unknown {
  const [s] = resolve(schema, schemas);
  if (!s || depth > 4) return null;
  if (s.default !== undefined) return s.default;
  if (s.enum) return s.enum[0];
  const type = (Array.isArray(s.type) ? s.type.find((tp) => tp !== 'null') : s.type) ?? 'object';
  switch (type) {
    case 'string':
      return s.format === 'date-time' ? '2026-01-31T12:00:00Z' : s.format === 'date' ? '2026-01-31' : '…';
    case 'integer':
    case 'number':
      return s.minimum ?? 1;
    case 'boolean':
      return false;
    case 'array':
      return [];
    default: {
      const out: Record<string, unknown> = {};
      for (const name of s.required ?? []) {
        if (name !== '$schema') out[name] = exampleOf(s.properties?.[name], schemas, depth + 1);
      }
      return out;
    }
  }
}

function Example({
  method,
  path,
  body,
  schemas,
}: {
  method: string;
  path: string;
  body?: Schema;
  schemas: Record<string, Schema>;
}) {
  const { t } = useTranslation();
  let curl = `curl -H "Authorization: Bearer $KOGGE_TOKEN"`;
  if (method !== 'get') curl += ` -X ${method.toUpperCase()}`;
  if (body)
    curl += ` \\\n  -H "Content-Type: application/json" -d '${JSON.stringify(exampleOf(body, schemas))}'`;
  curl += ` \\\n  ${window.location.origin}${path}`;
  return (
    <Box pos="relative">
      <Code block>{curl}</Code>
      <CopyButton value={curl.replace(/\\\n {2}/g, ' ')}>
        {({ copied, copy }) => (
          <Tooltip label={copied ? t('common.copied') : t('common.copy')}>
            <UnstyledButton onClick={copy} pos="absolute" top={8} right={8} aria-label={t('common.copy')}>
              {copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
            </UnstyledButton>
          </Tooltip>
        )}
      </CopyButton>
    </Box>
  );
}
