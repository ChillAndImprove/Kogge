// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, openProduct, signIn, test } from './fixtures';

test('build a dashboard with widgets', async ({ page }) => {
  await signIn(page);
  await openProduct(page, 'Kontor');
  await expect(page.getByRole('heading', { name: /Assigned to me/ })).toBeVisible();

  const name = `E2E dashboard ${Date.now() % 100000}`;
  await page.getByRole('navigation').getByRole('link', { name: 'Dashboards' }).click();
  await page.getByRole('button', { name: 'Create dashboard' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Name' }).fill(name);
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('heading', { name })).toBeVisible();
  const id = new URL(page.url()).pathname.split('/').pop();

  // Arrange: add a distribution widget to the second column.
  await page.getByRole('button', { name: 'Add widget' }).nth(1).click();
  const editor = page.getByRole('dialog', { name: 'Widget' });
  await editor.getByRole('combobox', { name: 'Type' }).click();
  await page.getByRole('option', { name: 'Issues by field' }).click();
  await editor.getByRole('textbox', { name: 'Title' }).fill('Open issues by status');
  await editor.getByRole('textbox', { name: 'KQL query' }).fill('resolution = Unresolved');
  await editor.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('heading', { name: 'Open issues by status' })).toBeVisible();
  await page.getByRole('button', { name: 'Done' }).click();

  // The starred dashboard is a tab on Kontor's start page.
  await page.getByRole('navigation').getByRole('link', { name: 'Your work' }).click();
  await page.getByRole('tab', { name }).click();
  await expect(page.getByRole('heading', { name: 'Open issues by status' })).toBeVisible();

  await apiCall(page, 'DELETE', `/api/v1/dashboards/${id}`);
});
