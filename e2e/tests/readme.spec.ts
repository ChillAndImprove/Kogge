// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, signIn, test, uniqueKey } from './fixtures';

test('a README shows the images of its repository and links to its files', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('R');
  await apiCall(page, 'POST', '/api/v1/code/projects', { key, name: `Readme ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/${key}/repos`, { name: 'Service', readme: true });
  const file = (path: string, content: string) =>
    apiCall(page, 'PUT', `/api/v1/code/repos/${key}/service/files`, { branch: 'main', path, content });
  await file(
    'docs/logo.svg',
    '<svg xmlns="http://www.w3.org/2000/svg" width="64" height="32"><rect width="64" height="32" fill="#1c7ed6"/></svg>',
  );
  await file('docs/guide.md', '# Guide\n\nHow to run the service.\n');
  await file(
    'README.md',
    '# Service\n\n![Logo](docs/logo.svg) The logo.\n\nRead [the guide](docs/guide.md), or see ![Chart](https://charts.example.com/c.png).\n',
  );

  await page.goto(`/code/${key.toLowerCase()}/service`);
  const logo = page.getByRole('img', { name: 'Logo' });
  await expect(logo).toBeVisible();
  await expect.poll(() => logo.evaluate((img: HTMLImageElement) => img.naturalWidth)).toBe(64);
  // Images from other servers stay links unless the instance allows them.
  await expect(page.getByRole('img', { name: 'Chart' })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Chart' })).toHaveAttribute(
    'href',
    'https://charts.example.com/c.png',
  );

  await page.getByRole('link', { name: 'the guide' }).click();
  await expect(page).toHaveURL(new RegExp(`/code/${key.toLowerCase()}/service/src/docs/guide.md$`));
  await expect(page.getByRole('heading', { name: 'Guide' })).toBeVisible();
});
