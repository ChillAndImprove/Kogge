// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { type Browser } from '@playwright/test';
import { apiCall, expect, signIn, test, uniqueKey } from './fixtures';

/** Signs a user in on a page of a browser of its own (a laptop, a phone). */
async function device(browser: Browser, baseURL: string, username: string, password: string) {
  const context = await browser.newContext({
    baseURL,
    locale: 'en-GB',
    storageState: { cookies: [], origins: [] },
  });
  const page = await context.newPage();
  await page.goto('/');
  await page.getByRole('textbox', { name: 'User name' }).fill(username);
  await page.getByRole('textbox', { name: 'Password' }).fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: /Hello/ })).toBeVisible();
  return { context, page };
}

test('sign out everywhere else from the profile', async ({ page, browser, baseURL }) => {
  await signIn(page);
  const username = `se-${uniqueKey('S').toLowerCase()}`;
  const password = 'Session-Secret-2026';
  await apiCall(page, 'POST', '/api/v1/admin/users', {
    username,
    displayName: 'Selma Engel',
    password,
    email: `${username}@example.com`,
  });
  const laptop = await device(browser, baseURL!, username, password);
  const phone = await device(browser, baseURL!, username, password);

  await laptop.page.goto('/profile?tab=sessions');
  const table = laptop.page.getByRole('table');
  await expect(table.getByRole('row')).toHaveCount(3); // the header, this session and the phone's
  await expect(table.getByText('This session')).toBeVisible();
  await laptop.page.getByRole('button', { name: 'Sign out everywhere else' }).click();
  await expect(laptop.page.getByText('1 other session ended.')).toBeVisible();
  await expect(table.getByRole('row')).toHaveCount(2);
  await expect(laptop.page.getByRole('button', { name: 'Sign out everywhere else' })).toBeDisabled();

  // The phone is signed out; the laptop stays signed in.
  await phone.page.reload();
  await expect(phone.page.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
  await laptop.page.reload();
  await expect(laptop.page.getByRole('button', { name: 'Sign out everywhere else' })).toBeDisabled();
  await laptop.context.close();
  await phone.context.close();
});
