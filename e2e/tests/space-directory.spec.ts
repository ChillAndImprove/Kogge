// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiGet, expect, signIn, test } from './fixtures';

test('the space directory finds, orders and pages through many spaces', async ({ page }) => {
  test.slow();
  await signIn(page);
  const stamp = Date.now() % 1000000;
  const token = `dir${stamp}`;
  const me = await apiGet(page, '/api/v1/me');
  const headers = { 'X-CSRF-Token': me.csrfToken, Origin: new URL(page.url()).origin };
  for (let i = 1; i <= 55; i++) {
    const n = String(i).padStart(2, '0');
    const res = await page.request.post('/api/v1/wiki/spaces', {
      data: { key: `D${stamp}${n}`, name: `Directory ${token} ${n}`, categories: i === 7 ? [`team-${token}`] : [] },
      headers,
    });
    expect(res.ok(), `space ${n}: ${res.status()}`).toBeTruthy();
  }

  // The server finds them by name and pages them: 50 to a page in the list.
  await page.goto('/wiki/spaces');
  await page.getByRole('textbox', { name: 'Filter spaces' }).fill(token);
  // The filter applies once typing pauses, and starts again on the first page.
  await expect(page).toHaveURL(new RegExp(`[?&]q=${token}`));
  await expect(page.getByText('55 spaces')).toBeVisible();
  const rows = page.getByTestId('space-table').locator('tbody tr');
  await expect(rows).toHaveCount(50);
  await expect(rows.first()).toContainText(`Directory ${token} 01`);
  await page.getByRole('button', { name: 'Page 2' }).click();
  await expect(rows).toHaveCount(5);
  await expect(rows.first()).toContainText(`Directory ${token} 51`);

  // The headers order the list: by key, then the other way round; the address keeps the order and the page.
  await page.getByRole('button', { name: 'Sort by Key' }).click();
  await expect(rows).toHaveCount(50);
  await expect(page.locator('th[aria-sort="ascending"]')).toContainText('Key');
  await page.getByRole('button', { name: 'Sort by Key' }).click();
  await expect(page.locator('th[aria-sort="descending"]')).toContainText('Key');
  await expect(rows.first()).toContainText(`D${stamp}55`);
  await page.getByRole('button', { name: 'Page 2' }).click();
  await expect(rows.last()).toContainText(`D${stamp}01`);
  await page.reload();
  await expect(rows).toHaveCount(5);
  await expect(page.getByRole('textbox', { name: 'Filter spaces' })).toHaveValue(token);

  // A category shows its spaces only.
  await page.getByRole('button', { name: 'Page 1', exact: true }).click();
  await page.getByRole('button', { name: `Show the spaces of the category team-${token}` }).click();
  await expect(page.getByText('1 space', { exact: true })).toBeVisible();
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText(`Directory ${token} 07`);
  await page.getByRole('button', { name: 'Show all categories' }).click();
  await expect(page.getByText('55 spaces')).toBeVisible();

  // Cards are an option, kept by the browser: 24 to a page.
  const view = page.getByRole('radiogroup', { name: 'Show spaces as' });
  await view.getByText('Cards').click();
  const cards = page.getByTestId('space-cards').locator('.mantine-Card-root');
  await expect(cards).toHaveCount(24);
  await expect(page.getByTestId('space-table')).toBeHidden();
  await page.goto(`/wiki/spaces?q=${token}`);
  await expect(cards).toHaveCount(24);
  await page.getByRole('button', { name: 'Page 3' }).click();
  await expect(cards).toHaveCount(7);
  await view.getByText('List').click();
  await expect(rows).toHaveCount(50);

  // Favourites are marked in the list and have a filter of their own.
  await rows.first().getByRole('button', { name: 'Favourite' }).click();
  await expect(rows.first().getByRole('button', { name: 'Favourite' })).toHaveAttribute('aria-pressed', 'true');
  await page.getByRole('radiogroup', { name: 'Which spaces' }).getByText('Favourites').click();
  await expect(page.getByText('1 space', { exact: true })).toBeVisible();
});
