// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { expect, test as base, type Page } from '@playwright/test';

export const adminPassword = process.env.KOGGE_E2E_ADMIN_PASSWORD ?? 'Kogge-Dev-Secret-2026';

/** The administrator's session, created by the global setup and used by every test's page. */
export const adminState = '.auth/admin.json';

const expectedErrors = new WeakMap<Page, RegExp[]>();

/**
 * Lets a test cause a console error on purpose, e.g. a request the server refuses; the pattern is matched against
 * the message and the address it is about.
 */
export function expectConsoleError(page: Page, pattern: RegExp) {
  expectedErrors.set(page, [...(expectedErrors.get(page) ?? []), pattern]);
}

/** Fails the test on uncaught errors, console errors and Content Security Policy violations. */
export const test = base.extend<{ page: Page }>({
  page: async ({ page }, use) => {
    const problems: string[] = [];
    page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
    page.on('console', (m) => {
      if (m.type() !== 'error' || m.text().includes('401')) return;
      const text = `${m.text()} ${m.location().url}`;
      if (!expectedErrors.get(page)?.some((p) => p.test(text))) problems.push(`console: ${text}`);
    });
    await page.addInitScript(() => {
      document.addEventListener('securitypolicyviolation', (e) => {
        console.error(`CSP violation: ${e.violatedDirective} ${e.blockedURI}`);
      });
    });
    await use(page);
    expect(problems, problems.join('\n')).toEqual([]);
  },
});

export { expect };

/**
 * Opens the overview signed in. Test pages already carry the administrator's session; other people sign in with
 * the form, on a page of a new browser context.
 */
export async function signIn(page: Page, username = 'admin', password = adminPassword) {
  await page.goto('/');
  if (username === 'admin' && (await page.context().storageState()).cookies.length > 0) {
    await expect(page.getByRole('heading', { name: /Hello/ })).toBeVisible();
    return;
  }
  await page.getByRole('textbox', { name: 'User name' }).fill(username);
  await page.getByRole('textbox', { name: 'Password' }).fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: /Hello/ })).toBeVisible();
}

/** Opens a product through the app switcher in the header. */
export async function openProduct(page: Page, name: 'Kontor' | 'Logbuch' | 'Hafen' | 'Helling') {
  await page.getByRole('button', { name: 'Switch product' }).click();
  await page.getByRole('menuitem', { name: new RegExp(`^${name}`) }).click();
}

/** Calls the API with the page's session (CSRF token and Origin as the web application sends them). */
export async function apiCall(page: Page, method: 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, data?: unknown) {
  const me = await (await page.request.get('/api/v1/me')).json();
  const origin = new URL(page.url()).origin;
  const res = await page.request.fetch(path, { method, data, headers: { 'X-CSRF-Token': me.csrfToken, Origin: origin } });
  expect(res.ok(), `${method} ${path}: ${res.status()}`).toBeTruthy();
  return res.status() === 204 ? null : res.json().catch(() => null);
}

/** Reads from the API with the page's session. */
export async function apiGet(page: Page, path: string) {
  const res = await page.request.get(path);
  expect(res.ok(), `GET ${path}: ${res.status()}`).toBeTruthy();
  return res.json();
}

/** A project key of six letters that no other test run uses. */
export function uniqueKey(first = 'E') {
  const letters = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ';
  let n = Date.now() + Math.floor(Math.random() * 1000);
  let key = first;
  for (let i = 0; i < 5; i++) {
    key += letters[n % 26];
    n = Math.floor(n / 26);
  }
  return key;
}
