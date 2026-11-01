// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

test('the owner of the files approves the pull request', async ({ page, browser, baseURL }) => {
  await signIn(page);
  const key = uniqueKey('W');
  const username = `owner-${key.toLowerCase()}`;
  const password = 'Owner-Secret-2026!';
  const owner = await apiCall(page, 'POST', '/api/v1/admin/users', {
    username,
    displayName: 'Petra Lang',
    password,
    email: `${username}@example.com`,
  });
  const me = await apiGet(page, '/api/v1/me');
  await apiCall(page, 'POST', '/api/v1/code/projects', { key, name: `Owners ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/${key}/repos`, { name: 'Billing', readme: true });
  await apiCall(page, 'PUT', `/api/v1/code/projects/${key}/permissions`, [
    { holder: { type: 'user', id: me.user.id }, permission: 'codeproject.admin' },
    { holder: { type: 'user', id: owner.id }, permission: 'repo.read' },
  ]);
  const repo = `/api/v1/code/repos/${key}/billing`;
  const settings = (await apiGet(page, repo)).settings;
  await apiCall(page, 'PATCH', repo, { settings: { ...settings, codeOwnerApproval: true } });
  await apiCall(page, 'PUT', `${repo}/files`, {
    branch: 'main',
    path: 'CODEOWNERS',
    content: `/invoices/ @${username}\n`,
  });
  await apiCall(page, 'PUT', `${repo}/files`, {
    branch: 'main',
    newBranch: 'vat',
    path: 'invoices/vat.go',
    content: 'package invoices\n\nconst VAT = 19\n',
  });
  const pr = await apiCall(page, 'POST', `${repo}/pull-requests`, {
    title: 'VAT on invoices',
    source: 'vat',
  });
  const prPath = `/code/${key.toLowerCase()}/billing/pull-requests/${pr.number}`;

  // The owner was added as a reviewer; merging waits for them.
  await page.goto(prPath);
  const owners = page.getByRole('list', { name: 'Code owners' });
  await expect(owners.getByRole('listitem')).toHaveText(/Petra Lang\s*1 file · waiting for approval/);
  await expect(page.getByRole('img', { name: 'Code owner' })).toBeVisible();
  await expect(page.getByText('One file waits for the approval of a code owner.')).toBeVisible();
  await owners.getByRole('button', { name: '1 file' }).click();
  await expect(page.getByText('invoices/vat.go', { exact: true })).toBeVisible();
  await page.keyboard.press('Escape');

  // The owner approves.
  const context = await browser.newContext({
    baseURL,
    locale: 'en-GB',
    storageState: { cookies: [], origins: [] },
  });
  const user = await context.newPage();
  await user.goto('/');
  await user.getByRole('textbox', { name: 'User name' }).fill(username);
  await user.getByRole('textbox', { name: 'Password' }).fill(password);
  await user.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(user.getByRole('heading', { name: /Hello/ })).toBeVisible();
  await user.goto(prPath);
  await user.getByRole('button', { name: 'Approve', exact: true }).click();
  await expect(user.getByRole('list', { name: 'Code owners' })).toContainText('approved by Petra Lang');
  await context.close();

  await page.reload();
  await expect(page.getByText('Code owners approved all the files they own.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Merge' })).toBeEnabled();

  // The file view names the owners, and the settings show the file.
  await page.goto(`/code/${key.toLowerCase()}/billing/src/invoices/vat.go?at=vat`);
  await expect(page.getByText('Owners: Petra Lang')).toBeVisible();
  await page.goto(`/code/${key.toLowerCase()}/billing/settings?tab=pull-requests`);
  await expect(page.getByRole('checkbox', { name: 'Code owners must approve' })).toBeChecked();
  await expect(page.getByText('CODEOWNERS on main: 1 line with owners.')).toBeVisible();
});
