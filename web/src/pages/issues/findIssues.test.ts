// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { findIssuesQuery, looksLikeKey } from './findIssues';

const opts = { project: 'TSK', exclude: 'TSK-149' };

describe('finding issues from what someone typed', () => {
  it('takes keys and numbers as keys', () => {
    expect(findIssuesQuery(' tsk-132 ', opts)).toBe('key = TSK-132');
    expect(findIssuesQuery('OPS-7', opts)).toBe('key = OPS-7');
    expect(findIssuesQuery('132', opts)).toBe('key = TSK-132');
  });

  it("lists a project's issues for the start of its keys", () => {
    expect(findIssuesQuery('TSK', opts)).toBe('project = TSK AND key != TSK-149 ORDER BY updated DESC');
    expect(findIssuesQuery('ts', opts)).toBe('project = TSK AND key != TSK-149 ORDER BY updated DESC');
    expect(findIssuesQuery('ops-', opts)).toBe('project = OPS AND key != TSK-149 ORDER BY updated DESC');
  });

  it('searches words otherwise, in the project if asked', () => {
    expect(findIssuesQuery('', opts)).toBe('key != TSK-149 ORDER BY updated DESC');
    expect(findIssuesQuery('Standesamt "neu"', opts)).toBe(
      'key != TSK-149 AND text ~ "Standesamt \\"neu\\"*" ORDER BY updated DESC',
    );
    expect(findIssuesQuery('Brief', { ...opts, inProject: true })).toBe(
      'project = TSK AND key != TSK-149 AND text ~ "Brief*" ORDER BY updated DESC',
    );
  });

  it('tells keys from titles', () => {
    for (const s of ['TSK', 'tsk-', 'TSK-13', '132', 'OPS-2']) expect(looksLikeKey(s, 'TSK'), s).toBe(true);
    for (const s of ['T', 'Brief abwarten', 'Tasks', 'TSK 2']) expect(looksLikeKey(s, 'TSK'), s).toBe(false);
  });
});
