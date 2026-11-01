// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { Page } from '@playwright/test';
import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

// One place to find everything: the quick search (Ctrl+K) across the products, the search page and
// the activity stream on the start page.

const doc = (text: string) => ({
  type: 'doc',
  content: [{ type: 'paragraph', content: [{ type: 'text', text }] }],
});

/** An issue and a page whose titles share a word no other test uses. */
async function fixtures(page: Page) {
  const key = uniqueKey('Q');
  const word = `zeppelin${key.toLowerCase()}`;
  await apiCall(page, 'POST', '/api/v1/projects', { key, name: `Quick ${key}`, template: 'kanban' });
  const types = await apiGet(page, `/api/v1/projects/${key}/issue-types`);
  const issue = await apiCall(page, 'POST', '/api/v1/issues', {
    project: key,
    typeId: types[0].issueTypeId,
    summary: `Launch the ${word}`,
  });
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', {
    key: `W${key}`,
    name: `Hangars ${key}`,
  });
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, {
    title: `Hangar of the ${word}`,
    parentId: space.homePageId,
  });
  await apiCall(page, 'POST', `/api/v1/wiki/pages/${draft.id}/publish`, {
    title: `Hangar of the ${word}`,
    body: doc('Where it is kept.'),
    baseVersion: 0,
  });
  return { key, word, issue: issue.key as string, space: `W${key}`, page: draft.id as number };
}

const isMac = process.platform === 'darwin';
const quickSearchKey = isMac ? 'Meta+k' : 'Control+k';

test('Ctrl+K finds an issue by its key and by words and opens a page with the keyboard', async ({ page }) => {
  await signIn(page);
  const f = await fixtures(page);
  await page.goto('/issues');
  await expect(page.getByRole('heading', { name: 'My desk' })).toBeVisible();

  // Ctrl+K opens the quick search from anywhere; the field has the focus. In Kontor it looks in Kontor.
  await page.keyboard.press(quickSearchKey);
  const box = page.getByRole('combobox', { name: 'Quick search' });
  await expect(box).toBeFocused();
  await expect(page.getByRole('button', { name: 'Search all products, not only Kontor' })).toBeVisible();

  // An exact key comes first, and Enter opens its issue.
  await box.fill(f.issue.toLowerCase());
  await expect(page.getByRole('option').first()).toContainText(f.issue);
  await expect(page.getByRole('option').first()).toHaveAttribute('aria-selected', 'true');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(new RegExp(`/browse/${f.issue}$`));
  await expect(page.getByText(`Launch the ${f.word}`).first()).toBeVisible();

  // The beginnings of words find it too, grouped by product.
  await page.keyboard.press(quickSearchKey);
  await box.fill(`laun ${f.word.slice(0, 10)}`);
  const kontor = page.getByRole('group', { name: 'Kontor' });
  await expect(
    kontor.getByRole('option', { name: new RegExp(`${f.issue} Launch the ${f.word}`) }),
  ).toBeVisible();

  // Backspace in the empty field widens the search to all products. The page, chosen with the arrow keys and
  // opened with Enter.
  await box.fill('');
  await box.press('Backspace');
  await expect(page.getByRole('button', { name: /^Search all products, not only/ })).toHaveCount(0);
  await box.fill(`hangar ${f.word}`);
  const option = page
    .getByRole('group', { name: 'Logbuch' })
    .getByRole('option', { name: new RegExp(`Hangar of the ${f.word}`) });
  await expect(option).toBeVisible();
  for (let i = 0; i < 12 && (await option.getAttribute('aria-selected')) !== 'true'; i++) {
    await page.keyboard.press('ArrowDown');
  }
  await expect(option).toHaveAttribute('aria-selected', 'true');
  await expect(box).toHaveAttribute('aria-activedescendant', (await option.getAttribute('id')) ?? '');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(new RegExp(`/wiki/spaces/${f.space}/pages/${f.page}`));
  await expect(page.getByRole('heading', { name: `Hangar of the ${f.word}` })).toBeVisible();

  // Before anything is typed it shows what was opened last, in the product on screen and, widened, in all of
  // them; Escape closes it and gives the focus back.
  await page.keyboard.press(quickSearchKey);
  const recent = page.getByRole('group', { name: 'Recently opened' });
  await expect(recent.getByRole('option').first()).toContainText(`Hangar of the ${f.word}`);
  await expect(recent.getByRole('option', { name: new RegExp(f.issue) })).toHaveCount(0);
  await page.getByRole('button', { name: 'Search all products, not only Logbuch' }).click();
  await expect(recent.getByRole('option', { name: new RegExp(f.issue) })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(box).toBeHidden();

  // Actions: "Create an issue" opens the dialog.
  await page.keyboard.press(quickSearchKey);
  await box.fill('create iss');
  await page.getByRole('group', { name: 'Actions' }).getByRole('option', { name: 'Create an issue' }).click();
  await expect(page.getByRole('dialog', { name: 'Create issue' })).toBeVisible();
  await page.keyboard.press('Escape');

  // The search field of the header opens it too, and "search all products" leads to the search page.
  await page.getByRole('button', { name: 'Search Logbuch' }).click();
  await box.fill(f.word);
  await page.getByRole('option', { name: new RegExp(`Search all products for “${f.word}”`) }).click();
  await expect(page).toHaveURL(new RegExp(`/search\\?q=${f.word}`));
  const results = page.getByRole('main');
  await expect(results.getByRole('link', { name: `${f.issue} Launch the ${f.word}` })).toBeVisible();
  await expect(results.getByRole('link', { name: `Hangar of the ${f.word}` })).toBeVisible();
  await page
    .getByRole('navigation', { name: 'Filter the results' })
    .getByRole('button', { name: /^Logbuch/ })
    .click();
  await expect(results.getByRole('heading', { name: 'Pages' })).toBeVisible();
  await expect(results.getByRole('link', { name: `${f.issue} Launch the ${f.word}` })).toHaveCount(0);
});

test('in the editor, Ctrl+K inserts a link rather than opening the quick search', async ({ page }) => {
  await signIn(page);
  const f = await fixtures(page);
  await page.goto(`/wiki/spaces/${f.space}/pages/${f.page}/edit`);
  const editor = page.getByRole('textbox', { name: /Start writing/ });
  await editor.click();
  await page.keyboard.press(quickSearchKey);
  await expect(page.getByRole('textbox', { name: 'Link address' })).toBeVisible();
  await expect(page.getByRole('combobox', { name: 'Quick search' })).toHaveCount(0);
});

test('the start page shows the activity: a page edit and a merged pull request', async ({ page }) => {
  await signIn(page);
  const f = await fixtures(page);
  // The page is edited: its second version.
  await apiCall(page, 'POST', `/api/v1/wiki/pages/${f.page}/publish`, {
    title: `Hangar of the ${f.word}`,
    body: doc('Where it is kept, and since when.'),
    baseVersion: 1,
  });
  // A pull request is opened and merged.
  await apiCall(page, 'POST', '/api/v1/code/projects', { key: `C${f.key}`, name: `Code ${f.key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/C${f.key}/repos`, { name: 'Airship', readme: true });
  await apiCall(page, 'PUT', `/api/v1/code/repos/C${f.key}/airship/files`, {
    branch: 'main',
    newBranch: 'mooring',
    path: 'MOORING.md',
    content: '# Mooring\n',
  });
  const pr = await apiCall(page, 'POST', `/api/v1/code/repos/C${f.key}/airship/pull-requests`, {
    title: `Moor the ${f.word}`,
    source: 'mooring',
  });
  await apiCall(page, 'POST', `/api/v1/code/repos/C${f.key}/airship/pull-requests/${pr.number}/merge`, {});

  await page.goto('/');
  const stream = page.getByRole('list', { name: 'Activity' });
  const edit = stream
    .getByRole('listitem')
    .filter({ hasText: `Hangar of the ${f.word}` })
    .filter({ hasText: 'updated' });
  await expect(edit).toBeVisible();
  const merge = stream
    .getByRole('listitem')
    .filter({ hasText: `Moor the ${f.word}` })
    .filter({ hasText: 'merged' });
  await expect(merge).toBeVisible();
  await merge.getByRole('link', { name: `#${pr.number} Moor the ${f.word}` }).click();
  await expect(page).toHaveURL(
    new RegExp(`/code/c${f.key.toLowerCase()}/airship/pull-requests/${pr.number}$`),
  );

  // The stream follows the live updates: a new issue appears without reloading the page.
  await page.goto('/');
  await expect(edit).toBeVisible();
  const types = await apiGet(page, `/api/v1/projects/${f.key}/issue-types`);
  await apiCall(page, 'POST', '/api/v1/issues', {
    project: f.key,
    typeId: types[0].issueTypeId,
    summary: `Refuel the ${f.word}`,
  });
  await expect(stream.getByRole('listitem').filter({ hasText: `Refuel the ${f.word}` })).toBeVisible({
    timeout: 15_000,
  });

  // The stream of one product, and of the repository.
  await page.goto('/');
  await page.getByRole('radiogroup', { name: 'Product' }).getByText('Logbuch', { exact: true }).click();
  await expect(stream.getByRole('listitem').filter({ hasText: `Moor the ${f.word}` })).toHaveCount(0);
  await expect(edit).toBeVisible();
  await page.goto(`/code/c${f.key.toLowerCase()}/airship/activity`);
  await expect(
    page.getByRole('list', { name: 'Activity' }).getByRole('listitem').filter({ hasText: 'merged' }),
  ).toBeVisible();
});
