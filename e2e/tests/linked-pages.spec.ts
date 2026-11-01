// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, signIn, test } from './fixtures';

test('write release notes for a version in the linked space', async ({ page }) => {
  await signIn(page);
  const key = `R${Date.now() % 100000}`;
  await apiCall(page, 'POST', '/api/v1/projects', { key, name: `Release ${key}`, template: 'scrum' });
  await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key: `D${key}`, name: `Docs ${key}` });
  const version = await apiCall(page, 'POST', `/api/v1/projects/${key}/versions`, { name: '2.1' });
  const types = await (await page.request.get(`/api/v1/projects/${key}/issue-types`)).json();
  const issue = await apiCall(page, 'POST', '/api/v1/issues', { project: key, typeId: types[0].issueTypeId, summary: 'Faster checkout' });
  await apiCall(page, 'PATCH', `/api/v1/issues/${issue.key}`, { fields: { fixVersions: [version.id] } });

  await page.goto(`/projects/${key}/releases`);
  await page.getByRole('button', { name: 'More' }).first().click();
  await page.getByRole('menuitem', { name: 'Create release notes' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('textbox', { name: 'Page title' })).toHaveValue(`Release ${key} 2.1 – release notes`);
  // The picker searches the spaces as you type.
  await dialog.getByRole('combobox', { name: 'Space' }).fill(`D${key}`);
  await page.getByRole('option', { name: new RegExp(`D${key}`) }).click();
  await dialog.getByRole('button', { name: 'Create page' }).click();

  // The editor opens with the template and the issues of the version.
  await expect(page).toHaveURL(/\/edit$/);
  await expect(page.getByText('Version 2.1')).toBeVisible();
  await expect(page.getByText('Issue list')).toBeVisible();
  await expect(page.getByText(`fixVersion = "2.1"`)).toBeVisible();

  // The space is the project's space from now on.
  const linked = await (await page.request.get(`/api/v1/projects/${key}/space`)).json();
  expect(linked.space.key).toBe(`D${key}`);
});

test('turn selected page text into an issue', async ({ page }) => {
  await signIn(page);
  const key = `T${Date.now() % 100000}`;
  await apiCall(page, 'POST', '/api/v1/projects', { key, name: `Tasks ${key}`, template: 'kanban' });
  await apiCall(page, 'POST', '/api/v1/wiki/spaces', { key: `W${key}`, name: `Wiki ${key}` });
  const doc = {
    type: 'doc',
    content: [{ type: 'paragraph', content: [{ type: 'text', text: 'Next we migrate the build servers before Friday.' }] }],
  };
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, { title: 'Plan', kind: 'page', parentId: 0, body: doc });
  await apiCall(page, 'POST', `/api/v1/wiki/pages/${draft.id}/publish`, { title: 'Plan', body: doc, baseVersion: 0 });

  await page.goto(`/wiki/spaces/W${key}/pages/${draft.id}`);
  await expect(page.getByText('migrate the build servers')).toBeVisible();
  await page.evaluate(() => {
    const p = Array.from(document.querySelectorAll('.kogge-doc p')).find((x) => x.textContent?.includes('build servers'))!;
    const text = p.firstChild!;
    const phrase = 'migrate the build servers';
    const at = text.textContent!.indexOf(phrase);
    const r = document.createRange();
    r.setStart(text, at);
    r.setEnd(text, at + phrase.length);
    const sel = window.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(r);
  });
  await page.locator('.kogge-doc').first().dispatchEvent('mouseup');
  await page.getByRole('button', { name: 'Create issue' }).first().click();
  const dialog = page.getByRole('dialog', { name: 'Create issue' });
  await expect(dialog.getByRole('textbox', { name: 'Summary' })).toHaveValue('migrate the build servers');
  await dialog.getByRole('combobox', { name: 'Project' }).click();
  await page.getByRole('option', { name: new RegExp(key) }).click();
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();

  // The selection became the new issue, in a new version of the page.
  await expect(page.locator('.kogge-doc').getByText(`${key}-1`)).toBeVisible();
  await expect(page.getByText('Next we')).toBeVisible();
  await expect(page.getByText('migrate the build servers')).toHaveCount(1); // the issue's summary in the chip
});
