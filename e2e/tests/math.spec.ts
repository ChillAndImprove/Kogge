// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

const text = (t: string) => ({ type: 'text', text: t });
const inline = (latex: string) => ({ type: 'inlineMath', attrs: { latex } });

test('formulas are typeset in pages, with KaTeX fonts, and show why where they cannot be', async ({
  page,
}) => {
  await signIn(page);
  const key = uniqueKey('F');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', {
    key: `W${key}`,
    name: `Formulas ${key}`,
  });
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, {
    title: 'Physics',
    parentId: space.homePageId,
  });
  await apiCall(page, 'POST', `/api/v1/wiki/pages/${draft.id}/publish`, {
    title: 'Physics',
    baseVersion: 0,
    body: {
      type: 'doc',
      content: [
        { type: 'paragraph', content: [text('Energy '), inline('E=mc^2'), text(' holds.')] },
        { type: 'math', attrs: { latex: '\\int_0^1 x^2\\,dx = \\frac{1}{3}' } },
        { type: 'paragraph', content: [text('Broken: '), inline('\\frac{1')] },
        // Refused commands stay inert: no link, no style.
        { type: 'paragraph', content: [inline('\\href{https://example.com}{x}')] },
      ],
    },
  });

  await page.goto(`/wiki/spaces/W${key}/pages/${draft.id}`);
  const doc = page.locator('.kogge-doc');
  const energy = doc.locator('.kogge-math-inline[data-latex="E=mc^2"]');
  await expect(energy.locator('.katex')).toBeVisible();
  await expect(energy.locator('annotation')).toHaveText('E=mc^2');
  const block = doc.locator('.kogge-math-block');
  await expect(block.locator('.katex-display')).toBeVisible();
  await expect(block).toHaveAttribute('data-latex', '\\int_0^1 x^2\\,dx = \\frac{1}{3}');
  // (Selectors name no LaTeX with backslashes: in CSS, those escape.)
  const broken = doc.locator('.kogge-math-inline code.kogge-math-error');
  await expect(broken).toHaveText('\\frac{1');
  await expect(broken).toHaveAttribute('title', /The formula cannot be typeset/);
  await expect(doc.locator('.kogge-math a')).toHaveCount(0);
  // KaTeX's own fonts are loaded (woff2, from Kogge itself).
  await expect
    .poll(() =>
      page.evaluate(() =>
        [...document.fonts].some((f) => f.family.replace(/"/g, '') === 'KaTeX_Main' && f.status === 'loaded'),
      ),
    )
    .toBe(true);

  // In the dark colour scheme, too.
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.reload();
  await expect(page.locator('.kogge-doc .kogge-math-block .katex-display')).toBeVisible();
});

test('formulas are written with the "/" menu, typed as $…$ and edited in their dialog', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('G');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', {
    key: `W${key}`,
    name: `Formulas ${key}`,
  });
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, {
    title: 'Geometry',
    parentId: space.homePageId,
  });
  await page.goto(`/wiki/spaces/W${key}/pages/${draft.id}/edit`);
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();
  const editor = page.getByRole('textbox', { name: /Start writing/ });
  await editor.click();

  // "/" offers formulas within the text and on a line of their own; the dialog previews while one types.
  await page.keyboard.type('/formula');
  const menu = page.getByRole('listbox', { name: 'Insert' });
  await expect(menu.getByRole('option')).toHaveCount(2);
  await menu.getByRole('option').filter({ hasText: 'Formula on its own line' }).click();
  const dialog = page.getByRole('dialog', { name: 'Formula' });
  const latex = dialog.getByRole('textbox', { name: 'LaTeX' });
  await latex.fill('\\frac{1');
  await expect(dialog.getByText(/The formula cannot be typeset/)).toBeVisible();
  await latex.fill('a^2 + b^2 = c^2');
  await expect(dialog.locator('.kogge-formula-preview .katex-display')).toBeVisible();
  await expect(dialog.getByText(/The formula cannot be typeset/)).toHaveCount(0);
  await dialog.getByRole('button', { name: 'Insert' }).click();
  const block = editor.locator('.kogge-math-block');
  await expect(block).toHaveAttribute('data-latex', 'a^2 + b^2 = c^2');
  await expect(block.locator('.katex-display')).toBeVisible();

  // Typed between dollars, a formula within the text.
  await page.keyboard.type('Square: $a^2$ done');
  await expect(editor.locator('.kogge-math-inline[data-latex="a^2"] .katex')).toBeVisible();

  // A double click edits a formula; Ctrl/Cmd+Enter saves.
  await block.dblclick();
  await expect(dialog).toBeVisible();
  await expect(latex).toHaveValue('a^2 + b^2 = c^2');
  await latex.fill('c = \\sqrt{a^2 + b^2}');
  await latex.press('ControlOrMeta+Enter');
  await expect(dialog).toHaveCount(0);
  await expect(block).toHaveAttribute('data-latex', 'c = \\sqrt{a^2 + b^2}');
  await expect(block.locator('.katex')).toBeVisible();
  await expect(page.locator('[data-sync="saved"]')).toBeVisible();

  // Everything arrived in the shared draft.
  const saved = await apiGet(page, `/api/v1/wiki/pages/${draft.id}/edit`);
  const json = JSON.stringify(saved.doc);
  expect(json).toContain('{"type":"math","attrs":{"latex":"c = \\\\sqrt{a^2 + b^2}"}}');
  expect(json).toContain('{"type":"inlineMath","attrs":{"latex":"a^2"}}');
});

test('the comment editor of an issue offers formulas', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('H');
  await apiCall(page, 'POST', '/api/v1/projects', { key, name: `Formulas ${key}`, template: 'scrum' });
  const types = await apiGet(page, `/api/v1/projects/${key}/issue-types`);
  const issue = await apiCall(page, 'POST', '/api/v1/issues', {
    project: key,
    typeId: types[0].issueTypeId,
    summary: 'Rounding of tax',
  });
  await page.goto(`/browse/${issue.key}`);
  const comment = page
    .locator('.kogge-editor')
    .filter({ has: page.getByRole('textbox', { name: 'Add a comment…' }) });
  await comment.getByRole('textbox', { name: 'Add a comment…' }).click();
  await page.keyboard.type('Round half to even: ');
  await comment.getByRole('button', { name: 'Formula' }).click();
  const dialog = page.getByRole('dialog', { name: 'Formula' });
  await dialog.getByRole('textbox', { name: 'LaTeX' }).fill('\\lfloor x + \\tfrac{1}{2} \\rfloor');
  await dialog.getByRole('button', { name: 'Insert' }).click();
  await expect(comment.locator('.kogge-math-inline .katex')).toBeVisible();
  await page.getByRole('button', { name: 'Comment', exact: true }).click();
  await expect(page.locator('.kogge-doc .kogge-math-inline .katex').first()).toBeVisible();
});
