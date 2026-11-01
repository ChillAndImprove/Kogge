// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, signIn, test, uniqueKey } from './fixtures';

const macro = (name: string, params: Record<string, unknown>) => ({ type: 'macro', attrs: { name, params } });

test('the page editor shows macros as readers see them, and the page in a preview', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('E');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key: `W${key}`, name: `Editor ${key}` });
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, {
    title: 'Handbook',
    parentId: space.homePageId,
  });
  await apiCall(page, 'POST', `/api/v1/wiki/pages/${draft.id}/publish`, {
    title: 'Handbook',
    baseVersion: 0,
    body: {
      type: 'doc',
      content: [
        macro('toc', { maxLevel: 3 }),
        { type: 'heading', attrs: { level: 2 }, content: [{ type: 'text', text: 'Onboarding' }] },
        macro('mermaid', { code: 'flowchart LR\n  Contract --> Laptop' }),
        { type: 'paragraph', content: [{ type: 'text', text: 'Welcome aboard.' }] },
      ],
    },
  });
  await page.goto(`/wiki/spaces/W${key}/pages/${draft.id}/edit`);
  const editor = page.getByRole('textbox', { name: /Start writing/ });
  const toc = page.locator('.kogge-macro[data-macro="toc"] .kogge-macro-preview');
  const diagram = page.locator('.kogge-macro[data-macro="mermaid"]');

  // Macros show their output while the page is written; the table of contents follows new headings.
  await expect(toc).toContainText('Onboarding');
  await expect(diagram.locator('.kogge-macro-preview .kogge-mermaid-svg > svg')).toContainText('Laptop');
  await editor.getByText('Welcome aboard.').click();
  await page.keyboard.press('End');
  await page.keyboard.press('Enter');
  await page.keyboard.type('## Equipment');
  await expect(editor.getByRole('heading', { name: 'Equipment' })).toBeVisible();
  await expect(toc).toContainText('Equipment');

  // A click selects a macro, a double click edits it; the output follows its parameters.
  await diagram.locator('.kogge-macro-preview').click();
  await expect(diagram).toHaveAttribute('data-selected', 'true');
  await diagram.locator('.kogge-macro-preview').dblclick();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Diagram text' }).fill('flowchart LR\n  Contract --> Badge');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(diagram.locator('.kogge-macro-preview .kogge-mermaid-svg > svg')).toContainText('Badge');

  // The preview shows the page as readers will see it; the editor keeps its state behind it.
  await page.locator('label').filter({ hasText: /^Preview$/ }).click();
  await expect(editor).toBeHidden();
  const preview = page.locator('[data-preview]');
  await expect(preview.getByRole('heading', { name: 'Handbook', level: 1 })).toBeVisible();
  await expect(preview.getByRole('heading', { name: 'Equipment', level: 2 })).toBeVisible();
  await expect(preview.getByRole('link', { name: 'Equipment' })).toBeVisible();
  await expect(preview.locator('.kogge-mermaid-svg > svg')).toContainText('Badge');
  await page.locator('label').filter({ hasText: /^Edit$/ }).click();
  await expect(preview).toHaveCount(0);
  await expect(editor.getByRole('heading', { name: 'Equipment' })).toBeVisible();
  // Undo still knows the changes from before the preview.
  await editor.getByRole('heading', { name: 'Equipment' }).click();
  await page.keyboard.press('ControlOrMeta+z');
  await expect(diagram.locator('.kogge-macro-preview .kogge-mermaid-svg > svg')).toContainText('Laptop');
});

test('the editor inserts blocks from the "/" menu, edits tables and links, and names code languages', async ({
  page,
}) => {
  await signIn(page);
  const key = uniqueKey('T');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key: `W${key}`, name: `Tools ${key}` });
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, {
    title: 'Release checklist',
    parentId: space.homePageId,
  });
  await page.goto(`/wiki/spaces/W${key}/pages/${draft.id}/edit`);
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();
  const editor = page.getByRole('textbox', { name: /Start writing/ });
  await editor.click();

  // "/" offers blocks, filtered by what follows; Enter inserts the chosen one.
  await page.keyboard.type('/table');
  const menu = page.getByRole('listbox', { name: 'Insert' });
  await expect(menu.getByRole('option')).toHaveCount(2); // the table, and the table of contents
  await expect(menu.getByRole('option').first()).toContainText('Rows and columns');
  await page.keyboard.press('Enter');
  await expect(editor.locator('table tr')).toHaveCount(3);
  // In a table, its tools appear.
  const tools = page.getByRole('toolbar', { name: 'Table' });
  await tools.getByRole('button', { name: 'Insert row below' }).click();
  await expect(editor.locator('table tr')).toHaveCount(4);
  await tools.getByRole('button', { name: 'Delete column' }).click();
  await expect(editor.locator('table tr').first().locator('th')).toHaveCount(2);
  await tools.getByRole('button', { name: 'Delete table' }).click();
  await expect(editor.locator('table')).toHaveCount(0);
  await expect(tools).toHaveCount(0);

  // A status, from a dialog with a preview.
  await page.keyboard.type('/status');
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Status' });
  await dialog.getByRole('textbox', { name: 'Status text' }).fill('READY');
  await dialog.getByRole('radio', { name: 'Green' }).click();
  await dialog.getByRole('button', { name: 'Insert' }).click();
  await expect(editor.locator('[data-status="green"]')).toHaveText('READY');

  // Links: Ctrl+K asks for the address; a missing scheme becomes https, scripts are refused.
  await page.keyboard.press('End');
  await page.keyboard.press('Enter');
  await page.keyboard.type('the runbook');
  await editor.getByText('the runbook').click({ clickCount: 3 });
  await page.keyboard.press('ControlOrMeta+k');
  const address = page.getByRole('textbox', { name: 'Link address' });
  await address.fill('javascript:alert(1)');
  await address.press('Enter');
  await expect(page.getByText('Enter a web address')).toBeVisible();
  await address.fill('kogge.example/runbook');
  await address.press('Enter');
  const link = editor.getByRole('link', { name: 'the runbook' });
  await expect(link).toHaveAttribute('href', 'https://kogge.example/runbook');
  // Next to a link: where it leads, and removing it.
  await link.click();
  const bubble = page.locator('.kogge-link-bubble');
  await expect(bubble.getByRole('link', { name: 'https://kogge.example/runbook' })).toBeVisible();
  await bubble.getByRole('button', { name: 'Remove link' }).click();
  await expect(editor.getByRole('link')).toHaveCount(0);

  // A code block gets its language.
  await page.keyboard.press('ControlOrMeta+End');
  await page.keyboard.press('Enter');
  await page.keyboard.type('/code');
  await page.keyboard.press('Enter');
  await page.keyboard.type('func main() {}');
  await page.getByRole('combobox', { name: 'Language of the code' }).click();
  await page.getByRole('option', { name: 'Go', exact: true }).click();
  await expect(editor.locator('.kogge-editor-codeblock')).toHaveAttribute('data-language', 'go');
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();

  // Everything arrived in the shared draft.
  const saved = await (await page.request.get(`/api/v1/wiki/pages/${draft.id}/edit`)).json();
  const json = JSON.stringify(saved.doc);
  expect(json).toContain('"language":"go"');
  expect(json).toContain('"text":"READY"');
});

test('Markdown pasted as text becomes headings, lists, code and links', async ({ page, browserName }) => {
  // Firefox gives paste events that a page dispatches an empty clipboard; what people paste is not affected.
  test.skip(browserName === 'firefox', 'synthetic paste events carry no data in Firefox');
  await signIn(page);
  const key = uniqueKey('M');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key: `W${key}`, name: `Paste ${key}` });
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, {
    title: 'Runbook',
    parentId: space.homePageId,
  });
  await page.goto(`/wiki/spaces/W${key}/pages/${draft.id}/edit`);
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();
  const editor = page.getByRole('textbox', { name: /Start writing/ });
  await editor.click();

  // What a terminal or a text editor puts on the clipboard: text, without HTML.
  await editor.evaluate((el, text) => {
    const data = new DataTransfer();
    data.setData('text/plain', text);
    el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: data, bubbles: true, cancelable: true }));
  }, '## Restart\n\n1. Drain the node\n2. Run `systemctl restart kogge`\n\nSee [the guide](https://example.com/guide).\n\n```sh\nkogge admin fsck\n```\n');
  await expect(editor.getByRole('heading', { name: 'Restart' })).toBeVisible();
  await expect(editor.locator('ol > li')).toHaveCount(2);
  await expect(editor.locator('li code')).toHaveText('systemctl restart kogge');
  await expect(editor.getByRole('link', { name: 'the guide' })).toHaveAttribute('href', 'https://example.com/guide');
  await expect(editor.locator('pre')).toContainText('kogge admin fsck');
  // It is saved like typing: the shared draft has it.
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();
  const saved = await (await page.request.get(`/api/v1/wiki/pages/${draft.id}/edit`)).json();
  expect(JSON.stringify(saved.doc)).toContain('kogge admin fsck');
});
