// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';
import type { Page } from '@playwright/test';

/** A project with a select, a number, a text and a long text field of its own, a version and two issues. */
async function project(page: Page, first: string) {
  const key = uniqueKey(first);
  const created = await apiCall(page, 'POST', '/api/v1/projects', {
    key,
    name: `E2E View ${key}`,
    template: 'scrum',
  });
  const field = (name: string, type: string, extra: Record<string, unknown> = {}) =>
    apiCall(page, 'POST', '/api/v1/issue-config/fields', {
      name: `${name} ${key}`,
      type,
      projectIds: [created.id],
      ...extra,
    });
  const team = await field('Team', 'select', {
    options: [
      { id: -1, value: 'Payments' },
      { id: -2, value: 'Search' },
    ],
  });
  const budget = await field('Budget', 'number');
  const criteria = await field('Acceptance criteria', 'textarea');
  await apiCall(page, 'POST', `/api/v1/projects/${key}/versions`, {
    name: '2.0',
  });
  const detail = await apiGet(page, `/api/v1/projects/${key}`);
  const typeOf = (icon: string) => detail.issueTypes.find((t: { icon: string }) => t.icon === icon).id;
  return {
    key,
    id: created.id,
    team: `cf_${team.id}`,
    budget,
    criteria,
    typeOf,
  };
}

test('edit an issue in place', async ({ page }) => {
  await signIn(page);
  const p = await project(page, 'V');
  await apiCall(page, 'POST', '/api/v1/issues', {
    project: p.key,
    typeId: p.typeOf('story'),
    summary: 'Pay with a saved card',
    descriptionMarkdown: 'Customers choose one of their **saved cards**.',
  });
  await apiCall(page, 'POST', '/api/v1/issues', {
    project: p.key,
    typeId: p.typeOf('bug'),
    summary: 'Card list is empty',
  });
  await page.goto(`/browse/${p.key}-1`);
  await expect(page.getByRole('heading', { name: 'Pay with a saved card' })).toBeVisible();
  const details = page.locator('.kogge-issue-side');

  // A choice saves at once.
  await details.getByRole('button', { name: /^Priority / }).click();
  await page.getByRole('option', { name: /^(High|Hoch)$/ }).click();
  await expect(details.getByRole('button', { name: /^Priority (High|Hoch)/ })).toBeVisible();

  await details.getByRole('button', { name: 'Assign to me' }).click();
  await expect(details.getByRole('button', { name: /^Assignee admin/ })).toBeVisible();

  // Empty fields wait behind "more fields".
  await details.getByRole('button', { name: /more fields/ }).click();
  await details.getByRole('button', { name: /^Labels / }).click();
  await page.getByRole('combobox', { name: 'Labels' }).fill('checkout');
  await page.getByRole('combobox', { name: 'Labels' }).press('Enter');
  await details.getByRole('heading', { name: 'Details' }).click(); // leaving the editor saves it
  await expect(details.getByRole('link', { name: 'checkout' })).toBeVisible();

  await details.getByRole('button', { name: new RegExp(`^Team ${p.key}`) }).click();
  await page.getByRole('option', { name: 'Payments' }).click();
  await expect(
    details.getByRole('button', {
      name: new RegExp(`^Team ${p.key} Payments`),
    }),
  ).toBeVisible();

  await details.getByRole('button', { name: new RegExp(`^Budget ${p.key}`) }).click();
  await page.getByRole('textbox', { name: `Budget ${p.key}` }).fill('1200.5');
  await page.getByRole('textbox', { name: `Budget ${p.key}` }).press('Enter');
  await expect(
    details.getByRole('button', {
      name: new RegExp(`^Budget ${p.key} 1,200.5`),
    }),
  ).toBeVisible();

  await details.getByRole('button', { name: /^Fix versions / }).click();
  await page.getByRole('option', { name: '2.0' }).click();
  await details.getByRole('heading', { name: 'Details' }).click();
  await expect(details.getByRole('link', { name: '2.0' })).toBeVisible();

  // The status is a menu of the workflow's transitions.
  await page.getByRole('button', { name: /^Status: / }).click();
  await page.getByRole('menuitem', { name: /(Start progress|Bearbeitung beginnen)/ }).click();
  await expect(page.getByRole('button', { name: /^Status: (In Progress|In Arbeit)/ })).toBeVisible();

  // Rich text fields: the description and a long text field of the project.
  await page.getByRole('button', { name: 'Edit Description' }).click();
  await page.locator('.ProseMirror').first().press('End');
  await page.locator('.ProseMirror').first().pressSequentially(' Cards that expired are not offered.');
  const save = page.getByRole('button', { name: 'Save', exact: true });
  await save.click();
  // Saved when the editor has closed (the text is visible in it before).
  await expect(save).toHaveCount(0);
  await expect(page.getByText('Cards that expired are not offered.')).toBeVisible();
  await page.getByText('Add text…').click();
  await page.locator('.ProseMirror').first().fill('The last four digits are shown.');
  await save.click();
  await expect(save).toHaveCount(0);
  await expect(page.getByText('The last four digits are shown.')).toBeVisible();

  // Sub-tasks, links and work.
  await page.getByRole('button', { name: 'Sub-task', exact: true }).click();
  await page.getByRole('textbox', { name: 'Add sub-task' }).fill('Show the card brand');
  await page.getByRole('textbox', { name: 'Add sub-task' }).press('Enter');
  await expect(page.getByRole('link', { name: `${p.key}-3` })).toBeVisible();
  await expect(page.getByText('0 of 1 done')).toBeVisible();

  await page.getByRole('button', { name: 'Link issue' }).click();
  const linkDialog = page.getByRole('dialog', { name: 'Link issue' });
  await linkDialog.getByRole('combobox', { name: 'Issue' }).fill(`${p.key}-2`);
  await page.getByRole('option', { name: new RegExp(`^${p.key}-2 Card list is empty`) }).click();
  await linkDialog.getByRole('button', { name: 'Link issue' }).click();
  await expect(linkDialog).toBeHidden();
  await expect(page.locator('.kogge-issue-main').getByRole('link', { name: `${p.key}-2` })).toBeVisible();

  await details.getByRole('button', { name: 'Log work' }).click();
  const logDialog = page.getByRole('dialog', { name: 'Log work' });
  await logDialog.getByRole('textbox', { name: /Time spent/ }).fill('1h 30m');
  await logDialog.getByRole('button', { name: 'Log work' }).click();
  await expect(logDialog).toBeHidden();
  await expect(details.getByText('1h 30m logged')).toBeVisible();

  // Everything was saved.
  await page.reload();
  await expect(details.getByRole('button', { name: /^Priority (High|Hoch)/ })).toBeVisible();
  await expect(
    details.getByRole('button', {
      name: new RegExp(`^Team ${p.key} Payments`),
    }),
  ).toBeVisible();
  await expect(details.getByRole('link', { name: 'checkout' })).toBeVisible();
  await expect(page.getByRole('button', { name: /^Status: (In Progress|In Arbeit)/ })).toBeVisible();
  const issue = await apiGet(page, `/api/v1/issues/${p.key}-1`);
  expect(issue.fields[p.team]).toBeTruthy();
  expect(issue.fields[`cf_${p.budget.id}`]).toBe(1200.5);
  expect(issue.timeSpent).toBe(5400);
});

test('add new and existing issues as sub-tasks and delete them', async ({ page }) => {
  await signIn(page);
  const p = await project(page, 'S');
  const create = (summary: string) =>
    apiCall(page, 'POST', '/api/v1/issues', { project: p.key, typeId: p.typeOf('story'), summary });
  const story = await create('Call the registry office again');
  const earlier = await create('Call the registry office');
  await page.goto(`/browse/${story.key}`);

  await page.getByRole('button', { name: 'Sub-task', exact: true }).click();
  const field = page.getByRole('textbox', { name: 'Add sub-task' });
  await field.fill('Wait for the letter');
  await expect(page.getByRole('option', { name: 'Create sub-task “Wait for the letter”' })).toBeVisible();
  await field.press('Enter');
  const created = `${p.key}-3`;
  await expect(page.getByRole('link', { name: created })).toBeVisible();

  // The start of a key lists the project's issues, not a new sub-task called so; the one chosen becomes a sub-task.
  await field.fill(p.key);
  await expect(page.getByRole('option', { name: /^Create sub-task/ })).toHaveCount(0);
  await page.getByRole('option', { name: new RegExp(`${earlier.key} Call the registry office$`) }).click();
  await expect(page.getByText('0 of 2 done')).toBeVisible();
  const adopted = await apiGet(page, `/api/v1/issues/${earlier.key}`);
  expect(adopted.type.level).toBe(-1);
  expect(adopted.parent.key).toBe(story.key);

  await page.getByRole('button', { name: `Delete ${created}` }).click();
  await page
    .getByRole('dialog', { name: `Delete ${created}` })
    .getByRole('button', { name: 'Delete' })
    .click();
  await expect(page.getByRole('link', { name: created })).toHaveCount(0);
  await expect(page.getByText('0 of 1 done')).toBeVisible();
});

test('create an issue with the fields the project requires', async ({ page }) => {
  await signIn(page);
  const p = await project(page, 'W');
  // The project's layout requires the team; a security scheme gives new issues a level by default.
  const layout = await apiGet(page, `/api/v1/projects/${p.key}/field-layouts/0`);
  await apiCall(
    page,
    'PUT',
    `/api/v1/projects/${p.key}/field-layouts/0`,
    layout.map((it: { field: string }) =>
      it.field === p.team ? { ...it, create: true, required: true } : it,
    ),
  );
  const me = await apiGet(page, '/api/v1/me');
  const scheme = await apiCall(page, 'POST', '/api/v1/issue-config/security-schemes', {
    name: `Confidential ${p.key}`,
    defaultLevelId: -1,
    levels: [
      {
        id: -1,
        name: `Staff ${p.key}`,
        members: [{ holderType: 'user', holderId: me.user.id }],
      },
      {
        id: -2,
        name: `Board ${p.key}`,
        members: [{ holderType: 'user', holderId: me.user.id }],
      },
    ],
  });
  await apiCall(page, 'PATCH', `/api/v1/projects/${p.key}`, {
    securitySchemeId: scheme.id,
  });

  await page.goto(`/projects/${p.key}`);
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Create issue' });
  await expect(dialog.getByRole('combobox', { name: 'Project' })).toHaveValue(new RegExp(p.key));
  await dialog.getByRole('textbox', { name: 'Summary' }).fill('Refund to the saved card');
  await expect(dialog.getByRole('combobox', { name: 'Security level' })).toHaveValue(`Staff ${p.key}`);
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(dialog.getByText('Required')).toBeVisible();

  await dialog.getByRole('combobox', { name: new RegExp(`^Team ${p.key}`) }).click();
  await page.getByRole('option', { name: 'Search' }).click();
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(dialog).toBeHidden();
  await page
    .getByRole('alert')
    .getByRole('link', { name: new RegExp(`^${p.key}-\\d+$`) })
    .click();

  const details = page.locator('.kogge-issue-side');
  await expect(details.getByRole('button', { name: new RegExp(`^Team ${p.key} Search`) })).toBeVisible();
  await expect(details.getByText(`Staff ${p.key}`)).toBeVisible();
});
