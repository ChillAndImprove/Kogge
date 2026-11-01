// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

test('set up pipelines from a template through a pull request', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('P');
  await apiCall(page, 'POST', '/api/v1/code/projects', { key, name: `Pipelines ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/${key}/repos`, { name: 'Payments API', readme: true });
  await apiCall(page, 'PUT', `/api/v1/code/repos/${key}/payments-api/files`, {
    branch: 'main',
    path: 'go.mod',
    content: 'module example.com/payments\n',
  });

  // The organisation's template comes first; for a Go module, the Go template is recommended.
  const go = (await apiGet(page, '/api/v1/builds/templates')).find((t: { id: string }) => t.id === 'go');
  await page.goto('/builds/templates');
  await page.getByRole('button', { name: 'New template' }).click();
  const editor = page.getByRole('dialog', { name: 'New template' });
  const name = `Payments service ${key}`;
  await editor.getByRole('textbox', { name: 'Name' }).fill(name);
  await editor.getByRole('textbox', { name: 'Pipeline file' }).fill(go.content);
  await expect(editor.getByText('The pipeline file is valid')).toBeVisible();
  await editor.getByRole('button', { name: 'Save' }).click();
  await expect(editor).toBeHidden();
  await expect(page.getByText(name)).toBeVisible();

  await page.goto(`/builds/${key.toLowerCase()}/payments-api`);
  await page.getByRole('button', { name: 'Set up pipelines' }).click();
  const picker = page.getByRole('dialog', { name: 'Set up pipelines' });
  const cards = picker.getByRole('button');
  await expect(cards.filter({ hasText: name })).toBeVisible();
  await expect(picker.getByRole('button', { name: 'Go', exact: true })).toContainText('Recommended');
  await picker.getByRole('button', { name: 'Go', exact: true }).click();

  // The editor checks the file as it is typed.
  const file = page.getByRole('dialog', { name: 'New file' });
  await expect(file.getByRole('textbox', { name: 'Path' })).toHaveValue('.kogge/pipelines.yml');
  await expect(file.getByText('The pipeline file is valid')).toBeVisible();
  const content = file.getByRole('textbox', { name: 'Content', exact: true });
  const original = await content.inputValue();
  await content.fill(original.replace('stages:', 'stagez:'));
  await expect(file.getByText('The pipeline file has mistakes')).toBeVisible();
  await content.fill(original);
  await expect(file.getByText('The pipeline file is valid')).toBeVisible();

  // Committed to a new branch, it becomes a pull request.
  await file.getByRole('checkbox', { name: 'Commit to a new branch and open a pull request' }).check();
  await file.getByRole('textbox', { name: 'Name of the new branch' }).fill('add-pipelines');
  await file.getByRole('button', { name: 'Commit changes' }).click();
  await page.getByRole('textbox', { name: 'Title' }).fill('Add the pipelines');
  await page.getByRole('button', { name: 'Create pull request' }).click();
  await expect(page.getByRole('heading', { name: /Add the pipelines/ })).toBeVisible();
  const tree = await apiGet(
    page,
    `/api/v1/code/repos/${key}/payments-api/tree?ref=add-pipelines&path=.kogge`,
  );
  expect(tree.entries.map((e: { name: string }) => e.name)).toEqual(['pipelines.yml']);

  // Clean up the template: templates are the instance's.
  const mine = (await apiGet(page, '/api/v1/builds/templates')).find(
    (t: { name?: string }) => t.name === name,
  );
  await apiCall(page, 'DELETE', `/api/v1/builds/templates/${mine.id}`);
});
