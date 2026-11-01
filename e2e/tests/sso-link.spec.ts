// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, signIn, test } from './fixtures';

test('administrators link an account to single sign-on before its first sign-in', async ({ page }) => {
  await signIn(page);
  const suffix = Date.now().toString(36);
  const username = `pre-${suffix}`;
  // An account created ahead of the first sign-in: without a password and an e-mail address, single sign-on
  // takes it over only through a link.
  await apiCall(page, 'POST', '/api/v1/admin/users', { username, displayName: `Ahead ${suffix}` });

  await page.goto('/admin/users');
  await page.getByRole('textbox', { name: 'Search', exact: true }).fill(username);
  const row = page.getByRole('row').filter({ hasText: username });
  await expect(row).toHaveCount(1);
  await row.getByRole('button', { name: 'Actions' }).click();
  await page.getByRole('menuitem', { name: 'Link to single sign-on…' }).click();
  const dialog = page.getByRole('dialog', { name: `Link Ahead ${suffix} to single sign-on` });
  const name = dialog.getByRole('textbox', { name: 'User name at the identity provider' });
  await expect(name).toHaveValue(username);
  await name.fill(`a.${suffix}`);
  await dialog.getByRole('button', { name: 'Link', exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(row.getByText(`Waits for the sign-in of a.${suffix}`)).toBeVisible();

  // The link can be withdrawn.
  await row.getByRole('button', { name: 'Actions' }).click();
  await page.getByRole('menuitem', { name: 'Remove the single sign-on link' }).click();
  await expect(row.getByText(/Waits for the sign-in/)).toBeHidden();
  // Accounts with a local password sign in with it: they are not offered for a link.
  await page.getByRole('textbox', { name: 'Search', exact: true }).fill('admin');
  const admin = page.getByRole('row').filter({ hasText: 'admin ·' }).first();
  await admin.getByRole('button', { name: 'Actions' }).click();
  await expect(page.getByRole('menuitem', { name: 'Set password' })).toBeVisible();
  await expect(page.getByRole('menuitem', { name: 'Link to single sign-on…' })).toHaveCount(0);
  await page.keyboard.press('Escape');
});
