// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { bulkEditInput, emptyEdit, selectionOf } from './bulkInput';
import { chosenInput, moveInput, noChoices, planReady, type MovePlan } from './MoveIssue';

describe('the input of bulk edits', () => {
  it('takes the chosen fields only, with the modes of fields with several values', () => {
    expect(bulkEditInput(emptyEdit)).toBeUndefined();
    expect(
      bulkEditInput({
        on: { priority: true, labels: true, components: true, duedate: false, assignee: true },
        values: { priority: 3, labels: ['urgent'], components: [4, 5], duedate: '2026-12-31' },
        modes: { labels: 'remove', components: 'replace' },
      }),
    ).toEqual({
      fields: { priority: 3, assignee: null },
      labels: { mode: 'remove', values: ['urgent'] },
      components: { mode: 'replace', ids: [4, 5] },
    });
    // Adding is the default mode; a field chosen without a value clears it.
    expect(bulkEditInput({ on: { fixVersions: true }, values: {}, modes: {} })).toEqual({
      fixVersions: { mode: 'add', ids: [] },
    });
  });

  it('names the issues by key or by query', () => {
    expect(selectionOf({ issues: ['A-1'], count: 1 })).toEqual({ issues: ['A-1'] });
    expect(selectionOf({ query: 'project = A', count: 5 })).toEqual({ query: 'project = A' });
  });
});

const plan: MovePlan = {
  project: { id: 2, key: 'OPS', name: 'Operations' },
  types: [
    {
      from: { id: 4, name: 'Bug', description: '', icon: 'bug', color: 'red', level: 0, position: 4 },
      choices: [{ id: 3, name: 'Task', description: '', icon: 'task', color: 'blue', level: 0, position: 3 }],
      chosen: 3,
      issues: ['SHOP-1'],
    },
  ],
  statuses: [
    {
      type: { id: 3, name: 'Task', description: '', icon: 'task', color: 'blue', level: 0, position: 3 },
      from: { id: 7, name: 'In Review', description: '', category: 'inprogress', position: 3 },
      choices: [{ id: 2, name: 'In Progress', description: '', category: 'inprogress', position: 2 }],
      chosen: 2,
      issues: ['SHOP-1'],
    },
  ],
  issues: [{ key: 'SHOP-1', movable: true, subtasks: [], changes: [] }],
};

describe('the choices of moves', () => {
  it('asks the server with the user’s choices and moves with the proposals for the rest', () => {
    expect(chosenInput('OPS', noChoices)).toEqual({ project: 'OPS', types: [], statuses: [] });
    expect(chosenInput('OPS', { types: { 4: 5 }, statuses: { '5:7': 1 } })).toEqual({
      project: 'OPS',
      types: [{ from: 4, to: 5 }],
      statuses: [{ type: 5, from: 7, to: 1 }],
    });
    expect(moveInput('OPS', plan, noChoices)).toEqual({
      project: 'OPS',
      types: [{ from: 4, to: 3 }],
      statuses: [{ type: 3, from: 7, to: 2 }],
    });
    expect(moveInput('OPS', plan, { types: {}, statuses: { '3:7': 9 } }).statuses).toEqual([
      { type: 3, from: 7, to: 9 },
    ]);
  });

  it('is ready when every question has an answer and an issue can move', () => {
    expect(planReady(plan)).toBe(true);
    expect(planReady(undefined)).toBe(false);
    expect(planReady({ ...plan, types: [{ ...plan.types[0], chosen: undefined }] })).toBe(false);
    expect(planReady({ ...plan, issues: [{ ...plan.issues[0], movable: false }] })).toBe(false);
  });
});
