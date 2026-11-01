// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import '@mantine/charts/styles.css';

import { DonutChart } from '@mantine/charts';
import { Anchor, ColorSwatch, Group, Loader, Stack, Table, Text } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../../api/client';
import { ErrorAlert } from '../../components/ErrorAlert';
import { CreatedResolvedReport } from '../agile/ReportsPage';
import { searchLink, str, type Widget } from './widgets';

const fallback = [
  'blue.6',
  'teal.6',
  'orange.6',
  'grape.6',
  'cyan.6',
  'pink.6',
  'lime.6',
  'indigo.6',
  'yellow.6',
  'red.6',
];

function DistributionWidget({ query, field }: { query: string; field: string }) {
  const { t } = useTranslation();
  const res = useQuery({
    queryKey: ['issue', 'distribution', query, field],
    queryFn: () =>
      unwrap(api.GET('/api/v1/reports/distribution', { params: { query: { q: query, field } } })),
    retry: false,
  });
  if (res.error) return <ErrorAlert error={res.error} />;
  if (!res.data) return <Loader size="sm" />;
  const base = query.replace(/\s+ORDER BY.*$/i, '').trim();
  const items = res.data.items.map((it, i) => ({
    ...it,
    label:
      it.label || (field === 'statusCategory' ? t(`issueAdmin.categories.${it.key}`) : t('dashboards.none')),
    color: it.color ? `${it.color}.6` : fallback[i % fallback.length],
  }));
  if (items.length === 0)
    return (
      <Text size="sm" c="dimmed">
        {t('issues.noResults')}
      </Text>
    );
  return (
    <Group align="center" wrap="wrap">
      <DonutChart
        data={items.map((it) => ({ name: it.label, value: it.count, color: it.color }))}
        size={150}
        thickness={24}
        withTooltip
      />
      <Stack gap={2} style={{ flex: 1, minWidth: 180 }}>
        <Table>
          <Table.Tbody>
            {items.map((it) => (
              <Table.Tr key={it.key}>
                <Table.Td w={20}>
                  <ColorSwatch color={`var(--mantine-color-${it.color.replace('.', '-')})`} size={12} />
                </Table.Td>
                <Table.Td>
                  {it.query ? (
                    <Anchor
                      component={Link}
                      to={searchLink(base ? `(${base}) AND ${it.query}` : it.query)}
                      size="sm"
                    >
                      {it.label}
                    </Anchor>
                  ) : (
                    <Text size="sm">{it.label}</Text>
                  )}
                </Table.Td>
                <Table.Td w={50} ta="right">
                  {it.count}
                </Table.Td>
                <Table.Td w={50} ta="right" c="dimmed">
                  {Math.round((100 * it.count) / (res.data!.total || 1))}%
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
        {res.data.truncated && (
          <Text size="xs" c="orange">
            {t('dashboards.truncatedValues', { count: items.length })}
          </Text>
        )}
      </Stack>
    </Group>
  );
}

export default function ChartWidgets({ widget }: { widget: Widget }) {
  const c = widget.config ?? {};
  if (widget.type === 'distribution')
    return <DistributionWidget query={str(c.query)} field={str(c.field, 'status')} />;
  return <CreatedResolvedReport query={str(c.query)} height={220} />;
}
