// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, signIn, test, uniqueKey } from './fixtures';

test('Markdown in Hafen is previewed as it will be stored, and changes start from the text as typed', async ({
  page,
}) => {
  await signIn(page);
  const key = uniqueKey('K');
  await apiCall(page, 'POST', '/api/v1/code/projects', { key: `C${key}`, name: `Texts ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/C${key}/repos`, { name: 'Service', readme: true });
  await apiCall(page, 'PUT', `/api/v1/code/repos/C${key}/service/files`, {
    branch: 'main',
    newBranch: 'retry',
    path: 'retry.go',
    content: 'package service\n',
  });

  // The description of a new pull request.
  await page.goto(`/code/c${key.toLowerCase()}/service/pull-requests/new?source=retry`);
  await expect(page.getByText('retry.go').first()).toBeVisible();
  await page.getByRole('textbox', { name: 'Title' }).fill('Retry failed deliveries');
  const description = page.getByRole('textbox', { name: 'Description' });
  await description.fill('Deliveries fail when the *mail server* restarts.\n\n- retry three times');
  await page.getByRole('tab', { name: 'Preview' }).click();
  const preview = page.locator('[data-markdown-preview]');
  await expect(preview.locator('em')).toHaveText('mail server');
  await expect(preview.getByRole('listitem')).toHaveText('retry three times');
  await page.getByRole('tab', { name: 'Write' }).click();
  await page.getByRole('button', { name: 'Create pull request' }).click();
  await expect(page.getByRole('heading', { name: /Retry failed deliveries #1/ })).toBeVisible();

  // A comment, formatted with the toolbar and previewed before it is sent.
  const comment = page.getByRole('textbox', { name: 'Add a comment' });
  await comment.fill('three');
  await comment.selectText();
  await page.getByRole('button', { name: 'Bold' }).click();
  await expect(comment).toHaveValue('**three**');
  await comment.press('End');
  await comment.pressSequentially(' attempts are enough');
  await page.getByRole('tab', { name: 'Preview' }).click();
  await expect(preview.locator('strong')).toHaveText('three');
  await expect(preview).toContainText('three attempts are enough');
  await page.getByRole('tab', { name: 'Write' }).click();
  await page.getByRole('button', { name: 'Comment', exact: true }).click();
  const posted = page.locator('.kogge-doc').filter({ hasText: 'attempts are enough' });
  await expect(posted.locator('strong')).toHaveText('three');

  // Changing the comment starts from its Markdown, not from the plain text.
  await page.getByRole('button', { name: 'More' }).last().click();
  await page.getByRole('menuitem', { name: 'Edit' }).click();
  const edit = page.getByRole('textbox', { name: 'Edit' });
  await expect(edit).toHaveValue('**three** attempts are enough');
  await edit.fill('**four** attempts are enough');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('.kogge-doc strong').filter({ hasText: 'four' })).toBeVisible();

  // So does changing the description.
  await page.getByRole('button', { name: 'Edit the description' }).click();
  const changed = page.getByRole('textbox', { name: 'Description' });
  await expect(changed).toHaveValue('Deliveries fail when the *mail server* restarts.\n\n- retry three times');
  await changed.fill('Deliveries fail when the *mail server* restarts.\n\n- retry four times');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('listitem').filter({ hasText: 'retry four times' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Edit the description' })).toBeVisible();
});
