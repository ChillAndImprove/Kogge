// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { Locator, Page } from '@playwright/test';
import { apiCall, expect, signIn, test } from './fixtures';

const doc = (text: string) => ({ type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text }] }] });

async function publish(page: Page, space: string, title: string, parentId: number): Promise<number> {
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/${space}/pages`, { title, parentId });
  await apiCall(page, 'POST', `/api/v1/wiki/pages/${draft.id}/publish`, { title, body: doc(title), baseVersion: 0 });
  return draft.id;
}

async function children(page: Page, space: string, parent: number): Promise<string[]> {
  const res = await page.request.get(`/api/v1/wiki/spaces/${space}/tree?parent=${parent}&limit=500`);
  return ((await res.json()) as { items: { title: string }[] }).items.map((n) => n.title);
}

/** Drags a page of the tree to the upper edge, the middle or the lower edge of another page's row. */
async function drag(tree: Locator, from: string, to: string, where: 'before' | 'inside' | 'after') {
  const source = tree.getByRole('link', { name: from, exact: true });
  const target = tree.locator('.kogge-tree-row', { has: tree.page().getByRole('link', { name: to, exact: true }) });
  const box = (await target.boundingBox())!;
  const y = { before: 2, inside: box.height / 2, after: box.height - 2 }[where];
  await source.dragTo(target, { targetPosition: { x: 60, y } });
}

test('nest and rearrange pages in the page tree', async ({ page }) => {
  await signIn(page);
  const key = `WT${Date.now() % 100000}`;
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key, name: `Tree ${key}` });
  const home: number = space.homePageId;
  const alpha = await publish(page, key, 'Alpha', home);
  const beta = await publish(page, key, 'Beta', home);
  await publish(page, key, 'Gamma', home);

  await page.goto(`/wiki/spaces/${key}`);
  const tree = page.getByRole('navigation', { name: 'Pages of the space' });
  await expect(tree.getByRole('link', { name: 'Gamma', exact: true })).toBeVisible();

  // "+" next to a page creates a page inside it; the dialog says where the page goes.
  await tree.getByRole('link', { name: 'Alpha', exact: true }).hover();
  await tree.getByRole('button', { name: 'Create a page in "Alpha"' }).click();
  const dialog = page.getByRole('dialog', { name: 'Create page' });
  await expect(dialog.getByTestId('page-location')).toHaveText(new RegExp(`Tree ${key}.*Alpha`));
  await dialog.getByRole('button', { name: /Blank page/ }).click();
  await expect(page).toHaveURL(/\/edit$/);
  await page.getByRole('textbox', { name: 'Page title' }).fill('Alpha child');
  await page.locator('.ProseMirror').fill('Below Alpha.');
  await page.getByRole('button', { name: 'Publish' }).click();
  await page.locator('.mantine-Popover-dropdown').getByRole('button', { name: 'Publish' }).click();
  await expect(page.getByRole('heading', { name: 'Alpha child', level: 1 })).toBeVisible();
  await expect(tree.getByRole('link', { name: 'Alpha child', exact: true })).toBeVisible();
  expect(await children(page, key, alpha)).toEqual(['Alpha child']);

  // The location can be changed before creating: here to the top level of the space.
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  const second = page.getByRole('dialog', { name: 'Create page' });
  await expect(second.getByTestId('page-location')).toContainText('Alpha child');
  await second.getByRole('button', { name: 'Change' }).click();
  await second.getByRole('combobox', { name: 'Parent page' }).click();
  await page.getByRole('option', { name: 'Beta', exact: true }).click();
  await expect(second.getByTestId('page-location')).toHaveText(new RegExp(`Tree ${key}.*Beta`));
  await page.keyboard.press('Escape');
  await expect(second).toBeHidden();

  // Drop a page in the middle of another page: it moves inside.
  await page.goto(`/wiki/spaces/${key}`);
  await drag(tree, 'Gamma', 'Beta', 'inside');
  await expect(page.getByText('"Gamma" moved')).toBeVisible();
  await expect.poll(() => children(page, key, beta)).toEqual(['Gamma']);
  await expect(tree.getByRole('link', { name: 'Gamma', exact: true })).toBeVisible();

  // Drop on the upper edge: reorder.
  await drag(tree, 'Beta', 'Alpha', 'before');
  await expect.poll(() => children(page, key, home)).toEqual(['Beta', 'Alpha']);

  // A page cannot go into its own subtree: the drop is refused and nothing changes.
  await drag(tree, 'Beta', 'Gamma', 'inside');
  await expect(tree.locator('.kogge-tree-row[data-drop]')).toHaveCount(0);
  expect(await children(page, key, beta)).toEqual(['Gamma']);
  expect(await children(page, key, home)).toEqual(['Beta', 'Alpha']);
});

test('people who may only read a space cannot rearrange it', async ({ page, browser }) => {
  await signIn(page);
  const key = `WR${Date.now() % 100000}`;
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key, name: `Read ${key}`, open: false });
  await publish(page, key, 'Only page', space.homePageId);
  const reader = `reader${Date.now() % 100000}`;
  const user = await apiCall(page, 'POST', '/api/v1/admin/users', {
    username: reader,
    displayName: 'Read Only',
    email: `${reader}@example.com`,
    password: 'Reader-Password-2026',
  });
  const grants = await (await page.request.get(`/api/v1/wiki/spaces/${key}/permissions`)).json();
  await apiCall(page, 'PUT', `/api/v1/wiki/spaces/${key}/permissions`, [
    ...grants,
    { permission: 'space.view', holder: { type: 'user', id: user.id } },
  ]);

  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const other = await context.newPage();
  await signIn(other, reader, 'Reader-Password-2026');
  await other.goto(`/wiki/spaces/${key}`);
  const tree = other.getByRole('navigation', { name: 'Pages of the space' });
  await expect(tree.getByRole('link', { name: 'Only page', exact: true })).toBeVisible();
  await expect(tree.getByRole('button', { name: /Create a page in/ })).toHaveCount(0);
  await expect(tree.locator('.kogge-tree-row[draggable="true"]')).toHaveCount(0);
  await expect(other.getByRole('button', { name: 'Create page' })).toHaveCount(0);
  await context.close();
});
