// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, apiGet, expect, signIn, test } from '../fixtures';

test('an administrator changes the limits, which are kept', async ({ page }) => {
  await signIn(page);
  const before = await apiGet(page, '/api/v1/admin/settings');
  // The defaults of the two limits, whatever an earlier run left.
  await apiCall(page, 'PUT', '/api/v1/admin/settings', { ...before, maxPushMb: 2048, repositoryQuotaMb: 0 });
  try {
    await page.goto('/admin/settings');
    const limits = page.getByRole('region', { name: 'Limits' });
    await expect(limits).toBeVisible();

    // The largest push, shown in GiB, raised to 3 GiB.
    const push = limits.getByRole('textbox', { name: 'Largest push' });
    await expect(push).toHaveValue('2');
    await expect(limits.getByLabel('Unit of Largest push')).toHaveValue('GiB');
    await push.fill('3');
    // A quota for repositories, which is off by default: on with 10 GiB, then 500 MiB.
    const noQuota = limits.getByRole('checkbox', { name: 'No quota' });
    await expect(noQuota).toBeChecked();
    await noQuota.uncheck();
    const quota = limits.getByRole('textbox', { name: 'Quota of a repository' });
    await expect(quota).toHaveValue('10');
    await limits.getByLabel('Unit of Quota of a repository').click();
    await page.getByRole('option', { name: 'MiB' }).click();
    await expect(quota).toHaveValue('10240');
    await quota.fill('500');
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.getByText('Saved')).toBeVisible();

    // After a reload the page shows what was saved.
    await page.reload();
    await expect(limits.getByRole('textbox', { name: 'Largest push' })).toHaveValue('3');
    await expect(limits.getByLabel('Unit of Largest push')).toHaveValue('GiB');
    await expect(limits.getByRole('checkbox', { name: 'No quota' })).not.toBeChecked();
    await expect(limits.getByRole('textbox', { name: 'Quota of a repository' })).toHaveValue('500');
    await expect(limits.getByLabel('Unit of Quota of a repository')).toHaveValue('MiB');
    const saved = await apiGet(page, '/api/v1/admin/settings');
    expect(saved.maxPushMb).toBe(3072);
    expect(saved.repositoryQuotaMb).toBe(500);

    // The storage page shows the data directory and lists the repositories, with the quota of the settings.
    await page.goto('/admin/storage');
    await expect(page.getByRole('heading', { name: 'Storage', exact: true })).toBeVisible();
    const disk = page.getByRole('region', { name: 'Data directory' });
    await expect(disk.getByText('Kept free')).toBeVisible();
    await expect(disk.getByText('256 MiB')).toBeVisible();
    const repos = page.getByRole('table', { name: 'Largest repositories' });
    await expect(repos.getByRole('row').nth(1)).toContainText('500 MiB');
  } finally {
    await apiCall(page, 'PUT', '/api/v1/admin/settings', before);
  }
});
