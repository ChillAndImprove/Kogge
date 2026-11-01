// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { readFile } from 'node:fs/promises';
import type { Page } from '@playwright/test';
import { apiCall, expect, signIn, test, uniqueKey } from './fixtures';

const text = (t: string) => ({ type: 'text', text: t });
const paragraph = (t: string) => ({ type: 'paragraph', content: [text(t)] });

async function publishedPage(page: Page, spaceKey: string, parentId: number, title: string, content: unknown[]) {
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/${spaceKey}/pages`, { title, parentId });
  return apiCall(page, 'POST', `/api/v1/wiki/pages/${draft.id}/publish`, {
    title,
    baseVersion: 0,
    body: { type: 'doc', content },
  });
}

test('a page is laid out in two columns, and readers see them side by side', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('C');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key: `W${key}`, name: `Columns ${key}` });
  const p = await publishedPage(page, `W${key}`, space.homePageId, 'Team page', [paragraph('Who we are')]);
  await page.goto(`/wiki/spaces/W${key}/pages/${p.id}/edit`);
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();
  const editor = page.getByRole('textbox', { name: /Start writing/ });
  await editor.getByText('Who we are').click();
  await page.keyboard.press('End');
  await page.keyboard.press('Enter');

  // "/" offers layouts; the cursor lands in the first column, Tab goes to the next one.
  await page.keyboard.type('/columns');
  const menu = page.getByRole('listbox', { name: 'Insert' });
  await expect(menu.getByRole('option').first()).toContainText('Two columns');
  await page.keyboard.press('Enter');
  const columns = editor.locator('.kogge-columns .kogge-column');
  await expect(columns).toHaveCount(2);
  await page.keyboard.type('Left: the team');
  await page.keyboard.press('Tab');
  await page.keyboard.type('Right: the contacts');
  await expect(columns.nth(0)).toHaveText('Left: the team');
  await expect(columns.nth(1)).toHaveText('Right: the contacts');
  // The layout's tools change its widths.
  const tools = page.getByRole('toolbar', { name: 'Layout' });
  await tools.getByRole('button', { name: 'Narrow column on the left' }).click();
  await expect(editor.locator('.kogge-columns')).toHaveAttribute('data-layout', 'left');
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();

  await page.getByRole('button', { name: 'Update' }).click();
  await page.locator('.mantine-Popover-dropdown').getByRole('button', { name: 'Update' }).click();
  await expect(page.getByRole('heading', { name: 'Team page', level: 1 })).toBeVisible();
  const view = page.locator('.kogge-doc .kogge-columns');
  await expect(view).toHaveAttribute('data-layout', 'left');
  await expect(view.locator('.kogge-column').nth(0)).toHaveText('Left: the team');
  await expect(view.locator('.kogge-column').nth(1)).toHaveText('Right: the contacts');
  // Side by side, the right column twice as wide; on a narrow screen one below the other.
  const [left, right] = await Promise.all([
    view.locator('.kogge-column').nth(0).boundingBox(),
    view.locator('.kogge-column').nth(1).boundingBox(),
  ]);
  expect(left && right && Math.abs(left.y - right.y) < 2 && right.width > left.width * 1.6).toBeTruthy();
  await page.setViewportSize({ width: 390, height: 800 });
  await expect
    .poll(async () => {
      const boxes = await Promise.all([0, 1].map((i) => view.locator('.kogge-column').nth(i).boundingBox()));
      return boxes[1]!.y > boxes[0]!.y + boxes[0]!.height - 1;
    })
    .toBe(true);
});

test('two people edit a page while one finds and replaces', async ({ page, browser }) => {
  await signIn(page);
  const key = uniqueKey('F');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key: `W${key}`, name: `Find ${key}` });
  const p = await publishedPage(page, `W${key}`, space.homePageId, 'Release plan', [
    paragraph('The plan is the plan of the planners.'),
    { type: 'codeBlock', attrs: { language: 'go' }, content: [text('func plan() {}')] },
  ]);
  const name = `fr${Date.now() % 100000}`;
  await apiCall(page, 'POST', '/api/v1/admin/users', {
    username: name,
    displayName: 'Finder Friend',
    email: `${name}@example.com`,
    password: 'Finder-Friend-Password-2026',
  });
  const edit = `/wiki/spaces/W${key}/pages/${p.id}/edit`;
  await page.goto(edit);
  const mine = page.getByRole('textbox', { name: /Start writing/ });
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();
  // The code block is highlighted while it is edited, as the page shows it.
  await expect(mine.locator('pre.kogge-hl .kd')).toHaveText('func');

  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const other = await context.newPage();
  await signIn(other, name, 'Finder-Friend-Password-2026');
  await other.goto(edit);
  const theirs = other.getByRole('textbox', { name: /Start writing/ });
  await expect(other.locator('[data-sync="saved"]')).toBeVisible();
  await expect(theirs.locator('pre.kogge-hl .kd')).toHaveText('func');

  // Ctrl/Cmd+F finds in the text and the code, highlighting every match.
  await mine.getByText('The plan is the plan of the planners.').click();
  await page.keyboard.press('ControlOrMeta+f');
  const panel = page.getByRole('search', { name: 'Find and replace' });
  await panel.getByRole('textbox', { name: 'Find' }).fill('plan');
  await expect(panel.locator('[data-find-count]')).toHaveText(/of 4$/);
  await expect(mine.locator('.kogge-find-match')).toHaveCount(4);
  await panel.getByRole('button', { name: 'Whole words' }).click();
  await expect(panel.locator('[data-find-count]')).toHaveText(/of 3$/);

  // The other person writes another match: it is found at once.
  await theirs.getByText('The plan is the plan of the planners.').click();
  await other.keyboard.press('Home');
  await other.keyboard.type('A plan B. ');
  await expect(panel.locator('[data-find-count]')).toHaveText(/of 4$/);
  await expect(mine.locator('.kogge-find-match')).toHaveCount(4);

  // Replacing all is one change for both editors, which undo takes back.
  await panel.getByRole('textbox', { name: 'Replace with' }).fill('roadmap');
  await panel.getByRole('button', { name: 'Replace all' }).click();
  await expect(theirs).toContainText('A roadmap B. The roadmap is the roadmap of the planners.');
  await expect(theirs).toContainText('func roadmap() {}');
  await expect(panel.locator('[data-find-count]')).toHaveText('None');
  await panel.getByRole('button', { name: 'Close (Escape)' }).click();
  await expect(panel).toHaveCount(0);
  await expect(mine.locator('.kogge-find-match')).toHaveCount(0);
  await page.keyboard.press('ControlOrMeta+z');
  await expect(theirs).toContainText('A plan B. The plan is the plan of the planners.');
  await expect(theirs).toContainText('func plan() {}');
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();
  await context.close();
});

test('a space exports as an HTML site, a page as PDF', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('X');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key: `W${key}`, name: `Export ${key}` });
  const guide = await publishedPage(page, `W${key}`, space.homePageId, 'Guide', [
    { type: 'heading', attrs: { level: 2 }, content: [text('Größen und Preise')] },
    paragraph('Kosten: 5 € pro „Seite“.'),
  ]);
  await publishedPage(page, `W${key}`, guide.id, 'Appendix', [paragraph('More details')]);

  // The page's menu exports it as PDF.
  await page.goto(`/wiki/spaces/W${key}/pages/${guide.id}`);
  await page.getByRole('button', { name: 'More', exact: true }).click();
  const [pdf] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('menuitem', { name: 'Export as PDF' }).click(),
  ]);
  expect(pdf.suggestedFilename()).toBe('guide.pdf');
  expect((await readFile((await pdf.path())!)).subarray(0, 5).toString()).toBe('%PDF-');

  // The space's sidebar exports the whole space; the export runs in the background and is offered when done.
  await page.getByRole('button', { name: 'Export space' }).click();
  const dialog = page.getByRole('dialog', { name: `Export Export ${key}` });
  await expect(dialog.getByRole('radio', { name: 'HTML site (ZIP)' })).toBeChecked();
  await dialog.getByRole('button', { name: 'Start export' }).click();
  const row = dialog.locator('[data-export]').first();
  await expect(row).toHaveAttribute('data-state', 'done', { timeout: 30_000 });
  await expect(row).toContainText('The whole space');
  const [zip] = await Promise.all([page.waitForEvent('download'), row.getByRole('link', { name: 'Download' }).click()]);
  expect(zip.suggestedFilename()).toMatch(new RegExp(`^W${key}-export-${key.toLowerCase()}-html-\\d{8}\\.zip$`));
  const data = await readFile((await zip.path())!);
  expect(data.subarray(0, 2).toString()).toBe('PK');
  for (const file of ['/index.html', '/style.css', `/pages/${guide.id}-guide.html`, '-appendix.html']) {
    expect(data.includes(Buffer.from(file)), file).toBeTruthy();
  }
  // Deleting it removes it from the list.
  await row.getByRole('button', { name: 'Delete export' }).click();
  await expect(dialog.getByText('No exports yet.')).toBeVisible();
});
