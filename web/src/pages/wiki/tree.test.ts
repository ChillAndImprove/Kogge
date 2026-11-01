// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { dropPosition, moveInput, pathBelow } from './tree';

describe('pathBelow', () => {
  const page = (
    id: number,
    more: Partial<{ kind: string; spaceKey: string; status: string; hasChildren: boolean }> = {},
  ) => ({
    id,
    kind: 'page',
    spaceKey: 'DOCS',
    status: 'current',
    hasChildren: false,
    ...more,
  });

  it('gives the page below the top level and below each page on the way', () => {
    const below = pathBelow([page(1), page(5), page(9, { hasChildren: true })], 'DOCS');
    expect([...below.keys()]).toEqual([0, 1, 5]);
    expect(below.get(0)).toMatchObject({ id: 1, hasChildren: true });
    expect(below.get(1)).toMatchObject({ id: 5, hasChildren: true });
    expect(below.get(5)).toMatchObject({ id: 9, hasChildren: true });
  });
  it('keeps what the page on screen says about its children', () => {
    expect(pathBelow([page(1), page(5)], 'DOCS').get(1)).toMatchObject({ id: 5, hasChildren: false });
  });
  it('ends where the way leaves the tree', () => {
    const draft = pathBelow([page(1), page(5), page(9, { status: 'draft' })], 'DOCS');
    expect([...draft.keys()]).toEqual([0, 1]);
    expect(draft.get(1)).toMatchObject({ id: 5, hasChildren: false });
    expect([...pathBelow([page(1), page(5, { status: 'archived' }), page(9)], 'DOCS').keys()]).toEqual([0]);
    expect([...pathBelow([page(3, { kind: 'blog' })], 'DOCS').keys()]).toEqual([]);
    expect([...pathBelow([page(1, { spaceKey: 'HR' })], 'DOCS').keys()]).toEqual([]);
  });
});

describe('dropPosition', () => {
  it('puts the page before the row in the upper quarter', () => {
    expect(dropPosition(0, 28)).toBe('before');
    expect(dropPosition(6.9, 28)).toBe('before');
  });
  it('puts the page inside the row in the middle half', () => {
    expect(dropPosition(7, 28)).toBe('inside');
    expect(dropPosition(14, 28)).toBe('inside');
    expect(dropPosition(21, 28)).toBe('inside');
  });
  it('puts the page after the row in the lower quarter', () => {
    expect(dropPosition(21.1, 28)).toBe('after');
    expect(dropPosition(28, 28)).toBe('after');
  });
  it('falls back to inside for rows without a height', () => {
    expect(dropPosition(5, 0)).toBe('inside');
  });
});

describe('moveInput', () => {
  const collapsed = { id: 7, parentId: 3, shownChildren: [] };
  const expanded = { id: 7, parentId: 3, shownChildren: [11, 12] };

  it('makes the page the last child when dropped inside', () => {
    expect(moveInput('inside', collapsed)).toEqual({ parentId: 7 });
    expect(moveInput('inside', expanded)).toEqual({ parentId: 7 });
  });
  it('places the page before a sibling', () => {
    expect(moveInput('before', collapsed)).toEqual({ parentId: 3, before: 7 });
    expect(moveInput('before', expanded)).toEqual({ parentId: 3, before: 7 });
  });
  it('places the page after a collapsed sibling', () => {
    expect(moveInput('after', collapsed)).toEqual({ parentId: 3, after: 7 });
  });
  it('makes the page the first child below an expanded page', () => {
    expect(moveInput('after', expanded)).toEqual({ parentId: 7, before: 11 });
  });
  it('works at the top level of the space', () => {
    expect(moveInput('before', { id: 1, parentId: 0, shownChildren: [] })).toEqual({
      parentId: 0,
      before: 1,
    });
  });
});
