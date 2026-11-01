// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { Browser, Page } from '@playwright/test';
import { apiCall, apiGet, expect, signIn, test, uniqueKey } from '../fixtures';
import { totp } from '../totp';

async function signInAs(
  browser: Browser,
  baseURL: string,
  username: string,
  password: string,
): Promise<Page> {
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
  return page;
}

test('a required second factor is set up before anything else', async ({ page, browser, baseURL }) => {
  await signIn(page);
  const key = uniqueKey('Q').toLowerCase();
  const sec = { username: `sec-${key}`, password: 'Security-Admin-2026!' };
  const user = { username: `new-${key}`, password: 'Newcomer-Secret-2026!' };
  const secAccount = await apiCall(page, 'POST', '/api/v1/admin/users', {
    ...sec,
    displayName: 'Sven Kraus',
    email: `${sec.username}@example.com`,
  });
  await apiCall(page, 'POST', '/api/v1/admin/global-permissions', {
    permission: 'admin',
    principalType: 'user',
    principalId: secAccount.id,
  });
  await apiCall(page, 'POST', '/api/v1/admin/users', {
    ...user,
    displayName: 'Nora Weiss',
    email: `${user.username}@example.com`,
  });

  // An administrator with a second factor requires it.
  const admin = await signInAs(browser, baseURL!, sec.username, sec.password);
  await expect(admin.getByRole('heading', { name: /Hello/ })).toBeVisible();
  const setup = await apiCall(admin, 'POST', '/api/v1/me/two-factor');
  await apiCall(admin, 'POST', '/api/v1/me/two-factor/enable', {
    code: totp(setup.secret),
    password: sec.password,
  });
  const settings = await apiGet(admin, '/api/v1/admin/settings');
  await apiCall(admin, 'PUT', '/api/v1/admin/settings', { ...settings, requireTwoFactor: true });
  try {
    // A newcomer signs in with the password and is led through the setup; nothing else opens before.
    const nora = await signInAs(browser, baseURL!, user.username, user.password);
    await expect(nora.getByRole('heading', { name: 'Set up two-factor authentication' })).toBeVisible();
    await expect(nora.getByText('Signed in as Nora Weiss')).toBeVisible();
    await nora.goto('/issues');
    await expect(nora.getByRole('heading', { name: 'Set up two-factor authentication' })).toBeVisible();
    const secret = (await nora.locator('pre').first().innerText()).trim();
    await nora.getByRole('textbox', { name: 'Code' }).fill(totp(secret));
    await nora.getByLabel('Current password').fill(user.password);
    await nora.getByRole('button', { name: 'Turn on' }).click();
    await expect(nora.getByRole('button', { name: 'I have saved the codes' })).toBeVisible();
    await nora.getByRole('button', { name: 'I have saved the codes' }).click();
    // Then the page she asked for opens.
    await expect(nora.getByRole('heading', { name: 'My desk' })).toBeVisible();

    // The administration shows the policy.
    await admin.goto('/admin');
    await expect(admin.getByText('Two-factor authentication required')).toBeVisible();
  } finally {
    await apiCall(admin, 'PUT', '/api/v1/admin/settings', { ...settings, requireTwoFactor: false });
  }
});
