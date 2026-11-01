// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { fallback, hitMatches, narrowBulkCheck, type Answers } from './fallback';

const json = (body: unknown) => ({ status: 200, type: 'application/json', body: JSON.stringify(body) });
const get = (answers: Answers, path: string) => {
  const r = fallback(answers, 'GET', new URL(path, 'https://preview.example.org'));
  return r ? (JSON.parse(r.body) as unknown) : null;
};
const post = (answers: Answers, path: string, body: unknown) => {
  const r = fallback(answers, 'POST', new URL(path, 'https://preview.example.org'), JSON.stringify(body));
  return r ? (JSON.parse(r.body) as Record<string, unknown>) : null;
};

const issue = (key: string, title: string) => ({ kind: 'issue', id: 1, key, title, url: `/browse/${key}` });

describe('searches typed into the preview', () => {
  const answers: Answers = {
    '/api/v1/quick-search?q=refund': json({
      groups: [
        {
          product: 'kontor',
          hits: [
            issue('SHOP-13', 'Partial refunds are rounded to whole euros'),
            issue('SHOP-7', 'Refund log for support'),
          ],
        },
        {
          product: 'hafen',
          hits: [
            { kind: 'pull-request', id: 2, key: '#2', title: 'Keep cents in partial refunds', url: '/x' },
          ],
        },
      ],
      incomplete: [],
    }),
    '/api/v1/quick-search?q=SHOP-': json({
      groups: [
        {
          product: 'kontor',
          hits: [issue('SHOP-1', 'Saved payment methods'), issue('SHOP-13', 'Partial refunds')],
        },
      ],
      incomplete: [],
    }),
    '/api/v1/search-all?q=refund&limit=5': json({
      groups: [
        {
          product: 'kontor',
          kind: 'issue',
          total: 2,
          hits: [
            issue('SHOP-13', 'Partial refunds are rounded to whole euros'),
            {
              ...issue('SHOP-4', 'Pay with a saved card'),
              excerpt: 'no \u0001refund\u0002 needed for partial payments',
            },
          ],
        },
        { product: 'logbuch', kind: 'page', total: 0, hits: [] },
      ],
      incomplete: [],
    }),
  };

  it('narrows the search of the longest recorded beginning of the text to what matches all of it', () => {
    expect(get(answers, '/api/v1/quick-search?q=refunds')).toEqual({
      groups: [
        { product: 'kontor', hits: [issue('SHOP-13', 'Partial refunds are rounded to whole euros')] },
        {
          product: 'hafen',
          hits: [
            { kind: 'pull-request', id: 2, key: '#2', title: 'Keep cents in partial refunds', url: '/x' },
          ],
        },
      ],
      incomplete: [],
    });
    // Every word must begin a word of the title; case and spacing do not matter.
    expect(get(answers, '/api/v1/quick-search?q=Refund%20%20LOG')).toEqual({
      groups: [{ product: 'kontor', hits: [issue('SHOP-7', 'Refund log for support')] }],
      incomplete: [],
    });
  });

  it('finds issues by the beginnings of their keys', () => {
    expect(get(answers, '/api/v1/quick-search?q=shop-13')).toEqual({
      groups: [{ product: 'kontor', hits: [issue('SHOP-13', 'Partial refunds')] }],
      incomplete: [],
    });
  });

  it('finds nothing where no recorded search begins the text', () => {
    expect(get(answers, '/api/v1/quick-search?q=refx')).toEqual({ groups: [], incomplete: [] });
    expect(get(answers, '/api/v1/quick-search?q=zeppelin')).toEqual({ groups: [], incomplete: [] });
  });

  it('keeps every kind of the search page, with what remains and its total', () => {
    expect(get(answers, '/api/v1/search-all?q=refund%20partial&limit=5')).toEqual({
      groups: [
        {
          product: 'kontor',
          kind: 'issue',
          total: 2,
          hits: [
            issue('SHOP-13', 'Partial refunds are rounded to whole euros'),
            {
              ...issue('SHOP-4', 'Pay with a saved card'),
              excerpt: 'no \u0001refund\u0002 needed for partial payments',
            },
          ],
        },
        { product: 'logbuch', kind: 'page', total: 0, hits: [] },
      ],
      incomplete: [],
    });
    // Another number of hits per kind was not recorded.
    expect(get(answers, '/api/v1/search-all?q=refunds&limit=20')).toEqual({ groups: [], incomplete: [] });
  });

  it('matches words of one letter only at the beginning of keys and titles', () => {
    const hit = issue('SHOP-4', 'Pay with a saved card');
    expect(hitMatches(hit, 'p')).toBe(true);
    expect(hitMatches(hit, 's')).toBe(true);
    expect(hitMatches(hit, 'c')).toBe(false);
    expect(hitMatches(hit, 'sav ca')).toBe(true);
  });

  const values: Answers = {
    '/api/v1/search/autocomplete': json({
      fields: [
        { name: 'issuetype', aliases: ['type'] },
        { name: 'assignee', aliases: [] },
        { name: 'parent', aliases: ['Epic Link', 'epic'] },
      ],
    }),
    '/api/v1/search/suggestions?field=assignee&prefix=': json([
      { value: 'lena', label: 'Lena Hofmann' },
      { value: 'jonas', label: 'Jonas Weber' },
      { value: 'mara', label: 'Mara Lindner' },
    ]),
    '/api/v1/search/suggestions?field=issuetype&prefix=': json([{ value: 'Bug' }, { value: 'Story' }]),
    '/api/v1/search/suggestions?field=parent&prefix=': json([
      { value: 'SHOP-1', label: 'Saved payment methods' },
    ]),
    '/api/v1/users?query=&limit=20': json({
      total: 2,
      items: [
        { id: 1, username: 'lena', displayName: 'Lena Hofmann' },
        { id: 2, username: 'jonas', displayName: 'Jonas Weber' },
      ],
    }),
  };

  it('suggests the recorded values of a field that contain what was typed', () => {
    expect(get(values, '/api/v1/search/suggestions?field=assignee&prefix=NA')).toEqual([
      { value: 'lena', label: 'Lena Hofmann' },
      { value: 'jonas', label: 'Jonas Weber' },
    ]);
    expect(get(values, '/api/v1/search/suggestions?field=assignee&prefix=we')).toEqual([
      { value: 'jonas', label: 'Jonas Weber' },
    ]);
  });

  it('knows fields by any spelling and alias the catalog has', () => {
    expect(get(values, '/api/v1/search/suggestions?field=Type&prefix=b')).toEqual([{ value: 'Bug' }]);
    expect(get(values, '/api/v1/search/suggestions?field=%22Epic%20Link%22&prefix=saved')).toEqual([
      { value: 'SHOP-1', label: 'Saved payment methods' },
    ]);
  });

  it('knows nothing of fields that were not recorded, and finds nothing in them', () => {
    expect(get(values, '/api/v1/search/suggestions?field=reporter&prefix=')).toBeNull();
    expect(get(values, '/api/v1/search/suggestions?field=reporter&prefix=le')).toEqual([]);
  });

  it('narrows the pages of pickers and keeps their totals true', () => {
    expect(get(values, '/api/v1/users?query=jo&limit=20')).toEqual({
      total: 1,
      items: [{ id: 2, username: 'jonas', displayName: 'Jonas Weber' }],
    });
    // A recorded page that holds the whole list serves fewer entries per page, too; others do not.
    expect(get(values, '/api/v1/users?query=jo&limit=5')).toEqual({
      total: 1,
      items: [{ id: 2, username: 'jonas', displayName: 'Jonas Weber' }],
    });
    expect(get(values, '/api/v1/users?query=jo&limit=1')).toEqual({ total: 0, items: [] });
    expect(get(values, '/api/v1/users?query=jo&offset=0')).toEqual({ total: 0, items: [] });
  });

  it('answers only searches', () => {
    expect(get(values, '/api/v1/issues/SHOP-1')).toBeNull();
    expect(post(values, '/api/v1/issues', { summary: 'x' })).toBeNull();
  });
});

describe('lists in another number per page', () => {
  const space = (key: string, name: string) => ({ key, name, description: '', permissions: ['space.view'] });
  const answers: Answers = {
    '/api/v1/wiki/spaces?sort=key&order=asc&limit=50&offset=0': json({
      total: 2,
      items: [space('ENG', 'Engineering'), space('SUP', 'Support')],
    }),
    '/api/v1/wiki/spaces?sort=name&order=asc&limit=1&offset=0': json({
      total: 2,
      items: [space('ENG', 'Engineering')],
    }),
  };

  it('are answered by a recorded first page that holds the whole list', () => {
    // The directory's cards: 24 a page, recorded as the list (50 a page).
    expect(get(answers, '/api/v1/wiki/spaces?sort=key&order=asc&limit=24&offset=0')).toEqual({
      total: 2,
      items: [space('ENG', 'Engineering'), space('SUP', 'Support')],
    });
    // Narrowed to what was typed, too.
    expect(get(answers, '/api/v1/wiki/spaces?query=sup&sort=key&order=asc&limit=24&offset=0')).toEqual({
      total: 1,
      items: [space('SUP', 'Support')],
    });
  });

  it('serve the export of the navigator, which asks for all issues at once', () => {
    const query = encodeURIComponent('project = SHOP ORDER BY created DESC');
    const issues = {
      [`/api/v1/search?q=${query}&limit=50&offset=0`]: json({
        total: 2,
        issues: [{ key: 'SHOP-2' }, { key: 'SHOP-1' }],
      }),
    };
    expect(get(issues, `/api/v1/search?q=${query}&limit=1000&offset=0`)).toEqual({
      total: 2,
      issues: [{ key: 'SHOP-2' }, { key: 'SHOP-1' }],
    });
  });

  it('are not answered by pages that hold part of the list, nor are later pages', () => {
    expect(get(answers, '/api/v1/wiki/spaces?sort=name&order=asc&limit=24&offset=0')).toBeNull();
    expect(get(answers, '/api/v1/wiki/spaces?sort=key&order=asc&limit=24&offset=24')).toBeNull();
    expect(get(answers, '/api/v1/wiki/spaces?sort=key&order=desc&limit=24&offset=0')).toBeNull();
  });
});

describe('activity streams the preview did not record', () => {
  const entry = (product: string, title: string) => ({ key: title, product, verb: 'issue.created', title });
  const answers: Answers = {
    '/api/v1/activity-stream?user=jonas&limit=20': json({
      entries: [entry('kontor', 'SHOP-4'), entry('hafen', '#1'), entry('kontor', 'SHOP-13')],
      next: 'older',
    }),
  };

  it('ends with the entries that were recorded', () => {
    expect(get(answers, '/api/v1/activity-stream?user=jonas&before=older&limit=20')).toEqual({
      entries: [],
      next: '',
    });
  });

  it('shows of one product what the stream of all products has of it', () => {
    expect(get(answers, '/api/v1/activity-stream?user=jonas&product=kontor&limit=20')).toEqual({
      entries: [entry('kontor', 'SHOP-4'), entry('kontor', 'SHOP-13')],
      next: '',
    });
    expect(get(answers, '/api/v1/activity-stream?product=kontor&limit=20')).toBeNull();
    expect(get(answers, '/api/v1/activity-stream?user=jonas&mine=true&limit=20')).toBeNull();
  });

  it('shows the start of a recorded stream where fewer entries are asked for', () => {
    expect(get(answers, '/api/v1/activity-stream?limit=2&user=jonas')).toEqual({
      entries: [entry('kontor', 'SHOP-4'), entry('hafen', '#1')],
      next: '',
    });
    expect(get(answers, '/api/v1/activity-stream?user=jonas&product=kontor&limit=1')).toEqual({
      entries: [entry('kontor', 'SHOP-4')],
      next: '',
    });
  });
});

describe('bulk changes and moves of issues chosen in the preview', () => {
  const check = (keys: string[], action: string, extra: Record<string, unknown> = {}) => ({
    total: keys.length,
    truncated: false,
    canMute: true,
    issues: keys.map((key) => ({
      key,
      permitted: key !== 'SHOP-3',
      reason: key === 'SHOP-3' ? 'No.' : undefined,
    })),
    fields:
      action === 'edit'
        ? [
            { key: 'priority', applies: false, missing: [{ key: 'SHOP-2', reason: 'Not on the screen.' }] },
            {
              key: 'components',
              applies: false,
              missing: keys.map((key) => ({ key, reason: 'Several projects.' })),
            },
          ]
        : [],
    statuses:
      action === 'transition'
        ? [
            {
              status: { id: 3 },
              issues: [{ key: 'SHOP-1', transition: 2 }],
              screen: [],
              commentRequired: false,
            },
            {
              status: { id: 4 },
              issues: [{ key: 'SHOP-2', transition: 5 }],
              screen: [],
              commentRequired: false,
            },
          ]
        : [],
    ...extra,
  });
  const plan = {
    project: { key: 'APP' },
    types: [{ from: { id: 1 }, choices: [], chosen: 2, issues: ['SHOP-1'] }],
    statuses: [{ type: { id: 2 }, from: { id: 3 }, choices: [], chosen: 1, issues: ['SHOP-2', 'SHOP-9'] }],
    issues: [
      { key: 'SHOP-1', movable: true, subtasks: ['SHOP-9'], changes: [] },
      { key: 'SHOP-9', movable: true, withParent: 'SHOP-1', subtasks: [], changes: [] },
      { key: 'SHOP-2', movable: true, subtasks: [], changes: [] },
    ],
  };
  const shop = ['SHOP-1', 'SHOP-2', 'SHOP-3', 'SHOP-9'];
  const key = (body: unknown) => `POST /api/v1/issues/bulk/check ${JSON.stringify(body)}`;
  const answers: Answers = {
    [key({ issues: shop, action: 'edit' })]: json(check(shop, 'edit')),
    [key({ issues: [...shop, 'APP-1'], action: 'edit' })]: json(check([...shop, 'APP-1'], 'edit')),
    [key({ issues: shop, action: 'transition' })]: json(check(shop, 'transition')),
    [key({ issues: shop, action: 'move', move: { project: 'APP', types: [], statuses: [] } })]: json(
      check(shop, 'move', { move: plan }),
    ),
  };

  it('narrows the check of the fewest recorded issues that include the chosen ones', () => {
    expect(
      post(answers, '/api/v1/issues/bulk/check', { issues: ['SHOP-1', 'SHOP-3'], action: 'edit' }),
    ).toEqual({
      total: 2,
      truncated: false,
      canMute: true,
      issues: [
        { key: 'SHOP-1', permitted: true },
        { key: 'SHOP-3', permitted: false, reason: 'No.' },
      ],
      fields: [
        { key: 'priority', applies: true, missing: [] },
        {
          key: 'components',
          applies: false,
          missing: [
            { key: 'SHOP-1', reason: 'Several projects.' },
            { key: 'SHOP-3', reason: 'Several projects.' },
          ],
        },
      ],
      statuses: [],
    });
  });

  it('keeps the statuses some chosen issue can go to', () => {
    const narrowed = post(answers, '/api/v1/issues/bulk/check', { issues: ['SHOP-2'], action: 'transition' });
    expect(narrowed?.statuses).toEqual([
      { status: { id: 4 }, issues: [{ key: 'SHOP-2', transition: 5 }], screen: [], commentRequired: false },
    ]);
  });

  it('plans the move of an issue with its sub-tasks from a recorded bulk move', () => {
    const asked = { issues: ['SHOP-1'], project: 'APP', types: [], statuses: [] };
    expect(post(answers, '/api/v1/issues/move-plan', asked)).toEqual({
      project: { key: 'APP' },
      types: [{ from: { id: 1 }, choices: [], chosen: 2, issues: ['SHOP-1'] }],
      statuses: [{ type: { id: 2 }, from: { id: 3 }, choices: [], chosen: 1, issues: ['SHOP-9'] }],
      issues: [
        { key: 'SHOP-1', movable: true, subtasks: ['SHOP-9'], changes: [] },
        { key: 'SHOP-9', movable: true, withParent: 'SHOP-1', subtasks: [], changes: [] },
      ],
    });
    // The bulk move narrows its plan, too.
    const bulk = post(answers, '/api/v1/issues/bulk/check', {
      issues: ['SHOP-2'],
      action: 'move',
      move: { project: 'APP', types: [], statuses: [] },
    });
    expect((bulk?.move as { issues: unknown[] }).issues).toEqual([
      { key: 'SHOP-2', movable: true, subtasks: [], changes: [] },
    ]);
  });

  it('knows nothing of other issues, actions, targets or choices by query', () => {
    expect(
      post(answers, '/api/v1/issues/bulk/check', { issues: ['SHOP-1', 'APP-9'], action: 'edit' }),
    ).toBeNull();
    expect(post(answers, '/api/v1/issues/bulk/check', { issues: ['SHOP-1'], action: 'delete' })).toBeNull();
    expect(
      post(answers, '/api/v1/issues/move-plan', {
        issues: ['SHOP-1'],
        project: 'OPS',
        types: [],
        statuses: [],
      }),
    ).toBeNull();
    expect(
      post(answers, '/api/v1/issues/bulk/check', { query: 'project = SHOP', action: 'edit' }),
    ).toBeNull();
  });

  it('counts only the chosen issues', () => {
    const narrowed = narrowBulkCheck(check(shop, 'edit', { total: 4, truncated: true }), new Set(['SHOP-9']));
    expect(narrowed.total).toBe(1);
    expect(narrowed.truncated).toBe(false);
  });
});
