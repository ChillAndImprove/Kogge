// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, signIn, test, uniqueKey } from './fixtures';

test('two people edit a page together, live', async ({ page, browser }) => {
  await signIn(page);
  const key = uniqueKey('L');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key: `W${key}`, name: `Together ${key}` });
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, {
    title: 'Release steps',
    parentId: space.homePageId,
  });
  await apiCall(page, 'POST', `/api/v1/wiki/pages/${draft.id}/publish`, {
    title: 'Release steps',
    baseVersion: 0,
    body: { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: 'Build the release.' }] }] },
  });
  const name = `co${Date.now() % 100000}`;
  await apiCall(page, 'POST', '/api/v1/admin/users', {
    username: name,
    displayName: 'Co Author',
    email: `${name}@example.com`,
    password: 'Co-Author-Password-2026',
  });

  const edit = `/wiki/spaces/W${key}/pages/${draft.id}/edit`;
  await page.goto(edit);
  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const other = await context.newPage();
  await signIn(other, name, 'Co-Author-Password-2026');
  await other.goto(edit);

  const mine = page.getByRole('textbox', { name: /Start writing/ });
  const theirs = other.getByRole('textbox', { name: /Start writing/ });
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();
  await expect(other.locator('[data-sync="saved"]')).toBeVisible();

  // What one types appears for the other, both ways.
  await mine.getByText('Build the release.').click();
  await page.keyboard.press('End');
  await page.keyboard.type(' Then tag it.');
  await expect(theirs).toContainText('Build the release. Then tag it.');
  await theirs.getByText(/Build the release/).click();
  await other.keyboard.press('Home');
  await other.keyboard.type('First: ');
  await expect(mine).toContainText('First: Build the release. Then tag it.');
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();

  // Each sees where the other is, and who edits.
  await expect(page.locator('.kogge-remote-caret[data-name="Co Author"]')).toBeVisible();
  await expect(other.locator('.kogge-remote-caret').first()).toBeVisible();
  await expect(page.locator('[data-people]')).toContainText('editing with 1 other person');

  // The title follows, too.
  await other.getByRole('textbox', { name: 'Page title' }).fill('Release steps for Monday');
  await other.getByRole('textbox', { name: 'Page title' }).blur();
  await expect(page.getByRole('textbox', { name: 'Page title' })).toHaveValue('Release steps for Monday');

  // Publishing publishes what both wrote; the other learns of it and goes on editing the next version.
  await page.getByRole('button', { name: 'Update' }).click();
  await page.locator('.mantine-Popover-dropdown').getByRole('button', { name: 'Update' }).click();
  await expect(page.getByRole('heading', { name: 'Release steps for Monday', level: 1 })).toBeVisible();
  await expect(page.getByText('First: Build the release. Then tag it.')).toBeVisible();
  await expect(other.getByText(/published version 2/)).toBeVisible();
  await expect(other.getByText('Editing version 2')).toBeVisible();

  // Changes after the publish can be discarded for everyone.
  await theirs.getByText(/Then tag it/).click();
  await other.keyboard.press('End');
  await other.keyboard.type(' Maybe not.');
  await expect(other.locator('[data-sync="saved"]')).toBeVisible();
  await page.goto(edit);
  await expect(mine).toContainText('Maybe not.');
  await page.getByRole('button', { name: 'More', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Discard unpublished changes' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Discard' }).click();
  await expect(theirs).not.toContainText('Maybe not.');
  await expect(theirs).toContainText('First: Build the release. Then tag it.');
  await expect(other.getByText(/discarded the unpublished changes/)).toBeVisible();

  // The history names who wrote the version with its author.
  await page.goto(`/wiki/spaces/W${key}/pages/${draft.id}/history`);
  await expect(page.locator('[data-contributors]')).toContainText('with Co Author');
  await context.close();
});
