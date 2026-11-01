// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { expect, openProduct, signIn, test, uniqueKey } from './fixtures';

test('create a project and work on an issue', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey();

  // Create a Scrum project.
  await openProduct(page, 'Kontor');
  await page.getByRole('navigation').getByRole('link', { name: 'Projects' }).click();
  await page.getByRole('button', { name: 'Create project' }).click();
  const projectDialog = page.getByRole('dialog');
  await projectDialog.getByRole('textbox', { name: 'Name' }).fill(`E2E Shop ${key}`);
  await projectDialog.getByRole('textbox', { name: 'Key' }).fill(key);
  await projectDialog.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/projects/${key}$`));
  await expect(page.getByText(`E2E Shop ${key}`).first()).toBeVisible();

  // Create an issue from the header.
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  const issueDialog = page.getByRole('dialog', { name: 'Create issue' });
  await expect(issueDialog.getByRole('combobox', { name: 'Project' })).toHaveValue(new RegExp(key));
  await issueDialog.getByRole('textbox', { name: 'Summary' }).fill('Checkout fails with an empty basket');
  await issueDialog.locator('.ProseMirror').fill('Steps: open the basket and press **checkout**.');
  await issueDialog.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(issueDialog).toBeHidden();
  await page.getByRole('alert').getByRole('link', { name: `${key}-1` }).click();

  // The issue view: inline edit, transition, comment.
  await expect(page.getByRole('heading', { name: 'Checkout fails with an empty basket' })).toBeVisible();
  await page.getByRole('heading', { name: 'Checkout fails with an empty basket' }).click();
  const summary = page.getByRole('textbox', { name: 'Summary' });
  await summary.fill('Checkout fails for an empty basket');
  await summary.press('Enter');
  await expect(page.getByRole('heading', { name: 'Checkout fails for an empty basket' })).toBeVisible();

  // The default configuration is seeded in the instance language (English or German).
  await page.getByRole('button', { name: /^Status: / }).click();
  await page.getByRole('menuitem', { name: /(Start progress|Bearbeitung beginnen)/ }).click();
  await expect(page.getByRole('button', { name: /^Status: (In Progress|In Arbeit)/ })).toBeVisible();

  const comment = page.locator('.ProseMirror').last();
  await comment.fill('Reproduced on the test system.');
  await page.getByRole('button', { name: 'Comment', exact: true }).click();
  await expect(page.getByText('Reproduced on the test system.')).toBeVisible();

  await page.getByRole('tab', { name: 'History' }).click();
  await expect(page.getByText('Checkout fails for an empty basket').last()).toBeVisible();

  // Search finds the issue; the quick search jumps to it by key.
  await page.goto(`/issues/search?q=${encodeURIComponent(`project = ${key} AND statusCategory = "In Progress"`)}`);
  await expect(page.getByRole('link', { name: `${key}-1` })).toBeVisible();
  await page.getByRole('button', { name: 'Search Kontor' }).click();
  await page.getByRole('combobox', { name: 'Quick search' }).fill(`${key}-1`);
  await page.getByRole('combobox', { name: 'Quick search' }).press('Enter');
  await expect(page).toHaveURL(new RegExp(`/browse/${key}-1$`));

  // Project pages: releases and settings.
  await page.goto(`/projects/${key}/releases`);
  await page.getByRole('button', { name: 'New version' }).click();
  const versionDialog = page.getByRole('dialog');
  await versionDialog.getByRole('textbox', { name: 'Name' }).fill('1.0');
  await versionDialog.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('link', { name: '1.0' })).toBeVisible();
  await page.goto(`/projects/${key}/settings?tab=members`);
  await expect(page.getByRole('cell', { name: /Administrators|Administratoren/ })).toBeVisible();
});

test('administer the issue configuration', async ({ page }) => {
  await signIn(page);
  const name = `E2E Waiting ${Date.now() % 100000}`;
  await page.goto('/admin/issues/statuses');
  await expect(page.getByRole('heading', { name: 'Statuses' })).toBeVisible();
  await page.getByRole('main').getByRole('button', { name: 'Create', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Name' }).fill(name);
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toBeHidden();
  const row = page.getByRole('row', { name: new RegExp(name) });
  await expect(row).toBeVisible();
  await row.getByRole('button', { name: 'Delete' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();
  await expect(row).toBeHidden();

  await page.goto('/admin/issues/workflows');
  await expect(page.getByRole('heading', { name: 'Workflows' })).toBeVisible();
  await page.getByRole('link', { name: /^Software[ -][Ww]orkflow$/ }).click();
  await expect(page.getByRole('heading', { name: 'Transitions' })).toBeVisible();

  await page.goto('/admin/issues/permission-schemes');
  await page.getByRole('main').getByRole('table').getByRole('link').first().click();
  await expect(page.getByText('Browse project')).toBeVisible();
});
