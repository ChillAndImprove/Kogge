// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { expect, openProduct, signIn, test } from './fixtures';

test('sign in, administer users and create an access token', async ({ page }) => {
  await signIn(page);
  // The overview offers the four products.
  for (const product of ['Kontor', 'Logbuch', 'Hafen', 'Helling']) {
    await expect(page.getByRole('link', { name: new RegExp(`^${product}`) })).toBeVisible();
  }

  // Administration: create a local user.
  const username = `e2e${Date.now() % 100000}`;
  await page.goto('/admin/users');
  await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible();
  await page.getByRole('button', { name: 'New local user' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'User name' }).fill(username);
  await dialog.getByRole('textbox', { name: 'Display name' }).fill('E2E Person');
  await dialog.getByRole('textbox', { name: 'Password (leave empty for single sign-on users)' }).fill('E2E-Password-Value-1');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog).toBeHidden();
  await page.getByRole('textbox', { name: 'Search', exact: true }).fill(username);
  await expect(page.getByRole('cell', { name: new RegExp(username) })).toBeVisible();

  // Profile: create an access token and see it once.
  await page.goto('/profile');
  await page.getByRole('tab', { name: 'Access tokens' }).click();
  await page.getByRole('button', { name: 'New access token' }).click();
  const tokenDialog = page.getByRole('dialog');
  const tokenName = `e2e token ${Date.now()}`;
  await tokenDialog.getByRole('textbox', { name: 'Name' }).fill(tokenName);
  await tokenDialog.getByRole('button', { name: 'Create' }).click();
  await expect(tokenDialog.getByText(/^kgp_/)).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(tokenDialog).toBeHidden();
  await expect(page.getByRole('cell', { name: new RegExp(tokenName) })).toBeVisible();

  // The products are reachable through the switcher.
  await openProduct(page, 'Logbuch');
  await expect(page).toHaveURL(/\/wiki$/);
  await expect(page.getByRole('heading', { name: /Welcome back/ })).toBeVisible();
});

test('the new user can sign in and out and is not an administrator', async ({ page, browser }) => {
  await signIn(page);
  await page.goto('/admin/users');
  await page.getByRole('button', { name: 'New local user' }).click();
  const username = `plain${Date.now() % 100000}`;
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'User name' }).fill(username);
  await dialog.getByRole('textbox', { name: 'Display name' }).fill('Plain User');
  await dialog.getByRole('textbox', { name: 'Password (leave empty for single sign-on users)' }).fill('Plain-Password-Value-1');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog).toBeHidden();

  // The new user signs in with the form, in a browser of their own, and signs out again.
  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const own = await context.newPage();
  await signIn(own, username, 'Plain-Password-Value-1');
  await own.getByRole('button', { name: 'Profile' }).click();
  await expect(own.getByRole('menuitem', { name: 'Administration' })).toHaveCount(0);
  await own.getByRole('menuitem', { name: 'Sign out' }).click();
  await expect(own.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
  await own.goto('/issues');
  await expect(own.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
  await context.close();
});
