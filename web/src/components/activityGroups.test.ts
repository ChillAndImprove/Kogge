// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import {
  changedFields,
  dayKey,
  dayKind,
  groupEntries,
  pushedCommits,
  statusChain,
  type Entry,
} from './activityGroups';

const mara = { id: 3, username: 'mara', displayName: 'Mara Lindner', active: true };
const jonas = { id: 4, username: 'jonas', displayName: 'Jonas Weber', active: true };
const repo = {
  kind: 'repository' as const,
  key: 'WEB/storefront',
  name: 'storefront',
  url: '/code/web/storefront',
};
const project = { kind: 'project' as const, key: 'SHOP', name: 'Webshop', url: '/projects/SHOP' };

let n = 0;
function entry(at: string, verb: string, more: Partial<Entry> = {}): Entry {
  n++;
  return {
    key: `k${n}`,
    product: 'kontor',
    verb,
    at,
    actor: mara,
    title: 'SHOP-13 Partial refunds',
    url: '/browse/SHOP-13',
    container: project,
    ...more,
  };
}

const status = (name: string, category: 'todo' | 'inprogress' | 'done') => ({ name, category });

describe('activity groups', () => {
  it('joins what the same person did to the same thing within an hour', () => {
    const groups = groupEntries(
      [
        entry('2026-10-23T10:50:00Z', 'issue.transitioned'),
        entry('2026-10-23T10:20:00Z', 'issue.transitioned'),
        entry('2026-10-23T09:55:00Z', 'issue.transitioned'), // 55 minutes after the newest: still one
        entry('2026-10-23T09:40:00Z', 'issue.transitioned'), // 70 minutes: a group of its own
        entry('2026-10-23T09:39:00Z', 'issue.transitioned', { actor: jonas }),
        entry('2026-10-23T09:38:00Z', 'issue.commented', { actor: jonas }),
        entry('2026-10-23T09:37:00Z', 'issue.commented', {
          actor: jonas,
          url: '/browse/SHOP-4',
          title: 'SHOP-4',
        }),
      ],
      'UTC',
    );
    expect(groups.map((g) => g.entries.length)).toEqual([3, 1, 1, 1, 1]);
  });

  it('compares pages without the comment in their address, and pushes by their branches', () => {
    const page = {
      product: 'logbuch' as const,
      url: '/wiki/spaces/ENG/pages/4/release-process',
      title: 'Release process',
    };
    const push = (at: string, ref: string, change: 'created' | 'updated' | 'deleted' = 'updated') =>
      entry(at, 'pushed', {
        product: 'hafen',
        container: repo,
        url: repo.url,
        title: 'WEB/storefront',
        refs: [{ name: ref, change }],
      });
    const groups = groupEntries(
      [
        entry('2026-10-23T10:05:00Z', 'page.commented', { ...page, url: `${page.url}#comment-9` }),
        entry('2026-10-23T10:00:00Z', 'page.commented', { ...page, url: `${page.url}#comment-8` }),
        push('2026-10-23T09:59:00Z', 'main'),
        push('2026-10-23T09:58:00Z', 'main'),
        push('2026-10-23T09:57:00Z', 'main', 'created'),
        push('2026-10-23T09:56:00Z', 'main', 'deleted'),
        push('2026-10-23T09:55:00Z', 'feature'),
      ],
      'UTC',
    );
    expect(groups.map((g) => g.entries.length)).toEqual([2, 3, 1, 1]);
  });

  it('makes series of the runs of a pipeline, whoever started them, until something else happens', () => {
    const run = (at: string, state: string, number: number, pipeline = 'Build and deploy') =>
      entry(at, `run.${state}`, {
        product: 'helling',
        container: repo,
        actor: number % 2 ? mara : jonas,
        pipeline,
        number,
        title: `#${number} ${pipeline}`,
        url: `/builds/web/storefront/runs/${number}`,
      });
    const groups = groupEntries(
      [
        run('2026-10-23T10:00:00Z', 'succeeded', 7),
        run('2026-10-23T08:00:00Z', 'succeeded', 6),
        run('2026-10-23T07:00:00Z', 'succeeded', 5),
        run('2026-10-23T06:00:00Z', 'failed', 4),
        run('2026-10-23T05:00:00Z', 'succeeded', 3, 'Nightly'),
        entry('2026-10-23T04:00:00Z', 'issue.created'),
        run('2026-10-23T03:00:00Z', 'succeeded', 2, 'Nightly'),
      ],
      'UTC',
    );
    expect(groups.map((g) => g.entries.map((e) => e.number ?? 0))).toEqual([[7, 6, 5], [4], [3], [0], [2]]);
  });

  it("does not join across days, in the user's time zone", () => {
    const entries = [
      entry('2026-10-23T00:10:00Z', 'issue.commented'),
      entry('2026-10-22T23:50:00Z', 'issue.commented'),
    ];
    expect(groupEntries(entries, 'UTC').map((g) => g.day)).toEqual(['2026-10-23', '2026-10-22']);
    // In Berlin (UTC+2 in October) both are on the 23rd.
    expect(groupEntries(entries, 'Europe/Berlin').map((g) => [g.day, g.entries.length])).toEqual([
      ['2026-10-23', 2],
    ]);
    expect(dayKey('2026-10-22T23:50:00Z', 'Europe/Berlin')).toBe('2026-10-23');
    expect(dayKey('2026-10-22T23:50:00Z', 'Not/A_Zone')).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it('names days: today, yesterday, a weekday within the week, a date before', () => {
    expect(dayKind('2026-10-23', '2026-10-23')).toBe('today');
    expect(dayKind('2026-10-22', '2026-10-23')).toBe('yesterday');
    expect(dayKind('2026-10-17', '2026-10-23')).toBe('weekday');
    expect(dayKind('2026-10-16', '2026-10-23')).toBe('date');
    expect(dayKind('2026-02-28', '2026-03-01')).toBe('yesterday');
  });

  it('chains the statuses of a group, unions its fields and adds up its commits', () => {
    const moves = [
      entry('2026-10-23T10:02:00Z', 'issue.transitioned', {
        status: { from: status('In Progress', 'inprogress'), to: status('In Review', 'inprogress') },
      }),
      entry('2026-10-23T10:01:00Z', 'issue.transitioned', {
        status: { from: status('To Do', 'todo'), to: status('In Progress', 'inprogress') },
      }),
    ];
    expect(statusChain(moves).map((s) => s.name)).toEqual(['To Do', 'In Progress', 'In Review']);
    expect(statusChain([...moves, entry('2026-10-23T10:00:00Z', 'issue.transitioned')])).toEqual([]);

    const edits = [
      entry('2026-10-23T10:02:00Z', 'issue.updated', {
        fields: [{ key: 'priority' }, { key: 'cf_3', name: 'Story points' }],
      }),
      entry('2026-10-23T10:01:00Z', 'issue.updated', { fields: [{ key: 'assignee' }, { key: 'priority' }] }),
    ];
    expect(changedFields(edits).map((f) => f.key)).toEqual(['assignee', 'priority', 'cf_3']);

    const c = (sha: string) => ({
      sha,
      subject: `Subject ${sha}`,
      url: `/code/web/storefront/commits/${sha}`,
    });
    const pushes = [
      entry('2026-10-23T10:02:00Z', 'pushed', { commits: { count: 2, newest: [c('d'), c('c')] } }),
      entry('2026-10-23T10:01:00Z', 'pushed', { commits: { count: 5, newest: [c('b'), c('a')] } }),
    ];
    expect(pushedCommits(pushes)).toEqual({ count: 7, newest: [c('d'), c('c'), c('b')] });
    expect(pushedCommits([...pushes, entry('2026-10-23T10:00:00Z', 'pushed')])).toEqual({
      count: null,
      newest: [],
    });
  });
});
