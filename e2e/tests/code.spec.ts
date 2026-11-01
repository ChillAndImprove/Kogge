// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { apiCall, expect, openProduct, signIn, test } from './fixtures';

function git(cwd: string, ...args: string[]) {
  return execFileSync('git', ['-c', 'user.name=Admin', '-c', 'user.email=admin@example.com', ...args], {
    cwd,
    env: { PATH: process.env.PATH ?? '', HOME: cwd, GIT_TERMINAL_PROMPT: '0', GIT_CONFIG_NOSYSTEM: '1' },
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  });
}

test('host a repository, review a pull request and merge it', async ({ page }) => {
  await signIn(page);
  const key = `C${Date.now() % 100000}`;

  // A code project and a repository with a README.
  await openProduct(page, 'Hafen');
  await page.getByRole('navigation').getByRole('link', { name: 'Repositories' }).click();
  await page.getByRole('button', { name: 'Create code project' }).click();
  let dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Name', exact: true }).fill(`Platform ${key}`);
  await dialog.getByRole('textbox', { name: 'Key' }).fill(key);
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { name: `Platform ${key}` })).toBeVisible();
  await page.getByRole('button', { name: 'Create repository' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Name', exact: true }).fill('Shop Service');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { name: 'Shop Service', level: 2 })).toBeVisible();
  await expect(page.getByText('README', { exact: true })).toBeVisible();

  // Push a branch with git over HTTPS and an access token.
  const token = (await apiCall(page, 'POST', '/api/v1/me/tokens', { name: `e2e ${key}`, scopes: ['read', 'write'], expiresInDays: 1 })).token as string;
  const repo = await (await page.request.get(`/api/v1/code/repos/${key}/shop-service`)).json();
  const url = new URL(repo.cloneHttp);
  url.username = 'admin';
  url.password = token;
  const dir = mkdtempSync(join(tmpdir(), 'kogge-e2e-'));
  try {
    git(dir, 'clone', url.toString(), 'shop');
    const work = join(dir, 'shop');
    git(work, 'checkout', '-b', 'feature/checkout');
    writeFileSync(join(work, 'checkout.go'), 'package shop\n\n// Checkout pays the cart.\nfunc Checkout() error {\n\treturn nil\n}\n');
    git(work, 'add', '.');
    git(work, 'commit', '-m', 'Add checkout');
    git(work, 'push', '-u', 'origin', 'feature/checkout');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }

  // Browse the new branch: highlighted source.
  await page.getByRole('tab', { name: 'Branches' }).click();
  await expect(page.getByRole('link', { name: 'feature/checkout' })).toBeVisible();
  await page.getByRole('link', { name: 'feature/checkout' }).click();
  await page.getByRole('link', { name: 'checkout.go' }).click();
  await expect(page.locator('.kogge-code .kd').first()).toHaveText('func');

  // Open a pull request from the branch list.
  await page.getByRole('tab', { name: 'Branches' }).click();
  await page.getByRole('button', { name: 'Create pull request' }).click();
  await expect(page.getByText('checkout.go').first()).toBeVisible();
  await page.getByRole('textbox', { name: 'Title' }).fill('Checkout');
  await page.getByRole('textbox', { name: 'Description' }).fill('Adds the **checkout**.');
  await page.getByRole('button', { name: 'Create pull request' }).click();
  await expect(page.getByRole('heading', { name: /Checkout #1/ })).toBeVisible();
  await expect(page.getByText('Adds the')).toBeVisible();

  // Comment on a line of the changes.
  await page.getByRole('tab', { name: 'Changes' }).click();
  await page.locator('.kogge-code tr.add td.ln').nth(3).click();
  await page.getByRole('textbox', { name: 'Add a comment' }).first().fill('Return a typed error here');
  await page.getByRole('button', { name: 'Comment', exact: true }).first().click();
  await expect(page.getByText('Return a typed error here')).toBeVisible();

  // Merge it.
  await page.getByRole('button', { name: 'Merge' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Merge' }).click();
  await expect(page.getByText('Merged', { exact: true }).first()).toBeVisible();
  await page.getByRole('tab', { name: 'Overview' }).click();
  await expect(page.getByText('merged the pull request')).toBeVisible();
  await page.getByRole('tab', { name: 'Source' }).click();
  await expect(page.getByRole('link', { name: 'checkout.go' })).toBeVisible();
});
