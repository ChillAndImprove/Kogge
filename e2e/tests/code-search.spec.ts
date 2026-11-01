// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, expectConsoleError, signIn, test, uniqueKey } from './fixtures';

test('code search finds the lines of a branch and leads to them', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('S');
  await apiCall(page, 'POST', '/api/v1/code/projects', { key, name: `Search ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/${key}/repos`, { name: 'Shop', readme: true });
  const file = (path: string, content: string, newBranch?: string) =>
    apiCall(page, 'PUT', `/api/v1/code/repos/${key}/shop/files`, {
      branch: 'main',
      newBranch,
      path,
      content,
    });
  await file(
    'src/checkout.go',
    'package src\n\n// Checkout pays the cart.\nfunc Checkout(c *Cart) error {\n\tif err := pay(c.Total); err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n',
  );
  await file(
    'src/pay.go',
    'package src\n\n// pay charges the card.\nfunc pay(amount int) error { return nil }\n',
  );
  await file('docs/operations.md', '# Operations\n\nIf a payment fails, the checkout shows the error.\n');
  await file(
    'data/ledger.txt',
    Array.from({ length: 60 }, (_, i) => `ledger entry ${i + 1}`).join('\n') + '\n',
  );
  await file('src/refund.go', 'package src\n\nfunc Refund() error { return pay() }\n', 'refunds');

  // From the box in the repository's header: matches by file, with what matched marked.
  const repo = `/code/${key.toLowerCase()}/shop`;
  await page.goto(repo);
  await page.getByRole('textbox', { name: 'Search code' }).fill('pay');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(new RegExp(`${repo}/search\\?q=pay$`));
  const summary = page.getByRole('status').filter({ hasText: /match/ });
  await expect(summary).toHaveText('5 matches in 3 files');
  await expect(page.locator('.kogge-search-file a')).toHaveText([
    'docs/operations.md',
    'src/checkout.go',
    'src/pay.go',
  ]);
  await expect(page.locator('mark')).toHaveText(['pay', 'pay', 'pay', 'pay', 'pay']);
  await expect(page.getByRole('textbox', { name: 'Search', exact: true })).toHaveValue('pay');
  await expect(page.getByRole('textbox', { name: 'Search code' })).toHaveValue('');

  // A line number leads to the line in the file.
  await page.getByRole('link', { name: 'Line 5 of src/checkout.go' }).click();
  await expect(page).toHaveURL(new RegExp(`${repo}/src/src/checkout.go#L5$`));
  await expect(page.locator('#L5')).toHaveClass(/selected/);
  await expect(page.locator('#L5')).toContainText('if err := pay(c.Total)');
  await page.goBack();

  // Matching case; Perl-compatible expressions; below a path.
  const query = page.getByRole('textbox', { name: 'Search', exact: true });
  const search = async (text: string) => {
    await query.fill(text);
    await page.getByRole('button', { name: 'Search', exact: true }).click();
  };
  await page.getByRole('checkbox', { name: 'Match case' }).check();
  await search('Pay');
  await expect(page.getByText('No line matches.')).toBeVisible();
  await page.getByRole('checkbox', { name: 'Regular expression' }).check();
  await search('func [a-z]+\\(');
  await expect(summary).toHaveText('One match in one file');
  await expect(page.locator('.kogge-search-file a')).toHaveText(['src/pay.go']);
  await search('entry \\d5$');
  await expect(summary).toHaveText('5 matches in one file');
  await expect(page).toHaveURL(/regex=1&case=1/);
  await page.getByRole('checkbox', { name: 'Regular expression' }).uncheck();
  await page.getByRole('checkbox', { name: 'Match case' }).uncheck();
  await page.getByRole('textbox', { name: 'Only below path' }).fill('docs');
  await search('pay');
  await expect(summary).toHaveText('One match in one file');
  await expect(page.locator('.kogge-search-file a')).toHaveText(['docs/operations.md']);

  // A file with more matching lines than are listed says so.
  await page.getByRole('textbox', { name: 'Only below path' }).fill('');
  await search('ledger');
  await expect(summary).toHaveText('50 matches in one file');
  await expect(page.getByText('more than 50 lines, the first 50 are listed')).toBeVisible();

  // An expression that is not valid says what is wrong.
  expectConsoleError(page, /status of 422 .*\/search\?q=pay\(/);
  await page.getByRole('checkbox', { name: 'Regular expression' }).check();
  await search('pay(');
  await expect(page.getByRole('alert')).toContainText('The regular expression is not valid.');
  await expect(page.getByRole('alert')).toContainText('missing closing )');

  // At another branch, and the file links stay at it.
  await page.goto(`${repo}/search?q=Refund&at=refunds`);
  await expect(summary).toHaveText('One match in one file');
  await expect(page.getByText('refunds', { exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'src/refund.go', exact: true })).toHaveAttribute(
    'href',
    `${repo}/src/src/refund.go?at=refunds`,
  );
  await page.goto(`${repo}/search?q=Refund`);
  await expect(page.getByText('No line matches.')).toBeVisible();
});
