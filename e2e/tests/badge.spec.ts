// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, signIn, test, uniqueKey } from './fixtures';

test('the status badge of a repository, with its Markdown', async ({ page, baseURL }) => {
  await signIn(page);
  const key = uniqueKey('S');
  await apiCall(page, 'POST', '/api/v1/code/projects', { key, name: `Badge ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/${key}/repos`, { name: 'Service', readme: true });

  await page.goto(`/builds/${key.toLowerCase()}/service`);
  await page.getByRole('button', { name: 'Status badge' }).click();
  const dialog = page.getByRole('dialog', { name: 'Status badge' });
  const preview = dialog.getByRole('img', { name: 'Build status' });
  await expect(preview).toBeVisible();
  await expect
    .poll(() => preview.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth))
    .toBeGreaterThan(40);
  const image = `${baseURL}/api/v1/code/repos/${key}/service/badge.svg`;
  await expect(
    dialog.getByText(`[![Build status](${image})](${baseURL}/builds/${key.toLowerCase()}/service)`),
  ).toBeVisible();

  // One pipeline of another branch.
  await dialog.getByRole('textbox', { name: 'Branch' }).fill('release');
  await expect(dialog.getByText(`${image}?branch=release`, { exact: true })).toBeVisible();
  const svg = await (await page.request.get(`${image}?branch=release`)).text();
  expect(svg).toContain('<title>build: no runs</title>');
});
