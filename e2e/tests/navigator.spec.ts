// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { readFileSync } from 'node:fs';
import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

test('the issue navigator shows the chosen columns, sorts by a column and exports what it shows', async ({
  page,
}) => {
  await signIn(page);
  const key = uniqueKey('N');
  await apiCall(page, 'POST', '/api/v1/projects', {
    key,
    name: `Navigator ${key}`,
    template: 'kanban',
  });
  const types = await apiGet(page, `/api/v1/projects/${key}/issue-types`);
  for (const summary of ['Banana bread', 'Apple pie', 'Cherry cake']) {
    await apiCall(page, 'POST', '/api/v1/issues', {
      project: key,
      typeId: types[0].issueTypeId,
      summary,
    });
  }
  await page.goto(`/issues/search?q=${encodeURIComponent(`project = ${key}`)}`);
  const table = page.getByTestId('issue-table');
  await expect(table.getByRole('row')).toHaveCount(4);
  const columns = page.getByRole('button', { name: 'Columns', exact: true });
  await columns.click();
  await page.getByRole('button', { name: 'Default columns' }).click();
  await page.keyboard.press('Escape');

  // The user's columns: Created added, Assignee removed; they stay after a reload.
  await columns.click();
  await page.getByRole('combobox', { name: 'Add a column' }).click();
  await page.getByRole('option', { name: 'Created', exact: true }).click();
  await page.getByRole('button', { name: 'Remove Assignee' }).click();
  await page.keyboard.press('Escape');
  await expect(table.getByRole('columnheader', { name: /Created/ })).toBeVisible();
  await expect(table.getByRole('columnheader', { name: /Assignee/ })).toHaveCount(0);
  await page.reload();
  await expect(table.getByRole('columnheader', { name: /Created/ })).toBeVisible();

  // A column header orders the issues: in the query, ascending first, then descending.
  const query = page.getByRole('combobox', { name: 'KQL query' });
  await table.getByRole('button', { name: 'Sort by Summary' }).click();
  await expect(query).toHaveValue(`project = ${key} ORDER BY summary ASC`);
  await expect(table.getByRole('row').nth(1)).toContainText('Apple pie');
  await table.getByRole('button', { name: 'Sort by Summary' }).click();
  await expect(query).toHaveValue(`project = ${key} ORDER BY summary DESC`);
  await expect(table.getByRole('row').nth(1)).toContainText('Cherry cake');

  // The export has the columns shown, for all issues of the query.
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    (async () => {
      await page.getByRole('button', { name: 'Export' }).click();
      await page.getByRole('menuitem', { name: 'CSV (separated by commas)' }).click();
    })(),
  ]);
  const csv = readFileSync((await download.path())!, 'utf8');
  const [header, first] = csv.split('\r\n');
  expect(header).toBe('\uFEFFIssue type,Key,Summary,Status,Priority,Updated,Created');
  expect(first).toContain(`${key}-3,Cherry cake`);
  expect(csv.trim().split('\r\n')).toHaveLength(4);

  // Back to the default columns.
  await columns.click();
  await page.getByRole('button', { name: 'Default columns' }).click();
  await page.keyboard.press('Escape');
  await expect(table.getByRole('columnheader', { name: /Assignee/ })).toBeVisible();
});
