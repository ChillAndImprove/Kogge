// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { expect, signIn, test } from './fixtures';

test('the system page names the build: version, commit and date', async ({ page }) => {
  await signIn(page);
  const info = await (await page.request.get('/api/v1/admin/system')).json();
  // A build from a Git checkout knows its commit and date: from the linker flags, or from Go's build information.
  expect(info.commit).toMatch(/^[0-9a-f]{40}$/);
  expect(info.buildDate).toBeTruthy();

  await page.goto('/admin/system');
  await expect(page.getByRole('heading', { name: 'System' })).toBeVisible();
  await expect(page.getByRole('row', { name: /^Version/ })).toContainText(info.version);
  const commit = page.getByRole('row', { name: /^Commit/ });
  await expect(commit).toContainText(info.commit.slice(0, 12));
  await expect(commit.getByTitle(info.commit)).toBeVisible();
  await expect(page.getByRole('row', { name: /^Built/ })).toBeVisible();
});
