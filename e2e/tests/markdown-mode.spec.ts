// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { expect, openProduct, signIn, test } from './fixtures';

test('write a page as Markdown beside its preview', async ({ page }) => {
  await signIn(page);
  const key = `MD${Date.now() % 100000}`;
  await openProduct(page, 'Logbuch');
  await page.getByRole('navigation').getByRole('link', { name: 'Spaces' }).click();
  await page.getByRole('button', { name: 'Create space' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Name' }).fill(`Markdown ${key}`);
  await dialog.getByRole('textbox', { name: 'Key' }).fill(key);
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(`Markdown ${key}`);
  await page.getByRole('button', { name: 'Create page' }).click();
  await page.getByRole('dialog').getByText('Blank page').click();
  await expect(page).toHaveURL(/\/edit$/);
  await page.getByRole('textbox', { name: 'Page title' }).fill('Runbook');

  // The Markdown on the left, what readers will see on the right.
  await page.getByText('Markdown', { exact: true }).click();
  const source = page.getByRole('textbox', { name: 'Markdown source' });
  await source.fill(
    '## Restart\n\nRun `systemctl restart kogge` as **root**.\n\n- [ ] check the log\n\n' +
      'Euler: $e^{i\\pi}+1=0$, costs \\$5.\n\n$$\n\\int_0^1 x\\,dx\n$$',
  );
  const preview = page.getByRole('region', { name: 'Preview' });
  await expect(preview.getByRole('heading', { name: 'Restart', level: 2 })).toBeVisible();
  await expect(preview.locator('strong')).toHaveText('root');
  await expect(preview.locator('code')).toHaveText('systemctl restart kogge');
  // Formulas are typeset, one within the text and one of its own; an escaped dollar is a dollar.
  await expect(preview.locator('.katex')).toHaveCount(2);
  await expect(preview.locator('.katex-display')).toHaveCount(1);
  await expect(preview).toContainText('costs $5.');

  // Formatted again: the editor holds what was typed, the formulas as formulas.
  await page.getByText('Formatted', { exact: true }).click();
  const editor = page.getByRole('textbox', { name: /Start writing/ });
  await expect(editor.getByRole('heading', { name: 'Restart', level: 2 })).toBeVisible();
  await expect(editor.locator('.katex')).toHaveCount(2);

  // Markdown typed just before publishing is published, too.
  await page.getByText('Markdown', { exact: true }).click();
  await expect(source).toContainText('## Restart');
  // Markdown holds formulas: they come back as they were written, and nothing warns of content it cannot hold.
  await expect(source).toContainText('Euler: $e^{i\\pi}+1=0$, costs \\$5.');
  await expect(source).toContainText('$$\n\\int_0^1 x\\,dx\n$$', { useInnerText: true });
  await expect(page.getByText(/content that Markdown cannot hold/)).toBeHidden();
  // The source has line numbers, and its toolbar marks text: here a new heading.
  await expect(page.locator('.cm-lineNumbers').getByText('3', { exact: true })).toBeVisible();
  await source.fill(
    '## Restart\n\nRun `systemctl restart kogge` as **root**, then wait.\n\n$$\nt \\le 5\n$$',
  );
  await page.getByRole('button', { name: 'Publish' }).click();
  await page
    .getByRole('dialog')
    .or(page.locator('.mantine-Popover-dropdown'))
    .getByRole('button', { name: 'Publish' })
    .click();
  await expect(page.getByRole('heading', { name: 'Runbook', level: 1 })).toBeVisible();
  await expect(page.getByText('then wait.')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Restart', level: 2 })).toBeVisible();
  await expect(page.locator('.katex-display')).toHaveCount(1);
});
