// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, expect, signIn, test, uniqueKey } from './fixtures';
import { totp } from './totp';

test('set up two-factor authentication and sign in with a code', async ({ page, browser, baseURL }) => {
  await signIn(page);
  const username = `tf-${uniqueKey('U').toLowerCase()}`;
  const password = 'Two-Factor-Secret-2026';
  await apiCall(page, 'POST', '/api/v1/admin/users', {
    username,
    displayName: 'Tamara Fuchs',
    password,
    email: `${username}@example.com`,
  });

  const context = await browser.newContext({
    baseURL,
    locale: 'en-GB',
    storageState: { cookies: [], origins: [] },
  });
  const user = await context.newPage();
  const login = async () => {
    await user.goto('/');
    await user.getByRole('textbox', { name: 'User name' }).fill(username);
    await user.getByRole('textbox', { name: 'Password' }).fill(password);
    await user.getByRole('button', { name: 'Sign in', exact: true }).click();
  };
  await login();
  await expect(user.getByRole('heading', { name: /Hello/ })).toBeVisible();

  await user.goto('/profile?tab=password');
  await user.getByRole('button', { name: 'Set up two-factor authentication' }).click();
  const setup = user.getByRole('dialog', { name: 'Set up two-factor authentication' });
  await expect(setup.getByRole('img', { name: 'QR code for the authenticator app' })).toBeVisible();
  const secret = (await setup.locator('pre').first().textContent())!.trim();
  await setup.getByRole('textbox', { name: 'Code' }).fill(totp(secret));
  await setup.getByRole('textbox', { name: 'Current password' }).fill(password);
  await setup.getByRole('button', { name: 'Turn on' }).click();
  const codes = user.getByRole('dialog', { name: 'Two-factor authentication' });
  await expect(codes.getByText('Your recovery codes')).toBeVisible();
  await expect(codes.locator('pre')).toHaveText(/^([a-z2-7]{4}-[a-z2-7]{4}\n?){10}$/);
  await codes.getByRole('button', { name: 'I have saved the codes' }).click();
  await expect(user.getByText('10 recovery codes left.')).toBeVisible();

  // Signing in again takes a code; the one that turned it on counts no more.
  await user.getByRole('button', { name: 'Profile' }).click();
  await user.getByRole('menuitem', { name: 'Sign out' }).click();
  await login();
  const code = user.getByRole('textbox', { name: 'Code' });
  await expect(code).toBeVisible();
  await code.fill('000000');
  await user.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(user.getByText('The code is not correct.')).toBeVisible();
  await code.fill(totp(secret, Math.floor(Date.now() / 1000 / 30) + 1));
  await user.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(user.getByRole('heading', { name: /Hello/ })).toBeVisible();
  await context.close();
});
