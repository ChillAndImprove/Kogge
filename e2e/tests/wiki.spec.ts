// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { expect, openProduct, signIn, test } from './fixtures';

test('write, update and comment on a wiki page', async ({ page }) => {
  await signIn(page);
  const key = `WD${Date.now() % 100000}`;

  // A new space opens on its home page.
  await openProduct(page, 'Logbuch');
  await page.getByRole('navigation').getByRole('link', { name: 'Spaces' }).click();
  await page.getByRole('button', { name: 'Create space' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Name' }).fill(`E2E Docs ${key}`);
  await dialog.getByRole('textbox', { name: 'Key' }).fill(key);
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(`E2E Docs ${key}`);

  // Create a page from a blank template and publish it.
  await page.getByRole('button', { name: 'Create page' }).click();
  await page.getByRole('dialog').getByText('Blank page').click();
  await expect(page).toHaveURL(/\/edit$/);
  await page.getByRole('textbox', { name: 'Page title' }).fill('Installation guide');
  const editor = page.getByRole('textbox', { name: /Start writing/ });
  await editor.fill('Install the agent first, then configure it.');
  await page.getByRole('button', { name: 'Publish' }).click();
  await page.getByRole('dialog').or(page.locator('.mantine-Popover-dropdown')).getByRole('button', { name: 'Publish' }).click();
  await expect(page.getByRole('heading', { name: 'Installation guide', level: 1 })).toBeVisible();
  await expect(page.getByText('Install the agent first')).toBeVisible();

  // Update it: version 2.
  await page.getByRole('link', { name: 'Edit' }).click();
  // The page's comment box is an editor too: wait for the page editor.
  await expect(page).toHaveURL(/\/edit$/);
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();
  await editor.fill('Install the agent first, then configure the agent service.');
  await page.getByRole('button', { name: 'Update' }).click();
  await page.locator('.mantine-Popover-dropdown').getByRole('textbox', { name: /What did you change/ }).fill('Service name');
  await page.locator('.mantine-Popover-dropdown').getByRole('button', { name: 'Update' }).click();
  await expect(page.getByText('configure the agent service')).toBeVisible();
  await expect(page.getByRole('link', { name: 'version 2' })).toBeVisible();

  // History: compare the versions.
  await page.getByRole('link', { name: 'version 2' }).click();
  await page.getByRole('button', { name: 'Changes' }).click();
  await expect(page.locator('.kogge-ins').first()).toBeVisible();
  await page.getByRole('link', { name: /Installation guide/ }).first().click();

  // Inline comment on selected text.
  await expect(page.getByText('configure the agent service')).toBeVisible();
  await page.evaluate(() => {
    const p = Array.from(document.querySelectorAll('.kogge-doc p')).find((x) => x.textContent?.includes('agent service'))!;
    const text = p.firstChild!;
    const at = text.textContent!.indexOf('agent service');
    const r = document.createRange();
    r.setStart(text, at);
    r.setEnd(text, at + 'agent service'.length);
    const sel = window.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(r);
  });
  await page.locator('.kogge-doc').first().dispatchEvent('mouseup');
  await page.getByRole('button', { name: 'Comment', exact: true }).first().click();
  const inline = page.getByRole('dialog', { name: 'Comment on the selection' });
  await inline.locator('.ProseMirror').fill('Which port does it use?');
  await inline.getByRole('button', { name: 'Comment' }).click();
  await expect(page.getByText('Which port does it use?')).toBeVisible();
  await expect(page.locator('.kogge-anchor')).toHaveText('agent service');

  // A page comment.
  await page.locator('.ProseMirror').last().fill('Looks good to me.');
  await page.getByRole('button', { name: 'Comment', exact: true }).last().click();
  await expect(page.getByText('Looks good to me.')).toBeVisible();

  // The header's search looks in Logbuch: Enter leads to Logbuch's search, which finds the page by its text.
  await page.getByRole('button', { name: 'Search Logbuch' }).click();
  const quick = page.getByRole('combobox', { name: 'Quick search' });
  await quick.fill('agent service');
  await expect(page.getByRole('option', { name: 'Search Logbuch for “agent service”' })).toBeVisible();
  await quick.press('Enter');
  await expect(page).toHaveURL(/\/wiki\/search\?q=agent/);
  await expect(page.getByRole('link', { name: 'Installation guide' }).first()).toBeVisible();
});
