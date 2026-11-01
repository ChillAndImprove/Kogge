// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

test('a reviewer suggests a change and the author applies it', async ({ page, browser, baseURL }) => {
  await signIn(page);
  const key = uniqueKey('G');
  const username = `rev-${key.toLowerCase()}`;
  const password = 'Review-Secret-2026!';
  const reviewer = await apiCall(page, 'POST', '/api/v1/admin/users', {
    username,
    displayName: 'Rita Vogt',
    password,
    email: `${username}@example.com`,
  });
  const me = await apiGet(page, '/api/v1/me');
  await apiCall(page, 'POST', '/api/v1/code/projects', { key, name: `Suggest ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/${key}/repos`, { name: 'Tax', readme: true });
  await apiCall(page, 'PUT', `/api/v1/code/projects/${key}/permissions`, [
    { holder: { type: 'user', id: me.user.id }, permission: 'codeproject.admin' },
    { holder: { type: 'user', id: reviewer.id }, permission: 'repo.read' },
  ]);
  const repo = `/api/v1/code/repos/${key}/tax`;
  await apiCall(page, 'PUT', `${repo}/files`, {
    branch: 'main',
    path: 'vat.go',
    content: 'package tax\n\nconst VAT = 16\n',
  });
  await apiCall(page, 'PUT', `${repo}/files`, {
    branch: 'main',
    newBranch: 'vat',
    path: 'vat.go',
    content: 'package tax\n\nconst VAT = 19 // check\n',
  });
  const pr = await apiCall(page, 'POST', `${repo}/pull-requests`, { title: 'VAT 19', source: 'vat' });
  const prPath = `/code/${key.toLowerCase()}/tax/pull-requests/${pr.number}`;

  // The reviewer suggests a change of the new line.
  const context = await browser.newContext({
    baseURL,
    locale: 'en-GB',
    storageState: { cookies: [], origins: [] },
  });
  const rita = await context.newPage();
  await rita.goto('/');
  await rita.getByRole('textbox', { name: 'User name' }).fill(username);
  await rita.getByRole('textbox', { name: 'Password' }).fill(password);
  await rita.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(rita.getByRole('heading', { name: /Hello/ })).toBeVisible();
  await rita.goto(prPath);
  await rita.getByRole('tab', { name: 'Changes' }).click();
  await rita.locator('.kogge-code tr.add td.ln').nth(1).click();
  await rita.getByRole('button', { name: 'Suggest a change' }).click();
  const box = rita.getByRole('textbox', { name: 'Add a comment' }).first();
  await expect(box).toHaveValue('```suggestion\nconst VAT = 19 // check\n```\n');
  await box.fill('The remark can go:\n\n```suggestion\nconst VAT = 19\n```');
  await rita.getByRole('button', { name: 'Comment', exact: true }).click();
  const suggestion = rita.getByRole('table', { name: 'Suggested change' });
  await expect(suggestion).toContainText('const VAT = 19 // check');
  // Readers cannot apply it.
  await expect(rita.getByRole('button', { name: 'Apply suggestion' })).toHaveCount(0);
  await context.close();

  // The author applies it: a commit on the source branch.
  await page.goto(prPath);
  await expect(page.getByText('The remark can go:')).toBeVisible();
  await page.getByRole('button', { name: 'Apply suggestion' }).click();
  await expect(page.getByText(/Applied by .* in/)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Apply suggestion' })).toHaveCount(0);
  const raw = await page.request.get(`${repo}/raw?ref=vat&path=vat.go`);
  expect(await raw.text()).toBe('package tax\n\nconst VAT = 19\n');
});
