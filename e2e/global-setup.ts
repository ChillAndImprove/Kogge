// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { chromium, expect, firefox, webkit, type FullConfig } from '@playwright/test';
import { adminPassword, adminState } from './tests/fixtures';

/**
 * Signs in as the administrator once and keeps the session for all tests: sign-ins are rate limited per account,
 * and the sign-in form has its own tests.
 */
export default async function globalSetup(config: FullConfig) {
  const { baseURL, browserName, channel, locale } = config.projects[0].use;
  const browser = await { chromium, firefox, webkit }[browserName ?? 'chromium'].launch({ channel });
  const page = await browser.newPage({ baseURL, locale });
  await page.goto('/');
  await page.getByRole('textbox', { name: 'User name' }).fill('admin');
  await page.getByRole('textbox', { name: 'Password' }).fill(adminPassword);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: /Hello/ })).toBeVisible();
  await page.context().storageState({ path: adminState });
  // The test server keeps 256 MiB free rather than the default (2 GiB or 2 % of the disk), so that the tests do not
  // depend on how full the disk of the machine is: below the reserve, Kogge refuses pushes and uploads.
  const me = await (await page.request.get('/api/v1/me')).json();
  const settings = await (await page.request.get('/api/v1/admin/settings')).json();
  const res = await page.request.put('/api/v1/admin/settings', {
    data: { ...settings, minFreeSpaceMb: 256, minFreeSpacePercent: 0 },
    headers: { 'X-CSRF-Token': me.csrfToken, Origin: new URL(page.url()).origin },
  });
  expect(res.ok(), `the limits of the test server: ${res.status()}`).toBeTruthy();
  await browser.close();
}
