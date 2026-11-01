// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { expect, signIn, test } from './fixtures';

test('the administration starts with an overview of the installation', async ({ page }) => {
  await signIn(page);
  await page.goto('/admin');
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
  for (const card of ['People', 'Security', 'Storage', 'Backup and e-mail']) {
    await expect(page.getByRole('region', { name: card, exact: true })).toBeVisible();
  }
  for (const product of ['Kontor', 'Logbuch', 'Hafen', 'Helling']) {
    await expect(page.getByRole('main').getByRole('link', { name: product, exact: true })).toBeVisible();
  }
  // The administrator of the test installation signs in with a password alone: the overview says so and leads
  // to the users.
  const attention = page.getByRole('list', { name: 'Needs attention' });
  const item = attention.getByRole('listitem').filter({ hasText: 'signs in with a password alone' });
  await expect(item).toBeVisible();
  await item.getByRole('link', { name: 'Users' }).click();
  await expect(page).toHaveURL(/\/admin\/users$/);
  await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible();
});
