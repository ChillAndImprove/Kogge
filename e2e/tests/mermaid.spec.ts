// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, signIn, test, uniqueKey } from './fixtures';

const macro = (code: string) => ({ type: 'macro', attrs: { name: 'mermaid', params: { code } } });

test('Mermaid diagrams are drawn in pages and Markdown files', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('M');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', {
    key: `W${key}`,
    name: `Diagrams ${key}`,
  });
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, {
    title: 'Order process',
    parentId: space.homePageId,
  });
  await apiCall(page, 'POST', `/api/v1/wiki/pages/${draft.id}/publish`, {
    title: 'Order process',
    baseVersion: 0,
    body: {
      type: 'doc',
      content: [
        macro('flowchart LR\n  A[Order] --> B{Paid?}\n  B -->|yes| C[Shipping]\n  B -->|no| D[Reminder]'),
        // ELK is not part of Kogge: the default layout draws it.
        macro('---\nconfig:\n  layout: elk\n---\nflowchart TD\n  Warehouse --> Truck'),
        macro('flowchart LR\n  F["$$x^2$$"] --> G[Square]'),
        macro('flowchart LR\n  A -->'),
      ],
    },
  });

  await page.goto(`/wiki/spaces/W${key}/pages/${draft.id}`);
  const diagrams = page.locator('.kogge-mermaid');
  await expect(diagrams).toHaveCount(4);
  const first = diagrams.nth(0);
  const drawing = first.locator('.kogge-mermaid-svg > svg');
  await expect(drawing).toBeVisible();
  await expect(drawing).toContainText('Shipping');
  await expect(diagrams.nth(1).locator('.kogge-mermaid-svg > svg')).toContainText('Truck');
  await expect(diagrams.nth(2).locator('.kogge-mermaid-svg math')).toHaveCount(1);
  await expect(diagrams.nth(3).getByRole('alert')).toContainText('The diagram cannot be drawn');
  await expect(diagrams.nth(3).locator('pre')).toContainText('A -->');
  // The text behind a diagram is a click away.
  await first.getByRole('button', { name: 'Show the source' }).click();
  await expect(first.locator('pre')).toContainText('B -->|yes| C[Shipping]');
  await expect(drawing).toBeHidden();
  await first.getByRole('button', { name: 'Show the diagram' }).click();
  await expect(drawing).toBeVisible();
  // In the dark colour scheme, too.
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.reload();
  await expect(page.locator('.kogge-mermaid-svg > svg').first()).toContainText('Shipping');

  // A Markdown file in Hafen.
  await apiCall(page, 'POST', '/api/v1/code/projects', { key: `C${key}`, name: `Diagrams ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/C${key}/repos`, { name: 'Service', readme: true });
  await apiCall(page, 'PUT', `/api/v1/code/repos/C${key}/service/files`, {
    branch: 'main',
    path: 'README.md',
    content: '# Service\n\n```mermaid\nsequenceDiagram\n  Shop->>Bank: Charge\n  Bank-->>Shop: Done\n```\n',
  });
  await page.goto(`/code/c${key.toLowerCase()}/service`);
  await expect(page.locator('.kogge-mermaid-svg > svg')).toContainText('Charge');
});

test('the macro dialog previews a diagram while it is written', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('N');
  const space = await apiCall(page, 'POST', '/api/v1/wiki/spaces', {
    key: `W${key}`,
    name: `Preview ${key}`,
  });
  const draft = await apiCall(page, 'POST', `/api/v1/wiki/spaces/W${key}/pages`, {
    title: 'Architecture',
    parentId: space.homePageId,
  });
  await page.goto(`/wiki/spaces/W${key}/pages/${draft.id}/edit`);
  await page.getByRole('button', { name: 'Insert', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Macro…' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('combobox', { name: 'Macro' }).fill('Mermaid');
  await page.getByRole('option', { name: 'Diagram (Mermaid)' }).click();
  await dialog.getByRole('textbox', { name: 'Diagram text' }).fill('flowchart LR\n  Browser --> Kogge');
  await expect(dialog.locator('.kogge-mermaid-svg > svg')).toContainText('Kogge');
});
