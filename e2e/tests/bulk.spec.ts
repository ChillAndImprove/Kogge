// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

/** The page as it is now passes axe's checks of WCAG 2.1 AA (see a11y.spec.ts). */
async function expectAccessible(page: Page, name: string) {
  const result = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
    .analyze();
  const violations = result.violations.map(
    (v) =>
      `${v.impact} ${v.id}: ${v.nodes
        .map((n) => n.target.join(' '))
        .slice(0, 3)
        .join(' | ')}`,
  );
  expect(violations, name).toEqual([]);
}

/** A project of a template with issues of a type, by their summaries. */
async function project(page: Page, first: string, template: string, type: string, summaries: string[]) {
  const key = uniqueKey(first);
  await apiCall(page, 'POST', '/api/v1/projects', { key, name: `Bulk ${key}`, template });
  const detail = await apiGet(page, `/api/v1/projects/${key}`);
  const typeId = detail.issueTypes.find((t: { icon: string }) => t.icon === type).id;
  for (const summary of summaries) {
    await apiCall(page, 'POST', '/api/v1/issues', { project: key, typeId, summary });
  }
  return key;
}

test('bulk-edit three issues from the navigator', async ({ page }) => {
  await signIn(page);
  const key = await project(page, 'B', 'kanban', 'task', [
    'Invoice layout',
    'Invoice numbers',
    'Invoice e-mail',
  ]);
  const config = await apiGet(page, '/api/v1/issue-config');
  const high = config.priorities[1];
  await page.goto(`/issues/search?q=${encodeURIComponent(`project = ${key} ORDER BY key`)}`);
  const table = page.getByTestId('issue-table');
  await expect(table.getByRole('row')).toHaveCount(4);

  // Choose the issues one by one, then all of the page.
  await table.getByRole('checkbox', { name: `Choose ${key}-1` }).check();
  await expect(page.getByTestId('bulk-bar')).toContainText('1 issue chosen');
  await table.getByRole('checkbox', { name: 'Choose all issues of the page' }).check();
  await expect(page.getByTestId('bulk-bar')).toContainText('3 issues chosen');
  await page.getByRole('button', { name: 'Bulk change…' }).click();

  const dialog = page.getByRole('dialog');
  await dialog.getByRole('radio', { name: 'Edit fields' }).check();
  await dialog.getByRole('button', { name: 'Next' }).click();
  await expect(dialog.getByTestId('bulk-count')).toHaveText('3 of 3 issues can be changed.');
  await dialog.getByTestId('bulk-field-priority').getByRole('checkbox').check();
  await dialog.getByTestId('bulk-field-priority').getByRole('combobox').click();
  await page.getByRole('option', { name: high.name, exact: true }).click();
  await dialog.getByTestId('bulk-field-labels').getByRole('checkbox').check();
  const labels = dialog.getByTestId('bulk-field-labels').getByRole('combobox');
  await labels.fill('billing');
  await labels.press('Enter');
  await expectAccessible(page, 'bulk change dialog');
  await dialog.getByRole('button', { name: 'Change 3 issues' }).click();

  // The job runs in the background; its page follows it to the end.
  await expect(page).toHaveURL(/\/issues\/bulk\/\d+$/);
  await expect(page.getByTestId('bulk-state')).toHaveText('Done');
  await expect(page.getByTestId('bulk-results').getByText('Changed', { exact: true })).toHaveCount(3);
  await expectAccessible(page, 'bulk change');
  for (const n of [1, 2, 3]) {
    const issue = await apiGet(page, `/api/v1/issues/${key}-${n}`);
    expect(issue.priority?.id).toBe(high.id);
    expect(issue.labels).toEqual(['billing']);
  }
});

test('move an issue and open it by its old key', async ({ page }) => {
  await signIn(page);
  const from = await project(page, 'M', 'scrum', 'bug', ['Checkout crashes']);
  const to = await project(page, 'N', 'business', 'task', []);
  await page.goto(`/browse/${from}-1`);
  await expect(page.getByRole('heading', { name: 'Checkout crashes' })).toBeVisible();
  await page.getByRole('button', { name: 'More', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Move…' }).click();

  const dialog = page.getByRole('dialog');
  await dialog.getByRole('combobox', { name: 'Target project' }).click();
  await page.getByRole('option', { name: new RegExp(`\\(${to}\\)$`) }).click();
  // The business project has no bugs: the plan proposes one of its types.
  await expect(dialog.getByTestId('move-plan')).toContainText(`${from}-1`);
  await expect(dialog.getByRole('combobox', { name: /^Issue type for / })).not.toHaveValue('');
  await expectAccessible(page, 'move dialog');
  await dialog.getByRole('button', { name: 'Move', exact: true }).click();

  await expect(page).toHaveURL(new RegExp(`/browse/${to}-1$`));
  await expect(page.getByRole('heading', { name: 'Checkout crashes' })).toBeVisible();

  // The old key leads to the issue.
  await page.goto(`/browse/${from}-1`);
  await expect(page).toHaveURL(new RegExp(`/browse/${to}-1$`));
  await expect(page.getByRole('heading', { name: 'Checkout crashes' })).toBeVisible();
  await page.getByRole('tab', { name: 'History' }).click();
  await expect(page.getByText(`${from}-1`).first()).toBeVisible();
});

test('build a query with the keyboard through the suggestions', async ({ page }) => {
  await signIn(page);
  const key = await project(page, 'K', 'kanban', 'task', ['Keyboard only']);
  await page.goto(`/issues/search?q=${encodeURIComponent('summary ~ nothing-at-all')}`);
  const query = page.getByRole('combobox', { name: 'KQL query' });
  await query.fill('');
  await query.pressSequentially('proj');
  const list = page.getByRole('listbox', { name: 'Suggestions' });
  await expect(list.getByRole('option', { name: /^project/ })).toBeVisible();
  await expectAccessible(page, 'suggestions');
  // Tab takes the first suggestion: the field.
  await query.press('Tab');
  await expect(query).toHaveValue('project ');
  // The operators of the field; the arrow keys choose one, Enter inserts it.
  await expect(list.getByRole('option').first()).toContainText('=');
  await query.press('ArrowDown');
  await expect(query).toHaveAttribute('aria-activedescendant', /.+/);
  await query.press('Enter');
  await expect(query).toHaveValue('project = ');
  // Values of what the user sees, from the server.
  await query.pressSequentially(key.slice(0, 4));
  await expect(list.getByRole('option', { name: new RegExp(`^${key}`) })).toBeVisible();
  await query.press('ArrowDown');
  await query.press('Enter');
  await expect(query).toHaveValue(`project = ${key} `);
  // After a clause: the keywords; Escape closes the list, and Enter runs the query.
  await expect(list.getByRole('option', { name: 'AND' })).toBeVisible();
  await query.press('Escape');
  await expect(list).toHaveCount(0);
  await query.press('Enter');
  await expect(page).toHaveURL(new RegExp(`q=project`));
  await expect(page.getByTestId('issue-table').getByRole('row')).toHaveCount(2);
  await expect(page.getByTestId('issue-table')).toContainText('Keyboard only');
});
