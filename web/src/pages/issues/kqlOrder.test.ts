// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { canonicalOrderField, orderBy, parseOrder } from './kqlOrder';

describe('ORDER BY of KQL queries', () => {
  it('finds the clause outside of quoted text and parentheses', () => {
    expect(parseOrder('project = SHOP ORDER BY priority DESC, updated')).toEqual({
      where: 'project = SHOP',
      items: [
        { field: 'priority', desc: true },
        { field: 'updated', desc: false },
      ],
    });
    expect(parseOrder('summary ~ "order by me" AND status = Open')).toEqual({
      where: 'summary ~ "order by me" AND status = Open',
      items: [],
    });
    expect(parseOrder("summary ~ 'it\\'s ORDER BY' order by \"Story Points\" asc").items).toEqual([
      { field: '"Story Points"', desc: false },
    ]);
    expect(parseOrder('ORDER BY created DESC')).toEqual({
      where: '',
      items: [{ field: 'created', desc: true }],
    });
    expect(parseOrder('labels = border by')).toEqual({ where: 'labels = border by', items: [] });
  });

  it('orders by a field first and keeps the earlier order', () => {
    expect(orderBy('project = SHOP ORDER BY priority DESC, updated DESC', 'created', true)).toBe(
      'project = SHOP ORDER BY created DESC, priority DESC, updated DESC',
    );
    expect(orderBy('project = SHOP ORDER BY priority DESC, updated', 'priority', false)).toBe(
      'project = SHOP ORDER BY priority ASC, updated ASC',
    );
    expect(orderBy('project = SHOP', 'cf[12]', false)).toBe('project = SHOP ORDER BY cf[12] ASC');
    expect(orderBy('', 'key', false)).toBe('ORDER BY key ASC');
    expect(orderBy('status = Open ORDER BY a, b, c', 'd', true)).toBe(
      'status = Open ORDER BY d DESC, a ASC, b ASC',
    );
    expect(orderBy('status = Open ORDER BY duedate', 'due', true)).toBe('status = Open ORDER BY due DESC');
  });

  it('compares fields by their KQL names', () => {
    expect(canonicalOrderField('DueDate')).toBe('due');
    expect(canonicalOrderField('"Issue"')).toBe('key');
    expect(canonicalOrderField('cf[12]')).toBe('cf[12]');
  });
});
