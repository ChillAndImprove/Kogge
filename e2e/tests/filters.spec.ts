// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

test('a query becomes a shared filter, is saved again, subscribed to by e-mail and managed on the Filters page', async ({
  page,
}) => {
  await signIn(page);
  const key = uniqueKey('F');
  await apiCall(page, 'POST', '/api/v1/projects', { key, name: `Filters ${key}`, template: 'kanban' });
  const types = await apiGet(page, `/api/v1/projects/${key}/issue-types`);
  for (const summary of ['Renew the certificates', 'Rotate the keys']) {
    await apiCall(page, 'POST', '/api/v1/issues', { project: key, typeId: types[0].issueTypeId, summary });
  }
  const name = `Open work ${key}`;

  // Search, and save the query as a filter shared with everyone signed in.
  await page.goto('/issues/search');
  const query = page.getByRole('combobox', { name: 'KQL query' });
  await query.fill(`project = ${key} ORDER BY created DESC`);
  await query.press('Enter');
  await expect(page.getByText('2 issues')).toBeVisible();
  await page.getByRole('button', { name: 'Save as filter' }).click();
  const form = page.getByRole('dialog');
  await form.getByRole('textbox', { name: 'Name' }).fill(name);
  await form.getByRole('combobox', { name: 'Share with' }).click();
  await page.getByRole('option', { name: 'All signed-in users' }).click();
  await form.getByRole('button', { name: 'Share', exact: true }).click();
  await expect(form.getByText('All signed-in users')).toBeVisible();
  await form.getByRole('button', { name: 'Save' }).click();
  const bar = page.getByTestId('current-filter');
  await expect(bar).toContainText(name);
  await expect(page).toHaveURL(/filter=\d+/);

  // A changed query is marked and saved into the filter.
  await query.fill(`project = ${key} AND summary ~ certificates`);
  await query.press('Enter');
  await expect(page.getByText('1 issue', { exact: true })).toBeVisible();
  await expect(bar.getByText('Changed')).toBeVisible();
  await bar.getByRole('button', { name: 'Save changes' }).click();
  await expect(bar.getByText('Changed')).toBeHidden();

  // Subscribe: hourly, at a quarter past, in office hours.
  const { mail } = await apiGet(page, '/api/v1/filter-subscriptions');
  if (!mail) {
    // Without e-mail there are no subscriptions, and the page says so.
    await expect(bar.getByRole('button', { name: 'Subscribe' })).toBeDisabled();
    await page.goto('/issues/filters?tab=subscriptions');
    await expect(page.getByText(/sends no e-mail/)).toBeVisible();
    return;
  }
  await bar.getByRole('button', { name: 'Subscribe' }).click();
  const dialog = page.getByRole('dialog', { name: `Subscribe to ${name}` });
  await dialog.getByText('Hourly', { exact: true }).click();
  await dialog.getByRole('textbox', { name: 'Minute of the hour' }).fill('15');
  await dialog.getByText('In certain hours', { exact: true }).click();
  await expect(dialog.getByRole('combobox', { name: 'From' })).toHaveValue('08:00');
  await expect(dialog.getByRole('combobox', { name: 'Until' })).toHaveValue('18:00');
  await expect(dialog.getByRole('checkbox', { name: 'Saturday' })).not.toBeChecked();
  await dialog.getByRole('button', { name: 'Subscribe' }).click();
  await expect(page.getByText(/Subscribed\. The first e-mail comes/)).toBeVisible();

  // The Filters page lists the filter, with whom it is shared; the list is paged, so it is found by its name.
  await page.getByRole('navigation').getByRole('link', { name: 'Filters' }).click();
  await page.getByRole('textbox', { name: 'Find filters' }).fill(name);
  await expect(page.getByText('1 filter', { exact: true })).toBeVisible();
  const row = page.getByTestId('filter-list').getByRole('row').filter({ hasText: name });
  await expect(row).toContainText('All signed-in users');
  await row.getByRole('button', { name: 'Mark as favourite' }).click();
  await page.getByText('Favourites', { exact: true }).click();
  await expect(row).toBeVisible();

  // Subscriptions: the schedule in words; changed to the last day of the month; sent now; deleted.
  await page.getByRole('tab', { name: 'Subscriptions' }).click();
  const sub = page.getByTestId('subscription-list').getByRole('row').filter({ hasText: name });
  await expect(sub).toContainText('Hourly at :15, Mon–Fri, 08:00–17:59');
  await sub.getByRole('button', { name: 'Edit' }).click();
  const edit = page.getByRole('dialog', { name: `Subscription of ${name}` });
  await edit.getByText('Monthly', { exact: true }).click();
  await edit.getByRole('combobox', { name: 'Day of the month' }).click();
  await page.getByRole('option', { name: '31', exact: true }).click();
  await edit.getByLabel('Time').fill('07:30');
  await edit.getByRole('button', { name: 'Save' }).click();
  await expect(sub).toContainText('Monthly on the last day at 07:30');
  await sub.getByRole('button', { name: 'Send now' }).click();
  await expect(page.getByText('The e-mails are on their way.')).toBeVisible();
  await expect(sub).not.toContainText('Not yet');
  await sub.getByRole('button', { name: 'Delete' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();
  await expect(sub).toBeHidden();

  // Deleting the filter.
  await page.getByRole('tab', { name: 'Filters' }).click();
  await page.getByText('All', { exact: true }).click();
  await page.getByRole('textbox', { name: 'Find filters' }).fill(name);
  await row.getByRole('button', { name: `Actions for ${name}` }).click();
  await page.getByRole('menuitem', { name: 'Delete filter' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();
  await expect(row).toBeHidden();
});
