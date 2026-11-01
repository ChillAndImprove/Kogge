// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Checkbox, Group, Table, Tooltip, UnstyledButton, VisuallyHidden } from '@mantine/core';
import { IconArrowDown, IconArrowUp, IconSelector } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import type { Column, IssueRow } from './columns';
import { canonicalOrderField, parseOrder } from './kqlOrder';

/** The issues chosen for a bulk change, by key. */
export interface Selection {
  selected: Set<string>;
  onChange: (keys: string[], selected: boolean) => void;
}

/**
 * Issues in the chosen columns. Headers of columns issues can be ordered by sort the query: the first click orders
 * by the column (dates, priorities and votes from the highest), the next one reverses the order. With a selection,
 * a first column chooses issues (for bulk changes), its header all of the page.
 */
export function IssueTable({
  rows,
  columns,
  query,
  onSort,
  selection,
}: {
  rows: IssueRow[];
  columns: Column[];
  query: string;
  onSort?: (field: string, desc: boolean) => void;
  selection?: Selection;
}) {
  const { t } = useTranslation();
  const first = parseOrder(query).items[0];
  const sortedBy = first ? canonicalOrderField(first.field) : '';
  const onPage = selection ? rows.filter((r) => selection.selected.has(r.key)).length : 0;
  return (
    <Table.ScrollContainer minWidth={640}>
      <Table data-testid="issue-table" verticalSpacing={6}>
        <Table.Thead>
          <Table.Tr>
            {selection && (
              <Table.Th w={36}>
                <Checkbox
                  size="xs"
                  aria-label={t('bulk.selectPage')}
                  checked={rows.length > 0 && onPage === rows.length}
                  indeterminate={onPage > 0 && onPage < rows.length}
                  onChange={(e) =>
                    selection.onChange(
                      rows.map((r) => r.key),
                      e.currentTarget.checked,
                    )
                  }
                />
              </Table.Th>
            )}
            {columns.map((c) => {
              const active = !!c.order && sortedBy === canonicalOrderField(c.order);
              const desc = active ? first.desc : !!c.descending;
              const label = c.narrow ? <VisuallyHidden>{c.label}</VisuallyHidden> : c.label;
              const icon = active ? (
                desc ? (
                  <IconArrowDown size={14} aria-hidden />
                ) : (
                  <IconArrowUp size={14} aria-hidden />
                )
              ) : (
                <IconSelector size={14} aria-hidden style={{ opacity: 0.45 }} />
              );
              return (
                <Table.Th
                  key={c.key}
                  w={c.narrow ? 40 : undefined}
                  aria-sort={active ? (desc ? 'descending' : 'ascending') : undefined}
                  style={{ whiteSpace: 'nowrap' }}
                >
                  {c.order && onSort ? (
                    <Tooltip label={t('navigator.sortBy', { name: c.label })} openDelay={400}>
                      <UnstyledButton
                        onClick={() => onSort(c.order!, active ? !first.desc : !!c.descending)}
                        aria-label={t('navigator.sortBy', { name: c.label })}
                        fz="sm"
                        fw={600}
                      >
                        <Group gap={4} wrap="nowrap">
                          {label}
                          {icon}
                        </Group>
                      </UnstyledButton>
                    </Tooltip>
                  ) : (
                    label
                  )}
                </Table.Th>
              );
            })}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {rows.map((r) => (
            <Table.Tr key={r.id} data-selected={selection?.selected.has(r.key) || undefined}>
              {selection && (
                <Table.Td>
                  <Checkbox
                    size="xs"
                    aria-label={t('bulk.selectIssue', { key: r.key })}
                    checked={selection.selected.has(r.key)}
                    onChange={(e) => selection.onChange([r.key], e.currentTarget.checked)}
                  />
                </Table.Td>
              )}
              {columns.map((c) => (
                <Table.Td key={c.key}>{c.cell(r)}</Table.Td>
              ))}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}
