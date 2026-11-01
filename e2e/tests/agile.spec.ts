// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { Locator, Page } from '@playwright/test';
import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

/** Drags with real mouse moves, which the board's drag-and-drop library needs. */
async function drag(page: Page, from: Locator, to: Locator) {
  const a = await from.boundingBox();
  const b = await to.boundingBox();
  if (!a || !b) throw new Error('element not visible');
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
  await page.mouse.down();
  await page.mouse.move(a.x + a.width / 2 + 10, a.y + a.height / 2 + 10, { steps: 5 });
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 15 });
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2 + 2, { steps: 2 });
  await page.mouse.up();
}

test('plan a sprint in the backlog and work on the board', async ({ page }) => {
  await signIn(page);
  const key = `SC${Date.now() % 100000}`;
  await page.goto('/issues/projects');
  await page.getByRole('button', { name: 'Create project' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Name' }).fill(`Scrum ${key}`);
  await dialog.getByRole('textbox', { name: 'Key' }).fill(key);
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/projects/${key}$`));

  // Backlog: create two issues inline and a sprint.
  await page.getByRole('link', { name: 'Backlog' }).click();
  await expect(page.getByText('The backlog is empty.')).toBeVisible();
  const backlog = page.locator('.mantine-Paper-root').filter({ hasText: /^Backlog/ });
  await backlog.getByRole('button', { name: 'Create issue' }).click();
  for (const summary of ['Login with smart card', 'Session timeout warning']) {
    await backlog.getByRole('textbox', { name: 'Summary' }).fill(summary);
    await backlog.getByRole('textbox', { name: 'Summary' }).press('Enter');
    await expect(page.getByText(summary)).toBeVisible();
  }
  await page.getByRole('button', { name: 'Create sprint' }).click();
  const sprint = page.locator('.mantine-Paper-root').filter({ hasText: `${key} Sprint 1` });
  await expect(sprint.getByText('Drag issues here to plan the sprint.')).toBeVisible();

  // Drag the first issue into the sprint and start it.
  await drag(
    page,
    page.locator('.kogge-backlog-row', { hasText: 'Login with smart card' }),
    sprint.getByText('Drag issues here'),
  );
  await expect(sprint.locator('.kogge-backlog-row', { hasText: 'Login with smart card' })).toBeVisible();
  await sprint.getByRole('button', { name: 'Start sprint' }).click();
  const start = page.getByRole('dialog', { name: 'Start sprint' });
  await start.getByRole('textbox', { name: 'Sprint goal' }).fill('Secure sign-in');
  await start.getByRole('button', { name: 'Start sprint' }).click();
  await expect(start).toBeHidden();

  // The board shows the sprint; dragging the card to the next column transitions the issue.
  await page.getByRole('link', { name: 'Active sprint' }).click();
  await expect(page.getByRole('heading', { name: `${key} Sprint 1` })).toBeVisible();
  const card = page.locator('.kogge-card', { hasText: 'Login with smart card' });
  await expect(card).toBeVisible();
  const columns = page.locator('.kogge-board-column');
  // The card moves at once; the reload waits for the transition, which it would otherwise cut off.
  const transitioned = page.waitForResponse((r) => r.request().method() === 'POST' && r.url().endsWith('/transitions'));
  await drag(page, card, columns.nth(1));
  await expect(columns.nth(1).locator('.kogge-card', { hasText: 'Login with smart card' })).toBeVisible();
  expect((await transitioned).ok()).toBeTruthy();
  await page.reload();
  await expect(columns.nth(1).locator('.kogge-card', { hasText: 'Login with smart card' })).toBeVisible();

  // Opening a card shows the issue in a drawer.
  await page.locator('.kogge-card', { hasText: 'Login with smart card' }).click();
  await expect(
    page.getByRole('dialog').getByRole('heading', { name: 'Login with smart card' }),
  ).toBeVisible();
  await page.keyboard.press('Escape');

  // Reports render for the active sprint.
  await page.getByRole('link', { name: 'Reports' }).click();
  await expect(page.getByRole('tab', { name: 'Burndown' })).toBeVisible();
  await expect(page.getByRole('cell', { name: /Sprint start|Sprintbeginn/ })).toBeVisible();
  await page.getByRole('tab', { name: 'Sprint report' }).click();
  await expect(page.getByText(/Issues not completed/)).toBeVisible();
});

test('filter, group and act on the cards of a board', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('B');
  await apiCall(page, 'POST', '/api/v1/projects', { key, name: `Board ${key}`, template: 'scrum' });
  const project = await apiGet(page, `/api/v1/projects/${key}`);
  const task = project.issueTypes.find((t: { icon: string }) => t.icon === 'task').id;
  const me = await apiGet(page, '/api/v1/me');
  const keys: string[] = [];
  for (const [summary, assignee] of [
    ['Rotate the signing keys', me.user.id],
    ['Audit the admin sign-ins', me.user.id],
    ['Translate the error pages', null],
  ] as const) {
    const issue = await apiCall(page, 'POST', '/api/v1/issues', {
      project: key,
      typeId: task,
      summary,
      fields: assignee ? { assignee } : {},
    });
    keys.push(issue.key);
  }
  const { items: boards } = await apiGet(page, `/api/v1/boards?project=${key}`);
  const sprint = await apiCall(page, 'POST', `/api/v1/boards/${boards[0].id}/sprints`, {
    name: `${key} Sprint`,
  });
  await apiCall(page, 'POST', `/api/v1/sprints/${sprint.id}/issues`, { issues: keys });
  await apiCall(page, 'POST', `/api/v1/sprints/${sprint.id}/start`, {});

  await page.goto(`/projects/${key}/boards/${boards[0].id}`);
  const cards = page.locator('.kogge-card');
  await expect(cards).toHaveCount(3);

  // Words of the summary or the key.
  await page.getByRole('textbox', { name: 'Filter cards' }).fill('sign');
  await expect(cards).toHaveCount(2);
  await page.getByRole('textbox', { name: 'Filter cards' }).fill(keys[2]);
  await expect(cards).toHaveCount(1);
  await page.getByRole('button', { name: 'Clear filters' }).click();
  await expect(cards).toHaveCount(3);

  // People: the unassigned card alone, then everything grouped by assignee.
  await page.getByRole('button', { name: 'Unassigned', exact: true }).click();
  await expect(cards).toHaveCount(1);
  await expect(page.getByText('1/3')).toBeVisible();
  await page.getByRole('button', { name: 'Unassigned', exact: true }).click();
  await page.getByRole('combobox', { name: 'Group the cards by' }).click();
  await page.getByRole('option', { name: 'Assignee' }).click();
  await expect(page.getByRole('button', { name: /Unassigned 1 issue/ })).toBeVisible();

  // Card actions: assign the card to oneself; take another out of the sprint.
  const lone = cards.filter({ hasText: 'Translate the error pages' });
  await lone.hover();
  await lone.getByRole('button', { name: `Actions for ${keys[2]}` }).click();
  await page.getByRole('menuitem', { name: 'Assign to me' }).click();
  await expect
    .poll(async () => (await apiGet(page, `/api/v1/issues/${keys[2]}`)).assignee?.id)
    .toBe(me.user.id);
  const audit = cards.filter({ hasText: 'Audit the admin sign-ins' });
  await audit.hover();
  await audit.getByRole('button', { name: `Actions for ${keys[1]}` }).click();
  await page.getByRole('menuitem', { name: 'Remove from the sprint' }).click();
  await expect(cards).toHaveCount(2);
  expect((await apiGet(page, `/api/v1/issues/${keys[1]}`)).sprint).toBeFalsy();
});
