// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/** Where a page dragged in the page tree lands, relative to the row under the pointer. */
export type DropPosition = 'before' | 'inside' | 'after';

/** The body of POST /api/v1/wiki/pages/{id}/move. */
export interface MoveInput {
  parentId: number;
  before?: number;
  after?: number;
}

/**
 * The drop position for a pointer offsetY pixels below the top of a row of the given height: the outer quarters
 * put the page next to the row's page, the middle half inside it.
 */
export function dropPosition(offsetY: number, height: number): DropPosition {
  if (height <= 0) return 'inside';
  const at = offsetY / height;
  if (at < 0.25) return 'before';
  if (at > 0.75) return 'after';
  return 'inside';
}

/** A row of the page tree as a drop target. */
export interface DropTarget {
  id: number;
  /** Parent page; 0 at the top level of the space. */
  parentId: number;
  /** The children shown below the row, if it is expanded. */
  shownChildren: number[];
}

/**
 * The move for a drop at a position of a target row. Below an expanded page the line sits between the page and its
 * first child, so "after" makes the page the first child there.
 */
export function moveInput(position: DropPosition, target: DropTarget): MoveInput {
  switch (position) {
    case 'inside':
      return { parentId: target.id };
    case 'before':
      return { parentId: target.parentId, before: target.id };
    case 'after':
      return target.shownChildren.length > 0
        ? { parentId: target.id, before: target.shownChildren[0] }
        : { parentId: target.parentId, after: target.id };
  }
}

/** A page on the way from the top of the space to the page on screen. */
export interface PathPage {
  id: number;
  kind: string;
  spaceKey: string;
  status: string;
  hasChildren: boolean;
}

/**
 * The way to the page on screen as far as the tree shows it: for the top level (0) and each page on the way, the page
 * below it on the way. The way ends where it leaves the tree — at a page of another space, a blog post or a page
 * that is not current; a page on it has children in the tree if the way goes on below it.
 */
export function pathBelow<T extends PathPage>(path: T[], spaceKey: string): Map<number, T> {
  const end = path.findIndex((p) => p.spaceKey !== spaceKey || p.kind !== 'page' || p.status !== 'current');
  const way = end < 0 ? path : path.slice(0, end);
  const below = new Map<number, T>();
  way.forEach((p, i) =>
    below.set(i === 0 ? 0 : way[i - 1].id, i < way.length - 1 ? { ...p, hasChildren: true } : p),
  );
  return below;
}
