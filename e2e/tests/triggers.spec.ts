// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

test('a pipeline trigger starts runs from other systems', async ({ page, playwright, baseURL }) => {
  await signIn(page);
  const key = uniqueKey('T');
  const label = `e2e-${key.toLowerCase()}`; // no agent has it: the run waits
  await apiCall(page, 'POST', '/api/v1/code/projects', { key, name: `Triggers ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/${key}/repos`, { name: 'Library', readme: true });
  await apiCall(page, 'PUT', `/api/v1/code/repos/${key}/library/files`, {
    branch: 'main',
    path: '.kogge/pipelines.yml',
    content: `version: 1
pipelines:
  release:
    on:
      manual:
        variables:
          VERSION: { default: "0.0.0" }
    stages:
      - name: Release
        jobs:
          - name: publish
            runs-on: [${label}]
            steps:
              - run: echo "$VERSION"
`,
  });

  await page.goto(`/builds/${key.toLowerCase()}/library/settings`);
  await page.getByRole('button', { name: 'New trigger' }).click();
  const dialog = page.getByRole('dialog', { name: 'New trigger' });
  await dialog.getByRole('textbox', { name: 'Description' }).fill('Release of the platform');
  await dialog.getByRole('button', { name: 'Create' }).click();
  const created = page.getByRole('alert').filter({ hasText: 'Trigger created' });
  await expect(created).toBeVisible();
  const token = (await created.locator('code').first().textContent())!.trim();
  expect(token).toMatch(/^kgt_/);
  await expect(created).toContainText(`${baseURL}/api/v1/builds/trigger`);

  // Another system, without a session: only the token.
  const outside = await playwright.request.newContext({ baseURL });
  const res = await outside.post('/api/v1/builds/trigger', {
    headers: { Authorization: `Bearer ${token}` },
    data: { ref: 'main', variables: { VERSION: '2.0.0' } },
  });
  expect(res.status()).toBe(200);
  const [run] = await res.json();
  expect(run.pipeline).toBe('release');

  await page.goto(`/builds/${key.toLowerCase()}/library/runs/${run.number}`);
  await expect(page.getByText('via trigger “Release of the platform”')).toBeVisible();
  await expect(page.getByText('VERSION=2.0.0')).toBeVisible();
  const detail = await apiGet(page, `/api/v1/code/repos/${key}/library/runs/${run.number}`);
  await apiCall(page, 'POST', `/api/v1/builds/runs/${detail.id}/cancel`);

  // Revoked, the token starts nothing.
  await page.goto(`/builds/${key.toLowerCase()}/library/settings`);
  const row = page.getByRole('row').filter({ hasText: 'Release of the platform' });
  await expect(row).toBeVisible();
  await expect(row).not.toContainText('Never'); // last used just now
  await row.getByRole('button', { name: 'Revoke trigger: Release of the platform' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Revoke trigger' }).click();
  await expect(row).toHaveCount(0);
  const again = await outside.post('/api/v1/builds/trigger', {
    headers: { Authorization: `Bearer ${token}` },
    data: {},
  });
  expect(again.status()).toBe(401);
  await outside.dispose();
});
