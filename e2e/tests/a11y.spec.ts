// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

// Accessibility (WCAG 2.1 AA, which BITV 2.0 and EN 301 549 require): every main page of the products, the
// administration and the profile pass axe's automatic checks, in the light and the dark colour scheme.

const wcag = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

async function expectAccessible(page: Page, name: string) {
  // The page has settled: no spinners, the content is there.
  await expect(page.locator('main, [role="main"], .mantine-AppShell-main').first()).toBeVisible();
  await page.waitForLoadState('networkidle');
  const result = await new AxeBuilder({ page }).withTags(wcag).analyze();
  const violations = result.violations.map(
    (v) =>
      `${v.impact} ${v.id}: ${v.nodes
        .map((n) => n.target.join(' '))
        .slice(0, 3)
        .join(' | ')}`,
  );
  expect(violations, `${name} (${page.url()})`).toEqual([]);
}

/** A document of one paragraph. */
const doc = (text: string) => ({
  type: 'doc',
  content: [{ type: 'paragraph', content: [{ type: 'text', text }] }],
});

async function fixtures(page: Page) {
  const key = uniqueKey('A');
  await apiCall(page, 'POST', '/api/v1/projects', { key, name: `Access ${key}`, template: 'scrum' });
  const types = await apiGet(page, `/api/v1/projects/${key}/issue-types`);
  const issue = await apiCall(page, 'POST', '/api/v1/issues', {
    project: key,
    typeId: types[0].issueTypeId,
    summary: 'Readable for everyone',
  });
  const { items: boards } = await apiGet(page, `/api/v1/boards?project=${key}`);
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', {
    key: `W${key}`,
    name: `Handbook ${key}`,
  });
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, {
    title: 'Getting started',
    parentId: space.homePageId,
  });
  await apiCall(page, 'POST', `/api/v1/wiki/pages/${draft.id}/publish`, {
    title: 'Getting started',
    body: {
      type: 'doc',
      content: [
        ...doc('Welcome.').content,
        { type: 'macro', attrs: { name: 'mermaid', params: { code: 'flowchart LR\n  Order --> Shipping' } } },
      ],
    },
    baseVersion: 0,
  });
  await apiCall(page, 'POST', '/api/v1/code/projects', { key: `C${key}`, name: `Code ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/C${key}/repos`, { name: 'Service', readme: true });
  await apiCall(page, 'PUT', `/api/v1/code/repos/C${key}/service/files`, {
    branch: 'main',
    path: 'CODEOWNERS',
    content: '* @admin\n/docs/ @nobody\n',
  });
  await apiCall(page, 'PUT', `/api/v1/code/repos/C${key}/service/files`, {
    branch: 'main',
    newBranch: 'feature',
    path: 'docs/notes.md',
    content: '# Notes\n',
  });
  const pr = await apiCall(page, 'POST', `/api/v1/code/repos/C${key}/service/pull-requests`, {
    title: 'Add notes',
    source: 'feature',
  });
  await apiCall(page, 'POST', `/api/v1/code/repos/C${key}/service/pull-requests/${pr.number}/comments`, {
    path: 'docs/notes.md',
    line: 1,
    side: 'new',
    markdown: 'A clearer title:\n\n```suggestion\n# Release notes\n```',
  });
  return {
    key,
    issue: issue.key as string,
    board: boards[0].id as number,
    page: draft.id as number,
    pr: pr.number as number,
  };
}

test('the main pages meet WCAG 2.1 AA', async ({ page }) => {
  test.slow();
  await signIn(page);
  const f = await fixtures(page);
  const code = `/code/c${f.key.toLowerCase()}/service`;
  const pages: [string, string][] = [
    ['start page', '/'],
    ['Kontor', '/issues'],
    ['projects', '/issues/projects'],
    ['backlog', `/projects/${f.key}/boards/${f.board}/backlog`],
    ['board', `/projects/${f.key}/boards/${f.board}`],
    ['issue', `/browse/${f.issue}`],
    ['issue search', `/issues/search?q=${encodeURIComponent(`project = ${f.key}`)}`],
    ['filters', '/issues/filters'],
    ['subscriptions', '/issues/filters?tab=subscriptions'],
    ['bulk changes', '/issues/bulk'],
    ['Logbuch', '/wiki'],
    ['space directory', '/wiki/spaces'],
    ['space', `/wiki/spaces/W${f.key}`],
    ['page', `/wiki/spaces/W${f.key}/pages/${f.page}`],
    ['Hafen', '/code'],
    ['repository', code],
    ['commits', `${code}/commits`],
    ['code search', `${code}/search?q=service`],
    ['pull request', `${code}/pull-requests/${f.pr}`],
    ['Helling', '/builds'],
    ['pipelines', `/builds/c${f.key.toLowerCase()}/service`],
    ['templates', '/builds/templates'],
    ['agents', '/builds/agents'],
    ['search', `/search?q=${encodeURIComponent('Readable')}`],
    ['activity of a repository', `${code}/activity`],
    ['administration', '/admin'],
    ['users', '/admin/users'],
    ['settings', '/admin/settings'],
    ['single sign-on', '/admin/sso'],
    ['audit log', '/admin/audit'],
    ['profile', '/profile?tab=password'],
  ];
  for (const [name, url] of pages) {
    await page.goto(url);
    await expectAccessible(page, name);
  }
  await expectQuickSearchAccessible(page, 'quick search');
  // The dark colour scheme.
  await page.emulateMedia({ colorScheme: 'dark' });
  for (const [name, url] of pages.filter(([n]) =>
    ['start page', 'board', 'issue', 'page', 'pull request', 'code search', 'Helling', 'search'].includes(n),
  )) {
    await page.goto(url);
    await expectAccessible(page, `${name}, dark`);
  }
  await expectQuickSearchAccessible(page, 'quick search, dark');
});

/** The quick search, open with results, on the start page. */
async function expectQuickSearchAccessible(page: Page, name: string) {
  await page.goto('/');
  await page.keyboard.press(process.platform === 'darwin' ? 'Meta+k' : 'Control+k');
  await page.getByRole('combobox', { name: 'Quick search' }).fill('Readable');
  await expect(page.getByRole('option').first()).toBeVisible();
  await expectAccessible(page, name);
  await page.keyboard.press('Escape');
}

test('signing in meets WCAG 2.1 AA', async ({ browser, baseURL }) => {
  const context = await browser.newContext({ baseURL, storageState: { cookies: [], origins: [] } });
  const page = await context.newPage();
  await page.goto('/');
  await expect(page.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
  const result = await new AxeBuilder({ page }).withTags(wcag).analyze();
  expect(result.violations.map((v) => `${v.id}: ${v.nodes.length}`)).toEqual([]);
  await context.close();
});
